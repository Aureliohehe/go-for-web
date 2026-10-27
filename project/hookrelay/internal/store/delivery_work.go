package store

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrDeliveryBusy = errors.New("delivery lease or retry cooldown is active")

// DeliveryBusyError carries the durable gate so callers can schedule one wait
// rather than poll an active lease/cooldown every few seconds. Claim still checks
// the gate again when the replacement runs; it may have changed in the meantime.
type DeliveryBusyError struct {
	Until time.Time
}

func (e *DeliveryBusyError) Error() string { return ErrDeliveryBusy.Error() }
func (e *DeliveryBusyError) Unwrap() error { return ErrDeliveryBusy }

type DeliveryWork struct {
	AttemptID uint64
	Delivery  Delivery
	Event     Event
	Endpoint  Endpoint
}

type DeliveryStore struct{ DB *gorm.DB }

// EndpointID is a read-only preflight for the process-local limiter. It does not
// claim work or consume an attempt. Events' endpoint IDs are immutable; Claim
// still rechecks generation, terminal state, enabled state and lease under lock.
// Zero means missing, terminal or stale work and can be acknowledged immediately.
func (s DeliveryStore) EndpointID(ctx context.Context, id uint64, generation uint32) (uint64, error) {
	var row struct{ EndpointID uint64 }
	err := s.DB.WithContext(ctx).Table("deliveries AS d").
		Select("e.endpoint_id").Joins("JOIN events AS e ON e.id = d.event_id").
		Where("d.id = ? AND d.generation = ? AND d.status IN ?", id, generation, []string{"pending", "processing", "retrying"}).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	return row.EndpointID, err
}

// Claim holds a row lock only during database work, never during HTTP.
// A nil work item means terminal, missing, stale generation or budget exhausted.
func (s DeliveryStore) Claim(ctx context.Context, id uint64, generation uint32, maxAttempts uint32, lease time.Duration) (*DeliveryWork, error) {
	var work *DeliveryWork
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var d Delivery
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&d).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if d.Generation != generation || d.Status == "succeeded" || d.Status == "failed" {
			return nil
		}
		now := time.Now().UTC().Truncate(time.Microsecond)
		// processing: execution lease; retrying: earliest permitted retry.
		// This also fences duplicate queue tasks that arrive before Asynq's
		// delayed retry, without consuming another attempt or history row.
		if d.LeaseUntil != nil && d.LeaseUntil.After(now) {
			return &DeliveryBusyError{Until: *d.LeaseUntil}
		}
		var event Event
		if err := tx.Where("id = ?", d.EventID).Take(&event).Error; err != nil {
			return err
		}
		var endpoint Endpoint
		if err := tx.Where("id = ?", event.EndpointID).Take(&endpoint).Error; err != nil {
			return err
		}
		reason := ""
		if !endpoint.Enabled {
			reason = "endpoint_disabled"
		}
		if d.AttemptCount >= maxAttempts {
			reason = "attempts_exhausted"
		}
		if reason != "" {
			return tx.Model(&d).Updates(map[string]any{"status": "failed", "last_error": reason, "lease_until": nil}).Error
		}
		until := now.Add(lease)
		d.AttemptCount++
		d.Status = "processing"
		d.LeaseUntil = &until
		if err := tx.Model(&d).Updates(map[string]any{
			"status": d.Status, "attempt_count": d.AttemptCount, "lease_until": until,
		}).Error; err != nil {
			return err
		}
		attempt := DeliveryAttempt{
			DeliveryID: d.ID, Generation: d.Generation, AttemptNumber: d.AttemptCount,
			Status: "processing", StartedAt: now,
		}
		if err := tx.Create(&attempt).Error; err != nil {
			return err
		}
		work = &DeliveryWork{AttemptID: attempt.ID, Delivery: d, Event: event, Endpoint: endpoint}
		return nil
	})
	return work, err
}

// Attempt count is an execution token: a late result cannot overwrite a newer
// attempt or generation. A crashed attempt consumes budget even before HTTP.
// retryDelay gates the next Claim only for retrying results (zero means immediate).
// It is persisted with the result, not scheduled by a separate database poller.
func (s DeliveryStore) Finish(ctx context.Context, work *DeliveryWork, status string, httpStatus int, reason string, latency, retryDelay time.Duration) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var notBefore *time.Time
		if status == "retrying" && retryDelay > 0 {
			until := time.Now().UTC().Add(retryDelay).Truncate(time.Microsecond)
			notBefore = &until
		}
		// Lock/update delivery before history, in the same order as Claim.
		updated := tx.Model(&Delivery{}).
			Where("id = ? AND generation = ? AND attempt_count = ? AND status = ?",
				work.Delivery.ID, work.Delivery.Generation, work.Delivery.AttemptCount, "processing").
			Updates(map[string]any{
				"status": status, "last_http_status": httpStatus, "last_error": reason, "lease_until": notBefore,
			})
		if updated.Error != nil {
			return updated.Error
		}
		var code *int
		if httpStatus != 0 {
			code = &httpStatus
		}
		var elapsed *int64
		if reason != "endpoint_not_allowed" {
			ms := latency.Milliseconds()
			elapsed = &ms
		}
		// A late result may finish its own history, but cannot change a newer
		// delivery execution. An already finished history row is immutable.
		history := tx.Model(&DeliveryAttempt{}).
			Where("id = ? AND delivery_id = ? AND generation = ? AND attempt_number = ? AND finished_at IS NULL",
				work.AttemptID, work.Delivery.ID, work.Delivery.Generation, work.Delivery.AttemptCount).
			Updates(map[string]any{
				"status": status, "finished_at": time.Now().UTC().Truncate(time.Microsecond),
				"http_status": code, "error_class": reason, "latency_ms": elapsed,
			})
		if history.Error != nil {
			return history.Error
		}
		if updated.RowsAffected != 0 && history.RowsAffected == 0 {
			return errors.New("delivery attempt missing or already finished")
		}
		return nil
	})
}
