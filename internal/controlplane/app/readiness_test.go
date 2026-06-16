package app

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/lib/pq"
	"github.com/redis/go-redis/v9"
)

func TestCPReadinessFailsWhenDBClosed(t *testing.T) {
	db, err := sql.Open("postgres", "postgres://invalid:5432/x?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	db.Close() // 关闭 → Ping 必失败
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	defer rdb.Close()
	if err := cpReadiness(db, rdb)(context.Background()); err == nil {
		t.Fatal("readiness want error when DB closed, got nil")
	}
}
