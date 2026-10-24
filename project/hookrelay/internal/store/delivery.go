package store

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

var ErrDeliveryNotFound = errors.New("delivery not found")

func FindDelivery(ctx context.Context, db *gorm.DB, id uint64) (Delivery, error) {
	var delivery Delivery
	err := db.WithContext(ctx).Where("id = ?", id).Take(&delivery).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Delivery{}, ErrDeliveryNotFound
	}
	if err != nil {
		return Delivery{}, fmt.Errorf("find delivery: %w", err)
	}
	return delivery, nil
}
