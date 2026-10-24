package config_test

import (
	"testing"

	"github.com/aurelio/hookrelay/internal/config"
)

func TestLoadDBConfigMissingPassword(t *testing.T) {
	t.Setenv("HTTP_ADDR", "")
	t.Setenv("MYSQL_PASSWORD", "")

	_, err := config.Load()
	if err == nil {
		t.Fatal("密码为空时应返回错误，但得到 nil")
	}
}

func TestLoadDBConfigSuccess(t *testing.T) {
	t.Setenv("MAX_PENDING_DELIVERIES", "10000")
	t.Setenv("REDIS_ADDR", "127.0.0.1:16379")
	t.Setenv("HTTP_ADDR", "")
	t.Setenv("MYSQL_HOST", "127.0.0.1")
	t.Setenv("MYSQL_PORT", "13306")
	t.Setenv("MYSQL_USER", "test_user")
	t.Setenv("MYSQL_PASSWORD", "test_password")
	t.Setenv("MYSQL_DATABASE", "test_db")
	t.Setenv("API_KEY", "test-key")
	t.Setenv("ALLOWED_ENDPOINT_URL", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("配置完整时不应报错：%v", err)
	}

	want := config.Config{
		HTTPAddr:             "127.0.0.1:8080",
		MySQLHost:            "127.0.0.1",
		MySQLPort:            "13306",
		RedisAddr:            "127.0.0.1:16379",
		MySQLUser:            "test_user",
		MySQLPassword:        "test_password",
		MySQLDatabase:        "test_db",
		APIKey:               "test-key",
		MaxPendingDeliveries: 10000,
	}
	if cfg != want {
		t.Fatal("读取到的配置与预期不一致")
	}
}

func TestLoadDBConfigDefaults(t *testing.T) {
	t.Setenv("MAX_PENDING_DELIVERIES", "")
	t.Setenv("REDIS_ADDR", "")
	t.Setenv("HTTP_ADDR", "")
	t.Setenv("MYSQL_HOST", "")
	t.Setenv("MYSQL_PORT", "")
	t.Setenv("MYSQL_USER", "")
	t.Setenv("MYSQL_PASSWORD", "test_password")
	t.Setenv("MYSQL_DATABASE", "")
	t.Setenv("API_KEY", "")
	t.Setenv("ALLOWED_ENDPOINT_URL", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("配置完整时不应报错：%v", err)
	}
	want := config.Config{
		HTTPAddr:             "127.0.0.1:8080",
		MySQLHost:            "127.0.0.1",
		MySQLPort:            "13306",
		RedisAddr:            "127.0.0.1:16379",
		MySQLUser:            "hookrelay",
		MySQLPassword:        "test_password",
		MySQLDatabase:        "hookrelay",
		MaxPendingDeliveries: 10000,
	}
	if cfg != want {
		t.Fatal("读取到的配置与预期不一致")
	}
}

func TestLoadDBConfigPort(t *testing.T) {
	t.Setenv("HTTP_ADDR", "")
	tests := []struct {
		name    string
		port    string
		wantErr bool
	}{
		{name: "minimum", port: "1"},
		{name: "maximum", port: "65535"},
		{name: "normal", port: "13306"},
		{name: "empty uses default", port: ""},
		{name: "not a number", port: "abc", wantErr: true},
		{name: "zero", port: "0", wantErr: true},
		{name: "negative", port: "-1", wantErr: true},
		{name: "too large", port: "65536", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("MYSQL_PASSWORD", "test_password")
			t.Setenv("MYSQL_PORT", tt.port)

			_, err := config.Load()
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestLoadHTTPAddr(t *testing.T) {
	tests := []struct {
		name    string
		addr    string
		wantErr bool
	}{
		{name: "custom", addr: "127.0.0.1:9090"},
		{name: "missing port", addr: "127.0.0.1", wantErr: true},
		{name: "invalid port", addr: "127.0.0.1:abc", wantErr: true},
		{name: "out of range", addr: "127.0.0.1:65536", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("MYSQL_PASSWORD", "test_password")
			t.Setenv("MYSQL_PORT", "13306")
			t.Setenv("HTTP_ADDR", tt.addr)

			cfg, err := config.Load()
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr = %v", err, tt.wantErr)
			}
			if err == nil && cfg.HTTPAddr != tt.addr {
				t.Fatal("HTTP_ADDR 未正确读取")
			}
		})
	}
}
