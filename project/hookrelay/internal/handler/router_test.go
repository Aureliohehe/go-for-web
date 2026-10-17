package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthChecks(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		dbErr    error
		wantCode int
		wantBody string
		wantPing bool
	}{
		{"live even without database", "/healthz", errors.New("database unavailable"), 200, `{"status":"ok"}`, false},
		{"ready", "/readyz", nil, 200, `{"status":"ready"}`, true},
		{"database unavailable", "/readyz", errors.New("sensitive connection details"), 503, `{"status":"not_ready"}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			router := NewRouter(func(ctx context.Context) error {
				called = true
				if _, ok := ctx.Deadline(); !ok {
					t.Error("readiness ping must have a deadline")
				}
				return tt.dbErr
			})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if response.Code != tt.wantCode || strings.TrimSpace(response.Body.String()) != tt.wantBody {
				t.Fatalf("response = %d %s", response.Code, response.Body.String())
			}
			if called != tt.wantPing {
				t.Fatalf("database ping called = %v, want %v", called, tt.wantPing)
			}
		})
	}
}
