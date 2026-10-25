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

func TestReplayDeliveryIntegration(t *testing.T) {
	if os.Getenv("HOOKRELAY_INTEGRATION") != "1" {
		t.Skip("requires MySQL and migrations 000001..000005")
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
	endpoint := Endpoint{URL: "http://127.0.0.1:9090/webhook", SigningSecret: strings.Repeat("a", 64), Enabled: true}
	if err := CreateEndpoint(ctx, db, &endpoint); err != nil {
		t.Fatal(err)
	}
	var keys []string
	defer func() { cleanupTestEvents(t, db, endpoint.ID, keys) }()
	create := func() EventResult {
		key := newTestEventKey(t)
		keys = append(keys, key)
		result, err := CreateOrGetEventDelivery(ctx, db, EventInput{EndpointID: endpoint.ID, IdempotencyKey: key, EventType: "test", Payload: []byte(`{}`)}, 10000)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	repo := DeliveryStore{DB: db}
	first := create()
	reject := func() {
		if _, err := ReplayDelivery(ctx, db, first.DeliveryID, 10000); !errors.Is(err, ErrDeliveryNotReplayable) {
			t.Fatalf("nonfailed replay: %v", err)
		}
	}
	if _, err := ReplayDelivery(ctx, db, 0, 10000); !errors.Is(err, ErrDeliveryNotFound) {
		t.Fatal("missing delivery must return not found")
	}
	reject() // pending
	work, err := repo.Claim(ctx, first.DeliveryID, 1, 3, time.Minute)
	if err != nil || work == nil {
		t.Fatal("claim failed")
	}
	reject() // processing
	if err := repo.Finish(ctx, work, "retrying", 500, "http_error", time.Millisecond, 0); err != nil {
		t.Fatal(err)
	}
	reject() // retrying
	work, err = repo.Claim(ctx, first.DeliveryID, 1, 3, time.Minute)
	if err != nil || work == nil {
		t.Fatal("retry claim failed")
	}
	if err := repo.Finish(ctx, work, "failed", 400, "http_error", 2*time.Millisecond, 0); err != nil {
		t.Fatal(err)
	}
	before, err := FindDelivery(ctx, db, first.DeliveryID)
	if err != nil {
		t.Fatal(err)
	}

	injected := errors.New("injected outbox write failure")
	if _, err := replayDelivery(ctx, db, first.DeliveryID, 10000, func(*gorm.DB, *Outbox) error { return injected }); !errors.Is(err, injected) {
		t.Fatal("expected replay rollback")
	}
	unchanged, err := FindDelivery(ctx, db, first.DeliveryID)
	if err != nil || unchanged.Generation != 1 || unchanged.Status != "failed" || unchanged.AttemptCount != 2 || unchanged.LastHTTPStatus == nil || *unchanged.LastHTTPStatus != 400 {
		t.Fatal("outbox failure did not roll back replay")
	}
	var count int64
	if err := db.Model(&Outbox{}).Where("delivery_id = ?", first.DeliveryID).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("failed replay created outbox")
	}
	if _, err := SetEndpointEnabled(ctx, db, endpoint.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := ReplayDelivery(ctx, db, first.DeliveryID, 10000); !errors.Is(err, ErrEndpointDisabled) {
		t.Fatal("disabled endpoint must reject replay")
	}
	if _, err := SetEndpointEnabled(ctx, db, endpoint.ID, true); err != nil {
		t.Fatal(err)
	}

	const workers = 8
	results, errs := make([]Delivery, workers), make([]error, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results[i], errs[i] = ReplayDelivery(ctx, db, first.DeliveryID, 10000)
		}()
	}
	close(start)
	wg.Wait()
	accepted := 0
	for i, got := range results {
		if errs[i] != nil {
			if !errors.Is(errs[i], ErrDeliveryNotReplayable) {
				t.Fatalf("unexpected concurrent replay error: %v", errs[i])
			}
			continue
		}
		accepted++
		if got.ID != before.ID || got.EventID != before.EventID || !got.CreatedAt.Equal(before.CreatedAt) || got.Generation != 2 || got.Status != "pending" || got.AttemptCount != 0 || got.LastHTTPStatus != nil || got.LastError != nil || got.LeaseUntil != nil {
			t.Fatalf("incorrect replay state: %+v", got)
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted %d concurrent replays, want 1", accepted)
	}
	if err := db.Model(&Outbox{}).Where("delivery_id = ? AND generation = 2", first.DeliveryID).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("must create exactly one outbox for new generation")
	}
	if err := db.Model(&Event{}).Where("endpoint_id = ?", endpoint.ID).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("replay must not create a new event")
	}
	if history, err := ListDeliveryAttempts(ctx, db, first.DeliveryID, 0, 100); err != nil || len(history) != 2 {
		t.Fatal("replay changed existing history")
	}
	if stale, err := repo.Claim(ctx, first.DeliveryID, 1, 3, time.Minute); err != nil || stale != nil {
		t.Fatal("old generation must skip")
	}
	if err := repo.Finish(ctx, work, "succeeded", 200, "", time.Millisecond, 0); err != nil {
		t.Fatal(err)
	}
	current, err := FindDelivery(ctx, db, first.DeliveryID)
	if err != nil || current.Generation != 2 || current.Status != "pending" {
		t.Fatal("old result changed new generation")
	}
	newWork, err := repo.Claim(ctx, first.DeliveryID, 2, 3, time.Minute)
	if err != nil || newWork == nil || newWork.Delivery.AttemptCount != 1 {
		t.Fatal("new generation must start at attempt 1")
	}
	if err := repo.Finish(ctx, newWork, "succeeded", 204, "", time.Millisecond, 0); err != nil {
		t.Fatal(err)
	}
	reject() // succeeded
	history, err := ListDeliveryAttempts(ctx, db, first.DeliveryID, 0, 100)
	if err != nil || len(history) != 3 || history[0].Generation != 1 || history[1].Status != "failed" || history[2].Generation != 2 || history[2].AttemptNumber != 1 || history[2].Status != "succeeded" {
		t.Fatal("history must span both generations")
	}

	// The first HTTP attempt may still return after its lease/budget expired.
	late := create()
	old, err := repo.Claim(ctx, late.DeliveryID, 1, 1, -time.Second)
	if err != nil || old == nil {
		t.Fatal("old claim failed")
	}
	if exhausted, err := repo.Claim(ctx, late.DeliveryID, 1, 1, time.Minute); err != nil || exhausted != nil {
		t.Fatal("budget must fail old generation")
	}
	if _, err := ReplayDelivery(ctx, db, late.DeliveryID, 10000); err != nil {
		t.Fatal(err)
	}
	if err := repo.Finish(ctx, old, "succeeded", 200, "", time.Millisecond, 0); err != nil {
		t.Fatal(err)
	}
	current, err = FindDelivery(ctx, db, late.DeliveryID)
	if err != nil || current.Generation != 2 || current.Status != "pending" || current.AttemptCount != 0 {
		t.Fatal("late old-generation result overwrote replay")
	}
	history, err = ListDeliveryAttempts(ctx, db, late.DeliveryID, 0, 100)
	if err != nil || len(history) != 1 || history[0].Generation != 1 || history[0].FinishedAt == nil {
		t.Fatal("late result must only complete its old history")
	}
}
