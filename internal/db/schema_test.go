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

// tableExists 用 to_regclass 判断表是否存在。
func tableExists(t *testing.T, db *sql.DB, tbl string) bool {
	t.Helper()
	var reg sql.NullString
	require.NoError(t, db.QueryRow(`SELECT to_regclass($1)`, tbl).Scan(&reg))
	return reg.Valid
}

func TestMigrations_UpDownRoundTrip(t *testing.T) {
	dsn := startPostgres(t)

	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	tables := []string{
		"tenant", "application", "role", "permission", "role_permission",
		"role_inheritance", "user_role_binding", "data_policy",
		"casbin_rule", "policy_audit_log", "admin_audit_log",
	}

	// up：11 张表均存在
	require.NoError(t, RunMigrations(dsn, migrationsSource))
	for _, tbl := range tables {
		require.Truef(t, tableExists(t, db, tbl), "表 %s 应在 up 后存在", tbl)
	}

	// down：全部回滚，11 张表均不存在
	require.NoError(t, MigrateDown(dsn, migrationsSource))
	for _, tbl := range tables {
		require.Falsef(t, tableExists(t, db, tbl), "表 %s 应在 down 后被删除", tbl)
	}

	// 再次 up：验证 down 未损坏可重建性——不仅无错误，且 11 张表确实重建
	require.NoError(t, RunMigrations(dsn, migrationsSource))
	for _, tbl := range tables {
		require.Truef(t, tableExists(t, db, tbl), "表 %s 应在再次 up 后重建", tbl)
	}
}
