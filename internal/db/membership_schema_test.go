package db_test

import (
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestTenantMembership_UniqueAndCascade(t *testing.T) {
	conn := dbtest.SetupSchema(t)

	var tenantID, opID int64
	require.NoError(t, conn.QueryRow(`INSERT INTO tenant (name) VALUES ('acme') RETURNING id`).Scan(&tenantID))
	require.NoError(t, conn.QueryRow(
		`INSERT INTO admin_operator (principal, secret_enc) VALUES ('u1', '\xab'::bytea) RETURNING id`).Scan(&opID))

	_, err := conn.Exec(`INSERT INTO tenant_membership (tenant_id, operator_id, tier) VALUES ($1,$2,1)`, tenantID, opID)
	require.NoError(t, err)

	// 唯一约束：同 (tenant, operator) 二次插入失败。
	_, err = conn.Exec(`INSERT INTO tenant_membership (tenant_id, operator_id, tier) VALUES ($1,$2,2)`, tenantID, opID)
	require.Error(t, err)

	// 级联：删 operator → membership 行随之消失。
	_, err = conn.Exec(`DELETE FROM admin_operator WHERE id=$1`, opID)
	require.NoError(t, err)
	var n int
	require.NoError(t, conn.QueryRow(`SELECT count(*) FROM tenant_membership WHERE tenant_id=$1`, tenantID).Scan(&n))
	require.Equal(t, 0, n)

	// tenant 侧级联：删 tenant → 其下 membership 行随之消失（与 operator 侧对称）。
	var t2, op2 int64
	require.NoError(t, conn.QueryRow(`INSERT INTO tenant (name) VALUES ('beta') RETURNING id`).Scan(&t2))
	require.NoError(t, conn.QueryRow(
		`INSERT INTO admin_operator (principal, secret_enc) VALUES ('u2', '\xcd'::bytea) RETURNING id`).Scan(&op2))
	_, err = conn.Exec(`INSERT INTO tenant_membership (tenant_id, operator_id, tier) VALUES ($1,$2,1)`, t2, op2)
	require.NoError(t, err)
	_, err = conn.Exec(`DELETE FROM tenant WHERE id=$1`, t2)
	require.NoError(t, err)
	require.NoError(t, conn.QueryRow(`SELECT count(*) FROM tenant_membership WHERE tenant_id=$1`, t2).Scan(&n))
	require.Equal(t, 0, n)
}
