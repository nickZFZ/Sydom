package db_test

import (
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestAdminSchema_TablesAndSeed(t *testing.T) {
	conn := dbtest.SetupSchema(t)

	// 五张 admin 表存在
	for _, tbl := range []string{"admin_operator", "admin_role", "admin_role_grant", "admin_subject_role", "admin_policy_version"} {
		var exists bool
		require.NoError(t, conn.QueryRow(
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name=$1)`, tbl).Scan(&exists))
		require.True(t, exists, "缺表 %s", tbl)
	}

	// 种子：super-admin 角色存在，且在 * 域拥有 (*,*) 全权
	var roleID int64
	require.NoError(t, conn.QueryRow(`SELECT id FROM admin_role WHERE code='super-admin'`).Scan(&roleID))
	var n int
	require.NoError(t, conn.QueryRow(
		`SELECT count(*) FROM admin_role_grant WHERE role_id=$1 AND domain='*' AND resource='*' AND action='*'`, roleID).Scan(&n))
	require.Equal(t, 1, n)

	// admin_policy_version 单行初始为 0
	var v int64
	require.NoError(t, conn.QueryRow(`SELECT version FROM admin_policy_version WHERE id=1`).Scan(&v))
	require.Equal(t, int64(0), v)
}
