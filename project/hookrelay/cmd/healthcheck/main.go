package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aurelio/hookrelay/internal/config"
	"github.com/aurelio/hookrelay/internal/handler"
	"github.com/aurelio/hookrelay/internal/store"
)

func main() {
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)

	err := run(ctx)
	stop()

	if err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	appCfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	connectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	_, pool, err := store.OpenMySQL(connectCtx, appCfg)
	cancel()
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer pool.Close()

	log.Print("mysql connected")

	router := handler.NewRouter(pool.PingContext)
	server := &http.Server{
		Addr:              appCfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		log.Printf("starting http server on %s", appCfg.HTTPAddr)
		serverErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serverErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve http: %w", err)

	case <-ctx.Done():
		log.Print("shutting down http server")

		if err := shutdownHTTP(server); err != nil {
			return err
		}

		log.Print("http server stopped")
		return nil
	}
}

func shutdownHTTP(server *http.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		_ = server.Close()
		return fmt.Errorf("shutdown http: %w", err)
	}
	return nil
}
