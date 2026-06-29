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

// TestImport_RoundTripIdempotent（PC-8）：seed iac 权限点 + iac 角色(引用该权限点) →
// Export → Import(dry_run) → 计划应为空（create/update/delete/conflict/adopt 全 0）。
func TestImport_RoundTripIdempotent(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	m := policy.NewPolicyManager(db, nil)
	ctx := context.Background()

	permID, err := store.UpsertPermissionWithSource(ctx, db, appID, "order.read", "order", "read", "act", "查看订单", "", "iac")
	require.NoError(t, err)
	roleID, err := store.InsertRoleWithSource(ctx, db, appID, "iac:ops", "运营", "iac")
	require.NoError(t, err)
	require.NoError(t, store.InsertRolePermission(ctx, db, appID, roleID, permID, cp.EffectAllow))

	out, err := m.ExportAppPolicy(ctx, appID, "yaml")
	require.NoError(t, err)
	require.NotEmpty(t, out)

	plan, _, delta, err := m.ImportAppPolicy(ctx, appID, []byte(out), true)
	require.NoError(t, err)
	require.Nil(t, delta, "dry-run 不产 Delta")
	require.Equal(t, 0, plan.Count("create"))
	require.Equal(t, 0, plan.Count("update"))
	require.Equal(t, 0, plan.Count("delete"))
	require.Equal(t, 0, plan.Count("conflict"))
	require.Equal(t, 0, plan.Count("adopt"))
	require.Empty(t, plan.Items, "往返应完全幂等，无任何收敛项")
}

// TestImport_AppliesCreateAndBumps：空 app → Import(1 权限点 + 1 角色引用该权限点, apply) →
// 权限点/角色存在且 source=iac、版本 bump、授权投影产生 casbin 规则增量。
func TestImport_AppliesCreateAndBumps(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	m := policy.NewPolicyManager(db, nil)
	ctx := context.Background()

	content := []byte(`{
		"apiVersion": "sydom.policy/v1",
		"permissions": [{"code":"order.read","resource":"order","action":"read","type":"act","name":"查看订单"}],
		"roles": [{"key":"ops","name":"运营","permission_codes":["order.read"]}]
	}`)

	plan, version, delta, err := m.ImportAppPolicy(ctx, appID, content, false)
	require.NoError(t, err)
	require.Equal(t, 2, plan.Count("create"), "1 权限点 + 1 角色")
	require.EqualValues(t, 1, version, "首个写从 v0 bump 到 v1")
	require.NotNil(t, delta, "授权投影产生策略变更 → 非空 Delta")
	require.NotEmpty(t, delta.RuleAdds, "角色授权投影出 casbin p 行")

	var psrc string
	require.NoError(t, db.QueryRow(`SELECT source FROM permission WHERE app_id=$1 AND code=$2`, appID, "order.read").Scan(&psrc))
	require.Equal(t, "iac", psrc)
	var rsrc string
	require.NoError(t, db.QueryRow(`SELECT source FROM role WHERE app_id=$1 AND code=$2`, appID, "iac:ops").Scan(&rsrc))
	require.Equal(t, "iac", rsrc)
}

// TestImport_NeverDeletesManual（PC-3 有齿）：seed 1 个 manual 权限点 → Import 空文件(apply) →
// 该 manual 权限点仍在（IaC 永不触碰 manual 治理边界）。
func TestImport_NeverDeletesManual(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	m := policy.NewPolicyManager(db, nil)
	ctx := context.Background()

	_, err := store.UpsertPermission(ctx, db, appID, "legacy.read", "legacy", "read", "act", "遗留") // source 默认 manual
	require.NoError(t, err)

	content := []byte(`{"apiVersion":"sydom.policy/v1","permissions":[],"roles":[]}`)
	plan, _, _, err := m.ImportAppPolicy(ctx, appID, content, false)
	require.NoError(t, err)
	require.Equal(t, 0, plan.Count("delete"), "manual 权限点不进收敛删除计划")

	var cnt int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM permission WHERE app_id=$1 AND code=$2`, appID, "legacy.read").Scan(&cnt))
	require.Equal(t, 1, cnt, "manual 权限点必须保留")
}

// TestImport_DryRunNoSideEffects（PC-4 有齿）：seed → Import(dry_run, 含会改动的文件) →
// DB 行数/version 全等（零副作用），但 plan 反映拟收敛项。
func TestImport_DryRunNoSideEffects(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	m := policy.NewPolicyManager(db, nil)
	ctx := context.Background()

	permID, err := store.UpsertPermissionWithSource(ctx, db, appID, "order.read", "order", "read", "act", "查看订单", "", "iac")
	require.NoError(t, err)
	roleID, err := store.InsertRoleWithSource(ctx, db, appID, "iac:ops", "运营", "iac")
	require.NoError(t, err)
	require.NoError(t, store.InsertRolePermission(ctx, db, appID, roleID, permID, cp.EffectAllow))

	snap := func() (perms, roles, rps, dps int, ver int64) {
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM permission WHERE app_id=$1`, appID).Scan(&perms))
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM role WHERE app_id=$1`, appID).Scan(&roles))
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM role_permission WHERE app_id=$1`, appID).Scan(&rps))
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM data_policy WHERE app_id=$1`, appID).Scan(&dps))
		require.NoError(t, db.QueryRow(`SELECT current_version FROM application WHERE id=$1`, appID).Scan(&ver))
		return
	}
	p0, r0, rp0, d0, v0 := snap()

	// dry-run 一个会新增权限点/角色授权的文件，证明纯读不落任何变更。
	content := []byte(`{
		"apiVersion":"sydom.policy/v1",
		"permissions":[
			{"code":"order.read","resource":"order","action":"read","type":"act","name":"查看订单"},
			{"code":"order.write","resource":"order","action":"write","type":"act","name":"写订单"}
		],
		"roles":[{"key":"ops","name":"运营","permission_codes":["order.read","order.write"]}]
	}`)
	plan, ver, delta, err := m.ImportAppPolicy(ctx, appID, content, true)
	require.NoError(t, err)
	require.Nil(t, delta)
	require.Greater(t, len(plan.Items), 0, "dry-run 应仍计算出非空收敛计划")
	require.EqualValues(t, v0, ver, "dry-run 返回当前版本，不 bump")

	p1, r1, rp1, d1, v1 := snap()
	require.Equal(t, p0, p1, "权限点行数不变")
	require.Equal(t, r0, r1, "角色行数不变")
	require.Equal(t, rp0, rp1, "授权行数不变")
	require.Equal(t, d0, d1, "数据策略行数不变")
	require.Equal(t, v0, v1, "版本不变")
}

// TestImport_DeleteBoundRoleConflict（PC-6）：seed iac 角色 + 用户绑定 → Import 空文件 →
// plan 含 conflict、apply 返回错误、角色与绑定仍在（删除安全：带绑定不删）。
func TestImport_DeleteBoundRoleConflict(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	m := policy.NewPolicyManager(db, nil)
	ctx := context.Background()

	roleID, err := store.InsertRoleWithSource(ctx, db, appID, "iac:ops", "运营", "iac")
	require.NoError(t, err)
	require.NoError(t, store.InsertUserRoleBinding(ctx, db, appID, "u1", roleID))

	content := []byte(`{"apiVersion":"sydom.policy/v1","permissions":[],"roles":[]}`)
	plan, _, delta, err := m.ImportAppPolicy(ctx, appID, content, false)
	require.Error(t, err, "存在 conflict 时 apply 必须整笔拒绝")
	require.Nil(t, delta)
	require.GreaterOrEqual(t, plan.Count("conflict"), 1)

	var rc, bc int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM role WHERE app_id=$1 AND code=$2`, appID, "iac:ops").Scan(&rc))
	require.Equal(t, 1, rc, "冲突时角色不得被删")
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM user_role_binding WHERE app_id=$1 AND role_id=$2`, appID, roleID).Scan(&bc))
	require.Equal(t, 1, bc, "冲突时绑定不得被删")
}
