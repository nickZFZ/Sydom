package adminauthz_test

import (
	"context"
	"testing"

	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestEnforcer_Matrix(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()

	opID, _ := adminauthz.InsertOperator(ctx, db, "alice", []byte("x"))
	roleID, _ := adminauthz.InsertRole(ctx, db, "app7-admin", "n")
	require.NoError(t, adminauthz.InsertRoleGrant(ctx, db, roleID, "7", "role", "create"))
	require.NoError(t, adminauthz.InsertSubjectRole(ctx, db, opID, roleID, "7"))
	sid, _ := adminauthz.InsertOperator(ctx, db, "bob", []byte("x"))
	var superID int64
	require.NoError(t, db.QueryRow(`SELECT id FROM admin_role WHERE code='super-admin'`).Scan(&superID))
	require.NoError(t, adminauthz.InsertSubjectRole(ctx, db, sid, superID, "*"))

	enf, err := adminauthz.NewEnforcer(db)
	require.NoError(t, err)

	allow, err := enf.Enforce(ctx, "alice", "7", "role", "create")
	require.NoError(t, err)
	require.True(t, allow)
	deny, _ := enf.Enforce(ctx, "alice", "9", "role", "create")
	require.False(t, deny, "跨 app 域必须拒绝")
	deny2, _ := enf.Enforce(ctx, "alice", "7", "application", "create")
	require.False(t, deny2, "未授予的资源必须拒绝")

	for _, dom := range []string{"7", "9", "*"} {
		ok, _ := enf.Enforce(ctx, "bob", dom, "application", "create")
		require.True(t, ok, "super-admin 在域 %s 应放行", dom)
	}

	no, _ := enf.Enforce(ctx, "ghost", "7", "role", "create")
	require.False(t, no)

	empty1, _ := enf.Enforce(ctx, "", "7", "role", "create")
	require.False(t, empty1, "空 principal 必须拒绝")
	empty2, _ := enf.Enforce(ctx, "alice", "", "role", "create")
	require.False(t, empty2, "空 domain 必须拒绝")
}

func TestEnforcer_ReloadOnVersionBump(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	opID, _ := adminauthz.InsertOperator(ctx, db, "alice", []byte("x"))
	roleID, _ := adminauthz.InsertRole(ctx, db, "app7-admin", "n")
	require.NoError(t, adminauthz.InsertSubjectRole(ctx, db, opID, roleID, "7"))

	enf, err := adminauthz.NewEnforcer(db)
	require.NoError(t, err)
	ok, _ := enf.Enforce(ctx, "alice", "7", "role", "create")
	require.False(t, ok)

	require.NoError(t, adminauthz.InsertRoleGrant(ctx, db, roleID, "7", "role", "create"))
	require.NoError(t, adminauthz.BumpPolicyVersion(ctx, db))
	ok2, _ := enf.Enforce(ctx, "alice", "7", "role", "create")
	require.True(t, ok2, "版本 bump 后应重载并放行")
}
