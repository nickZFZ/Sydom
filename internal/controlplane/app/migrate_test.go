package app

import (
	"database/sql"
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// runMigrate 对全新库应用嵌入迁移：关键表建立、幂等。
func TestRunMigrate_FreshDB(t *testing.T) {
	dsn := dbtest.StartPostgres(t)
	cfg := writeCfg(t, "database_dsn: \""+dsn+"\"\n")
	empty := func(string) string { return "" }

	require.NoError(t, runMigrate(cfg, empty))

	conn, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	var reg sql.NullString
	require.NoError(t, conn.QueryRow(`SELECT to_regclass('application')`).Scan(&reg))
	require.True(t, reg.Valid, "application 表应在 runMigrate 后存在")

	// 幂等
	require.NoError(t, runMigrate(cfg, empty))
}
