package policy_test

import (
	"context"
	"testing"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/policy"
	"github.com/nickZFZ/Sydom/internal/controlplane/projection"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestBindUserRole_ProducesGRow(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	m := policy.NewPolicyManager(db, nil)

	rid, _, err := m.CreateRole(context.Background(), appID, "manager", "经理")
	require.NoError(t, err)
	d, err := m.BindUserRole(context.Background(), appID, "u-100", rid)
	require.NoError(t, err)
	require.NotNil(t, d)
	require.Contains(t, d.RuleAdds,
		cp.Rule{Ptype: "g", V: [6]string{"u-100", "manager", dbtest.SeedDomain, "", "", ""}})
}

func TestAddRoleInheritance_CycleRejected(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	m := policy.NewPolicyManager(db, nil)

	a, _, err := m.CreateRole(context.Background(), appID, "A", "A")
	require.NoError(t, err)
	b, _, err := m.CreateRole(context.Background(), appID, "B", "B")
	require.NoError(t, err)

	// A 继承 B
	_, err = m.AddRoleInheritance(context.Background(), appID, a, b)
	require.NoError(t, err)
	// B 继承 A → 成环，必须被拒，且无任何变更
	_, err = m.AddRoleInheritance(context.Background(), appID, b, a)
	require.ErrorIs(t, err, projection.ErrCycle)

	var n int
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM role_inheritance WHERE app_id=$1`, appID).Scan(&n))
	require.Equal(t, 1, n) // 仍只有一条边
}

func TestDeleteRole_CascadesAndRemovesRules(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	m := policy.NewPolicyManager(db, nil)

	permID, _, err := m.UpsertPermission(context.Background(), appID, "order:read", "order", "read", "api", "读订单")
	require.NoError(t, err)
	roleID, _, err := m.CreateRole(context.Background(), appID, "manager", "经理")
	require.NoError(t, err)
	_, err = m.GrantPermission(context.Background(), appID, roleID, permID, "allow")
	require.NoError(t, err)

	d, err := m.DeleteRole(context.Background(), appID, roleID)
	require.NoError(t, err)
	require.NotNil(t, d)

	var rules int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM casbin_rule WHERE app_id=$1`, appID).Scan(&rules))
	require.Equal(t, 0, rules) // 角色删除后其 p 行被重投影清掉
}
