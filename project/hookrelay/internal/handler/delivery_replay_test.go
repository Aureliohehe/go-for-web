package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aurelio/hookrelay/internal/store"
)

func TestPostDeliveryReplay(t *testing.T) {
	for _, tt := range []struct {
		name, id, key, wantError string
		dbErr                    error
		wantCode, calls          int
	}{
		{name: "accepted", id: "9", key: "key", wantCode: 202, calls: 1},
		{name: "unauthorized before validation", id: "bad", wantCode: 401, wantError: "unauthorized"},
		{name: "zero id", id: "0", key: "key", wantCode: 400, wantError: "invalid_delivery_id"},
		{name: "invalid id", id: "bad", key: "key", wantCode: 400, wantError: "invalid_delivery_id"},
		{name: "missing", id: "9", key: "key", dbErr: store.ErrDeliveryNotFound, wantCode: 404, calls: 1, wantError: "delivery_not_found"},
		{name: "not failed", id: "9", key: "key", dbErr: fmt.Errorf("wrapped: %w", store.ErrDeliveryNotReplayable), wantCode: 409, calls: 1, wantError: "delivery_not_replayable"},
		{name: "disabled", id: "9", key: "key", dbErr: store.ErrEndpointDisabled, wantCode: 409, calls: 1, wantError: "endpoint_disabled"},
		{name: "capacity full", id: "9", key: "key", dbErr: store.ErrBacklogFull, wantCode: 503, calls: 1, wantError: "backlog_full"},
		{name: "database error", id: "9", key: "key", dbErr: errors.New("private database details"), wantCode: 500, calls: 1, wantError: "internal_error"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			type contextKey struct{}
			ctx := context.WithValue(context.Background(), contextKey{}, "request")
			calls := 0
			router := NewAPIRouter(func(context.Context) error { return nil }, "key", "", nil, nil, nil, nil, nil, nil,
				func(ctx context.Context, id uint64) (store.Delivery, error) {
					calls++
					if id != 9 || ctx.Value(contextKey{}) != "request" {
						t.Fatal("incorrect ID or context")
					}
					return store.Delivery{ID: id, EventID: ^uint64(0), Status: "pending", Generation: 2}, tt.dbErr
				})
			request := httptest.NewRequest(http.MethodPost, "/v1/deliveries/"+tt.id+"/replay", nil).WithContext(ctx)
			request.Header.Set("X-API-Key", tt.key)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != tt.wantCode || calls != tt.calls {
				t.Fatalf("status=%d calls=%d", response.Code, calls)
			}
			var got map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if tt.wantCode == 503 {
				if len(got) != 2 || got["error"] != tt.wantError || got["replayed"] != false || response.Header().Get("Retry-After") != "5" {
					t.Fatalf("bad capacity response: %v", got)
				}
				return
			}
			if response.Header().Get("Retry-After") != "" {
				t.Fatal("unexpected retry header")
			}
			if tt.wantError != "" {
				if len(got) != 1 || got["error"] != tt.wantError {
					t.Fatalf("bad error response: %v", got)
				}
				return
			}
			if len(got) != 4 || got["delivery_id"] != "9" || got["event_id"] != "18446744073709551615" || got["generation"] != float64(2) || got["status"] != "pending" {
				t.Fatalf("bad replay response: %v", got)
			}
		})
	}
}
