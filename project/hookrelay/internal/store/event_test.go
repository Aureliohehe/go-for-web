package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aurelio/hookrelay/internal/config"
	"gorm.io/gorm"
)

func TestCreateOrGetEventDeliveryRejectsInvalidInput(t *testing.T) {
	tests := []EventInput{
		{EndpointID: 0, IdempotencyKey: "key", EventType: "created"},
		{EndpointID: 1, EventType: "created"},
		{EndpointID: 1, IdempotencyKey: "space key", EventType: "created"},
		{EndpointID: 1, IdempotencyKey: strings.Repeat("k", 129), EventType: "created"},
		{EndpointID: 1, IdempotencyKey: "key"},
		{EndpointID: 1, IdempotencyKey: "key", EventType: strings.Repeat("x", 129)},
		{EndpointID: 1, IdempotencyKey: "key", EventType: "created", Payload: make([]byte, 256*1024+1)},
	}
	for _, input := range tests {
		if _, err := CreateOrGetEventDelivery(context.Background(), nil, input, 10000); !errors.Is(err, ErrInvalidEvent) {
			t.Fatal("invalid input must be rejected before accessing the database")
		}
	}
}

func TestCreateOrGetEventDeliveryIntegration(t *testing.T) {
	if os.Getenv("HOOKRELAY_INTEGRATION") != "1" {
		t.Skip("set HOOKRELAY_INTEGRATION=1 after applying migrations 000001..000004")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal("load database config failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, pool, err := OpenMySQL(ctx, cfg)
	if err != nil {
		t.Fatal("open MySQL failed")
	}
	defer pool.Close()

	endpoint := Endpoint{URL: "http://127.0.0.1:9090/webhook", SigningSecret: strings.Repeat("a", 64), Enabled: true}
	if err := CreateEndpoint(ctx, db, &endpoint); err != nil {
		t.Fatal("create test endpoint failed")
	}
	key := newTestEventKey(t)
	otherKey := newTestEventKey(t)
	concurrentKey := newTestEventKey(t)
	rollbackKey := newTestEventKey(t)
	missingKey := newTestEventKey(t)
	defer cleanupTestEvents(t, db, endpoint.ID, []string{key, otherKey, concurrentKey, rollbackKey, missingKey})

	failedDelivery := errors.New("injected delivery write failure")
	if _, err := createOrGetEventDelivery(ctx, db, EventInput{
		IdempotencyKey: rollbackKey, EndpointID: endpoint.ID,
		EventType: "order.created", Payload: []byte(`{"id":1}`),
	}, 10000, func(*gorm.DB, *Delivery) error { return failedDelivery }, nil); !errors.Is(err, failedDelivery) {
		t.Fatal("a failed delivery insert must fail the whole transaction")
	}
	var count int64
	if err := db.Model(&Event{}).Where("idempotency_key = ?", rollbackKey).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("failed delivery insert must roll back the event")
	}

	// Fail the third write and verify that both earlier writes roll back.
	var rolledBackDeliveryID uint64
	failedOutbox := errors.New("injected outbox write failure")
	if _, err := createOrGetEventDelivery(ctx, db, EventInput{
		IdempotencyKey: rollbackKey, EndpointID: endpoint.ID, EventType: "test", Payload: []byte(`{}`),
	}, 10000, func(tx *gorm.DB, d *Delivery) error {
		err := tx.Create(d).Error
		rolledBackDeliveryID = d.ID
		return err
	}, func(*gorm.DB, *Outbox) error { return failedOutbox }); !errors.Is(err, failedOutbox) {
		t.Fatal("outbox failure must fail the transaction")
	}
	if err := db.Model(&Event{}).Where("idempotency_key = ?", rollbackKey).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("outbox failure must roll back event")
	}
	if err := db.Model(&Delivery{}).Where("id = ?", rolledBackDeliveryID).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("outbox failure must roll back delivery")
	}

	input := EventInput{IdempotencyKey: key, EndpointID: endpoint.ID, EventType: "order.created", Payload: []byte(`{"id":1}`)}
	first, err := CreateOrGetEventDelivery(ctx, db, input, 10000)
	if err != nil || first.EventID == 0 || first.DeliveryID == 0 || first.Reused {
		t.Fatal("first submission must create an event and delivery")
	}
	again, err := CreateOrGetEventDelivery(ctx, db, input, 10000)
	if err != nil || !again.Reused || again.EventID != first.EventID || again.DeliveryID != first.DeliveryID {
		t.Fatal("repeated key and content must return the same IDs")
	}
	changed := input
	changed.Payload = []byte(`{ "id":1 }`)
	if _, err := CreateOrGetEventDelivery(ctx, db, changed, 10000); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatal("same key and different raw payload must conflict")
	}
	changed = input
	changed.EventType = "order.updated"
	if _, err := CreateOrGetEventDelivery(ctx, db, changed, 10000); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatal("same key and different event type must conflict")
	}
	changed = input
	changed.EndpointID = endpoint.ID + 1
	if _, err := CreateOrGetEventDelivery(ctx, db, changed, 10000); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatal("same key and different endpoint must conflict")
	}

	other := input
	other.IdempotencyKey = otherKey
	result, err := CreateOrGetEventDelivery(ctx, db, other, 10000)
	if err != nil || result.Reused || result.EventID == first.EventID || result.DeliveryID == first.DeliveryID {
		t.Fatal("a different key must create a new event and delivery")
	}

	if err := db.Model(&Event{}).Where("idempotency_key IN ?", []string{key, otherKey}).Count(&count).Error; err != nil || count != 2 {
		t.Fatal("unexpected number of events")
	}
	if err := db.Model(&Delivery{}).Where("event_id IN ?", []uint64{first.EventID, result.EventID}).Count(&count).Error; err != nil || count != 2 {
		t.Fatal("each event must have exactly one delivery")
	}
	delivery, err := FindDelivery(ctx, db, first.DeliveryID)
	if err != nil ||
		delivery.EventID != first.EventID || delivery.Status != "pending" ||
		delivery.Generation != 1 || delivery.AttemptCount != 0 {
		t.Fatal("first delivery has incorrect initial state")
	}
	if _, err := FindDelivery(ctx, db, 0); !errors.Is(err, ErrDeliveryNotFound) {
		t.Fatal("missing delivery must return not found")
	}

	if _, err := CreateOrGetEventDelivery(ctx, db, EventInput{
		IdempotencyKey: missingKey, EndpointID: ^uint64(0), EventType: input.EventType, Payload: input.Payload,
	}, 10000); !errors.Is(err, ErrEndpointNotFound) {
		t.Fatal("nonexistent endpoint must reject a new event")
	}
	if _, err := SetEndpointEnabled(ctx, db, endpoint.ID, false); err != nil {
		t.Fatal("disable test endpoint failed")
	}
	if _, err := CreateOrGetEventDelivery(ctx, db, EventInput{
		IdempotencyKey: concurrentKey, EndpointID: endpoint.ID, EventType: input.EventType, Payload: input.Payload,
	}, 10000); !errors.Is(err, ErrEndpointDisabled) {
		t.Fatal("disabled endpoint must reject a new event")
	}
	if again, err := CreateOrGetEventDelivery(ctx, db, input, 10000); err != nil || !again.Reused || again.EventID != first.EventID {
		t.Fatal("a retry of an accepted event must still work after disabling")
	}
	if _, err := SetEndpointEnabled(ctx, db, endpoint.ID, true); err != nil {
		t.Fatal("re-enable test endpoint failed")
	}
	if _, err := CreateOrGetEventDelivery(ctx, db, EventInput{
		IdempotencyKey: concurrentKey, EndpointID: 0, EventType: input.EventType,
	}, 10000); !errors.Is(err, ErrInvalidEvent) {
		t.Fatal("zero endpoint ID must be invalid")
	}

	// Concurrent requests use independent MySQL transactions: an outer test
	// transaction would hide the winner from the other connections.
	const workers = 8
	start := make(chan struct{})
	results := make([]EventResult, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results[i], errs[i] = CreateOrGetEventDelivery(ctx, db, EventInput{
				IdempotencyKey: concurrentKey, EndpointID: endpoint.ID,
				EventType: input.EventType, Payload: input.Payload,
			}, 10000)
		}()
	}
	close(start)
	wg.Wait()
	created := 0
	for i, got := range results {
		if errs[i] != nil || got.EventID == 0 || got.DeliveryID == 0 ||
			got.EventID != results[0].EventID || got.DeliveryID != results[0].DeliveryID {
			t.Fatal("concurrent duplicate requests must return the same IDs")
		}
		if !got.Reused {
			created++
		}
	}
	if created != 1 {
		t.Fatal("exactly one concurrent request must create the event")
	}
	if err := db.Model(&Delivery{}).Where("event_id = ?", results[0].EventID).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("concurrent duplicates must have only one delivery")
	}
	for _, id := range []uint64{first.DeliveryID, result.DeliveryID, results[0].DeliveryID} {
		if err := db.Model(&Outbox{}).Where("delivery_id = ? AND generation = 1", id).Count(&count).Error; err != nil || count != 1 {
			t.Fatal("each delivery must have exactly one outbox, including duplicate submissions")
		}
	}
}

func newTestEventKey(t *testing.T) string {
	t.Helper()
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal("generate test key failed")
	}
	return "test-" + hex.EncodeToString(random[:])
}

func cleanupTestEvents(t *testing.T, db *gorm.DB, endpointID uint64, keys []string) {
	t.Helper()
	// Only remove records belonging to this test; never truncate a business table.
	var events []Event
	if err := db.Select("id").Where("idempotency_key IN ?", keys).Find(&events).Error; err != nil {
		t.Error("find test events for cleanup failed")
		return
	}
	for _, event := range events {
		if err := db.Where("delivery_id IN (?)", db.Model(&Delivery{}).Select("id").Where("event_id = ?", event.ID)).Delete(&DeliveryAttempt{}).Error; err != nil {
			t.Error("delete test attempt history failed")
		}
		if err := db.Where("delivery_id IN (?)", db.Model(&Delivery{}).Select("id").Where("event_id = ?", event.ID)).Delete(&Outbox{}).Error; err != nil {
			t.Error("delete test outbox failed")
		}
		if err := db.Where("event_id = ?", event.ID).Delete(&Delivery{}).Error; err != nil {
			t.Error("delete test delivery failed")
		}
		if err := db.Delete(&Event{}, event.ID).Error; err != nil {
			t.Error("delete test event failed")
		}
	}
	if err := db.Delete(&Endpoint{}, endpointID).Error; err != nil {
		t.Error("delete test endpoint failed")
	}
}
