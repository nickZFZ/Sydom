package db

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTenant_NameUnique(t *testing.T) {
	db := setupSchema(t)

	_, err := db.Exec(`INSERT INTO tenant (name) VALUES ('acme')`)
	require.NoError(t, err)

	_, err = db.Exec(`INSERT INTO tenant (name) VALUES ('acme')`)
	require.Error(t, err)

	var status int
	require.NoError(t, db.QueryRow(
		`SELECT status FROM tenant WHERE name = 'acme'`).Scan(&status))
	require.Equal(t, 1, status)
}

func TestApplication_Constraints(t *testing.T) {
	db := setupSchema(t)

	var tenantID int64
	require.NoError(t, db.QueryRow(
		`INSERT INTO tenant (name) VALUES ('acme') RETURNING id`).Scan(&tenantID))

	_, err := db.Exec(`INSERT INTO application (tenant_id, domain, name, app_key, app_secret_hash)
		VALUES ($1, 'order-system', '订单系统', 'AK_order', 'hash1')`, tenantID)
	require.NoError(t, err)

	var ver int64
	require.NoError(t, db.QueryRow(
		`SELECT current_version FROM application WHERE app_key = 'AK_order'`).Scan(&ver))
	require.Equal(t, int64(0), ver)

	_, err = db.Exec(`INSERT INTO application (tenant_id, domain, name, app_key, app_secret_hash)
		VALUES ($1, 'other', '其他', 'AK_order', 'hash2')`, tenantID)
	require.Error(t, err)

	_, err = db.Exec(`INSERT INTO application (tenant_id, domain, name, app_key, app_secret_hash)
		VALUES ($1, 'order-system', '重复域', 'AK_dup', 'hash3')`, tenantID)
	require.Error(t, err)

	_, err = db.Exec(`INSERT INTO application (tenant_id, domain, name, app_key, app_secret_hash)
		VALUES (999999, 'x', 'x', 'AK_x', 'hashx')`)
	require.Error(t, err)
}
