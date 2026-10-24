package store

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrDeliveryBusy = errors.New("delivery lease is active")

type DeliveryWork struct {
	Delivery Delivery
	Event    Event
	Endpoint Endpoint
}

type DeliveryStore struct{ DB *gorm.DB }

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
		now := time.Now().UTC()
		if d.LeaseUntil != nil && d.LeaseUntil.After(now) {
			return ErrDeliveryBusy
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
		work = &DeliveryWork{Delivery: d, Event: event, Endpoint: endpoint}
		return nil
	})
	return work, err
}

// Attempt count is an execution token: a late result cannot overwrite a newer
// attempt or generation. A crashed attempt consumes budget even before HTTP.
func (s DeliveryStore) Finish(ctx context.Context, work *DeliveryWork, status string, httpStatus int, reason string) error {
	return s.DB.WithContext(ctx).Model(&Delivery{}).
		Where("id = ? AND generation = ? AND attempt_count = ? AND status = ?",
			work.Delivery.ID, work.Delivery.Generation, work.Delivery.AttemptCount, "processing").
		Updates(map[string]any{
			"status": status, "last_http_status": httpStatus, "last_error": reason, "lease_until": nil,
		}).Error
}
