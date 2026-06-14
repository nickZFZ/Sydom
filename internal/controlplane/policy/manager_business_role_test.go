package policy_test

import (
	"context"
	"testing"

	"github.com/nickZFZ/Sydom/internal/controlplane/policy"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestCreateBusinessRole_AtomicRoleAndGrants(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	ctx := context.Background()
	mgr := policy.NewPolicyManager(db, nil)

	p1, _, err := mgr.UpsertPermission(ctx, appID, "p_read", "orders", "read", "p", "查看订单")
	require.NoError(t, err)
	p2, _, err := mgr.UpsertPermission(ctx, appID, "p_export", "orders", "export", "p", "导出订单")
	require.NoError(t, err)

	roleID, d, err := mgr.CreateBusinessRole(ctx, appID, "销售经理", []int64{p1, p2})
	require.NoError(t, err)
	require.NotZero(t, roleID)
	require.NotNil(t, d)          // 有授权 → 产生 casbin_rule → Delta 非空
	require.Len(t, d.RuleAdds, 2) // 2 条授权 → 2 条 casbin_rule 新增传回调用方

	rules, err := store.ReadAppRules(ctx, db, appID)
	require.NoError(t, err)
	var pRows int
	for _, r := range rules {
		if r.Ptype == "p" {
			pRows++
		}
	}
	require.Equal(t, 2, pRows)
}

// TestCreateBusinessRole_GrantFailureRollsBackRole 验证原子回滚——CreateBusinessRole 相对
// CreateRole+GrantPermission 的唯一增量语义保障：批量授权中任一条失败，已建的角色整笔回滚，
// 绝不残留半授权空角色（一致性红线）。
func TestCreateBusinessRole_GrantFailureRollsBackRole(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	ctx := context.Background()
	mgr := policy.NewPolicyManager(db, nil)

	p1, _, err := mgr.UpsertPermission(ctx, appID, "p_read", "orders", "read", "p", "查看订单")
	require.NoError(t, err)

	// p1 合法 + 一个不存在的权限点 id → 第二条授权 FK 违反 → 整笔回滚。
	_, _, err = mgr.CreateBusinessRole(ctx, appID, "销售经理", []int64{p1, 999999})
	require.Error(t, err)

	// 角色表无任何残留（SeedApp 不建角色）。
	var roleCount int
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT count(*) FROM role WHERE app_id=$1`, appID).Scan(&roleCount))
	require.Equal(t, 0, roleCount)

	// casbin_rule 也无残留 p 行。
	rules, err := store.ReadAppRules(ctx, db, appID)
	require.NoError(t, err)
	for _, r := range rules {
		require.NotEqual(t, "p", r.Ptype, "回滚后绝不残留授权 p 行")
	}
}

func TestCreateBusinessRole_EmptyCapabilitiesOK(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	mgr := policy.NewPolicyManager(db, nil)

	roleID, _, err := mgr.CreateBusinessRole(context.Background(), appID, "空角色", nil)
	require.NoError(t, err)
	require.NotZero(t, roleID) // 无能力空角色合法
}
