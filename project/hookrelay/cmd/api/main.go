package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aurelio/hookrelay/internal/config"
	"github.com/aurelio/hookrelay/internal/handler"
	"github.com/aurelio/hookrelay/internal/store"
	"github.com/gin-gonic/gin"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error("api stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.APIKey == "" {
		return fmt.Errorf("API_KEY is required")
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
	slog.Info("mysql connected")

	gin.SetMode(gin.ReleaseMode)
	router := handler.NewAPIRouter(
		pool.PingContext,
		cfg.APIKey,
		cfg.AllowedEndpointURL,
		func(ctx context.Context, id uint64) (store.Endpoint, error) {
			return store.FindEndpoint(ctx, db, id)
		},
		func(ctx context.Context, endpoint *store.Endpoint) error {
			return store.CreateEndpoint(ctx, db, endpoint)
		},
		func(ctx context.Context, id uint64, enabled bool) (store.Endpoint, error) {
			return store.SetEndpointEnabled(ctx, db, id, enabled)
		},
		func(ctx context.Context, input store.EventInput) (store.EventResult, error) {
			return store.CreateOrGetEventDelivery(ctx, db, input, cfg.MaxPendingDeliveries)
		},
		func(ctx context.Context, id uint64) (store.Delivery, error) {
			return store.FindDelivery(ctx, db, id)
		},
		func(ctx context.Context, id, after uint64, limit int) ([]store.DeliveryAttempt, error) {
			return store.ListDeliveryAttempts(ctx, db, id, after, limit)
		},
		func(ctx context.Context, id uint64) (store.Delivery, error) {
			return store.ReplayDelivery(ctx, db, id, cfg.MaxPendingDeliveries)
		},
	)
	if err := router.SetTrustedProxies(nil); err != nil {
		return fmt.Errorf("configure trusted proxies: %w", err)
	}
	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	serverErr := make(chan error, 1)
	go func() {
		slog.Info("starting api", "addr", cfg.HTTPAddr)
		serverErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serverErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve http: %w", err)
	case <-ctx.Done():
		slog.Info("shutting down api")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			server.Close()
			return fmt.Errorf("shutdown http: %w", err)
		}
		return nil
	}
}
