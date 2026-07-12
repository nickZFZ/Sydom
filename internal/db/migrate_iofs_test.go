package db

import (
	"database/sql"
	"testing"

	migrations "github.com/nickZFZ/Sydom/db/migrations"
	"github.com/stretchr/testify/require"
)

// 从嵌入的迁移把全新库迁到最新，关键表存在，且二次调用幂等（无错）。
func TestRunMigrationsFS_EmbedFreshDBIdempotent(t *testing.T) {
	dsn := startPostgres(t)

	require.NoError(t, RunMigrationsFS(dsn, migrations.FS))

	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	for _, tbl := range []string{"tenant", "application", "casbin_rule", "policy_outbox"} {
		require.Truef(t, tableExists(t, db, tbl), "表 %s 应在嵌入迁移 up 后存在", tbl)
	}

	// 幂等：再次调用应无错（golang-migrate ErrNoChange 被吞）。
	require.NoError(t, RunMigrationsFS(dsn, migrations.FS))
}
