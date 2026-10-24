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

func TestGetDeliveryAttempts(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	code, latency := 500, int64(25)
	finished := store.DeliveryAttempt{ID: 7, DeliveryID: 9, Generation: 1, AttemptNumber: 1, Status: "retrying", StartedAt: now, FinishedAt: &now, HTTPStatus: &code, ErrorClass: "http_error", LatencyMS: &latency}
	unfinished := store.DeliveryAttempt{ID: ^uint64(0), DeliveryID: 9, Generation: 2, AttemptNumber: 1, Status: "processing", StartedAt: now}
	for _, tt := range []struct {
		name, id, query, wantError, next  string
		unauthed                          bool
		rows                              []store.DeliveryAttempt
		dbErr                             error
		wantCode, wantCalls, limit, items int
		after                             uint64
	}{
		{name: "empty", id: "9", wantCode: 200, wantCalls: 1, limit: 20},
		{name: "unfinished", id: "9", rows: []store.DeliveryAttempt{unfinished}, wantCode: 200, wantCalls: 1, limit: 20, items: 1},
		{name: "first page", id: "9", query: "?limit=1", rows: []store.DeliveryAttempt{finished, unfinished}, wantCode: 200, wantCalls: 1, limit: 1, items: 1, next: "7"},
		{name: "next page", id: "9", query: "?limit=1&after=7", rows: []store.DeliveryAttempt{unfinished}, wantCode: 200, wantCalls: 1, limit: 1, after: 7, items: 1},
		{name: "unauthorized", id: "9", unauthed: true, wantCode: 401, wantError: "unauthorized"},
		{name: "zero id", id: "0", wantCode: 400, wantError: "invalid_delivery_id"},
		{name: "invalid id", id: "bad", wantCode: 400, wantError: "invalid_delivery_id"},
		{name: "zero limit", id: "9", query: "?limit=0", wantCode: 400, wantError: "invalid_pagination"},
		{name: "large limit", id: "9", query: "?limit=101", wantCode: 400, wantError: "invalid_pagination"},
		{name: "invalid limit", id: "9", query: "?limit=bad", wantCode: 400, wantError: "invalid_pagination"},
		{name: "negative cursor", id: "9", query: "?after=-1", wantCode: 400, wantError: "invalid_pagination"},
		{name: "overflow cursor", id: "9", query: "?after=18446744073709551616", wantCode: 400, wantError: "invalid_pagination"},
		{name: "not found", id: "9", dbErr: fmt.Errorf("wrapped: %w", store.ErrDeliveryNotFound), wantCode: 404, wantCalls: 1, limit: 20, wantError: "delivery_not_found"},
		{name: "database error", id: "9", dbErr: errors.New("private database details"), wantCode: 500, wantCalls: 1, limit: 20, wantError: "internal_error"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			type requestKey struct{}
			ctx := context.WithValue(context.Background(), requestKey{}, "request")
			calls := 0
			router := NewAPIRouter(func(context.Context) error { return nil }, "key", "", nil, nil, nil, nil, nil,
				func(ctx context.Context, id, after uint64, limit int) ([]store.DeliveryAttempt, error) {
					calls++
					if id != 9 || after != tt.after || limit != tt.limit || ctx.Value(requestKey{}) != "request" {
						t.Fatal("incorrect query or context")
					}
					return tt.rows, tt.dbErr
				})
			request := httptest.NewRequest(http.MethodGet, "/v1/deliveries/"+tt.id+"/attempts"+tt.query, nil).WithContext(ctx)
			if !tt.unauthed {
				request.Header.Set("X-API-Key", "key")
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != tt.wantCode || calls != tt.wantCalls {
				t.Fatalf("status=%d calls=%d", response.Code, calls)
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
			items, ok := got["items"].([]any)
			if !ok || len(got) != 2 || len(items) != tt.items || got["next_after"] != tt.next {
				t.Fatalf("unexpected page: %v", got)
			}
			if len(items) == 0 {
				return
			}
			item := items[0].(map[string]any)
			if len(item) != 10 || item["delivery_id"] != "9" || item["started_at"] != now.Format(time.RFC3339Nano) {
				t.Fatalf("unexpected attempt: %v", item)
			}
			if tt.name == "first page" {
				if item["id"] != "7" || item["http_status"] != float64(500) || item["latency_ms"] != float64(25) || item["error_class"] != "http_error" || item["status"] != "retrying" || item["finished_at"] == nil {
					t.Fatal("finished fields missing")
				}
			} else if item["id"] != "18446744073709551615" || item["generation"] != float64(2) || item["attempt_number"] != float64(1) || item["status"] != "processing" || item["finished_at"] != nil || item["http_status"] != nil || item["latency_ms"] != nil {
				t.Fatal("unfinished fields or ID precision incorrect")
			}
		})
	}
}
