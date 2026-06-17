package adminauthz_test

import (
	"context"
	"testing"

	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestStore_CreateAndLoadPolicyRows(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()

	opID, err := adminauthz.InsertOperator(ctx, db, "alice", []byte("enc-secret"))
	require.NoError(t, err)
	roleID, err := adminauthz.InsertRole(ctx, db, "app7-admin", "App7 管理员")
	require.NoError(t, err)
	require.NoError(t, adminauthz.InsertRoleGrant(ctx, db, roleID, "7", "role", "create"))
	require.NoError(t, adminauthz.InsertSubjectRole(ctx, db, opID, roleID, "7"))

	pRows, err := adminauthz.LoadPolicyRows(ctx, db)
	require.NoError(t, err)
	require.Contains(t, pRows, []string{"app7-admin", "7", "role", "create"})
	require.Contains(t, pRows, []string{"super-admin", "*", "*", "*"})

	gRows, err := adminauthz.LoadGroupingRows(ctx, db)
	require.NoError(t, err)
	require.Contains(t, gRows, []string{"alice", "app7-admin", "7"})
}

func TestStore_BumpPolicyVersion(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()

	v0, err := adminauthz.ReadPolicyVersion(ctx, db)
	require.NoError(t, err)
	require.NoError(t, adminauthz.BumpPolicyVersion(ctx, db))
	v1, err := adminauthz.ReadPolicyVersion(ctx, db)
	require.NoError(t, err)
	require.Equal(t, v0+1, v1)
}

func TestDeleteRoleGrant_RemovesThenReportsMissing(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	roleID, err := adminauthz.InsertRole(ctx, db, "app9-admin", "App9 管理员")
	require.NoError(t, err)
	require.NoError(t, adminauthz.InsertRoleGrant(ctx, db, roleID, "9", "role", "create"))

	// 命中删除：成功，且行确实消失。
	require.NoError(t, adminauthz.DeleteRoleGrant(ctx, db, roleID, "9", "role", "create"))
	rows, err := adminauthz.LoadPolicyRows(ctx, db)
	require.NoError(t, err)
	require.NotContains(t, rows, []string{"app9-admin", "9", "role", "create"})

	// 再删（已不存在）：ErrNotFound（fail-close，不静默）。
	require.ErrorIs(t, adminauthz.DeleteRoleGrant(ctx, db, roleID, "9", "role", "create"), adminauthz.ErrNotFound)
}

func TestDeleteSubjectRole_RemovesThenReportsMissing(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	opID, err := adminauthz.InsertOperator(ctx, db, "carol", []byte("enc"))
	require.NoError(t, err)
	roleID, err := adminauthz.InsertRole(ctx, db, "app9-admin2", "n")
	require.NoError(t, err)
	require.NoError(t, adminauthz.InsertSubjectRole(ctx, db, opID, roleID, "9"))

	require.NoError(t, adminauthz.DeleteSubjectRole(ctx, db, opID, roleID, "9"))
	gRows, err := adminauthz.LoadGroupingRows(ctx, db)
	require.NoError(t, err)
	require.NotContains(t, gRows, []string{"carol", "app9-admin2", "9"})

	require.ErrorIs(t, adminauthz.DeleteSubjectRole(ctx, db, opID, roleID, "9"), adminauthz.ErrNotFound)
}
