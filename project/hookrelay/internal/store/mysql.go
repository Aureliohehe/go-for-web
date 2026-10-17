package store

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"time"

	"github.com/aurelio/hookrelay/internal/config"
	driver "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func mysqlDSN(cfg config.Config) string {
	// Use the driver's formatter instead of manually interpolating a DSN.
	dsn := driver.NewConfig()
	dsn.User = cfg.MySQLUser
	dsn.Passwd = cfg.MySQLPassword
	dsn.Net = "tcp"
	dsn.Addr = net.JoinHostPort(cfg.MySQLHost, cfg.MySQLPort)
	dsn.DBName = cfg.MySQLDatabase
	dsn.ParseTime = true
	dsn.Loc = time.UTC
	dsn.Timeout = 3 * time.Second
	dsn.ReadTimeout = 3 * time.Second
	dsn.WriteTimeout = 3 * time.Second
	dsn.Params = map[string]string{"charset": "utf8mb4", "time_zone": "'+00:00'"}
	return dsn.FormatDSN()
}

// OpenMySQL verifies connectivity before returning. The caller owns pool.Close.
// It does not create databases or run schema migrations.
func OpenMySQL(ctx context.Context, cfg config.Config) (*gorm.DB, *sql.DB, error) {
	pool, err := sql.Open("mysql", mysqlDSN(cfg))
	if err != nil {
		return nil, nil, fmt.Errorf("configure mysql: %w", err)
	}
	pool.SetMaxOpenConns(10)
	pool.SetMaxIdleConns(5)
	pool.SetConnMaxLifetime(3 * time.Minute)
	pool.SetConnMaxIdleTime(time.Minute)

	if err := pool.PingContext(ctx); err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("ping mysql: %w", err)
	}

	db, err := gorm.Open(mysql.New(mysql.Config{
		Conn:                      pool,
		SkipInitializeWithVersion: true,
	}), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		pool.Close()
		return nil, nil, fmt.Errorf("initialize gorm: %w", err)
	}
	return db, pool, nil
}
