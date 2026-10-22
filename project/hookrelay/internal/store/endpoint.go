package store

import (
	"context"
	"errors"
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

var ErrEndpointNotFound = errors.New("endpoint not found")

func CreateEndpoint(ctx context.Context, db *gorm.DB, endpoint *Endpoint) error {
	if endpoint.CreatedAt.IsZero() {
		// The column is DATETIME(6). Match its microsecond precision before
		// returning the in-memory value in the creation response.
		endpoint.CreatedAt = time.Now().UTC().Truncate(time.Microsecond)
	}
	if err := db.WithContext(ctx).Create(endpoint).Error; err != nil {
		return fmt.Errorf("create endpoint: %w", err)
	}
	return nil
}

// SetEndpointEnabled changes only enabled. A follow-up lookup handles both
// missing records and no-op updates (which may report zero affected rows).
func SetEndpointEnabled(ctx context.Context, db *gorm.DB, id uint64, enabled bool) (Endpoint, error) {
	if err := db.WithContext(ctx).Model(&Endpoint{}).
		Where("id = ?", id).Update("enabled", enabled).Error; err != nil {
		return Endpoint{}, fmt.Errorf("update endpoint enabled: %w", err)
	}
	return FindEndpoint(ctx, db, id)
}

func FindEndpoint(ctx context.Context, db *gorm.DB, id uint64) (Endpoint,
	error) {
	var endpoint Endpoint
	err := db.WithContext(ctx).
		Where("id = ?", id).
		First(&endpoint).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return Endpoint{}, ErrEndpointNotFound
	}
	if err != nil {
		return Endpoint{}, fmt.Errorf("find endpoint: %w", err)
	}

	return endpoint, nil
}
