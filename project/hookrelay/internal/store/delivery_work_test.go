package store

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aurelio/hookrelay/internal/config"
)

func TestDeliveryWorkIntegration(t *testing.T) {
	if os.Getenv("HOOKRELAY_INTEGRATION") != "1" {
		t.Skip("requires MySQL and migrations")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal("load config failed")
	}
	ctx := context.Background()
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
	if id, err := repo.EndpointID(ctx, first.DeliveryID, 1); err != nil || id != endpoint.ID {
		t.Fatal("endpoint preflight failed")
	}
	if id, err := repo.EndpointID(ctx, first.DeliveryID, 2); err != nil || id != 0 {
		t.Fatal("stale generation must skip preflight")
	}
	if id, err := repo.EndpointID(ctx, 0, 1); err != nil || id != 0 {
		t.Fatal("missing delivery must skip preflight")
	}
	if d, err := FindDelivery(ctx, db, first.DeliveryID); err != nil || d.AttemptCount != 0 || d.Status != "pending" {
		t.Fatal("preflight must not claim work")
	}
	if _, err := ListDeliveryAttempts(ctx, db, 0, 0, 20); !errors.Is(err, ErrDeliveryNotFound) {
		t.Fatal("missing delivery must return not found")
	}
	if rows, err := ListDeliveryAttempts(ctx, db, first.DeliveryID, 0, 20); err != nil || len(rows) != 0 {
		t.Fatal("new delivery must have empty history")
	}
	// Duplicate execution number forces the history insert to fail. Claim must
	// roll back status, attempt count and lease together with the failed insert.
	conflict := DeliveryAttempt{DeliveryID: first.DeliveryID, Generation: 1, AttemptNumber: 1, Status: "processing", StartedAt: time.Now().UTC()}
	if err := db.Create(&conflict).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Claim(ctx, first.DeliveryID, 1, 3, time.Minute); err == nil {
		t.Fatal("expected history insert failure")
	}
	unchanged, err := FindDelivery(ctx, db, first.DeliveryID)
	if err != nil || unchanged.Status != "pending" || unchanged.AttemptCount != 0 || unchanged.LeaseUntil != nil {
		t.Fatal("failed history insert must roll back claim")
	}
	if err := db.Delete(&conflict).Error; err != nil {
		t.Fatal(err)
	}
	if work, err := repo.Claim(ctx, first.DeliveryID, 2, 3, time.Minute); err != nil || work != nil {
		t.Fatal("old generation must skip")
	}
	work, err := repo.Claim(ctx, first.DeliveryID, 1, 3, time.Minute)
	if err != nil || work == nil || work.Delivery.AttemptCount != 1 || work.Event.ID != first.EventID {
		t.Fatalf("claim failed: %v", err)
	}
	if _, err := repo.Claim(ctx, first.DeliveryID, 1, 3, time.Minute); !errors.Is(err, ErrDeliveryBusy) {
		t.Fatal("active lease must block another claim")
	}
	rows, err := ListDeliveryAttempts(ctx, db, first.DeliveryID, 0, 20)
	if err != nil || len(rows) != 1 || rows[0].ID != work.AttemptID || rows[0].FinishedAt != nil || rows[0].HTTPStatus != nil || rows[0].LatencyMS != nil {
		t.Fatal("claim must register unfinished attempt")
	}
	invalid := *work
	invalid.AttemptID = ^uint64(0)
	if err := repo.Finish(ctx, &invalid, "succeeded", 200, "", time.Millisecond, 0); err == nil {
		t.Fatal("missing history must fail finish")
	}
	unchanged, err = FindDelivery(ctx, db, first.DeliveryID)
	if err != nil || unchanged.Status != "processing" {
		t.Fatal("history update failure must roll back delivery result")
	}
	if err := repo.Finish(ctx, work, "retrying", 500, "http_error", 25*time.Millisecond, time.Minute); err != nil {
		t.Fatal(err)
	}
	cooling, err := FindDelivery(ctx, db, first.DeliveryID)
	if err != nil || cooling.Status != "retrying" || cooling.LeaseUntil == nil || !cooling.LeaseUntil.After(time.Now()) {
		t.Fatal("retry cooldown was not persisted")
	}
	if _, err := repo.Claim(ctx, first.DeliveryID, 1, 3, time.Minute); !errors.Is(err, ErrDeliveryBusy) {
		t.Fatal("duplicate task bypassed retry cooldown")
	}
	if rows, err := ListDeliveryAttempts(ctx, db, first.DeliveryID, 0, 20); err != nil || len(rows) != 1 {
		t.Fatal("cooldown wait created an extra attempt")
	}
	// Advance the durable gate instead of sleeping for the retry delay.
	if err := db.Model(&Delivery{}).Where("id = ?", first.DeliveryID).
		Update("lease_until", time.Now().UTC().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	next, err := repo.Claim(ctx, first.DeliveryID, 1, 3, time.Minute)
	if err != nil || next == nil || next.Delivery.AttemptCount != 2 {
		t.Fatal("retry must increment attempt count")
	}
	if err := repo.Finish(ctx, work, "succeeded", 200, "", time.Millisecond, 0); err != nil {
		t.Fatal(err)
	}
	current, err := FindDelivery(ctx, db, first.DeliveryID)
	if err != nil || current.Status != "processing" {
		t.Fatal("old attempt overwrote current state")
	}
	if err := repo.Finish(ctx, next, "succeeded", 204, "", 12*time.Millisecond, 0); err != nil {
		t.Fatal(err)
	}
	current, err = FindDelivery(ctx, db, first.DeliveryID)
	if err != nil || current.Status != "succeeded" || current.LastHTTPStatus == nil || *current.LastHTTPStatus != 204 || current.LeaseUntil != nil {
		t.Fatal("success was not saved")
	}
	if id, err := repo.EndpointID(ctx, first.DeliveryID, 1); err != nil || id != 0 {
		t.Fatal("terminal delivery must skip preflight")
	}
	rows, err = ListDeliveryAttempts(ctx, db, first.DeliveryID, 0, 1)
	if err != nil || len(rows) != 2 {
		t.Fatal("page must fetch one extra row")
	}
	if rows[0].Status != "retrying" || rows[0].HTTPStatus == nil || *rows[0].HTTPStatus != 500 || rows[0].LatencyMS == nil || *rows[0].LatencyMS != 25 || rows[0].FinishedAt == nil {
		t.Fatal("first result was not saved or was overwritten by duplicate finish")
	}
	page, err := ListDeliveryAttempts(ctx, db, first.DeliveryID, rows[0].ID, 1)
	if err != nil || len(page) != 1 || page[0].ID != next.AttemptID || page[0].AttemptNumber != 2 || page[0].Status != "succeeded" || page[0].LatencyMS == nil || *page[0].LatencyMS != 12 {
		t.Fatal("cursor did not return second attempt")
	}
	if work, err := repo.Claim(ctx, first.DeliveryID, 1, 3, time.Minute); err != nil || work != nil {
		t.Fatal("successful delivery must skip")
	}

	budget := create()
	// Simulate a crash: the lease expires without a Finish call.
	if work, err := repo.Claim(ctx, budget.DeliveryID, 1, 1, -time.Second); err != nil || work == nil {
		t.Fatal("initial claim failed")
	}
	if work, err := repo.Claim(ctx, budget.DeliveryID, 1, 1, time.Minute); err != nil || work != nil {
		t.Fatal("exhausted budget must skip")
	}
	current, err = FindDelivery(ctx, db, budget.DeliveryID)
	if err != nil || current.Status != "failed" || current.LastError == nil || *current.LastError != "attempts_exhausted" {
		t.Fatal("exhausted budget must fail delivery")
	}

	rows, err = ListDeliveryAttempts(ctx, db, budget.DeliveryID, 0, 20)
	if err != nil || len(rows) != 1 || rows[0].FinishedAt != nil || rows[0].Status != "processing" {
		t.Fatal("crashed attempt must retain unknown result")
	}

	late := create()
	old, err := repo.Claim(ctx, late.DeliveryID, 1, 3, -time.Second)
	if err != nil || old == nil {
		t.Fatal("old claim failed")
	}
	newer, err := repo.Claim(ctx, late.DeliveryID, 1, 3, time.Minute)
	if err != nil || newer == nil {
		t.Fatal("new claim failed")
	}
	if err := repo.Finish(ctx, old, "succeeded", 200, "", 7*time.Millisecond, 0); err != nil {
		t.Fatal(err)
	}
	current, err = FindDelivery(ctx, db, late.DeliveryID)
	if err != nil || current.Status != "processing" || current.AttemptCount != 2 {
		t.Fatal("late result overwrote new execution")
	}
	rows, err = ListDeliveryAttempts(ctx, db, late.DeliveryID, 0, 20)
	if err != nil || len(rows) != 2 || rows[0].Status != "succeeded" || rows[0].FinishedAt == nil || rows[1].FinishedAt != nil {
		t.Fatal("late result must only finish its own history")
	}
	if err := repo.Finish(ctx, newer, "failed", 0, "network_error", 9*time.Millisecond, 0); err != nil {
		t.Fatal(err)
	}
	rows, err = ListDeliveryAttempts(ctx, db, late.DeliveryID, old.AttemptID, 20)
	if err != nil || len(rows) != 1 || rows[0].HTTPStatus != nil || rows[0].ErrorClass != "network_error" || rows[0].FinishedAt == nil {
		t.Fatal("network failure must record no HTTP status")
	}

	disabled := create()
	if _, err := SetEndpointEnabled(ctx, db, endpoint.ID, false); err != nil {
		t.Fatal(err)
	}
	if work, err := repo.Claim(ctx, disabled.DeliveryID, 1, 3, time.Minute); err != nil || work != nil {
		t.Fatal("disabled endpoint must skip")
	}
	current, err = FindDelivery(ctx, db, disabled.DeliveryID)
	if err != nil || current.Status != "failed" || current.AttemptCount != 0 || current.LastError == nil || *current.LastError != "endpoint_disabled" {
		t.Fatal("disabled endpoint must fail without sending")
	}
	if rows, err := ListDeliveryAttempts(ctx, db, disabled.DeliveryID, 0, 20); err != nil || len(rows) != 0 {
		t.Fatal("disabled endpoint must not fabricate an attempt")
	}
}
