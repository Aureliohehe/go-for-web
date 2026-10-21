package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
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

func loadDBConfig() (DBConfig, error) {
	cfg := DBConfig{
		Host:     envOrDefault("MYSQL_HOST", "127.0.0.1"),
		Port:     envOrDefault("MYSQL_PORT", "13306"),
		User:     envOrDefault("MYSQL_USER", "hookrelay"),
		Password: envOrDefault("MYSQL_PASSWORD", ""),
		Database: envOrDefault("MYSQL_DATABASE", "hookrelay"),
	}
	if cfg.Password == "" {
		return DBConfig{}, errors.New("MYSQL_PASSWORD is required")
	}
	port, err := strconv.Atoi(cfg.Port)
	if err != nil || port < 1 || port > 65535 {
		return DBConfig{}, errors.New(
			"MYSQL_PORT must be an integer between 1 and 65535",
		)
	}
	return cfg, nil
}
func envOrDefault(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}
func main() {
	appCfg, err := loadDBConfig()
	if err != nil {
		log.Fatal(err)
	}

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
