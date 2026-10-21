package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
)

// Config contains only the settings needed by the day-one API.
// Never log this struct: it contains the database password.
type Config struct {
	HTTPAddr      string
	MySQLHost     string
	MySQLPort     string
	MySQLDatabase string
	MySQLUser     string
	MySQLPassword string
	APIKey        string
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
		HTTPAddr:      value("HTTP_ADDR", "127.0.0.1:8080"),
		MySQLHost:     value("MYSQL_HOST", "127.0.0.1"),
		MySQLPort:     value("MYSQL_PORT", "13306"),
		MySQLDatabase: value("MYSQL_DATABASE", "hookrelay"),
		MySQLUser:     value("MYSQL_USER", "hookrelay"),
		MySQLPassword: getenv("MYSQL_PASSWORD"),
		APIKey:        getenv("API_KEY"),
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
	return cfg, nil
}

func validPort(raw string) bool {
	port, err := strconv.Atoi(raw)
	return err == nil && port >= 1 && port <= 65535
}
