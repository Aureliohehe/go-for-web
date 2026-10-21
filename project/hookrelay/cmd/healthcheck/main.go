package main

import (
	"context"
	"database/sql"

	"log"
	"net"
	"net/http"

	"time"

	"github.com/aurelio/hookrelay/internal/config"

	"github.com/gin-gonic/gin"
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
	router := gin.Default()
	router.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status": "ok",
		})
	})
	router.GET("/readyz", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), time.Second)
		defer cancel()
		if err := db.PingContext(ctx); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"status": "not_ready",
			})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"status": "ready",
		})
	})
	if err := router.Run(appCfg.HTTPAddr); err != nil {
		log.Fatal(err)
	}
}
