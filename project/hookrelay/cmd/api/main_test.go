package main

import (
	"context"
	"strings"
	"testing"
)

func TestRunRequiresAllowlistBeforeDatabaseConnection(t *testing.T) {
	t.Setenv("MYSQL_PASSWORD", "test-password")
	t.Setenv("MYSQL_PORT", "13306")
	t.Setenv("HTTP_ADDR", "127.0.0.1:8080")
	t.Setenv("API_KEY", "test-key")
	t.Setenv("ALLOWED_ENDPOINT_URL", "")
	if runErr := run(context.Background()); runErr == nil || !strings.Contains(runErr.Error(), "ALLOWED_ENDPOINT_URL") {
		t.Fatal("missing allowlist must stop startup before connecting to MySQL")
	}
}
