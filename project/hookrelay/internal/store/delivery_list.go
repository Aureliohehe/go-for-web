package store

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

var ErrInvalidDeliveryFilter = errors.New("invalid delivery filter")

// EndpointID == 0 and Status == "" mean no filter. After is an exclusive ID
// cursor, not an offset. Callers must keep filters unchanged between pages.
type DeliveryFilter struct {
	Status     string
	EndpointID uint64
	After      uint64
	Limit      int
}

func (f DeliveryFilter) Validate() error {
	if f.Limit < 1 || f.Limit > 100 {
		return ErrInvalidDeliveryFilter
	}
	switch f.Status {
	case "", "pending", "processing", "retrying", "succeeded", "failed":
		return nil
	default:
		return ErrInvalidDeliveryFilter
	}
}

// ListDeliveries returns at most Limit+1 rows for next-cursor detection. A missing
// endpoint or an empty result is an empty list, not a not-found error. This is a
// live view: a state change behind the cursor is only visible on a fresh scan.
func ListDeliveries(ctx context.Context, db *gorm.DB, filter DeliveryFilter) ([]Delivery, error) {
	if err := filter.Validate(); err != nil {
		return nil, err
	}
	query := db.WithContext(ctx).Table("deliveries AS d").Select("d.*").Where("d.id > ?", filter.After)
	if filter.Status != "" {
		query = query.Where("d.status = ?", filter.Status)
	}
	if filter.EndpointID != 0 {
		// Events have one endpoint and deliveries have one event: the join does
		// not duplicate deliveries and never reads the event payload or secret.
		query = query.Joins("JOIN events AS e ON e.id = d.event_id").Where("e.endpoint_id = ?", filter.EndpointID)
	}
	rows := make([]Delivery, 0)
	if err := query.Order("d.id ASC").Limit(filter.Limit + 1).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list deliveries: %w", err)
	}
	return rows, nil
}
