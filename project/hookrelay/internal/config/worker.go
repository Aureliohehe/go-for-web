package config

import (
	"fmt"
	"os"
	"strconv"
)

type WorkerLimits struct {
	Concurrency         int
	EndpointConcurrency int
}

// Only the Worker uses these settings; they do not change API admission capacity.
func LoadWorkerLimits() (WorkerLimits, error) { return loadWorkerLimits(os.Getenv) }

func loadWorkerLimits(getenv func(string) string) (WorkerLimits, error) {
	positive := func(key string, fallback int) (int, error) {
		v := getenv(key)
		if v == "" {
			return fallback, nil
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return 0, fmt.Errorf("%s must be a positive integer", key)
		}
		return n, nil
	}
	var cfg WorkerLimits
	var err error
	cfg.Concurrency, err = positive("WORKER_CONCURRENCY", 4)
	if err != nil {
		return cfg, err
	}
	cfg.EndpointConcurrency, err = positive("ENDPOINT_CONCURRENCY", 1)
	if err != nil {
		return cfg, err
	}
	if cfg.EndpointConcurrency > cfg.Concurrency {
		return cfg, fmt.Errorf("ENDPOINT_CONCURRENCY must not exceed WORKER_CONCURRENCY")
	}
	return cfg, nil
}
