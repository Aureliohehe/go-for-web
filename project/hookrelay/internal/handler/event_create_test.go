package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aurelio/hookrelay/internal/store"
)

func TestPostEvent(t *testing.T) {
	const body = `{"endpoint_id":"7","event_type":"order.created","payload":{ "order_id": 42 }}`
	tests := []struct {
		name      string
		body      string
		unauthed  bool
		reused    bool
		storeErr  error
		wantCode  int
		wantError string
		wantCalls int
	}{
		{name: "backlog full", body: body, storeErr: fmt.Errorf("wrapped: %w", store.ErrBacklogFull), wantCode: 503, wantCalls: 1, wantError: "backlog_full"},
		{name: "accepted", body: body, wantCode: 202, wantCalls: 1},
		{name: "reused", body: body, reused: true, wantCode: 200, wantCalls: 1},
		{name: "unauthorized", body: body, unauthed: true, wantCode: 401, wantError: "unauthorized"},
		{name: "invalid JSON", body: `{`, wantCode: 400, wantError: "invalid_request"},
		{name: "invalid endpoint", body: strings.Replace(body, `"7"`, `"0"`, 1), wantCode: 400, wantError: "invalid_event"},
		{name: "missing payload", body: `{"endpoint_id":"7","event_type":"order.created"}`, wantCode: 400, wantError: "invalid_event"},
		{name: "oversized body", body: body + strings.Repeat(" ", maxEventBodyBytes), wantCode: 413, wantError: "request_too_large"},
		{name: "invalid input", body: body, storeErr: store.ErrInvalidEvent, wantCode: 400, wantError: "invalid_event", wantCalls: 1},
		{name: "missing endpoint", body: body, storeErr: store.ErrEndpointNotFound, wantCode: 404, wantError: "endpoint_not_found", wantCalls: 1},
		{name: "disabled endpoint", body: body, storeErr: store.ErrEndpointDisabled, wantCode: 409, wantError: "endpoint_disabled", wantCalls: 1},
		{name: "conflict", body: body, storeErr: fmt.Errorf("wrapped: %w", store.ErrIdempotencyConflict), wantCode: 409, wantError: "idempotency_conflict", wantCalls: 1},
		{name: "database failure", body: body, storeErr: errors.New("private database details"), wantCode: 500, wantError: "internal_error", wantCalls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			type contextKey struct{}
			ctx := context.WithValue(context.Background(), contextKey{}, "request")
			calls := 0
			router := NewAPIRouter(func(context.Context) error { return nil }, "key", "", nil, nil, nil,
				func(ctx context.Context, input store.EventInput) (store.EventResult, error) {
					calls++
					if ctx.Value(contextKey{}) != "request" || input.IdempotencyKey != "order-42" ||
						input.EndpointID != 7 || input.EventType != "order.created" || string(input.Payload) != `{ "order_id": 42 }` {
						t.Fatalf("incorrect event input or context: %+v", input)
					}
					return store.EventResult{EventID: ^uint64(0), DeliveryID: 9, Reused: tt.reused}, tt.storeErr
				}, nil, nil)
			request := httptest.NewRequest(http.MethodPost, "/v1/events", strings.NewReader(tt.body)).WithContext(ctx)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", "order-42")
			if !tt.unauthed {
				request.Header.Set("X-API-Key", "key")
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != tt.wantCode || calls != tt.wantCalls {
				t.Fatalf("status=%d calls=%d, want status=%d calls=%d", response.Code, calls, tt.wantCode, tt.wantCalls)
			}
			var got map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if tt.wantCode == 503 {
				if len(got) != 2 || got["error"] != "backlog_full" || got["persisted"] != false || response.Header().Get("Retry-After") != "5" {
					t.Fatalf("unexpected backpressure response: %v", got)
				}
				return
			}
			if response.Header().Get("Retry-After") != "" {
				t.Fatal("only capacity rejection should set Retry-After")
			}
			if tt.wantError != "" {
				if len(got) != 1 || got["error"] != tt.wantError {
					t.Fatalf("unexpected error response: %v", got)
				}
				return
			}
			if len(got) != 3 || got["event_id"] != "18446744073709551615" || got["delivery_id"] != "9" || got["reused"] != tt.reused {
				t.Fatalf("unexpected success response: %v", got)
			}
		})
	}
}
