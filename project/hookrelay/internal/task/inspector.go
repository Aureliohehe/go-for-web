package task

import (
	"context"
	"errors"
	"time"

	"github.com/hibiken/asynq"
)

type Inspector struct {
	inspector *asynq.Inspector
	queue     string
}

func NewInspector(addr, queue string) *Inspector {
	return &Inspector{queue: queue, inspector: asynq.NewInspector(asynq.RedisClientOpt{
		Addr: addr, DialTimeout: 2 * time.Second, ReadTimeout: 2 * time.Second, WriteTimeout: 2 * time.Second,
	})}
}

func (i *Inspector) Close() error { return i.inspector.Close() }

// Alive treats every nonterminal task state as live, including paused/pending
// queues and scheduled retries. Connection errors must never mean "missing".
// Asynq's inspection API has no context argument; socket timeouts bound its I/O.
func (i *Inspector) Alive(ctx context.Context, queueID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	info, err := i.inspector.GetTaskInfo(i.queue, queueID)
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if errors.Is(err, asynq.ErrTaskNotFound) || errors.Is(err, asynq.ErrQueueNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.State != asynq.TaskStateArchived && info.State != asynq.TaskStateCompleted, nil
}
