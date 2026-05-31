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
