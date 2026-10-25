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
	TrackTask(context.Context, uint64, uint32, string) error
	Claim(context.Context, uint64, uint32, uint32, time.Duration) (*store.DeliveryWork, error)
	Finish(context.Context, *store.DeliveryWork, string, int, string, time.Duration, time.Duration) error
}

type Worker struct {
	Store       Repository
	Send        func(context.Context, Request) (Result, error)
	AllowedURL  string
	Limiter     *EndpointLimiter
	RetryPolicy RetryPolicy
	// Defer enqueues a delayed replacement for endpoint saturation, an active
	// delivery lease or retry cooldown before acknowledgement. It must preserve the
	// queue and remaining Asynq retry budget.
	Defer func(context.Context, uint64, uint32) (string, error)
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
			return w.deferTask(ctx, payload)
		}
		defer release() // Also releases on Claim/Finish errors, cancellation or panic.
	}
	work, err := w.Store.Claim(ctx, payload.DeliveryID, payload.Generation, task.MaxAttempts, task.LeaseDuration)
	if errors.Is(err, store.ErrDeliveryBusy) {
		// A duplicate task or a previous attempt whose result could not be saved
		// may still own the lease. The last result may also impose a retry
		// cooldown. Neither wait should exhaust the queue retry budget.
		return w.deferTask(ctx, payload)
	}
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
	retry := &retryError{message: "delivery temporarily failed", after: result.RetryAfter, attempt: work.Delivery.AttemptCount}
	if status == "retrying" {
		// Select once: both DB and queue enforce the same backoff/Retry-After.
		// Asynq may round schedules down to seconds; Claim remains authoritative.
		retry.delay = w.RetryPolicy.Delay(0, retry, t)
	}
	// HTTP must honor task cancellation, but persisting its observed result must
	// not inherit that cancellation or an almost-expired task deadline. Keep
	// cleanup bounded; generation/attempt fencing still rejects late updates.
	// Do not record raw HTTP errors: they can contain URLs or credentials.
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), task.ResultTimeout)
	defer cancel()
	if err := w.Store.Finish(finishCtx, work, status, result.HTTPStatus, reason, result.Latency, retry.delay); err != nil {
		retry.message = "save delivery result failed"
		return retry
	}
	slog.Info("delivery attempt", "event_id", work.Event.ID, "delivery_id", work.Delivery.ID,
		"generation", work.Delivery.Generation, "attempt", work.Delivery.AttemptCount, "status", status, "http_status", result.HTTPStatus, "latency_ms", result.Latency.Milliseconds())
	if status == "retrying" {
		return retry
	}
	if status == "failed" {
		return fmt.Errorf("delivery failed: %w", asynq.SkipRetry)
	}
	return nil
}

func (w Worker) deferTask(ctx context.Context, payload task.Payload) error {
	if w.Defer == nil {
		return errors.New("defer delivery failed")
	}
	queueID, err := w.Defer(ctx, payload.DeliveryID, payload.Generation)
	if err != nil || queueID == "" {
		return errors.New("defer delivery failed")
	}
	// Never acknowledge until both enqueue and task tracking succeed. A crash
	// between them can duplicate tasks; recovery fences the old generation.
	if err := w.Store.TrackTask(ctx, payload.DeliveryID, payload.Generation, queueID); err != nil {
		return errors.New("save deferred task failed")
	}
	// No attempt/history write, HTTP send or ordinary retry is consumed.
	return nil
}
