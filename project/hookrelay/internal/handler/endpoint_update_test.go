package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aurelio/hookrelay/internal/store"
)

func TestPatchEndpointEnabled(t *testing.T) {
	const key = "test-api-key"
	const secret = "must-not-leak-secret"
	const dbDetails = "sensitive database details"
	createdAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tests := []struct {
		name, id, body, key, contentType, wantError string
		wantCode, wantCalls                         int
		wantID                                      uint64
		wantEnabled                                 bool
		dbErr                                       error
	}{
		{name: "missing key", id: "1", body: `{"enabled":false}`, contentType: "application/json", wantCode: 401, wantError: "unauthorized"},
		{name: "wrong key", id: "1", body: `{"enabled":false}`, key: "wrong", contentType: "application/json", wantCode: 401, wantError: "unauthorized"},
		{name: "invalid ID", id: "abc", body: `{"enabled":false}`, key: key, contentType: "application/json", wantCode: 400, wantError: "invalid_endpoint_id"},
		{name: "zero ID", id: "0", body: `{"enabled":false}`, key: key, contentType: "application/json", wantCode: 400, wantError: "invalid_endpoint_id"},
		{name: "negative ID", id: "-1", body: `{"enabled":false}`, key: key, contentType: "application/json", wantCode: 400, wantError: "invalid_endpoint_id"},
		{name: "overflow ID", id: "18446744073709551616", body: `{"enabled":false}`, key: key, contentType: "application/json", wantCode: 400, wantError: "invalid_endpoint_id"},
		{name: "wrong content type", id: "1", body: `{"enabled":false}`, key: key, contentType: "text/plain", wantCode: 415, wantError: "unsupported_media_type"},
		{name: "empty body", id: "1", key: key, contentType: "application/json", wantCode: 400, wantError: "invalid_request"},
		{name: "malformed JSON", id: "1", body: `{"enabled":`, key: key, contentType: "application/json", wantCode: 400, wantError: "invalid_request"},
		{name: "multiple JSON values", id: "1", body: `{"enabled":false}{}`, key: key, contentType: "application/json", wantCode: 400, wantError: "invalid_request"},
		{name: "extra field", id: "1", body: `{"enabled":false,"url":"http://other"}`, key: key, contentType: "application/json", wantCode: 400, wantError: "invalid_request"},
		{name: "missing enabled", id: "1", body: `{}`, key: key, contentType: "application/json", wantCode: 400, wantError: "invalid_request"},
		{name: "null enabled", id: "1", body: `{"enabled":null}`, key: key, contentType: "application/json", wantCode: 400, wantError: "invalid_request"},
		{name: "non-boolean", id: "1", body: `{"enabled":"false"}`, key: key, contentType: "application/json", wantCode: 400, wantError: "invalid_request"},
		{name: "oversized body", id: "1", body: `{"enabled":false}` + strings.Repeat(" ", maxEndpointBodyBytes), key: key, contentType: "application/json", wantCode: 413, wantError: "request_too_large"},
		{name: "not found", id: "1", body: `{"enabled":false}`, key: key, contentType: "application/json", wantCode: 404, wantError: "endpoint_not_found", wantCalls: 1, wantID: 1, dbErr: fmt.Errorf("lookup: %w", store.ErrEndpointNotFound)},
		{name: "database error", id: "1", body: `{"enabled":false}`, key: key, contentType: "application/json", wantCode: 500, wantError: "internal_error", wantCalls: 1, wantID: 1, dbErr: errors.New(dbDetails)},
		{name: "disable", id: "1", body: `{"enabled":false}`, key: key, contentType: "application/json", wantCode: 200, wantCalls: 1, wantID: 1},
		{name: "enable", id: "1", body: `{"enabled":true}`, key: key, contentType: "application/json", wantCode: 200, wantCalls: 1, wantID: 1, wantEnabled: true},
		{name: "large ID", id: "18446744073709551615", body: `{"enabled":true}`, key: key, contentType: "application/json", wantCode: 200, wantCalls: 1, wantID: ^uint64(0), wantEnabled: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			type requestKey struct{}
			ctx := context.WithValue(context.Background(), requestKey{}, tt.name)
			calls := 0
			router := NewAPIRouter(
				func(context.Context) error { t.Error("PATCH must not ping database"); return nil },
				key, "http://127.0.0.1:9090/webhook",
				func(context.Context, uint64) (store.Endpoint, error) {
					t.Error("PATCH must not call GET endpoint")
					return store.Endpoint{}, nil
				},
				func(context.Context, *store.Endpoint) error {
					t.Error("PATCH must not create endpoint")
					return nil
				},
				func(ctx context.Context, id uint64, enabled bool) (store.Endpoint, error) {
					calls++
					if id != tt.wantID || enabled != tt.wantEnabled || ctx.Value(requestKey{}) != tt.name {
						t.Error("incorrect ID, enabled flag or request context")
					}
					return store.Endpoint{
						ID: id, URL: "http://127.0.0.1:9090/webhook", Enabled: enabled,
						SigningSecret: secret, CreatedAt: createdAt,
					}, tt.dbErr
				},
				nil, nil, nil,
			)
			request := httptest.NewRequest(http.MethodPatch, "/v1/endpoints/"+tt.id, strings.NewReader(tt.body)).WithContext(ctx)
			request.Header.Set("Content-Type", tt.contentType)
			if tt.key != "" {
				request.Header.Set("X-API-Key", tt.key)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != tt.wantCode || calls != tt.wantCalls {
				t.Fatalf("status=%d calls=%d, want status=%d calls=%d", response.Code, calls, tt.wantCode, tt.wantCalls)
			}
			if strings.Contains(response.Body.String(), secret) || strings.Contains(response.Body.String(), "signing_secret") ||
				strings.Contains(response.Body.String(), dbDetails) || strings.Contains(response.Body.String(), key) {
				t.Fatal("PATCH response exposed sensitive data")
			}
			var got map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
				t.Fatal("PATCH response is not valid JSON")
			}
			want := map[string]any{"error": tt.wantError}
			if tt.wantCode == http.StatusOK {
				want = map[string]any{
					"id": strconv.FormatUint(tt.wantID, 10), "url": "http://127.0.0.1:9090/webhook",
					"enabled": tt.wantEnabled, "created_at": createdAt.Format(time.RFC3339Nano),
				}
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatal("PATCH response has incorrect fields")
			}
		})
	}
}
