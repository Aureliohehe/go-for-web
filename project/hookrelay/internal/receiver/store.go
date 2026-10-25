// Package receiver implements the example recipient, not HookRelay's delivery state.
package receiver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"time"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

var ErrPayloadConflict = errors.New("delivery ID already used with different payload")

type Receipt struct {
	DeliveryID  uint64 `gorm:"primaryKey;autoIncrement:false"`
	PayloadHash []byte
	CreatedAt   time.Time
}

func (Receipt) TableName() string { return "receiver_receipts" }

// Effect is the simulated business write. It is deliberately in the same DB as
// Receipt; this transaction cannot protect external effects such as sending email.
type Effect struct {
	ID         uint64
	DeliveryID uint64
	Payload    []byte
	CreatedAt  time.Time
}

func (Effect) TableName() string { return "receiver_effects" }

type Store struct{ DB *gorm.DB }

// Accept atomically records the deduplication key and simulated business effect.
// The unique constraint arbitrates concurrent requests, not a pre-insert SELECT.
// Call only after verifying the signature. duplicate=true means already committed.
func (s Store) Accept(ctx context.Context, id uint64, body []byte) (duplicate bool, err error) {
	if id == 0 {
		return false, errors.New("invalid delivery ID")
	}
	hash := sha256.Sum256(body)
	now := time.Now().UTC().Truncate(time.Microsecond)
	var duplicateReceipt bool
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		receipt := Receipt{DeliveryID: id, PayloadHash: hash[:], CreatedAt: now}
		if err := tx.Create(&receipt).Error; err != nil {
			var mysqlErr *mysql.MySQLError
			duplicateReceipt = errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
			return err
		}
		return tx.Create(&Effect{DeliveryID: id, Payload: body, CreatedAt: now}).Error
	})
	if !duplicateReceipt {
		return false, err
	}
	// Read outside the rolled-back transaction, after the winning INSERT commits.
	var existing Receipt
	if err := s.DB.WithContext(ctx).First(&existing, "delivery_id = ?", id).Error; err != nil {
		return false, err
	}
	if !bytes.Equal(existing.PayloadHash, hash[:]) {
		return false, ErrPayloadConflict
	}
	return true, nil
}
