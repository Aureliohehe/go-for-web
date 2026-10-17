package main

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/go-sql-driver/mysql"
)

func main() {
	if err := checkDatabase(); err != nil {
		fmt.Println("check err:", err)
		os.Exit(1)
	}
}

func checkDatabase() error {
	cfg := mysql.NewConfig()
	cfg.User = os.Getenv("MYSQL_USER")
	cfg.Passwd = os.Getenv("MYSQL_PASSWORD")
	cfg.Net = "tcp"
	cfg.Addr = net.JoinHostPort(
		os.Getenv("MYSQL_HOST"),
		os.Getenv("MYSQL_PORT"),
	)
	cfg.DBName = os.Getenv("MYSQL_DATABASE")
	cfg.Timeout = 3 * time.Second
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return err
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return err
	}
	fmt.Println("musql connect successful")
	return nil
}
