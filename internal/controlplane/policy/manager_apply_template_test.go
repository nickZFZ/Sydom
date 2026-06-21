package policy_test

import (
	"context"
	"strings"
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

// TestApplyTemplate_RollsBackOnFailure 验证原子性（TP-5）：一次应用是单事务复合写——
// 中途任一步失败，已写入的权限点与角色全部回滚，绝不残留半成品（一致性红线，镜像
// CreateBusinessRole 的 GrantFailureRollsBack 范式）。这里用第二个角色的超长 code
// （role.code VARCHAR(64)）触发 DB 插入失败，断言第一个角色与权限点都未持久化。
func TestApplyTemplate_RollsBackOnFailure(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	m := policy.NewPolicyManager(db, nil)
	ctx := context.Background()

	perms := []cp.PermissionPoint{{Code: "order.read", Resource: "order", Action: "read", Type: "act", Name: "查看订单"}}
	roles := []policy.TemplateRole{
		{Key: "ok", Name: "正常", PermissionCodes: []string{"order.read"}},
		// 第二个角色 code = "tpl:t:" + 80×x 远超 VARCHAR(64) → role 插入失败 → 整笔回滚。
		{Key: strings.Repeat("x", 80), Name: "超长", PermissionCodes: []string{"order.read"}},
	}
	_, _, err := m.ApplyTemplate(ctx, appID, "t", perms, roles)
	require.Error(t, err) // 第二个角色失败

	// 整笔回滚：先建成的第一个角色、已升级的权限点、授权全部不残留（同一事务）。
	var roleCnt, permCnt, rpCnt int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM role WHERE app_id=$1`, appID).Scan(&roleCnt))
	require.Equal(t, 0, roleCnt, "已建角色必须随失败回滚")
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM permission WHERE app_id=$1`, appID).Scan(&permCnt))
	require.Equal(t, 0, permCnt, "已升级权限点必须随失败回滚")
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM role_permission WHERE app_id=$1`, appID).Scan(&rpCnt))
	require.Equal(t, 0, rpCnt)
}

// TestApplyTemplate_RejectsColonInIDs 验证确定性 code 不变量的 fail-close 守门：
// templateID 或 role.Key 含 ':' 会破坏 tpl:<id>:<key> 分隔语义，直接拒绝（不入事务）。
func TestApplyTemplate_RejectsColonInIDs(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	m := policy.NewPolicyManager(db, nil)
	ctx := context.Background()

	_, _, err := m.ApplyTemplate(ctx, appID, "a:b", nil, nil)
	require.Error(t, err, "templateID 含 ':' 必须拒绝")

	roles := []policy.TemplateRole{{Key: "bad:key", Name: "x"}}
	_, _, err = m.ApplyTemplate(ctx, appID, "t", nil, roles)
	require.Error(t, err, "role key 含 ':' 必须拒绝")
}

func TestApplyTemplate_SeedsDataScopesOnNewRole(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	m := policy.NewPolicyManager(db, nil)
	ctx := context.Background()

	perms := []cp.PermissionPoint{{Code: "order.read", Resource: "order", Action: "read", Type: "act", Name: "查看订单"}}
	roles := []policy.TemplateRole{{
		Key: "cs", Name: "客服", PermissionCodes: []string{"order.read"},
		DataScopes: []policy.TemplateDataScope{
			{Resource: "order", Effect: "allow", Condition: `{"field":"department","op":"EQ","value":"$user.department"}`},
		},
	}}

	res, d, err := m.ApplyTemplate(ctx, appID, "ecommerce-ops", perms, roles)
	require.NoError(t, err)
	require.Equal(t, 1, res.DataScopesCreated)
	require.NotNil(t, d)             // 数据范围产生 Delta（数据面同步，DSC-6）
	require.Len(t, d.DataChanges, 1) // 1 条 DataPolicyChange 下发

	// data_policy 落库：subject_type=role、subject_id=确定性 role code、condition 透传。
	var stype, sid, cond string
	require.NoError(t, db.QueryRow(
		`SELECT subject_type, subject_id, condition FROM data_policy WHERE app_id=$1 AND resource='order'`, appID).
		Scan(&stype, &sid, &cond))
	require.Equal(t, "role", stype)
	require.Equal(t, "tpl:ecommerce-ops:cs", sid)
	require.JSONEq(t, `{"field":"department","op":"EQ","value":"$user.department"}`, cond)

	// re-apply 幂等：角色已存在→跳过，不种数据范围、无重复 data_policy 行（DSC-4）。
	res2, _, err := m.ApplyTemplate(ctx, appID, "ecommerce-ops", perms, roles)
	require.NoError(t, err)
	require.Equal(t, 0, res2.DataScopesCreated)
	var cnt int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM data_policy WHERE app_id=$1`, appID).Scan(&cnt))
	require.Equal(t, 1, cnt)
}
