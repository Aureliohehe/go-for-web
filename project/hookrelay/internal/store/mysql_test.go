package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/aurelio/hookrelay/internal/config"
	driver "github.com/go-sql-driver/mysql"
)

func TestMySQLDSN(t *testing.T) {
	cfg := config.Config{
		MySQLHost: "::1", MySQLPort: "13306", MySQLDatabase: "hookrelay",
		MySQLUser: "hookrelay", MySQLPassword: "local@:/?#password",
	}
	dsn, err := driver.ParseDSN(mysqlDSN(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if dsn.Passwd != cfg.MySQLPassword || dsn.Addr != "[::1]:13306" || dsn.DBName != cfg.MySQLDatabase {
		t.Fatal("DSN did not preserve connection settings")
	}
	if !dsn.ParseTime || dsn.Loc != time.UTC || dsn.Timeout == 0 || dsn.ReadTimeout == 0 || dsn.WriteTimeout == 0 {
		t.Fatal("missing time parsing or timeout settings")
	}
}

// This test connects only when explicitly enabled. It performs no schema changes.
func TestOpenMySQLIntegration(t *testing.T) {
	if os.Getenv("HOOKRELAY_INTEGRATION") != "1" {
		t.Skip("set HOOKRELAY_INTEGRATION=1 and export database settings to run")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, pool, err := OpenMySQL(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var result int
	if err := db.WithContext(ctx).Raw("SELECT 1").Scan(&result).Error; err != nil {
		t.Fatal(err)
	}
	if result != 1 {
		t.Fatalf("SELECT 1 returned %d", result)
	}
}
