package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aurelio/hookrelay/internal/config"
	"github.com/aurelio/hookrelay/internal/outbox"
	"github.com/aurelio/hookrelay/internal/store"
	"github.com/aurelio/hookrelay/internal/task"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error("dispatcher stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	connectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	db, pool, err := store.OpenMySQL(connectCtx, cfg)
	cancel()
	if err != nil {
		return err
	}
	defer pool.Close()
	queue := task.NewClient(cfg.RedisAddr)
	defer queue.Close()
	dispatcher := outbox.Dispatcher{Store: store.OutboxStore{DB: db}, Enqueue: queue.EnqueueDelivery}
	slog.Info("dispatcher started")
	dispatcher.Run(ctx)
	return nil
}
