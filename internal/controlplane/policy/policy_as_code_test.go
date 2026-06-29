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

// TestImport_AdoptPermissionAlignsFields（P1）：seed 一个 manual 权限点(resource/name 与文件不同) →
// Import 声明同 code 但 resource/name 不同(apply) → 该权限点 source 翻为 iac 且 resource/name 对齐为文件值。
// 验证 adopt 走全量 upsert 对齐字段（文件为唯一真相源），而非仅翻 source 留旧值致投影分叉。
func TestImport_AdoptPermissionAlignsFields(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	m := policy.NewPolicyManager(db, nil)
	ctx := context.Background()

	// manual 权限点：resource="old", name="旧名"。
	_, err := store.UpsertPermissionWithSource(ctx, db, appID, "rep:read", "old", "read", "app", "旧名", "", "manual")
	require.NoError(t, err)

	// 文件声明同 code 但 resource/name 不同 → Diff 判 adopt（manual→iac）。
	content := []byte(`{
		"apiVersion":"sydom.policy/v1",
		"permissions":[{"code":"rep:read","resource":"new","action":"read","type":"app","name":"新名"}],
		"roles":[]
	}`)
	plan, _, _, err := m.ImportAppPolicy(ctx, appID, content, false)
	require.NoError(t, err)
	require.Equal(t, 1, plan.Count("adopt"), "manual 权限点被文件声明 → adopt")

	var src, resource, name string
	require.NoError(t, db.QueryRow(
		`SELECT source, resource, name FROM permission WHERE app_id=$1 AND code=$2`,
		appID, "rep:read").Scan(&src, &resource, &name))
	require.Equal(t, "iac", src, "adopt 后 source 必须翻为 iac")
	require.Equal(t, "new", resource, "adopt 必须对齐 resource 到文件值（文件为唯一真相源）")
	require.Equal(t, "新名", name, "adopt 必须对齐 name 到文件值")
}

// TestImport_RoleUpdate_ReauthorizesAndConvergesDataScopes：seed iac 角色(1 授权 + 1 data_scope) →
// Import 改其 permission_codes(增删)与 data_scope(改 condition/effect)(apply) →
// 直查角色 role_permission 集合 == 文件、其 data_policy(subject=code) == 文件态。验证 update 先清后设收敛。
func TestImport_RoleUpdate_ReauthorizesAndConvergesDataScopes(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	m := policy.NewPolicyManager(db, nil)
	ctx := context.Background()

	// 两个 iac 权限点（文件原样声明，避免被判 update/delete）。
	_, err := store.UpsertPermissionWithSource(ctx, db, appID, "a.read", "a", "read", "act", "A", "", "iac")
	require.NoError(t, err)
	bID, err := store.UpsertPermissionWithSource(ctx, db, appID, "b.read", "b", "read", "act", "B", "", "iac")
	require.NoError(t, err)
	_ = bID

	// iac 角色：先授权 a.read（待 import 改为 b.read），再种 1 条角色数据范围 allow{dept:sales}。
	roleID, err := store.InsertRoleWithSource(ctx, db, appID, "iac:ops", "运营", "iac")
	require.NoError(t, err)
	aID, err := store.UpsertPermissionWithSource(ctx, db, appID, "a.read", "a", "read", "act", "A", "", "iac")
	require.NoError(t, err)
	require.NoError(t, store.InsertRolePermission(ctx, db, appID, roleID, aID, cp.EffectAllow))
	_, _, err = store.UpsertDataPolicyWithSource(ctx, db, appID, cp.DataPolicy{
		SubjectType: "role", SubjectID: "iac:ops", Resource: "order",
		Condition: `{"dept":"sales"}`, Effect: "allow",
	}, "iac", 1)
	require.NoError(t, err)

	// 文件：role 授权改为 b.read、data_scope 改 effect=deny condition={dept:ops}。
	content := []byte(`{
		"apiVersion":"sydom.policy/v1",
		"permissions":[
			{"code":"a.read","resource":"a","action":"read","type":"act","name":"A"},
			{"code":"b.read","resource":"b","action":"read","type":"act","name":"B"}
		],
		"roles":[{"key":"ops","name":"运营","permission_codes":["b.read"],
			"data_scopes":[{"resource":"order","effect":"deny","condition":{"dept":"ops"}}]}]
	}`)
	plan, _, delta, err := m.ImportAppPolicy(ctx, appID, content, false)
	require.NoError(t, err)
	require.Equal(t, 1, plan.Count("update"), "角色授权/数据范围变更 → update")
	require.NotNil(t, delta, "授权码集变更重投影 → 非空 Delta")

	// 授权集合应等于文件态 [b.read]。
	codes, err := store.RolePermissionCodes(ctx, db, appID, roleID)
	require.NoError(t, err)
	require.Equal(t, []string{"b.read"}, codes, "授权先清后设对齐文件")

	// 角色数据范围应只剩文件态那 1 条（deny / {dept:ops}）。
	var dpCnt int
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM data_policy WHERE app_id=$1 AND subject_type='role' AND subject_id=$2`,
		appID, "iac:ops").Scan(&dpCnt))
	require.Equal(t, 1, dpCnt, "data_scope 先清后设后只剩 1 条")
	var dpEffect, dpResource, dpCond string
	require.NoError(t, db.QueryRow(
		`SELECT effect, resource, condition::text FROM data_policy WHERE app_id=$1 AND subject_type='role' AND subject_id=$2`,
		appID, "iac:ops").Scan(&dpEffect, &dpResource, &dpCond))
	require.Equal(t, "deny", dpEffect)
	require.Equal(t, "order", dpResource)
	require.JSONEq(t, `{"dept":"ops"}`, dpCond)
}

// TestImport_DeletePermissionStillReferenced_FailClose：seed iac 权限点 P + manual 角色引用 P
// （manual 角色不被收敛）→ Import 不声明 P（→ Diff 判 delete P）apply → ImportAppPolicy 报错、
// P 仍存在、manual 角色对 P 的授权仍在（Phase C DeletePermission 因仍被引用 fail-close 整笔回滚）。
func TestImport_DeletePermissionStillReferenced_FailClose(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	m := policy.NewPolicyManager(db, nil)
	ctx := context.Background()

	permID, err := store.UpsertPermissionWithSource(ctx, db, appID, "p.read", "p", "read", "act", "P", "", "iac")
	require.NoError(t, err)
	// manual 角色（source 默认 manual，不进收敛）引用 P。
	roleID, err := store.InsertRole(ctx, db, appID, "legacy_role", "遗留角色")
	require.NoError(t, err)
	require.NoError(t, store.InsertRolePermission(ctx, db, appID, roleID, permID, cp.EffectAllow))

	content := []byte(`{"apiVersion":"sydom.policy/v1","permissions":[],"roles":[]}`)
	plan, _, delta, err := m.ImportAppPolicy(ctx, appID, content, false)
	require.Error(t, err, "P 仍被 manual 角色引用 → DeletePermission fail-close 整笔回滚")
	require.Nil(t, delta)
	require.GreaterOrEqual(t, plan.Count("delete"), 1, "P 进删除计划")

	var pc, rpc int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM permission WHERE app_id=$1 AND code=$2`, appID, "p.read").Scan(&pc))
	require.Equal(t, 1, pc, "fail-close：权限点 P 必须仍在")
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM role_permission WHERE app_id=$1 AND role_id=$2 AND permission_id=$3`,
		appID, roleID, permID).Scan(&rpc))
	require.Equal(t, 1, rpc, "fail-close：manual 角色对 P 的授权必须仍在")
}
