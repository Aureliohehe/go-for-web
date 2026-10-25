package delivery

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aurelio/hookrelay/internal/webhook"
)

const testSigningSecret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestSenderStatus(t *testing.T) {
	for _, status := range []int{200, 204, 302, 400, 401, 404, 408, 429, 500, 503} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var redirects atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				redirects.Add(1)
			}))
			defer target.Close()
			const payload = `{ "order_id": 42 }`
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil || string(body) != payload || r.Method != http.MethodPost ||
					r.Header.Get("Content-Type") != "application/json" || r.Header.Get("X-Webhook-ID") != "9" {
					t.Error("unexpected webhook request")
				}
				if err := webhook.Verify(testSigningSecret, r.Header.Get(webhook.HeaderID), r.Header.Get(webhook.HeaderTimestamp), r.Header.Get(webhook.HeaderSignature), body, time.Now()); err != nil {
					t.Error("invalid outgoing signature")
				}
				w.Header().Set("Location", target.URL)
				w.Header().Set("Retry-After", "17")
				w.WriteHeader(status)
			}))
			defer server.Close()
			result, err := NewSender(time.Second).Send(context.Background(), Request{
				URL: server.URL, DeliveryID: "9", SigningSecret: testSigningSecret, Payload: []byte(payload),
			})
			wantRetry := status == 408 || status == 429 || status >= 500
			if wantRetry && result.RetryAfter != "17" || !wantRetry && result.RetryAfter != "" {
				t.Fatal("Retry-After must be forwarded only for retryable HTTP responses")
			}
			if result.HTTPStatus != status || result.Retryable != wantRetry || result.Latency <= 0 {
				t.Fatalf("unexpected result: %+v", result)
			}
			if status < 300 && err != nil || status >= 300 && !errors.Is(err, ErrHTTPStatus) {
				t.Fatalf("unexpected error: %v", err)
			}
			if redirects.Load() != 0 {
				t.Fatal("sender followed a redirect")
			}
		})
	}
}

func TestSenderRetryAfterHeaders(t *testing.T) {
	for _, tt := range []struct {
		name   string
		values []string
		want   string
	}{
		{name: "absent"},
		{name: "seconds", values: []string{"17"}, want: "17"},
		{name: "HTTP date contains comma", values: []string{"Fri, 02 Jan 2026 03:04:05 GMT"}, want: "Fri, 02 Jan 2026 03:04:05 GMT"},
		{name: "duplicate", values: []string{"17", "30"}},
		{name: "identical duplicate", values: []string{"17", "17"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for _, value := range tt.values {
					w.Header().Add("Retry-After", value)
				}
				w.WriteHeader(http.StatusTooManyRequests)
			}))
			defer server.Close()
			result, err := NewSender(time.Second).Send(context.Background(), Request{URL: server.URL, DeliveryID: "9", SigningSecret: testSigningSecret})
			if !errors.Is(err, ErrHTTPStatus) || !result.Retryable || result.RetryAfter != tt.want {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestSenderTimeoutAndCancellation(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer server.Close()
	defer close(release)

	result, err := NewSender(30*time.Millisecond).Send(context.Background(), Request{URL: server.URL, DeliveryID: "9", SigningSecret: testSigningSecret})
	if !errors.Is(err, context.DeadlineExceeded) || !result.Retryable || result.HTTPStatus != 0 {
		t.Fatalf("timeout: result=%+v err=%v", result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err = NewSender(time.Second).Send(ctx, Request{URL: server.URL, DeliveryID: "9", SigningSecret: testSigningSecret})
	if !errors.Is(err, context.Canceled) || !result.Retryable {
		t.Fatalf("cancellation: result=%+v err=%v", result, err)
	}
}

func TestSenderRejectsInvalidSecret(t *testing.T) {
	var sends atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		sends.Add(1)
	}))
	defer server.Close()
	for _, secret := range []string{"", "not-a-secret"} {
		result, err := NewSender(time.Second).Send(context.Background(), Request{
			URL: server.URL, DeliveryID: "9", SigningSecret: secret,
		})
		if !errors.Is(err, webhook.ErrInvalidSecret) || result.Retryable || result.HTTPStatus != 0 {
			t.Fatalf("invalid signing config: result=%+v err=%v", result, err)
		}
	}
	if sends.Load() != 0 {
		t.Fatal("invalid signing config must not send HTTP")
	}
}

func TestSenderNetworkFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.Close()
	result, err := NewSender(time.Second).Send(context.Background(), Request{URL: server.URL, DeliveryID: "9", SigningSecret: testSigningSecret})
	if err == nil || !result.Retryable || result.HTTPStatus != 0 {
		t.Fatalf("network failure: result=%+v err=%v", result, err)
	}
}
