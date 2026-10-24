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
)

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
				w.Header().Set("Location", target.URL)
				w.WriteHeader(status)
			}))
			defer server.Close()
			result, err := NewSender(time.Second).Send(context.Background(), Request{
				URL: server.URL, DeliveryID: "9", Payload: []byte(payload),
			})
			wantRetry := status == 408 || status == 429 || status >= 500
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

func TestSenderTimeoutAndCancellation(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer server.Close()
	defer close(release)

	result, err := NewSender(30*time.Millisecond).Send(context.Background(), Request{URL: server.URL})
	if !errors.Is(err, context.DeadlineExceeded) || !result.Retryable || result.HTTPStatus != 0 {
		t.Fatalf("timeout: result=%+v err=%v", result, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err = NewSender(time.Second).Send(ctx, Request{URL: server.URL})
	if !errors.Is(err, context.Canceled) || !result.Retryable {
		t.Fatalf("cancellation: result=%+v err=%v", result, err)
	}
}

func TestSenderNetworkFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.Close()
	result, err := NewSender(time.Second).Send(context.Background(), Request{URL: server.URL})
	if err == nil || !result.Retryable || result.HTTPStatus != 0 {
		t.Fatalf("network failure: result=%+v err=%v", result, err)
	}
}
