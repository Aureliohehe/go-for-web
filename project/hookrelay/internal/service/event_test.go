package service

import (
	"context"
	"errors"
	"testing"

	"github.com/aurelio/hookrelay/internal/store"
)

func TestAccept(t *testing.T) {
	for _, tt := range []struct {
		name                         string
		reused                       bool
		createErr, queueErr, wantErr error
		wantEnqueue                  bool
	}{
		{name: "new", wantEnqueue: true},
		{name: "retry repairs enqueue", reused: true, wantEnqueue: true},
		{name: "store failed", createErr: store.ErrIdempotencyConflict, wantErr: store.ErrIdempotencyConflict},
		{name: "queue failed after commit", queueErr: errors.New("redis unavailable"), wantErr: ErrEnqueue, wantEnqueue: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			created, enqueued := false, false
			service := Events{
				Create: func(context.Context, store.EventInput) (store.EventResult, error) {
					created = true
					return store.EventResult{EventID: 7, DeliveryID: 9, Reused: tt.reused}, tt.createErr
				},
				Enqueue: func(_ context.Context, id uint64) error {
					if !created || id != 9 {
						t.Fatal("enqueue must follow persistence")
					}
					enqueued = true
					return tt.queueErr
				},
			}
			got, err := service.Accept(context.Background(), store.EventInput{})
			if !errors.Is(err, tt.wantErr) || enqueued != tt.wantEnqueue || got.EventID != 7 || got.DeliveryID != 9 || got.Reused != tt.reused {
				t.Fatalf("got=%+v err=%v enqueued=%v", got, err, enqueued)
			}
		})
	}
}
