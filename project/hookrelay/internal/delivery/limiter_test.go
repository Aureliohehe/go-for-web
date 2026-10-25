package delivery

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aurelio/hookrelay/internal/store"
	"github.com/aurelio/hookrelay/internal/task"
	"github.com/hibiken/asynq"
)

func TestEndpointLimiterConcurrent(t *testing.T) {
	limiter := NewEndpointLimiter(2)
	const n = 32
	acquired := make(chan func(), n)
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if release, ok := limiter.TryAcquire(1); ok {
				acquired <- release
			}
		}()
	}
	wg.Wait()
	if len(acquired) != 2 {
		t.Fatalf("holders=%d", len(acquired))
	}
	other, ok := limiter.TryAcquire(2)
	if !ok {
		t.Fatal("one endpoint blocked another")
	}
	other()
	close(acquired)
	for release := range acquired {
		release()
		release()
	}
	if len(limiter.active) != 0 {
		t.Fatal("idle endpoint entries leaked")
	}
	release, ok := limiter.TryAcquire(1)
	if !ok {
		t.Fatal("capacity not released")
	}
	release()
}

func TestWorkerSaturatedEndpoint(t *testing.T) {
	limiter := NewEndpointLimiter(1)
	release, _ := limiter.TryAcquire(7)
	defer release()
	repo := &testRepository{endpointID: 7}
	deferrals := 0
	worker := Worker{Store: repo, Limiter: limiter, Defer: func(_ context.Context, id uint64, generation uint32) (string, error) {
		deferrals++
		if id != 9 || generation != 2 {
			t.Fatal("deferred identity changed")
		}
		return "replacement-task", nil
	}, Send: func(context.Context, Request) (Result, error) {
		t.Fatal("saturated endpoint sent HTTP")
		return Result{}, nil
	}}
	job := asynq.NewTask(task.TypeDelivery, []byte(`{"delivery_id":9,"generation":2}`))
	for range 5 {
		if err := worker.ProcessTask(context.Background(), job); err != nil {
			t.Fatal(err)
		}
	}
	if deferrals != 5 || repo.claims != 0 || repo.finishes != 0 {
		t.Fatal("deferral consumed attempts or skipped scheduling")
	}
	worker.Defer = func(context.Context, uint64, uint32) (string, error) { return "", errors.New("redis unavailable") }
	if err := worker.ProcessTask(context.Background(), job); err == nil {
		t.Fatal("failed enqueue must not acknowledge task")
	}
	worker.Defer = nil
	if err := worker.ProcessTask(context.Background(), job); err == nil {
		t.Fatal("missing deferrer must not acknowledge task")
	}
	repo.endpointID = 0
	if err := worker.ProcessTask(context.Background(), job); err != nil {
		t.Fatal("stale work should skip even when full")
	}
	repo.lookupErr = errors.New("DB unavailable")
	if err := worker.ProcessTask(context.Background(), job); err == nil || repo.claims != 0 {
		t.Fatal("lookup failure ignored")
	}
}

func TestWorkerReleasesEndpointPermit(t *testing.T) {
	for _, mode := range []string{"success", "terminal after lookup", "claim error", "finish error", "canceled", "panic"} {
		t.Run(mode, func(t *testing.T) {
			limiter := NewEndpointLimiter(1)
			repo := &testRepository{endpointID: 7, work: &store.DeliveryWork{
				Delivery: store.Delivery{ID: 9, Generation: 1, AttemptCount: 1}, Endpoint: store.Endpoint{URL: "http://receiver"},
			}}
			if mode == "terminal after lookup" {
				repo.work = nil
			}
			if mode == "claim error" {
				repo.claimErr = errors.New("db")
			}
			if mode == "finish error" {
				repo.finishErr = errors.New("db")
			}
			worker := Worker{Store: repo, Limiter: limiter, AllowedURL: "http://receiver", Send: func(context.Context, Request) (Result, error) {
				if mode == "panic" {
					panic("injected sender panic")
				}
				if mode == "canceled" {
					return Result{Retryable: true}, context.Canceled
				}
				return Result{HTTPStatus: 204, Latency: time.Millisecond}, nil
			}}
			func() {
				defer func() {
					if p := recover(); p != nil && mode != "panic" {
						t.Errorf("unexpected panic: %v", p)
					}
				}()
				_ = worker.ProcessTask(context.Background(), asynq.NewTask(task.TypeDelivery, []byte(`{"delivery_id":9,"generation":1}`)))
			}()
			release, ok := limiter.TryAcquire(7)
			if !ok {
				t.Fatal("permit leaked")
			}
			release()
		})
	}
}
