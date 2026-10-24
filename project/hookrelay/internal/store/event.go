package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Event struct {
	ID             uint64 `gorm:"primaryKey;autoIncrement"`
	IdempotencyKey string
	RequestHash    []byte
	EndpointID     uint64
	EventType      string
	Payload        []byte
	CreatedAt      time.Time
}

type Delivery struct {
	ID             uint64 `gorm:"primaryKey;autoIncrement"`
	EventID        uint64
	Status         string
	Generation     uint32
	AttemptCount   uint32
	LastHTTPStatus *int
	LastError      *string
	LeaseUntil     *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type EventInput struct {
	IdempotencyKey string
	EndpointID     uint64
	EventType      string
	Payload        []byte
}

type EventResult struct {
	EventID    uint64
	DeliveryID uint64
	Reused     bool
}

var (
	ErrInvalidEvent        = errors.New("invalid event")
	ErrEndpointDisabled    = errors.New("endpoint disabled")
	ErrIdempotencyConflict = errors.New("idempotency key reused with different content")
	errDuplicateEventKey   = errors.New("duplicate event key")
)

// CreateOrGetEventDelivery persists an event and its pending delivery together.
// No queue task or webhook is created here. A repeated key returns the original
// IDs only when endpoint, event type and raw payload bytes are unchanged.
// Pass a pool-backed db, not a long-lived outer transaction: after a unique

// conflict the winner must be read from a fresh committed snapshot.
func CreateOrGetEventDelivery(ctx context.Context, db *gorm.DB, input EventInput) (EventResult, error) {
	return createOrGetEventDelivery(ctx, db, input, func(tx *gorm.DB, delivery *Delivery) error {
		return tx.Create(delivery).Error
	})
}

// The insertion seam lets a test fail the second write without altering schema.
func createOrGetEventDelivery(ctx context.Context, db *gorm.DB, input EventInput,
	insertDelivery func(*gorm.DB, *Delivery) error,
) (EventResult, error) {
	if err := validateEventInput(input); err != nil {
		return EventResult{}, err
	}
	hash := HashEventRequest(input.EndpointID, input.EventType, input.Payload)
	if result, found, err := findEventByKey(ctx, db, input.IdempotencyKey, hash); err != nil || found {
		return result, err
	}

	var result EventResult
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Serialize acceptance against disabling this endpoint. A replay of an
		// existing key was handled above, even when the endpoint is now disabled.
		var endpoint Endpoint
		err := tx.Clauses(clause.Locking{Strength: "SHARE"}).
			Select("id", "enabled").Where("id = ?", input.EndpointID).Take(&endpoint).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrEndpointNotFound
		}
		if err != nil {
			return fmt.Errorf("check endpoint: %w", err)
		}
		if !endpoint.Enabled {
			return ErrEndpointDisabled
		}

		now := time.Now().UTC().Truncate(time.Microsecond)
		event := Event{
			IdempotencyKey: input.IdempotencyKey, RequestHash: hash[:],
			EndpointID: input.EndpointID, EventType: input.EventType,
			Payload: input.Payload, CreatedAt: now,
		}
		if err := tx.Create(&event).Error; err != nil {
			var mysqlErr *mysql.MySQLError
			if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
				// Exit the transaction first. MySQL's failed transaction snapshot
				// must not be used to read the winner of this concurrent insert.
				return errDuplicateEventKey
			}
			return fmt.Errorf("create event: %w", err)
		}
		delivery := Delivery{
			EventID: event.ID, Status: "pending", Generation: 1,
			CreatedAt: now, UpdatedAt: now,
		}
		if err := insertDelivery(tx, &delivery); err != nil {
			return fmt.Errorf("create delivery: %w", err)
		}
		result = EventResult{EventID: event.ID, DeliveryID: delivery.ID}
		return nil
	})
	if errors.Is(err, errDuplicateEventKey) {
		// Unique-key arbitration waited for the competing transaction to finish.
		// Read after rollback in a fresh transaction/snapshot.
		result, found, lookupErr := findEventByKey(ctx, db, input.IdempotencyKey, hash)
		if lookupErr != nil {
			return EventResult{}, lookupErr
		}
		if !found {
			return EventResult{}, errors.New("duplicate event key but no committed event found")
		}
		return result, nil
	}
	if errors.Is(err, ErrEndpointDisabled) || errors.Is(err, ErrEndpointNotFound) {
		// A concurrent winner may have committed after the initial lookup.
		// Its retry should still return the original IDs, not a disabled error.
		result, found, lookupErr := findEventByKey(ctx, db, input.IdempotencyKey, hash)
		if lookupErr != nil || found {
			return result, lookupErr
		}
	}
	if err != nil {
		return EventResult{}, err
	}
	return result, nil
}

func findEventByKey(ctx context.Context, db *gorm.DB, key string, hash [32]byte) (EventResult, bool, error) {
	var event Event
	err := db.WithContext(ctx).Select("id", "request_hash").Where("idempotency_key = ?", key).Take(&event).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return EventResult{}, false, nil
	}
	if err != nil {
		return EventResult{}, false, fmt.Errorf("find event by key: %w", err)
	}
	if !bytes.Equal(event.RequestHash, hash[:]) {
		return EventResult{}, true, ErrIdempotencyConflict
	}
	var delivery Delivery
	if err := db.WithContext(ctx).Select("id").Where("event_id = ?", event.ID).Take(&delivery).Error; err != nil {
		return EventResult{}, true, fmt.Errorf("find delivery for event: %w", err)
	}
	return EventResult{EventID: event.ID, DeliveryID: delivery.ID, Reused: true}, true, nil
}

func validateEventInput(input EventInput) error {
	if input.EndpointID == 0 || len(input.IdempotencyKey) == 0 || len(input.IdempotencyKey) > 128 ||
		len(input.EventType) == 0 || len(input.EventType) > 128 || len(input.Payload) > 256*1024 {
		return ErrInvalidEvent
	}
	for i := 0; i < len(input.IdempotencyKey); i++ {
		if input.IdempotencyKey[i] < '!' || input.IdempotencyKey[i] > '~' {
			return ErrInvalidEvent
		}
	}
	return nil
}
