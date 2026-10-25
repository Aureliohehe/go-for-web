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
	"github.com/aurelio/hookrelay/internal/handler"
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
	deliveryIDs := map[string]string{}
	retryDelays := map[uint64][]time.Duration{}
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		counts[string(body)]++
		if previous := deliveryIDs[string(body)]; previous != "" && previous != r.Header.Get("X-Webhook-ID") {
			t.Error("delivery ID changed between attempts")
		}
		deliveryIDs[string(body)] = r.Header.Get("X-Webhook-ID")
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
		case `"rate-limited"`:
			if count == 1 {
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(429)
				return
			}
		case `"retry-date"`:
			if count == 1 {
				w.Header().Set("Retry-After", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat))
				w.WriteHeader(503)
				return
			}
		case `"retry-cap"`:
			if count == 1 {
				w.Header().Set("Retry-After", "999999999999999999999999")
				w.WriteHeader(503)
				return
			}
		case `"retry-invalid"`:
			if count == 1 {
				w.Header().Set("Retry-After", "-1")
				w.WriteHeader(503)
				return
			}
		case `"retry-duplicate"`:
			if count == 1 {
				w.Header().Add("Retry-After", "1")
				w.Header().Add("Retry-After", "2")
				w.WriteHeader(503)
				return
			}
		case `"replay"`:
			if count == 1 {
				w.WriteHeader(400)
				return
			}
		case `"permanent"`:
			w.Header().Set("Retry-After", "3600")
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
			if err := db.Where("delivery_id = ?", result.DeliveryID).Delete(&store.DeliveryAttempt{}).Error; err != nil {
				t.Error(err)
			}
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
	retryPolicy := delivery.RetryPolicy{BaseDelay: 100 * time.Millisecond, MaxDelay: 2 * time.Second}
	server := asynq.NewServer(redis, asynq.Config{
		Concurrency: 1, Queues: map[string]int{queue: 1},
		TaskCheckInterval: 50 * time.Millisecond, DelayedTaskCheckInterval: 100 * time.Millisecond,
		RetryDelayFunc: func(n int, err error, job *asynq.Task) time.Duration {
			delay := retryPolicy.Delay(n, err, job)
			var payload task.Payload
			if err := json.Unmarshal(job.Payload(), &payload); err != nil {
				t.Error(err)
			}
			mu.Lock()
			retryDelays[payload.DeliveryID] = append(retryDelays[payload.DeliveryID], delay)
			mu.Unlock()
			return delay
		},
		ShutdownTimeout: 3 * time.Second,
	})
	mux := asynq.NewServeMux()
	mux.Handle(task.TypeDelivery, worker)
	if err := server.Start(mux); err != nil {
		t.Fatal(err)
	}
	defer server.Shutdown()
	api := httptest.NewServer(handler.NewAPIRouter(pool.PingContext, "test-key", receiver.URL, nil, nil, nil, nil, nil,
		func(ctx context.Context, id, after uint64, limit int) ([]store.DeliveryAttempt, error) {
			return store.ListDeliveryAttempts(ctx, db, id, after, limit)
		}, func(ctx context.Context, id uint64) (store.Delivery, error) {
			return store.ReplayDelivery(ctx, db, id, 10000)
		}))
	defer api.Close()
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
		result, err := store.CreateOrGetEventDelivery(ctx, db, input, 10000)
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
		{"rate-limited", "succeeded", 2}, {"retry-date", "succeeded", 2}, {"retry-cap", "succeeded", 2}, {"retry-invalid", "succeeded", 2}, {"retry-duplicate", "succeeded", 2},
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
			history, err := store.ListDeliveryAttempts(ctx, db, result.DeliveryID, 0, 100)
			if err != nil || len(history) != tt.attempts {
				t.Fatalf("attempt history: rows=%d err=%v", len(history), err)
			}
			for i, a := range history {
				wantCode, wantStatus := 204, tt.status
				if tt.name == "permanent" {
					wantCode = 400
				}
				if tt.name == "exhausted" {
					wantCode = 503
				}
				if i < len(history)-1 {
					wantStatus = "retrying"
					switch tt.name {
					case "temporary":
						wantCode = 500
					case "rate-limited":
						wantCode = 429
					case "retry-date", "retry-cap", "retry-invalid", "retry-duplicate":
						wantCode = 503
					}
				}
				if a.AttemptNumber != uint32(i+1) || a.Generation != 1 || a.Status != wantStatus || a.HTTPStatus == nil || *a.HTTPStatus != wantCode || a.FinishedAt == nil || a.LatencyMS == nil {
					t.Fatalf("incorrect attempt: %+v", a)
				}
			}
			// Exercise the real authenticated HTTP route and GORM cursor query.
			after := "0"
			for i := 0; i < tt.attempts; i++ {
				req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/v1/deliveries/%d/attempts?limit=1&after=%s", api.URL, result.DeliveryID, after), nil)
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("X-API-Key", "test-key")
				resp, err := api.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				var page struct {
					Items []struct {
						ID     string `json:"id"`
						Number int    `json:"attempt_number"`
						Status string `json:"status"`
					} `json:"items"`
					Next string `json:"next_after"`
				}
				err = json.NewDecoder(resp.Body).Decode(&page)
				resp.Body.Close()
				if err != nil || resp.StatusCode != 200 || len(page.Items) != 1 || page.Items[0].Number != i+1 || page.Items[0].Status != history[i].Status {
					t.Fatalf("invalid history HTTP page: %+v err=%v", page, err)
				}
				if i < tt.attempts-1 && page.Next != page.Items[0].ID || i == tt.attempts-1 && page.Next != "" {
					t.Fatal("incorrect next cursor")
				}
				after = page.Next
			}
			waitQueueFinal(t, result.DeliveryID, tt.status == "failed")
			mu.Lock()
			delays := append([]time.Duration(nil), retryDelays[result.DeliveryID]...)
			mu.Unlock()
			if len(delays) != tt.attempts-1 {
				t.Fatalf("retry count=%d want=%d", len(delays), tt.attempts-1)
			}
			for n, delay := range delays {
				switch tt.name {
				case "rate-limited":
					if delay != time.Second {
						t.Fatalf("seconds Retry-After not used: %v", delay)
					}
				case "retry-date", "retry-cap":
					if delay != retryPolicy.MaxDelay {
						t.Fatalf("Retry-After not capped: %v", delay)
					}
				default:
					ceiling := retryPolicy.BaseDelay * time.Duration(1<<n)
					if delay < ceiling/2 || delay > ceiling {
						t.Fatalf("incorrect fallback backoff: %v", delay)
					}
				}
			}
			if entryFor(t, result.DeliveryID).Status != "published" {
				t.Fatal("outbox must be published")
			}
		})
	}

	t.Run("HTTP replay preserves ID and history across generations", func(t *testing.T) {
		result := create(t, "replay")
		dispatch(t)
		waitDelivery(t, result.DeliveryID, "failed")
		waitQueueFinal(t, result.DeliveryID, true)
		replay := func(want int) {
			req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/v1/deliveries/%d/replay", api.URL, result.DeliveryID), nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("X-API-Key", "test-key")
			resp, err := api.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			var got map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != want {
				t.Fatalf("replay HTTP status=%d response=%v", resp.StatusCode, got)
			}
			if want == 202 && (got["delivery_id"] != fmt.Sprint(result.DeliveryID) || got["event_id"] != fmt.Sprint(result.EventID) || got["generation"] != float64(2) || got["status"] != "pending") {
				t.Fatal("replay changed IDs or wrong generation")
			}
		}
		replay(202)
		replay(409) // Already pending; a second replay must not create another round.

		// Force an old-generation task past queue deduplication to verify the
		// Worker checks the database generation, not just the Redis task ID.
		body, _ := json.Marshal(task.Payload{DeliveryID: result.DeliveryID, Generation: 1})
		staleID := "stale-" + uuid.NewString()
		if _, err := client.EnqueueContext(ctx, asynq.NewTask(task.TypeDelivery, body), asynq.Queue(queue), asynq.TaskID(staleID), asynq.MaxRetry(0)); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			_, err := inspector.GetTaskInfo(queue, staleID)
			if errors.Is(err, asynq.ErrTaskNotFound) {
				break
			}
			if err != nil || time.Now().After(deadline) {
				t.Fatalf("stale task not consumed: %v", err)
			}
			time.Sleep(20 * time.Millisecond)
		}
		pending, err := store.FindDelivery(ctx, db, result.DeliveryID)
		if err != nil || pending.Generation != 2 || pending.Status != "pending" || pending.AttemptCount != 0 {
			t.Fatal("old task changed replay state")
		}
		dispatch(t)
		completed := waitDelivery(t, result.DeliveryID, "succeeded")
		if completed.Generation != 2 || completed.AttemptCount != 1 || completed.EventID != result.EventID {
			t.Fatal("replayed delivery did not complete in a new generation")
		}
		history, err := store.ListDeliveryAttempts(ctx, db, result.DeliveryID, 0, 100)
		if err != nil || len(history) != 2 || history[0].Generation != 1 || history[0].Status != "failed" || history[1].Generation != 2 || history[1].AttemptNumber != 1 || history[1].Status != "succeeded" {
			t.Fatal("replay did not preserve both generations of history")
		}
		mu.Lock()
		sent := counts[`"replay"`]
		mu.Unlock()
		if sent != 2 {
			t.Fatalf("stale task caused an extra send: %d", sent)
		}
		replay(409) // Success cannot be replayed.
	})

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
		if history, err := store.ListDeliveryAttempts(ctx, db, result.DeliveryID, 0, 100); err != nil || len(history) != 1 {
			t.Fatal("duplicate task created extra history")
		}
		if got := entryFor(t, result.DeliveryID); got.Status != "published" || got.PublishAttempts != 2 {
			t.Fatal("duplicate publication not completed")
		}
	})
}
