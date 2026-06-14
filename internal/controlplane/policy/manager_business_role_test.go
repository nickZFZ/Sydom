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
	require.NotNil(t, d) // 有授权 → 产生 casbin_rule → Delta 非空

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

func TestCreateBusinessRole_EmptyCapabilitiesOK(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	mgr := policy.NewPolicyManager(db, nil)

	roleID, _, err := mgr.CreateBusinessRole(context.Background(), appID, "空角色", nil)
	require.NoError(t, err)
	require.NotZero(t, roleID) // 无能力空角色合法
}
