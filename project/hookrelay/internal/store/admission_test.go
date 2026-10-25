package store

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aurelio/hookrelay/internal/config"
	"gorm.io/gorm"
)

// Capacity is global: use a dedicated migrated database with no application
// processes, not a live development database with a changing backlog.
func TestAdmissionIntegration(t *testing.T) {
	if os.Getenv("HOOKRELAY_ADMISSION_INTEGRATION") != "1" {
		t.Skip("requires dedicated empty database and migrations 000001..000004")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal("load config failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, pool, err := OpenMySQL(ctx, cfg)
	if err != nil {
		t.Fatal("connect MySQL failed")
	}
	defer pool.Close()
	otherDB, otherPool, err := OpenMySQL(ctx, cfg)
	if err != nil {
		t.Fatal("connect second pool failed")
	}
	defer otherPool.Close()
	for _, model := range []any{&Event{}, &Delivery{}, &Outbox{}} {
		var count int64
		if err := db.Model(model).Count(&count).Error; err != nil || count != 0 {
			t.Fatal("requires empty dedicated database; do not clear business tables")
		}
	}
	endpoint := Endpoint{URL: "http://127.0.0.1:9090/webhook", SigningSecret: strings.Repeat("a", 64), Enabled: true}
	if err := CreateEndpoint(ctx, db, &endpoint); err != nil {
		t.Fatal(err)
	}
	otherEndpoint := Endpoint{URL: endpoint.URL, SigningSecret: endpoint.SigningSecret, Enabled: true}
	if err := CreateEndpoint(ctx, db, &otherEndpoint); err != nil {
		t.Fatal(err)
	}
	var keys []string
	defer func() {
		cleanupTestEvents(t, db, endpoint.ID, keys)
		if err := db.Delete(&Endpoint{}, otherEndpoint.ID).Error; err != nil {
			t.Error(err)
		}
	}()
	input := func(endpointID uint64) EventInput {
		key := newTestEventKey(t)
		keys = append(keys, key)
		return EventInput{IdempotencyKey: key, EndpointID: endpointID, EventType: "test", Payload: []byte(`{}`)}
	}
	const workers = 8
	inputs := make([]EventInput, workers)
	for i := range inputs {
		id := endpoint.ID
		if i%2 == 1 {
			id = otherEndpoint.ID
		}
		inputs[i] = input(id)
	}
	concurrent := func(inputs []EventInput) ([]EventResult, []error) {
		results, errs := make([]EventResult, len(inputs)), make([]error, len(inputs))
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range inputs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				conn := db
				if i%2 == 1 {
					conn = otherDB
				}
				results[i], errs[i] = CreateOrGetEventDelivery(ctx, conn, inputs[i], 1)
			}()
		}
		close(start)
		wg.Wait()
		return results, errs
	}
	results, errs := concurrent(inputs)
	winner, rejected := -1, -1
	for i, err := range errs {
		if err == nil {
			if winner != -1 {
				t.Fatal("concurrent requests exceeded the one-slot limit")
			}
			winner = i
		} else {
			if !errors.Is(err, ErrBacklogFull) || results[i].EventID != 0 || results[i].DeliveryID != 0 {
				t.Fatalf("unexpected rejection: %v", err)
			}
			rejected = i
		}
	}
	if winner == -1 || rejected == -1 {
		t.Fatal("expected one acceptance and seven rejections")
	}
	for _, model := range []any{&Event{}, &Delivery{}, &Outbox{}} {
		var count int64
		if err := db.Model(model).Count(&count).Error; err != nil || count != 1 {
			t.Fatal("rejected request persisted data")
		}
	}
	again, err := CreateOrGetEventDelivery(ctx, db, inputs[winner], 1)
	if err != nil || !again.Reused || again.DeliveryID != results[winner].DeliveryID {
		t.Fatal("full backlog must allow an accepted key to be reused")
	}
	changed := inputs[winner]
	changed.Payload = []byte(`{"changed":true}`)
	if _, err := CreateOrGetEventDelivery(ctx, db, changed, 1); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatal("conflicting key must remain a conflict, not capacity rejection")
	}

	// Publication alone must NOT free a slot. Processing and retrying also count.
	if err := db.Model(&Outbox{}).Where("delivery_id = ?", again.DeliveryID).Update("status", "published").Error; err != nil {
		t.Fatal(err)
	}
	repo := DeliveryStore{DB: db}
	work, err := repo.Claim(ctx, again.DeliveryID, 1, 3, time.Minute)
	if err != nil || work == nil {
		t.Fatal("claim failed")
	}
	if _, err := CreateOrGetEventDelivery(ctx, db, inputs[rejected], 1); !errors.Is(err, ErrBacklogFull) {
		t.Fatal("processing must count toward backlog")
	}
	if err := repo.Finish(ctx, work, "retrying", 500, "http_error", time.Millisecond, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateOrGetEventDelivery(ctx, db, inputs[rejected], 1); !errors.Is(err, ErrBacklogFull) {
		t.Fatal("retrying must count toward backlog")
	}
	work, err = repo.Claim(ctx, again.DeliveryID, 1, 3, time.Minute)
	if err != nil || work == nil {
		t.Fatal("retry claim failed")
	}
	if err := repo.Finish(ctx, work, "succeeded", 204, "", time.Millisecond, 0); err != nil {
		t.Fatal(err)
	}
	next, err := CreateOrGetEventDelivery(ctx, db, inputs[rejected], 1)
	if err != nil || next.Reused {
		t.Fatal("completion must free capacity for a previously rejected key")
	}
	work, err = repo.Claim(ctx, next.DeliveryID, 1, 3, time.Minute)
	if err != nil || work == nil {
		t.Fatal("second claim failed")
	}
	if err := repo.Finish(ctx, work, "failed", 400, "http_error", time.Millisecond, 0); err != nil {
		t.Fatal(err)
	}

	// Same-key contenders must recheck idempotency after waiting for the lock.
	same := input(endpoint.ID)
	for i := range inputs {
		inputs[i] = same
	}
	results, errs = concurrent(inputs)
	created := 0
	for i, result := range results {
		if errs[i] != nil || result.DeliveryID != results[0].DeliveryID {
			t.Fatalf("same-key request rejected: %v", errs[i])
		}
		if !result.Reused {
			created++
		}
	}
	if created != 1 {
		t.Fatal("same key must use exactly one slot")
	}
	work, err = repo.Claim(ctx, results[0].DeliveryID, 1, 3, time.Minute)
	if err != nil || work == nil {
		t.Fatal("third claim failed")
	}
	if err := repo.Finish(ctx, work, "succeeded", 204, "", time.Millisecond, 0); err != nil {
		t.Fatal(err)
	}

	// A failed creation must not consume capacity or leave a lock held.
	failed := input(endpoint.ID)
	injected := errors.New("injected outbox failure")
	_, err = createOrGetEventDelivery(ctx, db, failed, 1,
		func(tx *gorm.DB, d *Delivery) error { return tx.Create(d).Error },
		func(*gorm.DB, *Outbox) error { return injected })
	if !errors.Is(err, injected) {
		t.Fatal("expected injected rollback")
	}
	last, err := CreateOrGetEventDelivery(ctx, db, failed, 1)
	if err != nil || last.Reused {
		t.Fatal("rollback must not consume a slot")
	}
	// 'next' is failed, while 'last' occupies the only available slot.
	if _, err := ReplayDelivery(ctx, db, next.DeliveryID, 1); !errors.Is(err, ErrBacklogFull) {
		t.Fatal("replay must obey capacity")
	}
	unchanged, err := FindDelivery(ctx, db, next.DeliveryID)
	if err != nil || unchanged.Generation != 1 || unchanged.Status != "failed" {
		t.Fatal("rejected replay changed delivery")
	}
	var outboxes int64
	if err := db.Model(&Outbox{}).Where("delivery_id = ?", next.DeliveryID).Count(&outboxes).Error; err != nil || outboxes != 1 {
		t.Fatal("rejected replay created outbox")
	}
	work, err = repo.Claim(ctx, last.DeliveryID, 1, 3, time.Minute)
	if err != nil || work == nil {
		t.Fatal("last claim failed")
	}
	if err := repo.Finish(ctx, work, "succeeded", 204, "", time.Millisecond, 0); err != nil {
		t.Fatal(err)
	}

	// A new event and a replay, on separate pools, share the same gate.
	newInput := input(endpoint.ID)
	raceStart := make(chan struct{})
	var createErr, replayErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); <-raceStart; _, createErr = CreateOrGetEventDelivery(ctx, db, newInput, 1) }()
	go func() { defer wg.Done(); <-raceStart; _, replayErr = ReplayDelivery(ctx, otherDB, next.DeliveryID, 1) }()
	close(raceStart)
	wg.Wait()
	if !((createErr == nil && errors.Is(replayErr, ErrBacklogFull)) || (replayErr == nil && errors.Is(createErr, ErrBacklogFull))) {
		t.Fatalf("capacity race: create=%v replay=%v", createErr, replayErr)
	}
	var unfinished int64
	if err := db.Model(&Delivery{}).Where("status IN ?", []string{"pending", "processing", "retrying"}).Count(&unfinished).Error; err != nil || unfinished != 1 {
		t.Fatal("mixed acceptance exceeded capacity")
	}
}
