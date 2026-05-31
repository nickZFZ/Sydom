package db

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestPostgresContainerStarts 是基础设施冒烟测试：验证 testcontainers PG 容器可启动并连通。
func TestPostgresContainerStarts(t *testing.T) {
	dsn := startPostgres(t)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.Ping())
}

func TestMigrations_UpDownRoundTrip(t *testing.T) {
	dsn := startPostgres(t)

	// up
	require.NoError(t, RunMigrations(dsn, migrationsSource))

	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	defer db.Close()

	// 10 张业务表均存在
	tables := []string{
		"tenant", "application", "role", "permission", "role_permission",
		"role_inheritance", "user_role_binding", "data_policy",
		"casbin_rule", "policy_audit_log",
	}
	for _, tbl := range tables {
		var reg sql.NullString
		require.NoError(t, db.QueryRow(`SELECT to_regclass($1)`, tbl).Scan(&reg))
		require.Truef(t, reg.Valid, "表 %s 应在 up 后存在", tbl)
	}

	// down：全部回滚
	require.NoError(t, MigrateDown(dsn, migrationsSource))
	for _, tbl := range tables {
		var reg sql.NullString
		require.NoError(t, db.QueryRow(`SELECT to_regclass($1)`, tbl).Scan(&reg))
		require.Falsef(t, reg.Valid, "表 %s 应在 down 后被删除", tbl)
	}

	// 再次 up：验证 down 未损坏可重建性
	require.NoError(t, RunMigrations(dsn, migrationsSource))
}
