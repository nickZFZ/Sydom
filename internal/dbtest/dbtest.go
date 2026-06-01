// Package dbtest 提供跨包共享的 testcontainers PostgreSQL 测试基建。
// 仅供 _test.go 导入；不进生产二进制。
package dbtest

import (
	"context"
	"database/sql"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/nickZFZ/Sydom/internal/db"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// 种子应用的固定属性，供投影类测试断言 domain / app_key。
const (
	SeedDomain = "order-system"
	SeedAppKey = "AK_order"
)

// migrationsSource 基于本文件位置算出 file://<repo>/db/migrations，与调用方目录无关。
func migrationsSource() string {
	_, thisFile, _, ok := runtime.Caller(0) // <repo>/internal/dbtest/dbtest.go
	if !ok {
		// 拿不到源文件位置时若继续，repoRoot 会退化为相对路径并以调用方 CWD 解析，
		// 静默算错 migration 路径。这里直接 panic，把失败定位到根因。
		panic("dbtest: runtime.Caller(0) failed, cannot locate db/migrations")
	}
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	return "file://" + filepath.Join(repoRoot, "db", "migrations")
}

// StartPostgres 起一个临时 PostgreSQL 容器，返回 sslmode=disable 的 DSN。
func StartPostgres(t *testing.T) string {
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

// SetupSchema 起容器、跑全量 migration，返回已连接的 *sql.DB。
func SetupSchema(t *testing.T) *sql.DB {
	t.Helper()
	dsn := StartPostgres(t)
	require.NoError(t, db.RunMigrations(dsn, migrationsSource()))

	conn, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	require.NoError(t, conn.Ping())
	return conn
}

// SeedApp 建一个租户+应用，返回 app_id。app_secret_enc 用占位字节（不参与本包断言）。
// 注意：tenant.name 与 application.app_key 均有唯一约束，故每个数据库实例只可调用一次；
// 需要多条种子数据时请自行构造不重复的 name/app_key，不要重复调用本函数。
func SeedApp(t *testing.T, conn *sql.DB) int64 {
	t.Helper()
	var tenantID, appID int64
	require.NoError(t, conn.QueryRow(
		`INSERT INTO tenant (name) VALUES ('acme') RETURNING id`).Scan(&tenantID))
	require.NoError(t, conn.QueryRow(
		`INSERT INTO application (tenant_id, domain, name, app_key, app_secret_enc)
		 VALUES ($1, $2, '订单系统', $3, '\xab'::bytea) RETURNING id`,
		tenantID, SeedDomain, SeedAppKey).Scan(&appID))
	return appID
}
