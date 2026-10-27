package store

import (
	"context"
	"database/sql"

	"gorm.io/gorm"
)

// These are database snapshots, not monotonic counters: replay, recovery and
// future retention can change them. No endpoint IDs or payloads are collected.
type StateMetrics struct {
	Status    string
	Count     int64
	OldestAge float64
}

type AttemptMetrics struct {
	Status         string
	Count          int64
	LatencyCount   int64
	LatencySeconds float64
}

type MetricsSnapshot struct {
	Events             int64
	Deliveries         []StateMetrics
	Outbox             []StateMetrics
	Attempts           []AttemptMetrics
	UnfinishedAttempts int64
}

// CollectMetrics uses one read-only consistent snapshot without locking business
// rows. Aggregate scans are intended for the bounded local-demo dataset; callers
// must bound collection time and avoid high-frequency/concurrent scraping.
func CollectMetrics(ctx context.Context, db *gorm.DB) (MetricsSnapshot, error) {
	var snapshot MetricsSnapshot
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&Event{}).Count(&snapshot.Events).Error; err != nil {
			return err
		}
		const stateSelect = "status, COUNT(*) AS count, GREATEST(0, TIMESTAMPDIFF(MICROSECOND, MIN(created_at), UTC_TIMESTAMP(6)) / 1000000.0) AS oldest_age"
		if err := tx.Model(&Delivery{}).Select(stateSelect).Group("status").Scan(&snapshot.Deliveries).Error; err != nil {
			return err
		}
		if err := tx.Model(&Outbox{}).Select(stateSelect).Group("status").Scan(&snapshot.Outbox).Error; err != nil {
			return err
		}
		if err := tx.Model(&DeliveryAttempt{}).
			Select("status, COUNT(*) AS count, COUNT(latency_ms) AS latency_count, COALESCE(SUM(latency_ms), 0) / 1000.0 AS latency_seconds").
			Where("finished_at IS NOT NULL").Group("status").Scan(&snapshot.Attempts).Error; err != nil {
			return err
		}
		return tx.Model(&DeliveryAttempt{}).Where("finished_at IS NULL").Count(&snapshot.UnfinishedAttempts).Error
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return MetricsSnapshot{}, err // Never expose an incomplete snapshot as healthy.
	}
	return snapshot, nil
}
