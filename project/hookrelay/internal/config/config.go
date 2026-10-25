package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config contains settings for the API and the database.
// Never log this struct: it contains the database password.
type Config struct {
	RetryBaseDelay       time.Duration
	RetryMaxDelay        time.Duration
	MaxPendingDeliveries int64
	RedisAddr            string
	HTTPAddr             string
	MySQLHost            string
	MySQLPort            string
	MySQLDatabase        string
	MySQLUser            string
	MySQLPassword        string
	APIKey               string
	AllowedEndpointURL   string
}

func Load() (Config, error) {
	return load(os.Getenv)
}

func load(getenv func(string) string) (Config, error) {
	value := func(key, fallback string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return fallback
	}
	cfg := Config{
		RedisAddr:          value("REDIS_ADDR", "127.0.0.1:16379"),
		HTTPAddr:           value("HTTP_ADDR", "127.0.0.1:8080"),
		MySQLHost:          value("MYSQL_HOST", "127.0.0.1"),
		MySQLPort:          value("MYSQL_PORT", "13306"),
		MySQLDatabase:      value("MYSQL_DATABASE", "hookrelay"),
		MySQLUser:          value("MYSQL_USER", "hookrelay"),
		MySQLPassword:      getenv("MYSQL_PASSWORD"),
		APIKey:             getenv("API_KEY"),
		AllowedEndpointURL: getenv("ALLOWED_ENDPOINT_URL"),
	}
	limit, err := strconv.ParseInt(value("MAX_PENDING_DELIVERIES", "10000"), 10, 64)
	if err != nil || limit < 1 {
		return Config{}, fmt.Errorf("MAX_PENDING_DELIVERIES must be a positive integer")
	}
	cfg.MaxPendingDeliveries = limit
	cfg.RetryBaseDelay, err = time.ParseDuration(value("RETRY_BASE_DELAY", "5s"))
	if err != nil || cfg.RetryBaseDelay <= 0 {
		return Config{}, fmt.Errorf("RETRY_BASE_DELAY must be a positive duration")
	}
	cfg.RetryMaxDelay, err = time.ParseDuration(value("RETRY_MAX_DELAY", "1m"))
	if err != nil || cfg.RetryMaxDelay < cfg.RetryBaseDelay {
		return Config{}, fmt.Errorf("RETRY_MAX_DELAY must be a duration >= RETRY_BASE_DELAY")
	}
	if cfg.MySQLPassword == "" {
		return Config{}, fmt.Errorf("MYSQL_PASSWORD is required; export the variables in .env first")
	}
	if !validPort(cfg.MySQLPort) {
		return Config{}, fmt.Errorf("MYSQL_PORT must be an integer between 1 and 65535")
	}
	_, port, err := net.SplitHostPort(cfg.HTTPAddr)
	if err != nil || !validPort(port) {
		return Config{}, fmt.Errorf("HTTP_ADDR must have the form host:port with a port between 1 and 65535")
	}
	if cfg.AllowedEndpointURL != "" && !validEndpointURL(cfg.AllowedEndpointURL) {
		return Config{}, fmt.Errorf("ALLOWED_ENDPOINT_URL must be a valid absolute http(s) URL without credentials or fragment")
	}
	return cfg, nil
}

func validEndpointURL(raw string) bool {
	if len(raw) > 2048 || strings.TrimSpace(raw) != raw || strings.Contains(raw, "#") {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") &&
		u.Hostname() != "" && u.User == nil && u.Fragment == "" && u.Opaque == ""
}

func validPort(raw string) bool {
	port, err := strconv.Atoi(raw)
	return err == nil && port >= 1 && port <= 65535
}
