package reconcile

import (
	"context"
	"log/slog"
	"time"

	"github.com/aurelio/hookrelay/internal/store"
	"github.com/aurelio/hookrelay/internal/task"
)

type Repository interface {
	RecoveryCandidates(context.Context, uint64, time.Time, int) ([]store.Delivery, error)
	Recover(context.Context, store.Delivery, time.Time, uint32, uint32) (*store.Delivery, error)
}

type Reconciler struct {
	Store         Repository
	Alive         func(context.Context, string) (bool, error)
	Grace         time.Duration
	Interval      time.Duration
	BatchSize     int
	MaxRecoveries uint32
}

// Batch inspects Redis outside database transactions. The returned cursor wraps
// after a complete pass, including rows skipped because their queue task is live.
func (r Reconciler) Batch(ctx context.Context, after uint64) (uint64, error) {
	cutoff := time.Now().UTC().Add(-r.Grace)
	rows, err := r.Store.RecoveryCandidates(ctx, after, cutoff, r.BatchSize)
	if err != nil {
		return after, err
	}
	for _, d := range rows {
		queueID := d.QueueTaskID
		if queueID == "" {
			queueID = task.DeliveryTaskID(d.ID, d.Generation)
		}
		alive, err := r.Alive(ctx, queueID)
		if err != nil {
			return after, err // Keep this row for the next pass; fail closed.
		}
		if !alive {
			updated, err := r.Store.Recover(ctx, d, cutoff, task.MaxAttempts, r.MaxRecoveries)
			if err != nil {
				return after, err
			}
			if updated != nil {
				reason := ""
				if updated.LastError != nil {
					reason = *updated.LastError
				}
				slog.Info("delivery reconciled", "delivery_id", d.ID, "old_generation", d.Generation,
					"generation", updated.Generation, "status", updated.Status, "recovery_count", updated.RecoveryCount,
					"attempt_count", updated.AttemptCount, "reason", reason)
			}
		}
		after = d.ID
	}
	if len(rows) < r.BatchSize {
		return 0, nil
	}
	return after, nil
}

func (r Reconciler) Run(ctx context.Context) {
	var after uint64
	for ctx.Err() == nil {
		var err error
		after, err = r.Batch(ctx, after)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			slog.Warn("reconciliation failed; will retry") // Do not log driver errors/credentials.
		}
		timer := time.NewTimer(r.Interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
