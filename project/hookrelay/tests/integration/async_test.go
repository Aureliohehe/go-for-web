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
	"testing"
	"time"

	"github.com/aurelio/hookrelay/internal/config"
	"github.com/aurelio/hookrelay/internal/delivery"
	"github.com/aurelio/hookrelay/internal/service"
	"github.com/aurelio/hookrelay/internal/store"
	"github.com/aurelio/hookrelay/internal/task"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

// Uses a unique queue and removes only records created by this test.
func TestAsyncDelivery(t *testing.T) {
	if os.Getenv("HOOKRELAY_INTEGRATION") != "1" {
		t.Skip("requires MySQL migrations and Redis")
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
	events := service.Events{
		Create: func(ctx context.Context, input store.EventInput) (store.EventResult, error) {
			return store.CreateOrGetEventDelivery(ctx, db, input)
		},
		Enqueue: func(ctx context.Context, id uint64) error {
			body, _ := json.Marshal(task.Payload{DeliveryID: id, Generation: 1})
			_, err := client.EnqueueContext(ctx, asynq.NewTask(task.TypeDelivery, body), asynq.Queue(queue), asynq.MaxRetry(2), asynq.Timeout(task.TaskTimeout))
			return err
		},
	}
	for _, tt := range []struct {
		name, status string
		attempts     int
	}{
		{"success", "succeeded", 1}, {"temporary", "succeeded", 2}, {"permanent", "failed", 1}, {"exhausted", "failed", 3},
	} {
		t.Run(tt.name, func(t *testing.T) {
			input := store.EventInput{EndpointID: endpoint.ID, IdempotencyKey: uuid.NewString(), EventType: "test", Payload: []byte(fmt.Sprintf("%q", tt.name))}
			result, err := events.Accept(ctx, input)
			if result.EventID != 0 {
				results = append(results, result)
			}
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(10 * time.Second)
			for {
				d, err := store.FindDelivery(ctx, db, result.DeliveryID)
				if err != nil {
					t.Fatal(err)
				}
				if d.Status == tt.status {
					mu.Lock()
					count := counts[string(input.Payload)]
					mu.Unlock()
					if int(d.AttemptCount) != tt.attempts || count != tt.attempts {
						t.Fatalf("attempts=%d HTTP sends=%d", d.AttemptCount, count)
					}
					return
				}
				if time.Now().After(deadline) {
					t.Fatalf("timed out with status %s", d.Status)
				}
				time.Sleep(30 * time.Millisecond)
			}
		})
	}
}
