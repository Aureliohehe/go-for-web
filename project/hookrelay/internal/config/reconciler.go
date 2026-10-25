package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Reconciler struct {
	Grace         time.Duration
	Interval      time.Duration
	BatchSize     int
	MaxRecoveries uint32
}

func LoadReconciler() (Reconciler, error) { return loadReconciler(os.Getenv) }

func loadReconciler(getenv func(string) string) (Reconciler, error) {
	value := func(key, fallback string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return fallback
	}
	var cfg Reconciler
	var err error
	cfg.Grace, err = time.ParseDuration(value("RECONCILE_GRACE", "5m"))
	if err != nil || cfg.Grace < time.Minute {
		return cfg, fmt.Errorf("RECONCILE_GRACE must be at least 1m")
	}
	cfg.Interval, err = time.ParseDuration(value("RECONCILE_INTERVAL", "30s"))
	if err != nil || cfg.Interval <= 0 {
		return cfg, fmt.Errorf("RECONCILE_INTERVAL must be a positive duration")
	}
	cfg.BatchSize, err = strconv.Atoi(value("RECONCILE_BATCH_SIZE", "100"))
	if err != nil || cfg.BatchSize < 1 || cfg.BatchSize > 1000 {
		return cfg, fmt.Errorf("RECONCILE_BATCH_SIZE must be between 1 and 1000")
	}
	max, err := strconv.ParseUint(value("MAX_DELIVERY_RECOVERIES", "3"), 10, 32)
	if err != nil || max == 0 {
		return cfg, fmt.Errorf("MAX_DELIVERY_RECOVERIES must be a positive uint32")
	}
	cfg.MaxRecoveries = uint32(max)
	return cfg, nil
}
