package store

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Outbox struct {
	ID              uint64 `gorm:"primaryKey;autoIncrement"`
	DeliveryID      uint64
	Generation      uint32
	Status          string
	AvailableAt     time.Time
	LeaseUntil      *time.Time
	PublishAttempts uint32
	LastError       string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (Outbox) TableName() string { return "outbox" }

type OutboxStore struct{ DB *gorm.DB }

// AvailableAt means retry time for pending rows and lease expiry for processing
// rows, so one indexed scan also recovers a crashed publisher's work.
func (s OutboxStore) Claim(ctx context.Context, lease time.Duration) (*Outbox, error) {
	var claimed *Outbox
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC().Truncate(time.Microsecond)
		var entry Outbox
		err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("status IN ? AND available_at <= ?", []string{"pending", "processing"}, now).
			Order("available_at, id").Take(&entry).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		until := now.Add(lease)
		entry.PublishAttempts++
		entry.Status, entry.AvailableAt, entry.LeaseUntil = "processing", until, &until
		if err := tx.Model(&entry).Updates(map[string]any{
			"status": entry.Status, "available_at": until, "lease_until": until,
			"publish_attempts": entry.PublishAttempts,
		}).Error; err != nil {
			return err
		}
		claimed = &entry
		return nil
	})
	return claimed, err
}

func (s OutboxStore) Published(ctx context.Context, entry *Outbox) error {
	return s.current(ctx, entry).Updates(map[string]any{
		"status": "published", "lease_until": nil, "last_error": "",
	}).Error
}

func (s OutboxStore) Retry(ctx context.Context, entry *Outbox, delay time.Duration) error {
	return s.current(ctx, entry).Updates(map[string]any{
		"status": "pending", "lease_until": nil,
		"available_at": time.Now().UTC().Add(delay), "last_error": "enqueue_failed",
	}).Error
}

// An expired claimant must not overwrite the next claimant's result.
func (s OutboxStore) current(ctx context.Context, entry *Outbox) *gorm.DB {
	return s.DB.WithContext(ctx).Model(&Outbox{}).
		Where("id = ? AND status = 'processing' AND publish_attempts = ?", entry.ID, entry.PublishAttempts)
}
