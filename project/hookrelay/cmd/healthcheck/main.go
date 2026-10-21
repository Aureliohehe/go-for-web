package main

import (
	"context"
	"database/sql"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-sql-driver/mysql"
)

type DBConfig struct {
	Host     string
	Port     string
	User     string
	Password string
	Database string
}

func loadDBConfig() DBConfig {
	return DBConfig{
		Host:     os.Getenv("MYSQL_HOST"),
		Port:     os.Getenv("MYSQL_PORT"),
		User:     os.Getenv("MYSQL_USER"),
		Password: os.Getenv("MYSQL_PASSWORD"),
		Database: os.Getenv("MYSQL_DATABASE"),
	}
}

func main() {
	appCfg := loadDBConfig()

	cfg := mysql.NewConfig()
	cfg.User = appCfg.User
	cfg.Passwd = appCfg.Password
	cfg.Net = "tcp"
	cfg.Addr = net.JoinHostPort(appCfg.Host, appCfg.Port)
	cfg.DBName = appCfg.Database
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
	if err := router.Run("127.0.0.1:8080"); err != nil {
		log.Fatal(err)
	}
}
