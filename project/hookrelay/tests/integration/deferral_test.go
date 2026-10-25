package integration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aurelio/hookrelay/internal/config"
	"github.com/aurelio/hookrelay/internal/task"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

func TestDeferralPreservesRemainingRetries(t *testing.T) {
	if os.Getenv("HOOKRELAY_OUTBOX_INTEGRATION") != "1" {
		t.Skip("requires isolated Redis")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal("load config failed")
	}
	redis := asynq.RedisClientOpt{Addr: cfg.RedisAddr}
	queue := "deferral-" + uuid.NewString()
	client := asynq.NewClient(redis)
	defer client.Close()
	deferrer := task.NewClient(cfg.RedisAddr)
	defer deferrer.Close()
	inspector := asynq.NewInspector(redis)
	defer inspector.Close()
	defer inspector.DeleteQueue(queue, true)
	var calls atomic.Int32
	jobs := asynq.NewServer(redis, asynq.Config{Concurrency: 1, Queues: map[string]int{queue: 1},
		TaskCheckInterval: 20 * time.Millisecond, DelayedTaskCheckInterval: 50 * time.Millisecond,
		RetryDelayFunc:  func(int, error, *asynq.Task) time.Duration { return 100 * time.Millisecond },
		ShutdownTimeout: time.Second,
	})
	handler := asynq.HandlerFunc(func(ctx context.Context, job *asynq.Task) error {
		step := calls.Add(1)
		max, _ := asynq.GetMaxRetry(ctx)
		retried, _ := asynq.GetRetryCount(ctx)
		gotQueue, _ := asynq.GetQueueName(ctx)
		var p task.Payload
		if err := json.Unmarshal(job.Payload(), &p); err != nil || p.DeliveryID != 9 || p.Generation != 2 || gotQueue != queue {
			t.Error("deferral changed identity or queue")
			return asynq.SkipRetry
		}
		wantMax, wantRetried := 2, 0
		switch step {
		case 2:
			wantRetried = 1
		case 3:
			wantMax = 1
		case 4:
			wantMax, wantRetried = 1, 1
		case 5:
			wantMax = 0
		}
		if step > 5 || max != wantMax || retried != wantRetried {
			t.Errorf("step=%d max=%d retried=%d", step, max, retried)
			return asynq.SkipRetry
		}
		if step == 2 || step == 4 {
			_, err := deferrer.DeferDelivery(ctx, p.DeliveryID, p.Generation)
			return err
		}
		return errors.New("injected ordinary failure")
	})
	if err := jobs.Start(handler); err != nil {
		t.Fatal(err)
	}
	defer jobs.Shutdown()
	if _, err := client.Enqueue(asynq.NewTask(task.TypeDelivery, []byte(`{"delivery_id":9,"generation":2}`)), asynq.Queue(queue), asynq.MaxRetry(2)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		info, err := inspector.GetQueueInfo(queue)
		if err != nil {
			t.Fatal(err)
		}
		if info.Archived == 1 && info.Pending+info.Active+info.Scheduled+info.Retry == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("deferred retry sequence did not finish")
		}
		time.Sleep(30 * time.Millisecond)
	}
	if calls.Load() != 5 {
		t.Fatalf("calls=%d: retries lost or replenished", calls.Load())
	}
	t.Log("ordinary failure -> defer(max=1) -> ordinary failure -> defer(max=0) -> final failure archived; no budget replenishment")
}
