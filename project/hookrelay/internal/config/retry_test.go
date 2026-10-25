package config

import (
	"testing"
	"time"
)

func TestRetryConfig(t *testing.T) {
	for _, tt := range []struct {
		name, base, max   string
		wantBase, wantMax time.Duration
		invalid           bool
	}{
		{name: "defaults", wantBase: 5 * time.Second, wantMax: time.Minute},
		{name: "short test delays", base: "100ms", max: "2s", wantBase: 100 * time.Millisecond, wantMax: 2 * time.Second},
		{name: "equal limits", base: "1s", max: "1s", wantBase: time.Second, wantMax: time.Second},
		{name: "zero base", base: "0s", invalid: true},
		{name: "negative base", base: "-1s", invalid: true},
		{name: "bad base", base: "oops", invalid: true},
		{name: "bad max", max: "oops", invalid: true},
		{name: "zero max", max: "0s", invalid: true},
		{name: "max below base", base: "5s", max: "1s", invalid: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := load(func(key string) string {
				switch key {
				case "MYSQL_PASSWORD":
					return "test"
				case "RETRY_BASE_DELAY":
					return tt.base
				case "RETRY_MAX_DELAY":
					return tt.max
				}
				return ""
			})
			if (err != nil) != tt.invalid {
				t.Fatalf("error=%v invalid=%v", err, tt.invalid)
			}
			if err == nil && (cfg.RetryBaseDelay != tt.wantBase || cfg.RetryMaxDelay != tt.wantMax) {
				t.Fatal("incorrect retry configuration")
			}
		})
	}
}
