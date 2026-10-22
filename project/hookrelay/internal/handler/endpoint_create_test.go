package handler

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aurelio/hookrelay/internal/store"
)

func TestPostEndpoint(t *testing.T) {
	const allowedURL = "http://127.0.0.1:9090/webhook"
	const apiKey = "test-api-key"
	const dbDetails = "sensitive database error"
	createdAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tests := []struct {
		name, body, key, contentType string
		wantError                    string
		wantCode                     int
		wantCreate                   bool
		dbErr                        error
	}{
		{name: "missing key", body: `{"url":"` + allowedURL + `"}`, contentType: "application/json", wantCode: 401, wantError: "unauthorized"},
		{name: "wrong key", body: `{"url":"` + allowedURL + `"}`, key: "wrong", contentType: "application/json", wantCode: 401, wantError: "unauthorized"},
		{name: "wrong content type", body: `{"url":"` + allowedURL + `"}`, key: apiKey, contentType: "text/plain", wantCode: 415, wantError: "unsupported_media_type"},
		{name: "empty body", key: apiKey, contentType: "application/json", wantCode: 400, wantError: "invalid_request"},
		{name: "malformed JSON", body: `{"url":`, key: apiKey, contentType: "application/json", wantCode: 400, wantError: "invalid_request"},
		{name: "multiple JSON values", body: `{"url":"` + allowedURL + `"}{}`, key: apiKey, contentType: "application/json", wantCode: 400, wantError: "invalid_request"},
		{name: "trailing garbage", body: `{"url":"` + allowedURL + `"}garbage`, key: apiKey, contentType: "application/json", wantCode: 400, wantError: "invalid_request"},
		{name: "unknown field", body: `{"url":"` + allowedURL + `","enabled":false}`, key: apiKey, contentType: "application/json", wantCode: 400, wantError: "invalid_request"},
		{name: "client-supplied secret", body: `{"url":"` + allowedURL + `","signing_secret":"guess"}`, key: apiKey, contentType: "application/json", wantCode: 400, wantError: "invalid_request"},
		{name: "non-string URL", body: `{"url":42}`, key: apiKey, contentType: "application/json", wantCode: 400, wantError: "invalid_request"},
		{name: "missing URL", body: `{}`, key: apiKey, contentType: "application/json", wantCode: 400, wantError: "invalid_endpoint_url"},
		{name: "different URL", body: `{"url":"http://127.0.0.1:9090/other"}`, key: apiKey, contentType: "application/json", wantCode: 400, wantError: "invalid_endpoint_url"},
		{name: "prefix attack", body: `{"url":"` + allowedURL + `/other"}`, key: apiKey, contentType: "application/json", wantCode: 400, wantError: "invalid_endpoint_url"},
		{name: "unconfigured allowlist", body: `{"url":"` + allowedURL + `"}`, key: apiKey, contentType: "application/json", wantCode: 400, wantError: "invalid_endpoint_url"},
		{name: "oversized body", body: `{"url":"` + allowedURL + `"}` + strings.Repeat(" ", maxEndpointBodyBytes), key: apiKey, contentType: "application/json", wantCode: 413, wantError: "request_too_large"},
		{name: "database failure", body: `{"url":"` + allowedURL + `"}`, key: apiKey, contentType: "application/json; charset=utf-8", dbErr: errors.New(dbDetails), wantCode: 500, wantError: "internal_error", wantCreate: true},
		{name: "created", body: `{"url":"` + allowedURL + `"}`, key: apiKey, contentType: "application/json", wantCode: 201, wantCreate: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// In all tests except the explicit unconfigured case, use the allowlist.
			allowed := allowedURL
			if tt.name == "unconfigured allowlist" {
				allowed = ""
			}
			type contextKey struct{}
			ctx := context.WithValue(context.Background(), contextKey{}, "request")
			calls := 0
			var saved store.Endpoint
			router := NewAPIRouter(
				func(context.Context) error { t.Error("POST must not ping database"); return nil },
				apiKey, allowed,
				func(context.Context, uint64) (store.Endpoint, error) {
					t.Error("POST must not query endpoints")
					return store.Endpoint{}, nil
				},
				func(ctx context.Context, endpoint *store.Endpoint) error {
					calls++
					if ctx.Value(contextKey{}) != "request" {
						t.Error("request context not forwarded")
					}
					saved = *endpoint
					endpoint.ID = ^uint64(0)
					endpoint.CreatedAt = createdAt
					return tt.dbErr
				},
			)
			request := httptest.NewRequest(http.MethodPost, "/v1/endpoints", strings.NewReader(tt.body)).WithContext(ctx)
			request.Header.Set("Content-Type", tt.contentType)
			if tt.key != "" {
				request.Header.Set("X-API-Key", tt.key)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", response.Code, tt.wantCode)
			}
			wantCalls := 0
			if tt.wantCreate {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Fatalf("create calls = %d, want %d", calls, wantCalls)
			}
			if strings.Contains(response.Body.String(), dbDetails) || strings.Contains(response.Body.String(), apiKey) {
				t.Fatal("response exposed sensitive information")
			}
			var got map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
				t.Fatalf("response is not JSON: %v", err)
			}
			if tt.wantCode != 201 {
				if !reflect.DeepEqual(got, map[string]any{"error": tt.wantError}) {
					t.Fatal("unexpected error response")
				}
				if _, exists := got["signing_secret"]; exists {
					t.Fatal("failed requests must not expose a secret")
				}
				return
			}
			if saved.URL != allowedURL || !saved.Enabled || saved.ID != 0 {
				t.Fatal("unexpected endpoint passed to store")
			}
			secret, err := hex.DecodeString(saved.SigningSecret)
			if err != nil || len(secret) != 32 || len(saved.SigningSecret) != 64 {
				t.Fatal("secret must be 32 random bytes encoded as 64 hex characters")
			}
			want := map[string]any{
				"id": "18446744073709551615", "url": allowedURL,
				"enabled": true, "created_at": createdAt.Format(time.RFC3339Nano),
				"signing_secret": saved.SigningSecret,
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatal("unexpected creation response")
			}
		})
	}
}

func TestPostEndpointGeneratesNewSecret(t *testing.T) {
	const url = "http://127.0.0.1:9090/webhook"
	var secrets []string
	router := NewAPIRouter(
		func(context.Context) error { return nil }, "key", url,
		func(context.Context, uint64) (store.Endpoint, error) { return store.Endpoint{}, nil },
		func(_ context.Context, endpoint *store.Endpoint) error {
			secrets = append(secrets, endpoint.SigningSecret)
			endpoint.ID = uint64(len(secrets))
			return nil
		},
	)
	for range 2 {
		request := httptest.NewRequest(http.MethodPost, "/v1/endpoints", strings.NewReader(`{"url":"`+url+`"}`))
		request.Header.Set("X-API-Key", "key")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != 201 {
			t.Fatalf("status = %d, want 201", response.Code)
		}
	}
	if len(secrets) != 2 || secrets[0] == secrets[1] {
		t.Fatal("each endpoint must have its own secret")
	}
}
