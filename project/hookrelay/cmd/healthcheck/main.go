package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/aurelio/hookrelay/internal/config"
	"github.com/aurelio/hookrelay/internal/handler"
	"github.com/aurelio/hookrelay/internal/store"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	appCfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, pool, err := store.OpenMySQL(ctx, appCfg)
	cancel()
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer pool.Close()

	log.Print("mysql connected")

	router := handler.NewRouter(pool.PingContext)
	if err := router.Run(appCfg.HTTPAddr); err != nil {
		return fmt.Errorf("serve http: %w", err)
	}
	return nil
}
