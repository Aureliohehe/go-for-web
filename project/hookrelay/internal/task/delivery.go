package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
