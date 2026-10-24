package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aurelio/hookrelay/internal/config"
	"github.com/aurelio/hookrelay/internal/delivery"
	"github.com/aurelio/hookrelay/internal/outbox"
	"github.com/aurelio/hookrelay/internal/store"
	"github.com/aurelio/hookrelay/internal/task"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

type failPublishOnce struct {
	store.OutboxStore
	failed bool
}

func (s *failPublishOnce) Published(ctx context.Context, entry *store.Outbox) error {
	if !s.failed {
		s.failed = true
		return errors.New("injected acknowledgement failure")
	}
	return s.OutboxStore.Published(ctx, entry)
}

// The dispatcher scans globally. Explicit opt-in requires an EMPTY, dedicated
// migrated database, with no application processes. Redis uses a unique queue.
func TestAsyncDelivery(t *testing.T) {
	if os.Getenv("HOOKRELAY_OUTBOX_INTEGRATION") != "1" {
		t.Skip("requires dedicated empty MySQL, migrations and Redis; see README")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal("load config failed")
	}
	ctx := context.Background()
	db, pool, err := store.OpenMySQL(ctx, cfg)
	if err != nil {
		t.Fatal("connect MySQL failed")
	}
	defer pool.Close()
	var count int64
	if err := db.Model(&store.Outbox{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("requires empty outbox in a dedicated database")
	}

	var mu sync.Mutex
	counts := map[string]int{}
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		counts[string(body)]++
		count := counts[string(body)]
		mu.Unlock()
		if r.Header.Get("X-Webhook-ID") == "" {
			t.Error("missing stable delivery ID")
		}
		switch string(body) {
		case `"temporary"`:
			if count == 1 {
				w.WriteHeader(500)
				return
			}
		case `"permanent"`:
			w.WriteHeader(400)
			return
		case `"exhausted"`:
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(204)
	}))
	defer receiver.Close()
	endpoint := store.Endpoint{URL: receiver.URL, SigningSecret: strings.Repeat("a", 64), Enabled: true}
	if err := store.CreateEndpoint(ctx, db, &endpoint); err != nil {
		t.Fatal(err)
	}
	var results []store.EventResult
	defer func() {
		for _, result := range results {
			if err := db.Where("delivery_id = ?", result.DeliveryID).Delete(&store.Outbox{}).Error; err != nil {
				t.Error(err)
			}
			if err := db.Delete(&store.Delivery{}, result.DeliveryID).Error; err != nil {
				t.Error(err)
			}
			if err := db.Delete(&store.Event{}, result.EventID).Error; err != nil {
				t.Error(err)
			}
		}
		if err := db.Delete(&store.Endpoint{}, endpoint.ID).Error; err != nil {
			t.Error(err)
		}
	}()

	queue := "test-" + uuid.NewString()
	redis := asynq.RedisClientOpt{Addr: cfg.RedisAddr}
	client := asynq.NewClient(redis)
	defer client.Close()
	inspector := asynq.NewInspector(redis)
	defer inspector.Close()
	defer func() {
		if err := inspector.DeleteQueue(queue, true); err != nil {
			t.Error(err)
		}
	}()
	worker := delivery.Worker{Store: store.DeliveryStore{DB: db}, Send: delivery.NewSender(time.Second).Send, AllowedURL: receiver.URL}
	server := asynq.NewServer(redis, asynq.Config{
		Concurrency: 1, Queues: map[string]int{queue: 1},
		TaskCheckInterval: 50 * time.Millisecond, DelayedTaskCheckInterval: 100 * time.Millisecond,
		RetryDelayFunc:  func(int, error, *asynq.Task) time.Duration { return 100 * time.Millisecond },
		ShutdownTimeout: 3 * time.Second,
	})
	mux := asynq.NewServeMux()
	mux.Handle(task.TypeDelivery, worker)
	if err := server.Start(mux); err != nil {
		t.Fatal(err)
	}
	defer server.Shutdown()
	enqueue := func(ctx context.Context, id uint64, generation uint32) error {
		body, _ := json.Marshal(task.Payload{DeliveryID: id, Generation: generation})
		_, err := client.EnqueueContext(ctx, asynq.NewTask(task.TypeDelivery, body), asynq.Queue(queue),
			asynq.TaskID(fmt.Sprintf("delivery-%d-%d", id, generation)), asynq.MaxRetry(2), asynq.Timeout(task.TaskTimeout))
		if errors.Is(err, asynq.ErrTaskIDConflict) {
			return nil
		}
		return err
	}
	repo := store.OutboxStore{DB: db}
	dispatcher := outbox.Dispatcher{Store: repo, Enqueue: enqueue}
	create := func(t *testing.T, name string) store.EventResult {
		t.Helper()
		input := store.EventInput{EndpointID: endpoint.ID, IdempotencyKey: uuid.NewString(), EventType: "test", Payload: []byte(fmt.Sprintf("%q", name))}
		result, err := store.CreateOrGetEventDelivery(ctx, db, input)
		if err != nil {
			t.Fatal(err)
		}
		results = append(results, result)
		return result
	}
	entryFor := func(t *testing.T, id uint64) store.Outbox {
		t.Helper()
		var entry store.Outbox
		if err := db.Where("delivery_id = ?", id).Take(&entry).Error; err != nil {
			t.Fatal(err)
		}
		return entry
	}
	dispatch := func(t *testing.T) {
		t.Helper()
		if worked, err := dispatcher.DispatchOne(ctx); !worked || err != nil {
			t.Fatalf("dispatch: worked=%v err=%v", worked, err)
		}
	}
	waitDelivery := func(t *testing.T, id uint64, status string) store.Delivery {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			d, err := store.FindDelivery(ctx, db, id)
			if err != nil {
				t.Fatal(err)
			}
			if d.Status == status {
				return d
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out with status %s", d.Status)
			}
			time.Sleep(30 * time.Millisecond)
		}
	}
	waitQueueFinal := func(t *testing.T, id uint64, archived bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			info, err := inspector.GetTaskInfo(queue, fmt.Sprintf("delivery-%d-1", id))
			if !archived && errors.Is(err, asynq.ErrTaskNotFound) {
				return
			}
			if archived && err == nil && info.State == asynq.TaskStateArchived {
				return
			}
			if err != nil || time.Now().After(deadline) {
				t.Fatalf("queue task did not finish: archived=%v err=%v", archived, err)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	expire := func(t *testing.T, entry store.Outbox) {
		t.Helper()
		past := time.Now().UTC().Add(-time.Minute)
		if err := db.Model(&entry).Updates(map[string]any{"available_at": past, "lease_until": past}).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, tt := range []struct {
		name, status string
		attempts     int
	}{
		{"success", "succeeded", 1}, {"temporary", "succeeded", 2}, {"permanent", "failed", 1}, {"exhausted", "failed", 3},
	} {
		t.Run(tt.name, func(t *testing.T) {
			result := create(t, tt.name)
			dispatch(t)
			d := waitDelivery(t, result.DeliveryID, tt.status)
			mu.Lock()
			sent := counts[fmt.Sprintf("%q", tt.name)]
			mu.Unlock()
			if int(d.AttemptCount) != tt.attempts || sent != tt.attempts {
				t.Fatalf("attempts=%d HTTP sends=%d", d.AttemptCount, sent)
			}
			waitQueueFinal(t, result.DeliveryID, tt.status == "failed")
			if entryFor(t, result.DeliveryID).Status != "published" {
				t.Fatal("outbox must be published")
			}
		})
	}

	t.Run("Redis unavailable then restored", func(t *testing.T) {
		// Use a closed local port, without stopping or mutating the user's Redis.
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := listener.Addr().String()
		listener.Close()
		unavailable := task.NewClient(addr)
		defer unavailable.Close()
		result := create(t, "redis-recovery")
		broken := outbox.Dispatcher{Store: repo, Enqueue: unavailable.EnqueueDelivery}
		if worked, err := broken.DispatchOne(ctx); !worked || err == nil {
			t.Fatal("enqueue must fail")
		}
		entry := entryFor(t, result.DeliveryID)
		if entry.Status != "pending" || entry.LastError != "enqueue_failed" || !entry.AvailableAt.After(time.Now()) {
			t.Fatal("failed publish must be deferred")
		}
		if worked, err := dispatcher.DispatchOne(ctx); worked || err != nil {
			t.Fatal("must respect retry delay")
		}
		expire(t, entry)
		dispatch(t)
		waitDelivery(t, result.DeliveryID, "succeeded")
	})

	t.Run("claim crash and stale acknowledgement", func(t *testing.T) {
		result := create(t, "claim-crash")
		old, err := repo.Claim(ctx, time.Minute)
		if err != nil || old == nil || old.DeliveryID != result.DeliveryID {
			t.Fatal("claim failed")
		}
		if other, err := repo.Claim(ctx, time.Minute); other != nil || err != nil {
			t.Fatal("active lease must not be reclaimed")
		}
		expire(t, *old)
		// A new dispatcher instance recovers using only the database.
		restarted := outbox.Dispatcher{Store: store.OutboxStore{DB: db}, Enqueue: enqueue}
		if worked, err := restarted.DispatchOne(ctx); !worked || err != nil {
			t.Fatal("expired lease not recovered")
		}
		if err := repo.Retry(ctx, old, time.Minute); err != nil {
			t.Fatal(err)
		}
		entry := entryFor(t, result.DeliveryID)
		if entry.Status != "published" || entry.PublishAttempts != 2 {
			t.Fatal("stale claimant overwrote publication")
		}
		waitDelivery(t, result.DeliveryID, "succeeded")
	})

	t.Run("enqueue succeeded but acknowledgement lost", func(t *testing.T) {
		result := create(t, "ack-lost")
		broken := outbox.Dispatcher{Store: &failPublishOnce{OutboxStore: repo}, Enqueue: enqueue}
		if worked, err := broken.DispatchOne(ctx); !worked || err == nil {
			t.Fatal("expected failed acknowledgement")
		}
		entry := entryFor(t, result.DeliveryID)
		if entry.Status != "processing" {
			t.Fatal("unacknowledged publish must retain lease")
		}
		waitDelivery(t, result.DeliveryID, "succeeded")
		waitQueueFinal(t, result.DeliveryID, false)
		expire(t, entry)
		dispatch(t) // Redis no longer has the first task; this really enqueues again.
		waitQueueFinal(t, result.DeliveryID, false)
		mu.Lock()
		sent := counts[`"ack-lost"`]
		mu.Unlock()
		if sent != 1 {
			t.Fatalf("completed delivery sent again: %d", sent)
		}
		if got := entryFor(t, result.DeliveryID); got.Status != "published" || got.PublishAttempts != 2 {
			t.Fatal("duplicate publication not completed")
		}
	})
}
