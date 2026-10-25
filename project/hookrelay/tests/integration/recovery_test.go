package integration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aurelio/hookrelay/internal/config"
	"github.com/aurelio/hookrelay/internal/delivery"
	"github.com/aurelio/hookrelay/internal/outbox"
	"github.com/aurelio/hookrelay/internal/reconcile"
	"github.com/aurelio/hookrelay/internal/store"
	"github.com/aurelio/hookrelay/internal/task"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

// One focused scenario, using only an empty disposable database/private queue.
func TestRecovery(t *testing.T) {
	if os.Getenv("HOOKRELAY_OUTBOX_INTEGRATION") != "1" {
		t.Skip("requires dedicated empty MySQL, all migrations and Redis")
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	cfg, err := config.Load()
	must(err)
	db, pool, err := store.OpenMySQL(ctx, cfg)
	must(err)
	defer pool.Close()
	var count int64
	must(db.Model(&store.Delivery{}).Count(&count).Error)
	if count != 0 {
		t.Fatal("recovery test requires an empty database")
	}
	var sends atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sends.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer receiver.Close()
	endpoint := store.Endpoint{URL: receiver.URL, SigningSecret: strings.Repeat("a", 64), Enabled: true}
	must(store.CreateEndpoint(ctx, db, &endpoint))
	var created []store.EventResult
	defer func() {
		for _, d := range created {
			for _, model := range []any{&store.DeliveryAttempt{}, &store.Outbox{}} {
				if err := db.Where("delivery_id = ?", d.DeliveryID).Delete(model).Error; err != nil {
					t.Error(err)
				}
			}
			db.Delete(&store.Delivery{}, d.DeliveryID)
			db.Delete(&store.Event{}, d.EventID)
		}
		db.Delete(&store.Endpoint{}, endpoint.ID)
	}()
	create := func() store.EventResult {
		d, err := store.CreateOrGetEventDelivery(ctx, db, store.EventInput{EndpointID: endpoint.ID,
			IdempotencyKey: uuid.NewString(), EventType: "recovery", Payload: []byte(`{}`)}, 10000)
		must(err)
		created = append(created, d)
		return d
	}
	queue := "recovery-" + uuid.NewString()
	redis := asynq.RedisClientOpt{Addr: cfg.RedisAddr}
	client := asynq.NewClient(redis)
	defer client.Close()
	inspector := asynq.NewInspector(redis)
	defer inspector.Close()
	defer inspector.DeleteQueue(queue, true)
	check := task.NewInspector(cfg.RedisAddr, queue)
	defer check.Close()
	repo := store.DeliveryStore{DB: db}
	r := reconcile.Reconciler{Store: repo, Alive: check.Alive, Grace: time.Minute, BatchSize: 10, MaxRecoveries: 2}
	dispatcher := outbox.Dispatcher{Store: store.OutboxStore{DB: db}, Enqueue: func(ctx context.Context, id uint64, gen uint32) error {
		body, _ := json.Marshal(task.Payload{DeliveryID: id, Generation: gen})
		_, err := client.EnqueueContext(ctx, asynq.NewTask(task.TypeDelivery, body), asynq.Queue(queue),
			asynq.TaskID(task.DeliveryTaskID(id, gen)), asynq.MaxRetry(2), asynq.Timeout(task.TaskTimeout))
		return err
	}}
	dispatch := func() {
		t.Helper()
		worked, err := dispatcher.DispatchOne(ctx)
		must(err)
		if !worked {
			t.Fatal("nothing published")
		}
	}
	batch := func() { t.Helper(); _, err := r.Batch(ctx, 0); must(err) }
	get := func(id uint64) store.Delivery {
		t.Helper()
		d, err := store.FindDelivery(ctx, db, id)
		must(err)
		return d
	}
	age := func(id uint64) {
		t.Helper()
		past := time.Now().UTC().Add(-2 * time.Minute)
		must(db.Model(&store.Delivery{}).Where("id = ?", id).Updates(map[string]any{"updated_at": past, "lease_until": past}).Error)
		must(db.Model(&store.Outbox{}).Where("delivery_id = ?", id).Update("updated_at", past).Error)
	}

	first := create()
	dispatch()
	old, err := repo.Claim(ctx, first.DeliveryID, 1, task.MaxAttempts, task.LeaseDuration)
	must(err)
	must(inspector.DeleteTask(queue, task.DeliveryTaskID(first.DeliveryID, 1))) // Lost after publication.
	batch()
	if get(first.DeliveryID).Generation != 1 {
		t.Fatal("active lease/recent work was recovered")
	}
	age(first.DeliveryID)

	// A failed Outbox insert must roll back recovery's generation and budget.
	conflict := store.Outbox{DeliveryID: first.DeliveryID, Generation: 2, Status: "pending", AvailableAt: time.Now().UTC()}
	must(db.Create(&conflict).Error)
	if _, err := r.Batch(ctx, 0); err == nil {
		t.Fatal("expected outbox uniqueness conflict")
	}
	if d := get(first.DeliveryID); d.Generation != 1 || d.RecoveryCount != 0 {
		t.Fatal("recovery did not roll back")
	}
	must(db.Delete(&conflict).Error)
	batch()
	if d := get(first.DeliveryID); d.Generation != 2 || d.AttemptCount != 1 || d.RecoveryCount != 1 || d.Status != "pending" {
		t.Fatalf("lost work not recovered with its budget: %+v", d)
	}
	must(repo.Finish(ctx, old, "succeeded", 204, "", time.Millisecond, 0))
	if get(first.DeliveryID).Status != "pending" {
		t.Fatal("late result overwrote recovered generation")
	}
	if work, err := repo.Claim(ctx, first.DeliveryID, 1, task.MaxAttempts, time.Second); err != nil || work != nil {
		t.Fatal("old generation executed")
	}
	age(first.DeliveryID)
	batch()
	if get(first.DeliveryID).Generation != 2 {
		t.Fatal("unpublished outbox must remain owned by dispatcher")
	}
	dispatch()

	// Simulate a tracked deferred replacement. The original ID no longer exists.
	must(inspector.DeleteTask(queue, task.DeliveryTaskID(first.DeliveryID, 2)))
	body, _ := json.Marshal(task.Payload{DeliveryID: first.DeliveryID, Generation: 2})
	info, err := client.Enqueue(asynq.NewTask(task.TypeDelivery, body), asynq.Queue(queue), asynq.ProcessIn(time.Hour))
	must(err)
	must(repo.TrackTask(ctx, first.DeliveryID, 2, info.ID))
	age(first.DeliveryID)
	batch()
	if get(first.DeliveryID).Generation != 2 {
		t.Fatal("live replacement was mistaken for a lost task")
	}
	snapshot := get(first.DeliveryID)
	must(repo.TrackTask(ctx, first.DeliveryID, 2, info.ID))
	updated, err := repo.Recover(ctx, snapshot, time.Now().UTC().Add(-time.Minute), task.MaxAttempts, 2)
	must(err)
	if updated != nil {
		t.Fatal("recovery ignored concurrent activity")
	}
	age(first.DeliveryID)
	r.Alive = func(context.Context, string) (bool, error) { return false, errors.New("redis unavailable") }
	if _, err := r.Batch(ctx, 0); err == nil || get(first.DeliveryID).Generation != 2 {
		t.Fatal("inspection errors must fail closed")
	}
	r.Alive = check.Alive
	must(inspector.ArchiveTask(queue, info.ID))
	batch()
	if d := get(first.DeliveryID); d.Generation != 3 || d.RecoveryCount != 2 || d.AttemptCount != 1 {
		t.Fatal("archived task was not recovered")
	}
	dispatch()

	deferrer := task.NewClient(cfg.RedisAddr)
	defer deferrer.Close()
	worker := delivery.Worker{Store: repo, Send: delivery.NewSender(time.Second).Send, AllowedURL: endpoint.URL, Defer: deferrer.DeferDelivery}
	jobs := asynq.NewServer(redis, asynq.Config{Concurrency: 1, Queues: map[string]int{queue: 1}, TaskCheckInterval: 20 * time.Millisecond, ShutdownTimeout: time.Second})
	must(jobs.Start(worker))
	defer jobs.Shutdown()
	deadline := time.Now().Add(5 * time.Second)
	for get(first.DeliveryID).Status != "succeeded" {
		if time.Now().After(deadline) {
			t.Fatal("recovered task never completed")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if get(first.DeliveryID).AttemptCount != 2 || sends.Load() != 1 {
		t.Fatal("recovery reset attempt budget or sent extra HTTP")
	}
	jobs.Shutdown()

	// Missing tasks must terminate rather than bypass either budget indefinitely.
	for _, reason := range []string{"attempts_exhausted", "recovery_exhausted"} {
		d := create()
		must(db.Model(&store.Outbox{}).Where("delivery_id = ?", d.DeliveryID).Update("status", "published").Error)
		attempts := uint32(0)
		if reason == "attempts_exhausted" {
			attempts = task.MaxAttempts
		}
		must(db.Model(&store.Delivery{}).Where("id = ?", d.DeliveryID).Updates(map[string]any{"attempt_count": attempts, "recovery_count": 2}).Error)
		age(d.DeliveryID)
		batch()
		failed := get(d.DeliveryID)
		if failed.Status != "failed" || failed.LastError == nil || *failed.LastError != reason {
			t.Fatalf("budget not enforced: %+v", failed)
		}
		replayed, err := store.ReplayDelivery(ctx, db, d.DeliveryID, 10000)
		must(err)
		if replayed.RecoveryCount != 0 || replayed.AttemptCount != 0 || replayed.QueueTaskID != "" {
			t.Fatal("manual replay did not start a fresh budget")
		}
	}
}
