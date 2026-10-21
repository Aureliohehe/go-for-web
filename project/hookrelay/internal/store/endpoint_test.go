package store

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aurelio/hookrelay/internal/config"
)

func TestEndpointRoundTripIntegration(t *testing.T) {
	if os.Getenv("HOOKRELAY_INTEGRATION") != "1" {
		t.Skip("set HOOKRELAY_INTEGRATION=1 to run")
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db, pool, err := OpenMySQL(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	tx := db.WithContext(ctx).Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()

	tests := []struct {
		name    string
		enabled bool
	}{
		{name: "enabled", enabled: true},
		{name: "disabled", enabled: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			endpoint := Endpoint{
				URL:           "http://127.0.0.1:9090/webhook",
				SigningSecret: strings.Repeat("a", 64),
				Enabled:       tt.enabled,
			}

			if err := CreateEndpoint(ctx, tx, &endpoint); err != nil {
				t.Fatal(err)
			}
			if endpoint.ID == 0 {
				t.Fatal("插入成功后应回填 ID")
			}

			got, err := FindEndpoint(ctx, tx, endpoint.ID)
			if err != nil {
				t.Fatal(err)
			}

			if got.ID != endpoint.ID ||
				got.URL != endpoint.URL ||
				got.SigningSecret != endpoint.SigningSecret ||
				got.Enabled != endpoint.Enabled {
				t.Fatal("查询结果与插入内容不一致")
			}
			if got.CreatedAt.IsZero() {
				t.Fatal("创建时间不应为空")
			}

			if err := tx.WithContext(ctx).
				Delete(&Endpoint{}, endpoint.ID).Error; err != nil {
				t.Fatal(err)
			}

			_, err = FindEndpoint(ctx, tx, endpoint.ID)
			if !errors.Is(err, ErrEndpointNotFound) {
				t.Fatalf("error = %v, want ErrEndpointNotFound", err)
			}
		})
	}
}
