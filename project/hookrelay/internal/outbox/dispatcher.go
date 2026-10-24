package outbox

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/aurelio/hookrelay/internal/store"
)

const (
	LeaseDuration  = 30 * time.Second
	PublishTimeout = 5 * time.Second
	PollInterval   = time.Second
)

type Repository interface {
	Claim(context.Context, time.Duration) (*store.Outbox, error)
	Published(context.Context, *store.Outbox) error
	Retry(context.Context, *store.Outbox, time.Duration) error
}

type Dispatcher struct {
	Store   Repository
	Enqueue func(context.Context, uint64, uint32) error
}

// DispatchOne commits the claim before contacting Redis. A failed database
// acknowledgement leaves the lease recoverable, even if enqueue succeeded.
func (d Dispatcher) DispatchOne(ctx context.Context) (bool, error) {
	entry, err := d.Store.Claim(ctx, LeaseDuration)
	if err != nil || entry == nil {
		return false, err
	}
	publishCtx, cancel := context.WithTimeout(ctx, PublishTimeout)
	err = d.Enqueue(publishCtx, entry.DeliveryID, entry.Generation)
	cancel()
	if err != nil {
		if err := d.Store.Retry(ctx, entry, retryDelay(entry.PublishAttempts)); err != nil {
			return true, err
		}
		slog.Warn("outbox enqueue failed", "outbox_id", entry.ID, "delivery_id", entry.DeliveryID)
		return true, errors.New("outbox enqueue failed; retry scheduled")
	}
	if err := d.Store.Published(ctx, entry); err != nil {
		return true, err
	}
	slog.Info("outbox published", "outbox_id", entry.ID, "delivery_id", entry.DeliveryID, "generation", entry.Generation)
	return true, nil
}

func (d Dispatcher) Run(ctx context.Context) {
	for ctx.Err() == nil {
		worked, err := d.DispatchOne(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			slog.Warn("dispatcher iteration failed; will retry")
		}
		if !worked || err != nil {
			timer := time.NewTimer(PollInterval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
}

func retryDelay(attempt uint32) time.Duration {
	delay := 5 * time.Second
	for i := uint32(1); i < attempt && delay < time.Minute; i++ {
		delay *= 2
	}
	if delay > time.Minute {
		return time.Minute
	}
	return delay
}
