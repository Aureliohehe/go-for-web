package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/hibiken/asynq"
)

const (
	TypeDelivery  = "webhook:deliver"
	Queue         = "webhooks"
	MaxAttempts   = 3
	HTTPTimeout   = 5 * time.Second
	TaskTimeout   = 20 * time.Second
	ResultTimeout = 5 * time.Second  // Bounded result persistence after task cancellation.
	LeaseDuration = 30 * time.Second // Longer than TaskTimeout + ResultTimeout.
)

type Payload struct {
	DeliveryID uint64 `json:"delivery_id"`
	Generation uint32 `json:"generation"`
}

type Client struct{ client *asynq.Client }

func NewClient(addr string) *Client {
	return &Client{client: asynq.NewClient(asynq.RedisClientOpt{Addr: addr})}
}

func (c *Client) Close() error { return c.client.Close() }

// DeferDelivery is only called from an active Asynq handler. A saturated endpoint
// or an active execution lease/retry cooldown is not an HTTP failure. Enqueue a delayed
// replacement BEFORE acknowledging the old task, even if it has MaxRetry(0).
// The replacement needs a new queue task ID: the old ID is still active.
// Business delivery ID/generation remain unchanged.
// A crash between enqueue and acknowledgement can duplicate tasks; DB claims and
// receiver idempotency still protect execution. The caller must persist the
// returned queue task ID before acknowledging the original task.
func (c *Client) DeferDelivery(ctx context.Context, id uint64, generation uint32) (string, error) {
	return c.DeferDeliveryUntil(ctx, id, generation, time.Time{})
}

// DeferDeliveryUntil schedules after both now and the observed database gate,
// with jitter to spread waiters. A zero/past gate is ordinary endpoint deferral.
// No attempt budget is consumed by waiting, or replenished by the replacement.
func (c *Client) DeferDeliveryUntil(ctx context.Context, id uint64, generation uint32, notBefore time.Time) (string, error) {
	queue, hasQueue := asynq.GetQueueName(ctx)
	max, hasMax := asynq.GetMaxRetry(ctx)
	retried, hasRetried := asynq.GetRetryCount(ctx)
	if !hasQueue || queue == "" || !hasMax || !hasRetried || max < 0 || retried < 0 || retried > max {
		return "", errors.New("missing or invalid Asynq deferral context")
	}
	body, err := json.Marshal(Payload{DeliveryID: id, Generation: generation})
	if err != nil {
		return "", err
	}
	base := time.Now()
	if notBefore.After(base) {
		base = notBefore
	}
	processAt := base.Add(2*time.Second + time.Duration(rand.Int64N(int64(2*time.Second))))
	// Asynq persists Unix seconds. Round upward, never schedule before the gate.
	if rounded := processAt.Truncate(time.Second); rounded.Before(processAt) {
		processAt = rounded.Add(time.Second)
	}
	info, err := c.client.EnqueueContext(ctx, asynq.NewTask(TypeDelivery, body),
		asynq.Queue(queue), asynq.MaxRetry(max-retried), asynq.Timeout(TaskTimeout), asynq.ProcessAt(processAt))
	if err != nil {
		return "", err
	}
	return info.ID, nil
}

func DeliveryTaskID(id uint64, generation uint32) string {
	return fmt.Sprintf("delivery-%d-%d", id, generation)
}

// Stable task IDs suppress duplicate publications while a task exists;
// the database remains authoritative after a completed task is removed.
func (c *Client) EnqueueDelivery(ctx context.Context, id uint64, generation uint32) error {
	payload, err := json.Marshal(Payload{DeliveryID: id, Generation: generation})
	if err != nil {
		return err
	}
	_, err = c.client.EnqueueContext(ctx, asynq.NewTask(TypeDelivery, payload),
		asynq.Queue(Queue), asynq.TaskID(DeliveryTaskID(id, generation)),
		asynq.MaxRetry(MaxAttempts-1), asynq.Timeout(TaskTimeout))
	if errors.Is(err, asynq.ErrTaskIDConflict) {
		return nil
	}
	return err
}
