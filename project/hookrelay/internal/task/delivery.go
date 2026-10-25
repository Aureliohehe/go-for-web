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
	LeaseDuration = 30 * time.Second
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
// is not an HTTP failure. Enqueue a delayed replacement BEFORE acknowledging the
// old task, even if it has MaxRetry(0). The replacement needs a new queue task ID:
// the old ID is still active. Business delivery ID/generation remain unchanged.
// A crash between enqueue and acknowledgement can duplicate tasks; DB claims and
// receiver idempotency still protect execution. Redis data loss remains a limit.
func (c *Client) DeferDelivery(ctx context.Context, id uint64, generation uint32) error {
	queue, hasQueue := asynq.GetQueueName(ctx)
	max, hasMax := asynq.GetMaxRetry(ctx)
	retried, hasRetried := asynq.GetRetryCount(ctx)
	if !hasQueue || !hasMax || !hasRetried || retried > max {
		return errors.New("missing or invalid Asynq deferral context")
	}
	body, err := json.Marshal(Payload{DeliveryID: id, Generation: generation})
	if err != nil {
		return err
	}
	delay := 2*time.Second + time.Duration(rand.Int64N(int64(2*time.Second)))
	_, err = c.client.EnqueueContext(ctx, asynq.NewTask(TypeDelivery, body),
		asynq.Queue(queue), asynq.MaxRetry(max-retried), asynq.Timeout(TaskTimeout), asynq.ProcessIn(delay))
	return err
}

// Stable task IDs suppress duplicate publications while a task exists;
// the database remains authoritative after a completed task is removed.
func (c *Client) EnqueueDelivery(ctx context.Context, id uint64, generation uint32) error {
	payload, err := json.Marshal(Payload{DeliveryID: id, Generation: generation})
	if err != nil {
		return err
	}
	_, err = c.client.EnqueueContext(ctx, asynq.NewTask(TypeDelivery, payload),
		asynq.Queue(Queue), asynq.TaskID(fmt.Sprintf("delivery-%d-%d", id, generation)),
		asynq.MaxRetry(MaxAttempts-1), asynq.Timeout(TaskTimeout))
	if errors.Is(err, asynq.ErrTaskIDConflict) {
		return nil
	}
	return err
}
