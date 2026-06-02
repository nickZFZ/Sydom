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
