package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aurelio/hookrelay/internal/config"
	"github.com/aurelio/hookrelay/internal/handler"
	"github.com/aurelio/hookrelay/internal/store"
	"github.com/gin-gonic/gin"
)

// Requires a running MySQL with migrations/000001_create_endpoints.up.sql applied.
// Each inserted endpoint is rolled back when the test finishes.
func TestEndpointHTTPMySQLRoundTrip(t *testing.T) {
	if os.Getenv("HOOKRELAY_INTEGRATION") != "1" {
		t.Skip("set HOOKRELAY_INTEGRATION=1 to run against MySQL")
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load database config: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	db, pool, err := store.OpenMySQL(ctx, cfg)
	cancel()
	if err != nil {
		t.Fatalf("open MySQL: %v", err)
	}
	defer pool.Close()
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal("begin endpoint test transaction failed")
	}
	defer tx.Rollback()

	const key = "integration-test-key"
	const allowedURL = "http://127.0.0.1:9090/webhook"
	gin.SetMode(gin.TestMode)
	router := handler.NewAPIRouter(
		pool.PingContext, key, allowedURL,
		func(ctx context.Context, id uint64) (store.Endpoint, error) {
			return store.FindEndpoint(ctx, tx, id)
		},
		func(ctx context.Context, endpoint *store.Endpoint) error {
			return store.CreateEndpoint(ctx, tx, endpoint)
		},
		func(ctx context.Context, id uint64, enabled bool) (store.Endpoint, error) {
			return store.SetEndpointEnabled(ctx, tx, id, enabled)
		},
		nil, nil, nil, nil,
	)
	server := httptest.NewServer(router)
	defer server.Close()

	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/endpoints",
		strings.NewReader(`{"url":"`+allowedURL+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-API-Key", key)
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("POST endpoint: %v", err)
	}
	createBody, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusCreated {
		t.Fatalf("POST failed: status=%d, read error=%v", response.StatusCode, err)
	}
	var created struct {
		ID            string `json:"id"`
		URL           string `json:"url"`
		Enabled       bool   `json:"enabled"`
		CreatedAt     string `json:"created_at"`
		SigningSecret string `json:"signing_secret"`
	}
	if err := json.Unmarshal(createBody, &created); err != nil {
		t.Fatal("POST response is not valid JSON")
	}
	id, err := strconv.ParseUint(created.ID, 10, 64)
	if err != nil || id == 0 || created.URL != allowedURL || !created.Enabled {
		t.Fatal("POST response has unexpected endpoint fields")
	}
	if _, err := time.Parse(time.RFC3339Nano, created.CreatedAt); err != nil {
		t.Fatal("POST response has invalid creation time")
	}
	secretBytes, err := hex.DecodeString(created.SigningSecret)
	if err != nil || len(secretBytes) != 32 || len(created.SigningSecret) != 64 {
		t.Fatal("POST response has no valid signing secret")
	}

	request, err = http.NewRequest(http.MethodGet, server.URL+"/v1/endpoints/"+created.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-API-Key", key)
	response, err = server.Client().Do(request)
	if err != nil {
		t.Fatalf("GET endpoint: %v", err)
	}
	getBody, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("GET failed: status=%d, read error=%v", response.StatusCode, err)
	}
	var got map[string]any
	if err := json.Unmarshal(getBody, &got); err != nil {
		t.Fatal("GET response is not valid JSON")
	}
	if len(got) != 4 {
		t.Fatal("GET response has unexpected fields")
	}
	if got["id"] != created.ID {
		t.Fatal("GET id does not match POST id")
	}
	if got["url"] != allowedURL {
		t.Fatal("GET url does not match POST url")
	}
	if got["enabled"] != true {
		t.Fatal("GET enabled does not match POST enabled")
	}
	if got["created_at"] != created.CreatedAt {
		t.Fatalf("created_at mismatch: POST=%q GET=%q", created.CreatedAt, got["created_at"])
	}
	if _, exists := got["signing_secret"]; exists || strings.Contains(string(getBody), created.SigningSecret) {
		t.Fatal("GET response exposed the signing secret")
	}

	// Disable, repeat the same update, then enable again. Both PATCH and GET
	// must reflect the new flag without returning the saved signing secret.
	for _, enabled := range []bool{false, false, true} {
		body := `{"enabled":false}`
		if enabled {
			body = `{"enabled":true}`
		}
		request, err := http.NewRequest(http.MethodPatch, server.URL+"/v1/endpoints/"+created.ID,
			strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("X-API-Key", key)
		request.Header.Set("Content-Type", "application/json")
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatalf("PATCH endpoint: %v", err)
		}
		patchBody, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("PATCH failed: status=%d, read error=%v", response.StatusCode, err)
		}
		var patched map[string]any
		if err := json.Unmarshal(patchBody, &patched); err != nil {
			t.Fatal("PATCH response is not valid JSON")
		}
		if len(patched) != 4 || patched["id"] != created.ID || patched["url"] != allowedURL ||
			patched["enabled"] != enabled || patched["created_at"] != created.CreatedAt {
			t.Fatal("PATCH response does not match the updated endpoint")
		}
		if strings.Contains(string(patchBody), created.SigningSecret) {
			t.Fatal("PATCH response exposed the signing secret")
		}

		request, err = http.NewRequest(http.MethodGet, server.URL+"/v1/endpoints/"+created.ID, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("X-API-Key", key)
		response, err = server.Client().Do(request)
		if err != nil {
			t.Fatalf("GET after PATCH: %v", err)
		}
		getBody, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("GET after PATCH failed: status=%d, read error=%v", response.StatusCode, err)
		}
		if err := json.Unmarshal(getBody, &got); err != nil {
			t.Fatal("GET after PATCH response is not valid JSON")
		}
		if len(got) != 4 || got["enabled"] != enabled ||
			got["id"] != created.ID || got["url"] != allowedURL ||
			got["created_at"] != created.CreatedAt || strings.Contains(string(getBody), created.SigningSecret) {
			t.Fatal("GET after PATCH does not match the updated endpoint or exposed the secret")
		}
	}
	persisted, err := store.FindEndpoint(context.Background(), tx, id)
	if err != nil || persisted.SigningSecret != created.SigningSecret {
		t.Fatal("PATCH changed or lost the stored signing secret")
	}
}
