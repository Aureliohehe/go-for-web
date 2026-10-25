package receiver

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aurelio/hookrelay/internal/delivery"
	"github.com/aurelio/hookrelay/internal/webhook"
)

const testSecret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func acceptOK(context.Context, uint64, []byte) (bool, error) { return false, nil }

func TestReceiverVerification(t *testing.T) {
	body := []byte(`{ "order_id": 42 }`)
	for _, tt := range []struct {
		name   string
		mutate func(*http.Request)
		status int
	}{
		{"valid", func(*http.Request) {}, 204},
		{"missing signature", func(r *http.Request) { r.Header.Del(webhook.HeaderSignature) }, 401},
		{"tampered ID", func(r *http.Request) { r.Header.Set(webhook.HeaderID, "10") }, 401},
		{"tampered body", func(r *http.Request) { r.Body = http.NoBody }, 401},
		{"expired", func(r *http.Request) {
			ts, sig, _ := webhook.Sign(testSecret, "9", body, time.Now().Add(-6*time.Minute))
			r.Header.Set(webhook.HeaderTimestamp, ts)
			r.Header.Set(webhook.HeaderSignature, sig)
		}, 401},
		{"future", func(r *http.Request) {
			ts, sig, _ := webhook.Sign(testSecret, "9", body, time.Now().Add(6*time.Minute))
			r.Header.Set(webhook.HeaderTimestamp, ts)
			r.Header.Set(webhook.HeaderSignature, sig)
		}, 401},
		{"duplicate signature", func(r *http.Request) { r.Header.Add(webhook.HeaderSignature, r.Header.Get(webhook.HeaderSignature)) }, 401},
		{"duplicate ID", func(r *http.Request) { r.Header.Add(webhook.HeaderID, "9") }, 401},
		{"duplicate timestamp", func(r *http.Request) { r.Header.Add(webhook.HeaderTimestamp, r.Header.Get(webhook.HeaderTimestamp)) }, 401},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
			ts, sig, _ := webhook.Sign(testSecret, "9", body, time.Now())
			r.Header.Set(webhook.HeaderID, "9")
			r.Header.Set(webhook.HeaderTimestamp, ts)
			r.Header.Set(webhook.HeaderSignature, sig)
			tt.mutate(r)
			w := httptest.NewRecorder()
			calls := 0
			accept := func(ctx context.Context, id uint64, payload []byte) (bool, error) {
				calls++
				if id != 9 || !bytes.Equal(payload, body) {
					t.Fatal("wrong persistence input")
				}
				return false, nil
			}
			NewHandler(testSecret, accept, 204, 0, "").ServeHTTP(w, r)
			if (calls == 1) != (tt.status == 204) {
				t.Fatal("invalid signature reached persistence")
			}
			if w.Code != tt.status {
				t.Fatalf("status=%d want=%d", w.Code, tt.status)
			}
		})
	}
}

func TestSignedSenderReceiver(t *testing.T) {
	for _, status := range []int{204, 429, 500} {
		server := httptest.NewServer(NewHandler(testSecret, acceptOK, status, 0, "10"))
		defer server.Close()
		input := delivery.Request{URL: server.URL + "/webhook", DeliveryID: "9", SigningSecret: testSecret, Payload: []byte(`{ "id": 42 }`)}
		result, err := delivery.NewSender(time.Second).Send(context.Background(), input)
		if result.HTTPStatus != status || (err == nil) != (status == 204) {
			t.Fatalf("signed HTTP roundtrip: result=%+v err=%v", result, err)
		}
		if status != 204 && (!result.Retryable || result.RetryAfter != "10") {
			t.Fatal("signed receiver lost retry behavior")
		}
		input.SigningSecret = strings.Repeat("b", 64)
		result, err = delivery.NewSender(time.Second).Send(context.Background(), input)
		if !errors.Is(err, delivery.ErrHTTPStatus) || result.HTTPStatus != 401 || result.Retryable {
			t.Fatal("wrong key must cause permanent HTTP failure")
		}
	}
}

func TestReceiverBodyLimit(t *testing.T) {
	server := httptest.NewServer(NewHandler(testSecret, acceptOK, 204, 0, ""))
	defer server.Close()
	for _, size := range []int{256 << 10, (256 << 10) + 1} {
		result, _ := delivery.NewSender(time.Second).Send(context.Background(), delivery.Request{
			URL: server.URL + "/webhook", DeliveryID: "9", SigningSecret: testSecret, Payload: bytes.Repeat([]byte("a"), size),
		})
		want := 204
		if size > 256<<10 {
			want = 413
		}
		if result.HTTPStatus != want {
			t.Fatalf("size=%d status=%d want=%d", size, result.HTTPStatus, want)
		}
	}
}
