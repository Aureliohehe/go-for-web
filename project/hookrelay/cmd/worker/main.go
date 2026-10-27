package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aurelio/hookrelay/internal/config"
	"github.com/aurelio/hookrelay/internal/delivery"
	"github.com/aurelio/hookrelay/internal/store"
	"github.com/aurelio/hookrelay/internal/task"
	"github.com/hibiken/asynq"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error("worker stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	limits, err := config.LoadWorkerLimits()
	if err != nil {
		return err
	}
	if cfg.AllowedEndpointURL == "" {
		return fmt.Errorf("ALLOWED_ENDPOINT_URL is required")
	}
	connectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	db, pool, err := store.OpenMySQL(connectCtx, cfg)
	cancel()
	if err != nil {
		return err
	}
	defer pool.Close()

	client := task.NewClient(cfg.RedisAddr)
	defer client.Close()
	retryPolicy := delivery.RetryPolicy{BaseDelay: cfg.RetryBaseDelay, MaxDelay: cfg.RetryMaxDelay}
	worker := delivery.Worker{
		Store: store.DeliveryStore{DB: db}, Send: delivery.NewSender(task.HTTPTimeout).Send, AllowedURL: cfg.AllowedEndpointURL,
		Limiter: delivery.NewEndpointLimiter(limits.EndpointConcurrency), DeferUntil: client.DeferDeliveryUntil, RetryPolicy: retryPolicy,
	}
	server := asynq.NewServer(asynq.RedisClientOpt{Addr: cfg.RedisAddr}, asynq.Config{
		Concurrency:     limits.Concurrency,
		Queues:          map[string]int{task.Queue: 1},
		ShutdownTimeout: 25 * time.Second,
		RetryDelayFunc:  retryPolicy.Delay,
	})
	mux := asynq.NewServeMux()
	mux.Handle(task.TypeDelivery, worker)
	if err := server.Start(mux); err != nil {
		return err
	}
	slog.Info("worker started", "retry_base_delay", cfg.RetryBaseDelay, "retry_max_delay", cfg.RetryMaxDelay,
		"concurrency", limits.Concurrency, "endpoint_concurrency", limits.EndpointConcurrency)
	if limits.EndpointConcurrency == limits.Concurrency {
		slog.Warn("one endpoint can occupy every worker slot; no spare capacity for other endpoints")
	}
	<-ctx.Done()
	server.Shutdown()
	return nil
}
