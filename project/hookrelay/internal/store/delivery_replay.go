package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrDeliveryNotReplayable = errors.New("only failed deliveries can be replayed")

// ReplayDelivery preserves the event, delivery ID and history. The generation
// change and its publication record commit together. No Redis request is made.
func ReplayDelivery(ctx context.Context, db *gorm.DB, id uint64, maxPending int64) (Delivery, error) {
	return replayDelivery(ctx, db, id, maxPending, func(tx *gorm.DB, entry *Outbox) error {
		return tx.Create(entry).Error
	})
}

func replayDelivery(ctx context.Context, db *gorm.DB, id uint64, maxPending int64,
	insertOutbox func(*gorm.DB, *Outbox) error,
) (Delivery, error) {
	var result Delivery
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// New events and replays compete for the same capacity, in the same
		// lock order. A failed transaction releases the gate without a counter.
		if err := lockAdmission(tx); err != nil {
			return err
		}
		var d Delivery
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&d).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrDeliveryNotFound
		}
		if err != nil {
			return err
		}
		if d.Status != "failed" {
			return ErrDeliveryNotReplayable
		}

		var event Event
		if err := tx.Select("endpoint_id").Where("id = ?", d.EventID).Take(&event).Error; err != nil {
			return err
		}
		var endpoint Endpoint
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Select("id", "enabled").
			Where("id = ?", event.EndpointID).Take(&endpoint).Error; err != nil {
			return err
		}
		if !endpoint.Enabled {
			return ErrEndpointDisabled
		}
		if err := checkBacklog(tx, maxPending); err != nil {
			return err
		}

		now := time.Now().UTC().Truncate(time.Microsecond)
		updated := tx.Model(&Delivery{}).Where("id = ? AND status = 'failed' AND generation = ?", id, d.Generation).
			Updates(map[string]any{
				"status": "pending", "generation": d.Generation + 1, "attempt_count": 0,
				"last_http_status": nil, "last_error": nil, "lease_until": nil, "updated_at": now,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return ErrDeliveryNotReplayable
		}
		d.Generation++
		d.Status, d.AttemptCount = "pending", 0
		d.LastHTTPStatus, d.LastError, d.LeaseUntil = nil, nil, nil
		d.UpdatedAt = now
		entry := Outbox{DeliveryID: d.ID, Generation: d.Generation, Status: "pending", AvailableAt: now, CreatedAt: now, UpdatedAt: now}
		if err := insertOutbox(tx, &entry); err != nil {
			return fmt.Errorf("create replay outbox: %w", err)
		}
		result = d
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return Delivery{}, err
	}
	return result, nil
}
