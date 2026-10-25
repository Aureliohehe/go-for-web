package config

import (
	"fmt"
	"os"
)

// LoadReceiverDB intentionally does not inherit MYSQL_* credentials/database.
// The example receiver owns its data independently of the delivery platform.
func LoadReceiverDB() (Config, error) {
	return loadReceiverDB(os.Getenv)
}

func loadReceiverDB(getenv func(string) string) (Config, error) {
	value := func(key, fallback string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return fallback
	}
	cfg := Config{
		MySQLHost:     value("RECEIVER_MYSQL_HOST", "127.0.0.1"),
		MySQLPort:     value("RECEIVER_MYSQL_PORT", "13306"),
		MySQLDatabase: value("RECEIVER_MYSQL_DATABASE", "hookrelay_receiver"),
		MySQLUser:     value("RECEIVER_MYSQL_USER", "hookrelay_receiver"),
		MySQLPassword: getenv("RECEIVER_MYSQL_PASSWORD"),
	}
	if cfg.MySQLPassword == "" {
		return Config{}, fmt.Errorf("RECEIVER_MYSQL_PASSWORD is required")
	}
	if !validPort(cfg.MySQLPort) {
		return Config{}, fmt.Errorf("RECEIVER_MYSQL_PORT must be an integer between 1 and 65535")
	}
	return cfg, nil
}
