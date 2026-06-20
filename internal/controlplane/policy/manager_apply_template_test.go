package policy_test

import (
	"context"
	"testing"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/policy"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestApplyTemplate_CreatesAndIsIdempotent(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	m := policy.NewPolicyManager(db, nil)
	ctx := context.Background()

	perms := []cp.PermissionPoint{
		{Code: "order.read", Resource: "order", Action: "read", Type: "act", Name: "查看订单"},
		{Code: "order.export", Resource: "order", Action: "export", Type: "act", Name: "导出订单"},
	}
	roles := []policy.TemplateRole{
		{Key: "ops", Name: "运营", PermissionCodes: []string{"order.read", "order.export"}},
	}

	res, _, err := m.ApplyTemplate(ctx, appID, "ecommerce-ops", perms, roles)
	require.NoError(t, err)
	require.Equal(t, 2, res.PermsUpserted)
	require.Equal(t, 1, res.RolesCreated)
	require.Equal(t, 0, res.RolesSkipped)

	// 角色 code 确定性。
	var cnt int
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM role WHERE app_id=$1 AND code=$2`, appID, "tpl:ecommerce-ops:ops").Scan(&cnt))
	require.Equal(t, 1, cnt)
	// 角色已授到 2 个权限点。
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM role_permission rp JOIN role r ON r.id=rp.role_id WHERE r.code=$1`, "tpl:ecommerce-ops:ops").Scan(&cnt))
	require.Equal(t, 2, cnt)

	// re-apply：幂等——无重复角色、role 计入 skipped。
	res2, _, err := m.ApplyTemplate(ctx, appID, "ecommerce-ops", perms, roles)
	require.NoError(t, err)
	require.Equal(t, 1, res2.RolesSkipped)
	require.Equal(t, 0, res2.RolesCreated)
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM role WHERE app_id=$1 AND code=$2`, appID, "tpl:ecommerce-ops:ops").Scan(&cnt))
	require.Equal(t, 1, cnt) // 仍只有一个角色
}

func TestApplyTemplate_NeverClobbersManual(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	m := policy.NewPolicyManager(db, nil)
	ctx := context.Background()

	// 预置一个 manual 权限点同 code。
	_, err := store.UpsertPermission(ctx, db, appID, "order.read", "order", "read", "act", "人工命名")
	require.NoError(t, err)

	perms := []cp.PermissionPoint{{Code: "order.read", Resource: "order", Action: "read", Type: "act", Name: "查看订单"}}
	res, _, err := m.ApplyTemplate(ctx, appID, "x", perms, nil)
	require.NoError(t, err)
	require.Equal(t, 1, res.PermsSkipped) // manual 命中→跳过保留
	var name string
	require.NoError(t, db.QueryRow(`SELECT name FROM permission WHERE app_id=$1 AND code=$2`, appID, "order.read").Scan(&name))
	require.Equal(t, "人工命名", name) // 名称未被覆盖
}
