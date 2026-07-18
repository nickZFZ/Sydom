package db

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration000024_TenantIdp(t *testing.T) {
	dsn := startPostgres(t)
	require.NoError(t, RunMigrations(dsn, migrationsSource))
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	require.True(t, tableExists(t, db, "tenant_idp"))
	require.True(t, tableExists(t, db, "tenant_idp_domain"))

	var t1, t2 int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant (name) VALUES ('idp-a') RETURNING id`).Scan(&t1))
	require.NoError(t, db.QueryRow(`INSERT INTO tenant (name) VALUES ('idp-b') RETURNING id`).Scan(&t2))

	// 一租户一 IdP：同租户第二条 tenant_idp→冲突。
	_, err = db.Exec(`INSERT INTO tenant_idp (tenant_id, issuer, client_id, client_secret_enc)
		VALUES ($1,'https://a','cid','\xab'::bytea)`, t1)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO tenant_idp (tenant_id, issuer, client_id, client_secret_enc)
		VALUES ($1,'https://a2','cid2','\xab'::bytea)`, t1)
	require.Error(t, err, "uq_tenant_idp_tenant 应拒同租户第二条 IdP")

	// 域全局唯一：不同租户抢同域→冲突。
	_, err = db.Exec(`INSERT INTO tenant_idp_domain (tenant_id, domain) VALUES ($1,'acme.com')`, t1)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO tenant_idp_domain (tenant_id, domain) VALUES ($1,'acme.com')`, t2)
	require.Error(t, err, "uq_tenant_idp_domain 应拒跨租户同域")

	require.NoError(t, MigrateDown(dsn, migrationsSource))
	require.False(t, tableExists(t, db, "tenant_idp"))
}
