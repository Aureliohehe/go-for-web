package store

import (
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Held until the event transaction commits/rolls back, not during delivery.
// A database lock (rather than a Go mutex) also serializes separate API processes.
// Event creation and failed -> pending replay both use this admission gate.
func lockAdmission(tx *gorm.DB) error {
	var row struct{ ID uint8 }
	if err := tx.Table("admission_locks").Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id = 1").Take(&row).Error; err != nil {
		return fmt.Errorf("lock event admission: %w", err)
	}
	return nil
}

func checkBacklog(tx *gorm.DB, limit int64) error {
	var pending int64
	if err := tx.Model(&Delivery{}).
		Where("status IN ?", []string{"pending", "processing", "retrying"}).Count(&pending).Error; err != nil {
		return fmt.Errorf("count unfinished deliveries: %w", err)
	}
	if pending >= limit {
		return ErrBacklogFull
	}
	return nil
}
