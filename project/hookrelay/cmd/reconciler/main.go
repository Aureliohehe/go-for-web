package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aurelio/hookrelay/internal/config"
	"github.com/aurelio/hookrelay/internal/reconcile"
	"github.com/aurelio/hookrelay/internal/store"
	"github.com/aurelio/hookrelay/internal/task"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error("reconciler stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	settings, err := config.LoadReconciler()
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
	inspector := task.NewInspector(cfg.RedisAddr, task.Queue)
	defer inspector.Close()
	r := reconcile.Reconciler{
		Store: store.DeliveryStore{DB: db}, Alive: inspector.Alive,
		Grace: settings.Grace, Interval: settings.Interval,
		BatchSize: settings.BatchSize, MaxRecoveries: settings.MaxRecoveries,
	}
	slog.Info("reconciler started", "grace", settings.Grace, "interval", settings.Interval,
		"batch_size", settings.BatchSize, "max_recoveries", settings.MaxRecoveries)
	r.Run(ctx)
	return nil
}
