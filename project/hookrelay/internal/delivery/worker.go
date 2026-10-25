package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/aurelio/hookrelay/internal/store"
	"github.com/aurelio/hookrelay/internal/task"
	"github.com/aurelio/hookrelay/internal/webhook"
	"github.com/hibiken/asynq"
)

type Repository interface {
	EndpointID(context.Context, uint64, uint32) (uint64, error)
	Claim(context.Context, uint64, uint32, uint32, time.Duration) (*store.DeliveryWork, error)
	Finish(context.Context, *store.DeliveryWork, string, int, string, time.Duration) error
}

type Worker struct {
	Store      Repository
	Send       func(context.Context, Request) (Result, error)
	AllowedURL string
	Limiter    *EndpointLimiter
	// Defer enqueues a delayed replacement before this task is acknowledged.
	// It must preserve the queue and remaining Asynq retry budget.
	Defer func(context.Context, uint64, uint32) error
}

func (w Worker) ProcessTask(ctx context.Context, t *asynq.Task) error {
	var payload task.Payload
	if err := json.Unmarshal(t.Payload(), &payload); err != nil || payload.DeliveryID == 0 || payload.Generation == 0 {
		return fmt.Errorf("invalid delivery task: %w", asynq.SkipRetry)
	}
	if w.Limiter != nil {
		id, err := w.Store.EndpointID(ctx, payload.DeliveryID, payload.Generation)
		if err != nil {
			return errors.New("lookup delivery endpoint failed")
		}
		if id == 0 {
			return nil
		}
		release, ok := w.Limiter.TryAcquire(id)
		if !ok {
			if w.Defer == nil || w.Defer(ctx, payload.DeliveryID, payload.Generation) != nil {
				return errors.New("defer saturated endpoint failed")
			}
			// No claim, attempt/history write, HTTP send or ordinary retry here.
			return nil
		}
		defer release() // Also releases on Claim/Finish errors, cancellation or panic.
	}
	work, err := w.Store.Claim(ctx, payload.DeliveryID, payload.Generation, task.MaxAttempts, task.LeaseDuration)
	if err != nil {
		return errors.New("claim delivery failed")
	}
	if work == nil {
		return nil
	}

	var result Result
	var sendErr error
	reason := ""
	if work.Endpoint.URL != w.AllowedURL || w.AllowedURL == "" {
		sendErr = errors.New("endpoint URL is not allowed")
		reason = "endpoint_not_allowed"
	} else {
		result, sendErr = w.Send(ctx, Request{
			URL: work.Endpoint.URL, DeliveryID: strconv.FormatUint(work.Delivery.ID, 10), Payload: work.Event.Payload,
			SigningSecret: work.Endpoint.SigningSecret,
		})
		if sendErr != nil {
			reason = "network_error"
			switch {
			case errors.Is(sendErr, webhook.ErrInvalidSecret), errors.Is(sendErr, webhook.ErrInvalidSignature):
				reason = "signing_error"
			case result.HTTPStatus != 0:
				reason = "http_error"
			case errors.Is(sendErr, context.DeadlineExceeded):
				reason = "timeout"
			case errors.Is(sendErr, context.Canceled):
				reason = "canceled"
			}
		}
	}
	status := "succeeded"
	if sendErr != nil {
		status = "failed"
		retryCount, _ := asynq.GetRetryCount(ctx)
		maxRetry, hasLimit := asynq.GetMaxRetry(ctx)
		if result.Retryable && work.Delivery.AttemptCount < task.MaxAttempts && (!hasLimit || retryCount < maxRetry) {
			status = "retrying"
		}
	}
	// Do not record raw HTTP errors: they can contain URLs or credentials.
	if err := w.Store.Finish(ctx, work, status, result.HTTPStatus, reason, result.Latency); err != nil {
		return &retryError{message: "save delivery result failed", after: result.RetryAfter, attempt: work.Delivery.AttemptCount}
	}
	slog.Info("delivery attempt", "event_id", work.Event.ID, "delivery_id", work.Delivery.ID,
		"generation", work.Delivery.Generation, "attempt", work.Delivery.AttemptCount, "status", status, "http_status", result.HTTPStatus, "latency_ms", result.Latency.Milliseconds())
	if status == "retrying" {
		return &retryError{message: "delivery temporarily failed", after: result.RetryAfter, attempt: work.Delivery.AttemptCount}
	}
	if status == "failed" {
		return fmt.Errorf("delivery failed: %w", asynq.SkipRetry)
	}
	return nil
}
