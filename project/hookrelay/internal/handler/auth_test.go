package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRequireAPIKey(t *testing.T) {
	tests := []struct {
		name     string
		expected string
		provided string
		wantCode int
	}{
		{"valid", "test-key", "test-key", http.StatusOK},
		{"missing", "test-key", "", http.StatusUnauthorized},
		{"wrong", "test-key", "wrong-key", http.StatusUnauthorized},
		{"empty config", "", "test-key", http.StatusUnauthorized},
		{"both empty", "", "", http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false

			router := gin.New()
			router.GET(
				"/protected",
				RequireAPIKey(tt.expected),
				func(c *gin.Context) {
					called = true
					c.Status(http.StatusOK)
				},
			)

			request := httptest.NewRequest(
				http.MethodGet, "/protected", nil,
			)
			if tt.provided != "" {
				request.Header.Set("X-API-Key", tt.provided)
			}

			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			if response.Code != tt.wantCode {
				t.Fatalf(
					"status = %d, want %d",
					response.Code, tt.wantCode,
				)
			}
			if called != (tt.wantCode == http.StatusOK) {
				t.Fatalf("unexpected handler execution: %v", called)
			}
		})
	}
}
