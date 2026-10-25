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
	"github.com/aurelio/hookrelay/internal/receiver"
	"github.com/aurelio/hookrelay/internal/store"
	"github.com/aurelio/hookrelay/internal/task"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

// Two EMPTY dedicated databases, all platform + receiver migrations, Redis 7.4.
// Never point the global dispatcher at the user's development database.
func TestReceiverTimeoutDeduplication(t *testing.T) {
	if os.Getenv("HOOKRELAY_OUTBOX_INTEGRATION") != "1" || os.Getenv("HOOKRELAY_RECEIVER_INTEGRATION") != "1" {
		t.Skip("requires isolated platform and receiver databases plus Redis")
	}
	ctx := context.Background()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal("load config failed")
	}
	rcfg, err := config.LoadReceiverDB()
	if err != nil {
		t.Fatal("load receiver config failed")
	}
	db, pool, err := store.OpenMySQL(ctx, cfg)
	if err != nil {
		t.Fatal("connect platform database failed")
	}
	defer pool.Close()
	rdb, rpool, err := store.OpenMySQL(ctx, rcfg)
	if err != nil {
		t.Fatal("connect receiver database failed")
	}
	defer rpool.Close()
	var n int64
	if err := db.Model(&store.Outbox{}).Count(&n).Error; err != nil || n != 0 {
		t.Fatal("requires empty dedicated platform outbox")
	}
	if err := rdb.Model(&receiver.Receipt{}).Count(&n).Error; err != nil || n != 0 {
		t.Fatal("requires empty dedicated receiver database")
	}

	secret := strings.Repeat("a", 64)
	repo := receiver.Store{DB: rdb}
	var requests atomic.Int32
	h := receiver.NewHandler(secret, repo.Accept, 204, 10*time.Second, "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		h.ServeHTTP(w, r)
	}))
	defer server.Close()
	endpoint := store.Endpoint{URL: server.URL + "/webhook", SigningSecret: secret, Enabled: true}
	if err := store.CreateEndpoint(ctx, db, &endpoint); err != nil {
		t.Fatal(err)
	}
	defer db.Delete(&endpoint)
	result, err := store.CreateOrGetEventDelivery(ctx, db, store.EventInput{
		EndpointID: endpoint.ID, IdempotencyKey: uuid.NewString(), EventType: "timeout-test", Payload: []byte(`{ "order": 42 }`),
	}, 10000)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, model := range []any{&receiver.Effect{}, &receiver.Receipt{}} {
			if err := rdb.Where("delivery_id = ?", result.DeliveryID).Delete(model).Error; err != nil {
				t.Error(err)
			}
		}
		for _, model := range []any{&store.DeliveryAttempt{}, &store.Outbox{}} {
			if err := db.Where("delivery_id = ?", result.DeliveryID).Delete(model).Error; err != nil {
				t.Error(err)
			}
		}
		db.Delete(&store.Delivery{}, result.DeliveryID)
		db.Delete(&store.Event{}, result.EventID)
	}()
	queue := "receiver-test-" + uuid.NewString()
	redis := asynq.RedisClientOpt{Addr: cfg.RedisAddr}
	client := asynq.NewClient(redis)
	defer client.Close()
	inspector := asynq.NewInspector(redis)
	defer inspector.Close()
	defer inspector.DeleteQueue(queue, true)
	deferrer := task.NewClient(cfg.RedisAddr)
	defer deferrer.Close()
	worker := delivery.Worker{Store: store.DeliveryStore{DB: db}, Send: delivery.NewSender(time.Second).Send, AllowedURL: endpoint.URL,
		Limiter: delivery.NewEndpointLimiter(1), Defer: deferrer.DeferDelivery}
	jobs := asynq.NewServer(redis, asynq.Config{
		Concurrency: 4, Queues: map[string]int{queue: 1}, TaskCheckInterval: 50 * time.Millisecond,
		DelayedTaskCheckInterval: 100 * time.Millisecond, ShutdownTimeout: 3 * time.Second,
		RetryDelayFunc: (delivery.RetryPolicy{BaseDelay: 100 * time.Millisecond, MaxDelay: time.Second}).Delay,
	})
	mux := asynq.NewServeMux()
	mux.Handle(task.TypeDelivery, worker)
	if err := jobs.Start(mux); err != nil {
		t.Fatal(err)
	}
	defer jobs.Shutdown()
	var jobID string
	dispatcher := outbox.Dispatcher{Store: store.OutboxStore{DB: db}, Enqueue: func(ctx context.Context, id uint64, generation uint32) error {
		// Use the same payload and budget as the production client, on a private queue.
		body, _ := json.Marshal(task.Payload{DeliveryID: id, Generation: generation})
		job := asynq.NewTask(task.TypeDelivery, body)
		info, err := client.EnqueueContext(ctx, job, asynq.Queue(queue), asynq.MaxRetry(2), asynq.Timeout(task.TaskTimeout))
		if err == nil {
			jobID = info.ID
		}
		return err
	}}
	if worked, err := dispatcher.DispatchOne(ctx); !worked || err != nil {
		t.Fatal("dispatch failed")
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		d, err := store.FindDelivery(ctx, db, result.DeliveryID)
		if err != nil {
			t.Fatal(err)
		}
		if d.Status == "succeeded" {
			if d.AttemptCount != 2 {
				t.Fatalf("attempts=%d, want first timeout then duplicate success", d.AttemptCount)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out, delivery status=%s", d.Status)
		}
		time.Sleep(30 * time.Millisecond)
	}
	for {
		_, err := inspector.GetTaskInfo(queue, jobID)
		if errors.Is(err, asynq.ErrTaskNotFound) {
			break
		}
		if err != nil || time.Now().After(deadline) {
			t.Fatal("queue task did not complete")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if requests.Load() != 2 {
		t.Fatalf("HTTP sends=%d", requests.Load())
	}
	for _, model := range []any{&receiver.Receipt{}, &receiver.Effect{}} {
		if err := rdb.Model(model).Where("delivery_id = ?", result.DeliveryID).Count(&n).Error; err != nil || n != 1 {
			t.Fatalf("%T rows=%d err=%v", model, n, err)
		}
	}
	history, err := store.ListDeliveryAttempts(ctx, db, result.DeliveryID, 0, 10)
	if err != nil || len(history) != 2 || history[0].ErrorClass != "timeout" || history[0].Status != "retrying" || history[1].Status != "succeeded" {
		t.Fatalf("unexpected timeout/retry history: %+v err=%v", history, err)
	}
	t.Log("2 signed HTTP requests, first response timed out after commit, 1 receipt + 1 business effect, delivery succeeded")
}
