package main

import (
	"database/sql"
	"log"
	"net"
	"time"

	"github.com/aurelio/hookrelay/internal/config"
	"github.com/aurelio/hookrelay/internal/handler"
	"github.com/go-sql-driver/mysql"
)

func main() {
	appCfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}

	cfg := mysql.NewConfig()
	cfg.User = appCfg.MySQLUser
	cfg.Passwd = appCfg.MySQLPassword
	cfg.Net = "tcp"
	cfg.Addr = net.JoinHostPort(appCfg.MySQLHost, appCfg.MySQLPort)
	cfg.DBName = appCfg.MySQLDatabase
	cfg.Timeout = 3 * time.Second
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	router := handler.NewRouter(db.PingContext)
	if err := router.Run(appCfg.HTTPAddr); err != nil {
		log.Fatal(err)
	}
}
