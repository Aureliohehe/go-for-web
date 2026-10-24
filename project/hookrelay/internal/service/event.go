package service

import (
	"context"
	"errors"

	"github.com/aurelio/hookrelay/internal/store"
)

var ErrEnqueue = errors.New("event saved but queue unavailable")

type Events struct {
	Create  func(context.Context, store.EventInput) (store.EventResult, error)
	Enqueue func(context.Context, uint64) error
}

func (s Events) Accept(ctx context.Context, input store.EventInput) (store.EventResult, error) {
	result, err := s.Create(ctx, input)
	if err != nil {
		return result, err
	}
	// Also enqueue on idempotent retries: this can repair a previous enqueue
	// failure. A crash between these two writes still requires Outbox later.
	if err := s.Enqueue(ctx, result.DeliveryID); err != nil {
		return result, ErrEnqueue
	}
	return result, nil
}
