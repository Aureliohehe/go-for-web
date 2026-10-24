package config

import "testing"

func TestMaxPendingDeliveries(t *testing.T) {
	for _, tt := range []struct {
		value   string
		want    int64
		invalid bool
	}{
		{"", 10000, false}, {"1", 1, false}, {"37", 37, false},
		{"0", 0, true}, {"-1", 0, true}, {"abc", 0, true}, {"9223372036854775808", 0, true},
	} {
		t.Run(tt.value, func(t *testing.T) {
			cfg, err := load(func(key string) string {
				switch key {
				case "MYSQL_PASSWORD":
					return "test"
				case "MAX_PENDING_DELIVERIES":
					return tt.value
				}
				return ""
			})
			if (err != nil) != tt.invalid {
				t.Fatalf("err=%v invalid=%v", err, tt.invalid)
			}
			if err == nil && cfg.MaxPendingDeliveries != tt.want {
				t.Fatalf("limit=%d want=%d", cfg.MaxPendingDeliveries, tt.want)
			}
		})
	}
}
