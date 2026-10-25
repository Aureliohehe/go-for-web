package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aurelio/hookrelay/internal/config"
	"github.com/aurelio/hookrelay/internal/delivery"
	"github.com/aurelio/hookrelay/internal/outbox"
	"github.com/aurelio/hookrelay/internal/store"
	"github.com/aurelio/hookrelay/internal/task"
	"github.com/aurelio/hookrelay/internal/webhook"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

func TestEndpointIsolation(t *testing.T) {
	if os.Getenv("HOOKRELAY_OUTBOX_INTEGRATION") != "1" {
		t.Skip("requires isolated empty MySQL and Redis")
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
	var n int64
	if err := db.Model(&store.Outbox{}).Count(&n).Error; err != nil || n != 0 {
		t.Fatal("requires dedicated empty outbox")
	}

	const secret = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	releaseSlow := make(chan struct{})
	enteredSlow := make(chan struct{}, 1)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseSlow) }) }
	var activeSlow, peakSlow, sends atomic.Int32
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := webhook.Verify(secret, r.Header.Get(webhook.HeaderID), r.Header.Get(webhook.HeaderTimestamp), r.Header.Get(webhook.HeaderSignature), body, time.Now()); err != nil {
			t.Error("invalid signature")
			w.WriteHeader(401)
			return
		}
		sends.Add(1)
		if strings.HasPrefix(string(body), `"slow`) {
			n := activeSlow.Add(1)
			defer activeSlow.Add(-1)
			for old := peakSlow.Load(); n > old && !peakSlow.CompareAndSwap(old, n); old = peakSlow.Load() {
			}
			select {
			case enteredSlow <- struct{}{}:
			default:
			}
			select {
			case <-releaseSlow:
			case <-r.Context().Done():
				return
			}
		}
		w.WriteHeader(204)
	}))
	defer receiver.Close()
	defer release()
	// Two logical endpoint IDs on the single allowed controlled URL. The quota is
	// per ID, not per host/URL; the fixture responds slowly only for the first ID.
	slow := store.Endpoint{URL: receiver.URL, SigningSecret: secret, Enabled: true}
	fast := slow
	if err := store.CreateEndpoint(ctx, db, &slow); err != nil {
		t.Fatal(err)
	}
	defer db.Delete(&slow)
	if err := store.CreateEndpoint(ctx, db, &fast); err != nil {
		t.Fatal(err)
	}
	defer db.Delete(&fast)
	var records []store.EventResult
	defer func() {
		for _, r := range records {
			for _, model := range []any{&store.DeliveryAttempt{}, &store.Outbox{}} {
				if err := db.Where("delivery_id = ?", r.DeliveryID).Delete(model).Error; err != nil {
					t.Error(err)
				}
			}
			if err := db.Delete(&store.Delivery{}, r.DeliveryID).Error; err != nil {
				t.Error(err)
			}
			if err := db.Delete(&store.Event{}, r.EventID).Error; err != nil {
				t.Error(err)
			}
		}
	}()
	create := func(endpoint uint64, name string) store.EventResult {
		r, err := store.CreateOrGetEventDelivery(ctx, db, store.EventInput{EndpointID: endpoint, IdempotencyKey: uuid.NewString(), EventType: "isolation", Payload: []byte(fmt.Sprintf("%q", name))}, 10000)
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, r)
		return r
	}
	leader := create(slow.ID, "slow-leader")
	var backlog []store.EventResult
	for i := range 6 {
		backlog = append(backlog, create(slow.ID, fmt.Sprintf("slow-%d", i)))
	}
	zeroRetryID := backlog[0].DeliveryID
	quick := create(fast.ID, "fast")

	queue := "isolation-" + uuid.NewString()
	redis := asynq.RedisClientOpt{Addr: cfg.RedisAddr}
	client := asynq.NewClient(redis)
	defer client.Close()
	deferrer := task.NewClient(cfg.RedisAddr)
	defer deferrer.Close()
	inspector := asynq.NewInspector(redis)
	defer inspector.Close()
	defer inspector.DeleteQueue(queue, true)
	var mu sync.Mutex
	deferred := map[uint64]int{}
	sender := delivery.NewSender(15 * time.Second)
	worker := delivery.Worker{Store: store.DeliveryStore{DB: db}, AllowedURL: receiver.URL, Limiter: delivery.NewEndpointLimiter(1),
		Send: func(ctx context.Context, req delivery.Request) (delivery.Result, error) {
			if req.DeliveryID == fmt.Sprint(zeroRetryID) {
				if max, ok := asynq.GetMaxRetry(ctx); !ok || max != 0 {
					t.Error("deferral replenished exhausted queue retry budget")
				}
			}
			return sender.Send(ctx, req)
		},
		Defer: func(ctx context.Context, id uint64, gen uint32) error {
			if err := deferrer.DeferDelivery(ctx, id, gen); err != nil {
				return err
			}
			mu.Lock()
			deferred[id]++
			mu.Unlock()
			return nil
		},
	}
	jobs := asynq.NewServer(redis, asynq.Config{Concurrency: 4, Queues: map[string]int{queue: 1},
		TaskCheckInterval: 20 * time.Millisecond, DelayedTaskCheckInterval: 50 * time.Millisecond, ShutdownTimeout: 3 * time.Second,
		RetryDelayFunc: (delivery.RetryPolicy{BaseDelay: 100 * time.Millisecond, MaxDelay: time.Second}).Delay,
	})
	mux := asynq.NewServeMux()
	mux.Handle(task.TypeDelivery, worker)
	if err := jobs.Start(mux); err != nil {
		t.Fatal(err)
	}
	defer jobs.Shutdown()
	defer release() // Unblock HTTP before graceful Worker shutdown on test failure.
	dispatcher := outbox.Dispatcher{Store: store.OutboxStore{DB: db}, Enqueue: func(ctx context.Context, id uint64, gen uint32) error {
		body, _ := json.Marshal(task.Payload{DeliveryID: id, Generation: gen})
		max := task.MaxAttempts - 1
		if id == zeroRetryID {
			max = 0
		} // Saturation must not archive the last opportunity.
		_, err := client.EnqueueContext(ctx, asynq.NewTask(task.TypeDelivery, body), asynq.Queue(queue), asynq.MaxRetry(max), asynq.Timeout(task.TaskTimeout))
		return err
	}}
	dispatch := func() {
		if worked, err := dispatcher.DispatchOne(ctx); !worked || err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	}
	dispatch()
	select {
	case <-enteredSlow:
	case <-time.After(5 * time.Second):
		t.Fatal("slow leader never entered HTTP")
	}
	started := time.Now()
	for range len(backlog) + 1 {
		dispatch()
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		d, err := store.FindDelivery(ctx, db, quick.DeliveryID)
		if err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		deferredAll := len(deferred) == len(backlog)
		mu.Unlock()
		if d.Status == "succeeded" && deferredAll {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fast endpoint blocked behind slow endpoint")
		}
		time.Sleep(20 * time.Millisecond)
	}
	fastLatency := time.Since(started)
	// Keep the endpoint blocked through another scheduling round. Even a task
	// with no ordinary retries left must survive repeated saturation.
	deadline = time.Now().Add(6 * time.Second)
	for {
		mu.Lock()
		count := deferred[zeroRetryID]
		mu.Unlock()
		if count >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("last-retry task was not deferred again")
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, r := range backlog {
		d, err := store.FindDelivery(ctx, db, r.DeliveryID)
		if err != nil || d.AttemptCount != 0 || d.Status != "pending" {
			t.Fatal("saturation consumed attempts or changed status")
		}
		history, err := store.ListDeliveryAttempts(ctx, db, r.DeliveryID, 0, 10)
		if err != nil || len(history) != 0 {
			t.Fatal("saturation created attempt history")
		}
	}
	if d, err := store.FindDelivery(ctx, db, leader.DeliveryID); err != nil || d.Status != "processing" {
		t.Fatal("slow leader was not still blocked")
	}
	release()
	deadline = time.Now().Add(30 * time.Second)
	for {
		complete := true
		for _, r := range records {
			d, err := store.FindDelivery(ctx, db, r.DeliveryID)
			if err != nil {
				t.Fatal(err)
			}
			if d.Status != "succeeded" {
				complete = false
			}
			if d.AttemptCount > 1 {
				t.Fatal("deferred work consumed extra attempts")
			}
		}
		info, err := inspector.GetQueueInfo(queue)
		if err != nil {
			t.Fatal(err)
		}
		if complete && info.Pending+info.Active+info.Scheduled+info.Retry == 0 {
			if info.Archived != 0 {
				t.Fatal("deferral archived tasks")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("deferred tasks did not drain")
		}
		time.Sleep(30 * time.Millisecond)
	}
	if peakSlow.Load() != 1 || sends.Load() != int32(len(records)) {
		t.Fatalf("slow peak=%d HTTP sends=%d", peakSlow.Load(), sends.Load())
	}
	t.Logf("worker=4 endpoint_limit=1 slow_backlog=%d fast_completed_while_slow_blocked=%s slow_peak=%d total_HTTP=%d; MaxRetry(0) survived deferral", len(backlog), fastLatency, peakSlow.Load(), sends.Load())
}
