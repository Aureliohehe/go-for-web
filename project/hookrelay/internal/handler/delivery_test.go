package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aurelio/hookrelay/internal/store"
)

func TestGetDelivery(t *testing.T) {
	tests := []struct {
		name, id, key, wantError string
		dbErr                    error
		wantCode, wantCalls      int
	}{
		{name: "pending", id: "9", key: "key", wantCode: 200, wantCalls: 1},
		{name: "unauthorized", id: "9", wantCode: 401, wantError: "unauthorized"},
		{name: "invalid id", id: "abc", key: "key", wantCode: 400, wantError: "invalid_delivery_id"},
		{name: "zero id", id: "0", key: "key", wantCode: 400, wantError: "invalid_delivery_id"},
		{name: "not found", id: "9", key: "key", dbErr: fmt.Errorf("wrapped: %w", store.ErrDeliveryNotFound), wantCode: 404, wantCalls: 1, wantError: "delivery_not_found"},
		{name: "database failure", id: "9", key: "key", dbErr: errors.New("private database details"), wantCode: 500, wantCalls: 1, wantError: "internal_error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			type contextKey struct{}
			ctx := context.WithValue(context.Background(), contextKey{}, "request")
			calls := 0
			now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
			router := NewAPIRouter(func(context.Context) error { return nil }, "key", "", nil, nil, nil, nil,
				func(ctx context.Context, id uint64) (store.Delivery, error) {
					calls++
					if id != 9 || ctx.Value(contextKey{}) != "request" {
						t.Fatal("incorrect ID or context")
					}
					return store.Delivery{ID: id, EventID: ^uint64(0), Status: "pending", Generation: 1, CreatedAt: now, UpdatedAt: now}, tt.dbErr
				})
			request := httptest.NewRequest(http.MethodGet, "/v1/deliveries/"+tt.id, nil).WithContext(ctx)
			request.Header.Set("X-API-Key", tt.key)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != tt.wantCode || calls != tt.wantCalls {
				t.Fatalf("status=%d calls=%d, want status=%d calls=%d", response.Code, calls, tt.wantCode, tt.wantCalls)
			}
			var got map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if tt.wantError != "" {
				if len(got) != 1 || got["error"] != tt.wantError {
					t.Fatalf("unexpected error: %v", got)
				}
				return
			}
			if len(got) != 9 || got["last_http_status"] != nil || got["last_error"] != nil || got["id"] != "9" || got["event_id"] != "18446744073709551615" ||
				got["status"] != "pending" || got["generation"] != float64(1) || got["attempt_count"] != float64(0) ||
				got["created_at"] != now.Format(time.RFC3339Nano) || got["updated_at"] != now.Format(time.RFC3339Nano) {
				t.Fatalf("unexpected delivery: %v", got)
			}
		})
	}
}
