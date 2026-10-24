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

func TestGetEndpoint(t *testing.T) {
	const apiKey = "test-api-key"
	const signingSecret = "secret-that-must-not-appear-in-responses"
	const databaseDetails = "sensitive database connection details"
	createdAt := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)

	tests := []struct {
		name       string
		id         string
		key        string
		dbErr      error
		disabled   bool
		wantCode   int
		wantError  string
		wantLookup bool
		wantID     uint64
	}{
		{
			name: "missing key", id: "1",
			wantCode: http.StatusUnauthorized, wantError: "unauthorized",
		},
		{
			name: "wrong key", id: "1", key: "wrong-api-key",
			wantCode: http.StatusUnauthorized, wantError: "unauthorized",
		},
		{
			name: "authentication precedes ID validation", id: "abc",
			wantCode: http.StatusUnauthorized, wantError: "unauthorized",
		},
		{
			name: "non-numeric ID", id: "abc", key: apiKey,
			wantCode: http.StatusBadRequest, wantError: "invalid_endpoint_id",
		},
		{
			name: "zero ID", id: "0", key: apiKey,
			wantCode: http.StatusBadRequest, wantError: "invalid_endpoint_id",
		},
		{
			name: "negative ID", id: "-1", key: apiKey,
			wantCode: http.StatusBadRequest, wantError: "invalid_endpoint_id",
		},
		{
			name: "overflowing ID", id: "18446744073709551616", key: apiKey,
			wantCode: http.StatusBadRequest, wantError: "invalid_endpoint_id",
		},
		{
			name: "found", id: "1", key: apiKey,
			wantCode: http.StatusOK, wantLookup: true, wantID: 1,
		},
		{
			name: "disabled endpoint remains queryable", id: "1", key: apiKey,
			disabled: true, wantCode: http.StatusOK, wantLookup: true, wantID: 1,
		},
		{
			name: "large ID stays an exact string", id: "18446744073709551615", key: apiKey,
			wantCode: http.StatusOK, wantLookup: true, wantID: ^uint64(0),
		},
		{
			name: "not found", id: "1", key: apiKey, dbErr: store.ErrEndpointNotFound,
			wantCode: http.StatusNotFound, wantError: "endpoint_not_found", wantLookup: true, wantID: 1,
		},
		{
			name: "wrapped not found", id: "1", key: apiKey,
			dbErr:    fmt.Errorf("lookup: %w", store.ErrEndpointNotFound),
			wantCode: http.StatusNotFound, wantError: "endpoint_not_found", wantLookup: true, wantID: 1,
		},
		{
			name: "database failure", id: "1", key: apiKey, dbErr: errors.New(databaseDetails),
			wantCode: http.StatusInternalServerError, wantError: "internal_error", wantLookup: true, wantID: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A private context key verifies that the request context reaches the store.
			type requestIDKey struct{}
			ctx := context.WithValue(context.Background(), requestIDKey{}, tt.name)
			request := httptest.NewRequest(http.MethodGet, "/v1/endpoints/"+tt.id, nil).WithContext(ctx)
			if tt.key != "" {
				request.Header.Set("X-API-Key", tt.key)
			}

			lookupCalls := 0
			router := NewAPIRouter(
				func(context.Context) error {
					t.Error("endpoint lookup must not invoke the health check")
					return nil
				},
				apiKey,
				"http://127.0.0.1:9090/webhook",
				func(ctx context.Context, id uint64) (store.Endpoint, error) {
					lookupCalls++
					if id != tt.wantID {
						t.Errorf("lookup ID = %d, want %d", id, tt.wantID)
					}
					if ctx.Value(requestIDKey{}) != tt.name {
						t.Error("request context was not forwarded to lookup")
					}
					return store.Endpoint{
						ID: id, URL: "http://127.0.0.1:9090/webhook",
						SigningSecret: signingSecret, Enabled: !tt.disabled, CreatedAt: createdAt,
					}, tt.dbErr
				},
				func(context.Context, *store.Endpoint) error {
					t.Error("GET must not create endpoints")
					return nil
				},
				nil, // GET must not update endpoints.
				nil, nil, nil, nil,
			)

			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			if response.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", response.Code, tt.wantCode)
			}
			wantCalls := 0
			if tt.wantLookup {
				wantCalls = 1
			}
			if lookupCalls != wantCalls {
				t.Fatalf("lookup calls = %d, want %d", lookupCalls, wantCalls)
			}
			if got := response.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
				t.Fatalf("Content-Type = %q, want application/json", got)
			}

			body := response.Body.String()
			for _, sensitive := range []string{"signing_secret", signingSecret, apiKey, databaseDetails} {
				if strings.Contains(body, sensitive) {
					t.Fatal("response exposed sensitive information")
				}
			}

			var got map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
				t.Fatalf("invalid JSON response: %v", err)
			}
			want := map[string]any{"error": tt.wantError}
			if tt.wantCode == http.StatusOK {
				want = map[string]any{
					"id":         strconv.FormatUint(tt.wantID, 10),
					"url":        "http://127.0.0.1:9090/webhook",
					"enabled":    !tt.disabled,
					"created_at": createdAt.Format(time.RFC3339Nano),
				}
			}
			// Compare parsed JSON so key ordering does not affect the test.
			// Exact field matching also rejects accidental extra model fields.
			if !reflect.DeepEqual(got, want) {
				t.Fatal("response fields or values do not match the expected JSON")
			}
		})
	}
}

func TestAPIRouterHealthChecksWithoutAPIKey(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		pingErr   error
		wantCode  int
		wantBody  string
		wantPings int
	}{
		{
			name: "health does not depend on database", path: "/healthz",
			pingErr:  errors.New("database unavailable"),
			wantCode: http.StatusOK, wantBody: `{"status":"ok"}`,
		},
		{
			name: "ready", path: "/readyz",
			wantCode: http.StatusOK, wantBody: `{"status":"ready"}`, wantPings: 1,
		},
		{
			name: "not ready", path: "/readyz", pingErr: errors.New("sensitive connection details"),
			wantCode: http.StatusServiceUnavailable, wantBody: `{"status":"not_ready"}`, wantPings: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pingCalls := 0
			router := NewAPIRouter(
				func(context.Context) error {
					pingCalls++
					return tt.pingErr
				},
				"test-api-key",
				"http://127.0.0.1:9090/webhook",
				func(context.Context, uint64) (store.Endpoint, error) {
					t.Error("health routes must not query endpoints")
					return store.Endpoint{}, errors.New("unexpected lookup")
				},
				func(context.Context, *store.Endpoint) error {
					t.Error("health routes must not create endpoints")
					return nil
				},
				nil, // Health routes must not update endpoints.
				nil, nil, nil, nil,
			)

			response := httptest.NewRecorder()
			// Deliberately omit X-API-Key on both health routes.
			request := httptest.NewRequest(http.MethodGet, tt.path, nil)
			router.ServeHTTP(response, request)

			if response.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", response.Code, tt.wantCode)
			}
			if strings.TrimSpace(response.Body.String()) != tt.wantBody {
				t.Fatal("unexpected health response")
			}
			if pingCalls != tt.wantPings {
				t.Fatalf("ping calls = %d, want %d", pingCalls, tt.wantPings)
			}
		})
	}
}
