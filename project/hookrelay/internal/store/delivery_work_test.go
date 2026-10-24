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
		result, err := CreateOrGetEventDelivery(ctx, db, EventInput{EndpointID: endpoint.ID, IdempotencyKey: key, EventType: "test", Payload: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	repo := DeliveryStore{DB: db}
	first := create()
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
	if err := repo.Finish(ctx, work, "retrying", 500, "http_error"); err != nil {
		t.Fatal(err)
	}
	next, err := repo.Claim(ctx, first.DeliveryID, 1, 3, time.Minute)
	if err != nil || next == nil || next.Delivery.AttemptCount != 2 {
		t.Fatal("retry must increment attempt count")
	}
	if err := repo.Finish(ctx, work, "succeeded", 200, ""); err != nil {
		t.Fatal(err)
	}
	current, err := FindDelivery(ctx, db, first.DeliveryID)
	if err != nil || current.Status != "processing" {
		t.Fatal("old attempt overwrote current state")
	}
	if err := repo.Finish(ctx, next, "succeeded", 204, ""); err != nil {
		t.Fatal(err)
	}
	current, err = FindDelivery(ctx, db, first.DeliveryID)
	if err != nil || current.Status != "succeeded" || current.LastHTTPStatus == nil || *current.LastHTTPStatus != 204 || current.LeaseUntil != nil {
		t.Fatal("success was not saved")
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
}
