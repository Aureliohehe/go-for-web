package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// TrackTask records a deferred replacement and refreshes activity. It cannot
// revive terminal work or overwrite a newer generation. The queue request has
// already finished; no Redis I/O is performed while a database lock is held.
func (s DeliveryStore) TrackTask(ctx context.Context, id uint64, generation uint32, queueID string) error {
	if queueID == "" {
		return errors.New("empty queue task ID")
	}
	return s.DB.WithContext(ctx).Model(&Delivery{}).
		Where("id = ? AND generation = ? AND status IN ?", id, generation, []string{"pending", "processing", "retrying"}).
		Updates(map[string]any{"queue_task_id": queueID, "updated_at": time.Now().UTC().Truncate(time.Microsecond)}).Error
}

// RecoveryCandidates excludes unpublished work (owned by Dispatcher), recent
// activity, execution leases and retry cooldowns, all with the same grace period.
// ID keyset pagination prevents old but healthy queued work starving later rows.
func (s DeliveryStore) RecoveryCandidates(ctx context.Context, after uint64, cutoff time.Time, limit int) ([]Delivery, error) {
	var rows []Delivery
	err := s.DB.WithContext(ctx).Table("deliveries AS d").Select("d.*").
		Joins("JOIN outbox AS o ON o.delivery_id = d.id AND o.generation = d.generation").
		Where("d.id > ? AND d.status IN ? AND d.updated_at <= ?", after, []string{"pending", "processing", "retrying"}, cutoff).
		Where("(d.lease_until IS NULL OR d.lease_until <= ?) AND o.status = 'published' AND o.updated_at <= ?", cutoff, cutoff).
		Order("d.id").Limit(limit).Find(&rows).Error
	return rows, err
}

// Recover is called only AFTER the queue inspector confirms the tracked task is
// missing, archived or completed. Revalidate that snapshot under a short lock:
// another worker/deferral/reconciler may have acted while Redis was inspected.
// Recovery changes the execution generation but never replenishes HTTP attempts.
func (s DeliveryStore) Recover(ctx context.Context, snapshot Delivery, cutoff time.Time, maxAttempts, maxRecoveries uint32) (*Delivery, error) {
	var result *Delivery
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var d Delivery
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", snapshot.ID).Take(&d).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if d.Generation != snapshot.Generation || d.Status != snapshot.Status ||
			d.AttemptCount != snapshot.AttemptCount || d.RecoveryCount != snapshot.RecoveryCount ||
			d.QueueTaskID != snapshot.QueueTaskID || !d.UpdatedAt.Equal(snapshot.UpdatedAt) {
			return nil
		}
		if (d.Status != "pending" && d.Status != "processing" && d.Status != "retrying") ||
			d.UpdatedAt.After(cutoff) || (d.LeaseUntil != nil && d.LeaseUntil.After(cutoff)) {
			return nil
		}
		var entry Outbox
		err = tx.Where("delivery_id = ? AND generation = ? AND status = 'published' AND updated_at <= ?", d.ID, d.Generation, cutoff).Take(&entry).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}

		now := time.Now().UTC().Truncate(time.Microsecond)
		reason := "task_recovered"
		switch {
		case d.AttemptCount >= maxAttempts:
			reason = "attempts_exhausted"
		case d.RecoveryCount >= maxRecoveries:
			reason = "recovery_exhausted"
		case d.Generation == ^uint32(0):
			reason = "generation_exhausted"
		}
		d.Status = "failed"
		if reason == "task_recovered" {
			d.Status = "pending"
			d.Generation++ // Old queued tasks and late HTTP results are fenced out.
			d.RecoveryCount++
		}
		d.QueueTaskID, d.LeaseUntil, d.LastError, d.UpdatedAt = "", nil, &reason, now
		if err := tx.Model(&Delivery{}).Where("id = ?", d.ID).Updates(map[string]any{
			"status": d.Status, "generation": d.Generation, "recovery_count": d.RecoveryCount,
			"queue_task_id": "", "lease_until": nil, "last_error": reason, "updated_at": now,
		}).Error; err != nil {
			return err
		}
		if d.Status == "pending" {
			if err := tx.Create(&Outbox{DeliveryID: d.ID, Generation: d.Generation, Status: "pending", AvailableAt: now, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
				return err // Roll back generation/count/state if publication cannot be persisted.
			}
		}
		result = &d
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, err
	}
	return result, nil
}
