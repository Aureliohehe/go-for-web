// Local signed receiver with transactional, persistent deduplication.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aurelio/hookrelay/internal/config"
	"github.com/aurelio/hookrelay/internal/receiver"
	"github.com/aurelio/hookrelay/internal/store"
	"github.com/aurelio/hookrelay/internal/webhook"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:9090", "listen address")
	status := flag.Int("status", 200, "HTTP status; non-2xx rejects without business writes")
	delay := flag.Duration("delay", 0, "response delay; for 2xx only after the first committed write, e.g. 8s")
	retryAfter := flag.String("retry-after", "", "Retry-After response header, e.g. 10")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, *addr, *status, *delay, *retryAfter); err != nil {
		slog.Error("receiver stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, addr string, status int, delay time.Duration, retryAfter string) error {
	if status < 200 || status > 599 || delay < 0 {
		return fmt.Errorf("status must be 200..599 and delay must be non-negative")
	}
	secret := os.Getenv("RECEIVER_SIGNING_SECRET")
	if err := webhook.ValidateSecret(secret); err != nil {
		return fmt.Errorf("RECEIVER_SIGNING_SECRET must be the endpoint's 64-character hex signing secret")
	}
	cfg, err := config.LoadReceiverDB()
	if err != nil {
		return err
	}
	db, pool, err := store.OpenMySQL(ctx, cfg)
	if err != nil {
		return errors.New("connect receiver database failed")
	}
	defer pool.Close()
	// No AutoMigrate. Fail startup if the receiver's separate migration is absent.
	if err := db.WithContext(ctx).Select("delivery_id", "payload_hash", "created_at").Limit(1).Find(&[]receiver.Receipt{}).Error; err != nil {
		return errors.New("receiver receipt schema unavailable; apply receiver migration")
	}
	if err := db.WithContext(ctx).Select("id", "delivery_id", "payload", "created_at").Limit(1).Find(&[]receiver.Effect{}).Error; err != nil {
		return errors.New("receiver effect schema unavailable; apply receiver migration")
	}
	repo := receiver.Store{DB: db}
	server := &http.Server{
		Addr: addr, Handler: receiver.NewHandler(secret, repo.Accept, status, delay, retryAfter),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, IdleTimeout: time.Minute,
	}
	serverErr := make(chan error, 1)
	go func() {
		slog.Info("local receiver listening", "addr", addr)
		serverErr <- server.ListenAndServe()
	}()
	select {
	case err := <-serverErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			server.Close()
			return err
		}
		return nil
	}
}
