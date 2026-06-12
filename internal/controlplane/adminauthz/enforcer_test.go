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

	// alice 直绑 app 域 "7"：r.dom 路径放行（tdom 在此无关，传 ""）。
	allow, err := enf.Enforce(ctx, "alice", "7", "", "role", "create")
	require.NoError(t, err)
	require.True(t, allow)
	deny, _ := enf.Enforce(ctx, "alice", "9", "", "role", "create")
	require.False(t, deny, "跨 app 域必须拒绝")
	deny2, _ := enf.Enforce(ctx, "alice", "7", "", "application", "create")
	require.False(t, deny2, "未授予的资源必须拒绝")

	for _, dom := range []string{"7", "9", "*"} {
		ok, _ := enf.Enforce(ctx, "bob", dom, "", "application", "create")
		require.True(t, ok, "super-admin 在域 %s 应放行", dom)
	}

	no, _ := enf.Enforce(ctx, "ghost", "7", "", "role", "create")
	require.False(t, no)

	empty1, _ := enf.Enforce(ctx, "", "7", "", "role", "create")
	require.False(t, empty1, "空 principal 必须拒绝")
	empty2, _ := enf.Enforce(ctx, "alice", "", "", "role", "create")
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
	ok, _ := enf.Enforce(ctx, "alice", "7", "", "role", "create")
	require.False(t, ok)

	require.NoError(t, adminauthz.InsertRoleGrant(ctx, db, roleID, "7", "role", "create"))
	require.NoError(t, adminauthz.BumpPolicyVersion(ctx, db))
	ok2, _ := enf.Enforce(ctx, "alice", "7", "", "role", "create")
	require.True(t, ok2, "版本 bump 后应重载并放行")
}

// 新增：租户域作为 app 域之上的包含层——租户管理员在 t:<id> 的通配 grant
// 覆盖其名下任意 app；跨租户必须拒绝。
func TestEnforcer_TenantDomainContainment(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()

	opID, _ := adminauthz.InsertOperator(ctx, db, "alice", []byte("x"))
	roleID, _ := adminauthz.InsertRole(ctx, db, "tadmin-5", "n")
	// 通配 grant 锚定在租户域 t:5。
	require.NoError(t, adminauthz.InsertRoleGrant(ctx, db, roleID, "t:5", "*", "*"))
	require.NoError(t, adminauthz.InsertSubjectRole(ctx, db, opID, roleID, "t:5"))

	enf, err := adminauthz.NewEnforcer(db)
	require.NoError(t, err)

	// 本租户名下任意 app 域（"42"）+ 租户域 t:5 → 放行（经 r.tdom 析取项）。
	ok, err := enf.Enforce(ctx, "alice", "42", "t:5", "role", "create")
	require.NoError(t, err)
	require.True(t, ok, "租户管理员对本租户 app 应放行")
	ok2, _ := enf.Enforce(ctx, "alice", "100", "t:5", "data_policy", "update")
	require.True(t, ok2, "通配 grant 覆盖本租户全部 app-scoped 资源/动作")

	// 跨租户：app 域 "99" + 租户域 t:7 → 拒绝（无任何析取项命中）。
	deny, _ := enf.Enforce(ctx, "alice", "99", "t:7", "role", "create")
	require.False(t, deny, "跨租户必须拒绝")
	// system 域（"*"）→ 租户管理员的 t:5 通配不命中，拒绝。
	denySys, _ := enf.Enforce(ctx, "alice", "*", "*", "admin", "create")
	require.False(t, denySys, "租户管理员不得触达 SaaS 级 system 域")
}

func TestEnforcer_TenantDomainOf(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	appID := dbtest.SeedApp(t, db) // 建 'acme' 租户 + 1 app
	var tenantID int64
	require.NoError(t, db.QueryRow(`SELECT tenant_id FROM application WHERE id=$1`, appID).Scan(&tenantID))

	enf, err := adminauthz.NewEnforcer(db)
	require.NoError(t, err)

	td, err := enf.TenantDomainOf(ctx, appID)
	require.NoError(t, err)
	require.Equal(t, adminauthz.TenantDomain(tenantID), td)

	_, err = enf.TenantDomainOf(ctx, 999999) // 不存在 → fail-close error
	require.Error(t, err)
}
