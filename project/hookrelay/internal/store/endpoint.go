package store

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

type Endpoint struct {
	ID            uint64 `gorm:"primaryKey;autoIncrement"`
	URL           string
	SigningSecret string `json:"-"`
	Enabled       bool
	CreatedAt     time.Time
}

func CreateEndpoint(ctx context.Context, db *gorm.DB, endpoint *Endpoint) error {
	if err := db.WithContext(ctx).Create(endpoint).Error; err != nil {
		return fmt.Errorf("create endpoint: %w", err)
	}
	return nil
}

func FindEndpoint(ctx context.Context, db *gorm.DB, id uint64) (Endpoint,
	error) {
	var endpoint Endpoint
	if err := db.WithContext(ctx).
		Where("id = ?", id).
		First(&endpoint).Error; err != nil {
		return Endpoint{}, fmt.Errorf("find endpoint: %w", err)
	}
	return endpoint, nil
}
