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
	"github.com/hibiken/asynq"
)

type Repository interface {
	Claim(context.Context, uint64, uint32, uint32, time.Duration) (*store.DeliveryWork, error)
	Finish(context.Context, *store.DeliveryWork, string, int, string, time.Duration) error
}

type Worker struct {
	Store      Repository
	Send       func(context.Context, Request) (Result, error)
	AllowedURL string
}

func (w Worker) ProcessTask(ctx context.Context, t *asynq.Task) error {
	var payload task.Payload
	if err := json.Unmarshal(t.Payload(), &payload); err != nil || payload.DeliveryID == 0 || payload.Generation == 0 {
		return fmt.Errorf("invalid delivery task: %w", asynq.SkipRetry)
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
		})
		if sendErr != nil {
			reason = "network_error"
			switch {
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
		return &retryError{message: "save delivery result failed", after: result.RetryAfter}
	}
	slog.Info("delivery attempt", "event_id", work.Event.ID, "delivery_id", work.Delivery.ID,
		"generation", work.Delivery.Generation, "attempt", work.Delivery.AttemptCount, "status", status, "http_status", result.HTTPStatus, "latency_ms", result.Latency.Milliseconds())
	if status == "retrying" {
		return &retryError{message: "delivery temporarily failed", after: result.RetryAfter}
	}
	if status == "failed" {
		return fmt.Errorf("delivery failed: %w", asynq.SkipRetry)
	}
	return nil
}
