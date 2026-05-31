package db

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

const migrationsSource = "file://../../db/migrations"

// startPostgres 起一个临时 PostgreSQL 容器，返回 sslmode=disable 的 DSN。
func startPostgres(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	ctr, err := postgres.RunContainer(ctx,
		testcontainers.WithImage("docker.io/postgres:17-alpine"),
		postgres.WithDatabase("sydom"),
		postgres.WithUsername("sydom"),
		postgres.WithPassword("sydom"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ctr.Terminate(ctx) })

	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	return dsn
}

// setupSchema 起容器、跑全量 migration，返回已连接的 *sql.DB。
func setupSchema(t *testing.T) *sql.DB {
	t.Helper()
	dsn := startPostgres(t)
	require.NoError(t, RunMigrations(dsn, migrationsSource))

	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.Ping())
	return db
}

// seedApp 建一个租户+应用，返回 app_id。供需要 app 上下文的表测试复用。
func seedApp(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var tenantID, appID int64
	require.NoError(t, db.QueryRow(
		`INSERT INTO tenant (name) VALUES ('acme') RETURNING id`).Scan(&tenantID))
	require.NoError(t, db.QueryRow(
		`INSERT INTO application (tenant_id, domain, name, app_key, app_secret_hash)
		 VALUES ($1, 'order-system', '订单系统', 'AK_order', 'hash1') RETURNING id`,
		tenantID).Scan(&appID))
	return appID
}
