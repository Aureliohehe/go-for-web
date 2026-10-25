package delivery

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/aurelio/hookrelay/internal/webhook"
)

var ErrHTTPStatus = errors.New("receiver returned non-2xx status")

type Request struct {
	URL           string
	DeliveryID    string
	SigningSecret string `json:"-"`
	Payload       []byte
}

type Result struct {
	HTTPStatus int
	Retryable  bool
	RetryAfter string
	Latency    time.Duration
}

// Sender signs and performs one attempt. Scheduling and persistence are separate.
// URLs must come from operator-approved endpoints, not arbitrary user input.
type Sender struct {
	client *http.Client
}

func NewSender(timeout time.Duration) *Sender {
	return &Sender{client: &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

// A nil error means HTTP 2xx. Retryable classifies failures, but does not retry.
func (s *Sender) Send(ctx context.Context, input Request) (result Result, err error) {
	started := time.Now()
	defer func() { result.Latency = time.Since(started) }()

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, input.URL, bytes.NewReader(input.Payload))
	if err != nil {
		return result, err
	}
	timestamp, signature, err := webhook.Sign(input.SigningSecret, input.DeliveryID, input.Payload, time.Now())
	if err != nil {
		return result, err // Invalid local signing configuration is not retryable.
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(webhook.HeaderID, input.DeliveryID)
	request.Header.Set(webhook.HeaderTimestamp, timestamp)
	request.Header.Set(webhook.HeaderSignature, signature)
	response, err := s.client.Do(request)
	if err != nil {
		result.Retryable = true
		return result, err
	}
	defer response.Body.Close()
	// Best-effort small-body drain for connection reuse. Only the status code
	// acknowledges delivery; response bodies are neither retained nor logged.
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	result.HTTPStatus = response.StatusCode
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return result, nil
	}
	result.Retryable = response.StatusCode == 408 || response.StatusCode == 429 ||
		(response.StatusCode >= 500 && response.StatusCode < 600)
	if values := response.Header.Values("Retry-After"); result.Retryable && len(values) == 1 {
		// Retry-After is a single value, not a list. Ignore ambiguous duplicates.
		result.RetryAfter = values[0]
	}
	return result, ErrHTTPStatus
}
