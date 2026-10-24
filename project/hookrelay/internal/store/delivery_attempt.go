package store

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// An execution is registered before HTTP. An unfinished row means its result is
// unknown, not proof that HTTP was sent or that the receiver did not process it.
type DeliveryAttempt struct {
	ID            uint64 `gorm:"primaryKey;autoIncrement"`
	DeliveryID    uint64
	Generation    uint32
	AttemptNumber uint32
	Status        string
	StartedAt     time.Time
	FinishedAt    *time.Time
	HTTPStatus    *int
	ErrorClass    string
	LatencyMS     *int64
}

// Returns at most limit+1 rows, allowing the handler to construct a next cursor.
// The caller validates limit in [1,100]. Cursor order includes all generations.
func ListDeliveryAttempts(ctx context.Context, db *gorm.DB, id, after uint64, limit int) ([]DeliveryAttempt, error) {
	if _, err := FindDelivery(ctx, db, id); err != nil {
		return nil, err
	}
	attempts := make([]DeliveryAttempt, 0)
	err := db.WithContext(ctx).Where("delivery_id = ? AND id > ?", id, after).
		Order("id").Limit(limit + 1).Find(&attempts).Error
	return attempts, err
}
