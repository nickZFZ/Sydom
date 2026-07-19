package db

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration000026_TenantIdpJIT(t *testing.T) {
	dsn := startPostgres(t)
	require.NoError(t, RunMigrations(dsn, migrationsSource))
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	var tid int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant (name) VALUES ('jit') RETURNING id`).Scan(&tid))
	_, err = db.Exec(`INSERT INTO tenant_idp (tenant_id, issuer, client_id, client_secret_enc)
		VALUES ($1,'https://i','cid','\xaa'::bytea)`, tid)
	require.NoError(t, err)

	// 默认 false（向后兼容）。
	var jit bool
	require.NoError(t, db.QueryRow(`SELECT jit_enabled FROM tenant_idp WHERE tenant_id=$1`, tid).Scan(&jit))
	require.False(t, jit, "jit_enabled 默认应 false")

	_, err = db.Exec(`UPDATE tenant_idp SET jit_enabled=true WHERE tenant_id=$1`, tid)
	require.NoError(t, err)

	require.NoError(t, MigrateDown(dsn, migrationsSource))
	_, err = db.Exec(`SELECT jit_enabled FROM tenant_idp LIMIT 1`)
	require.Error(t, err, "down 后 jit_enabled 列应不存在")
}
