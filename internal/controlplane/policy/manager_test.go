package policy_test

import (
	"context"
	"database/sql"
	"sync"
	"testing"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/policy"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// 建一个角色 + 权限点，返回 (roleID, permID)。
func seedRoleAndPerm(t *testing.T, db *sql.DB, appID int64) (int64, int64) {
	t.Helper()
	var roleID, permID int64
	require.NoError(t, db.QueryRow(
		`INSERT INTO role (app_id, code, name) VALUES ($1,'manager','经理') RETURNING id`,
		appID).Scan(&roleID))
	require.NoError(t, db.QueryRow(
		`INSERT INTO permission (app_id, code, resource, action, type, name)
		 VALUES ($1,'order:read','order','read','api','读订单') RETURNING id`,
		appID).Scan(&permID))
	return roleID, permID
}

func TestGrantPermission_EndToEnd(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	roleID, permID := seedRoleAndPerm(t, db, appID)
	m := policy.NewPolicyManager(db)

	d, err := m.GrantPermission(context.Background(), appID, roleID, permID, "allow")
	require.NoError(t, err)
	require.NotNil(t, d)
	require.Equal(t, int64(1), d.Version)
	require.ElementsMatch(t, []cp.Rule{
		{Ptype: "p", V: [6]string{"manager", dbtest.SeedDomain, "order", "read", "allow", ""}},
	}, d.RuleAdds)
	require.Empty(t, d.RuleRemoves)

	var rules, ver, audits int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM casbin_rule WHERE app_id=$1`, appID).Scan(&rules))
	require.Equal(t, 1, rules)
	require.NoError(t, db.QueryRow(`SELECT current_version FROM application WHERE id=$1`, appID).Scan(&ver))
	require.Equal(t, 1, ver)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM policy_audit_log WHERE app_id=$1`, appID).Scan(&audits))
	require.Equal(t, 1, audits)
}

func TestGrantPermission_IdempotentNoOp(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	roleID, permID := seedRoleAndPerm(t, db, appID)
	m := policy.NewPolicyManager(db)

	_, err := m.GrantPermission(context.Background(), appID, roleID, permID, "allow")
	require.NoError(t, err)
	d, err := m.GrantPermission(context.Background(), appID, roleID, permID, "allow")
	require.NoError(t, err)
	require.Nil(t, d)

	var ver int
	require.NoError(t, db.QueryRow(`SELECT current_version FROM application WHERE id=$1`, appID).Scan(&ver))
	require.Equal(t, 1, ver)
}

func TestGrantPermission_AtomicRollback(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	roleID, _ := seedRoleAndPerm(t, db, appID)
	m := policy.NewPolicyManager(db)

	_, err := m.GrantPermission(context.Background(), appID, roleID, 999999, "allow")
	require.Error(t, err)

	var rules, ver int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM casbin_rule WHERE app_id=$1`, appID).Scan(&rules))
	require.Equal(t, 0, rules)
	require.NoError(t, db.QueryRow(`SELECT current_version FROM application WHERE id=$1`, appID).Scan(&ver))
	require.Equal(t, 0, ver)
}

func TestRevokePermission(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	roleID, permID := seedRoleAndPerm(t, db, appID)
	m := policy.NewPolicyManager(db)

	_, err := m.GrantPermission(context.Background(), appID, roleID, permID, "allow")
	require.NoError(t, err)
	d, err := m.RevokePermission(context.Background(), appID, roleID, permID)
	require.NoError(t, err)
	require.NotNil(t, d)
	require.Equal(t, int64(2), d.Version)
	require.Len(t, d.RuleRemoves, 1)

	var rules int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM casbin_rule WHERE app_id=$1`, appID).Scan(&rules))
	require.Equal(t, 0, rules)
}

func TestVersionSerialized(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	m := policy.NewPolicyManager(db)

	const n = 10
	var permID int64
	require.NoError(t, db.QueryRow(
		`INSERT INTO permission (app_id, code, resource, action, type, name)
		 VALUES ($1,'order:read','order','read','api','读订单') RETURNING id`, appID).Scan(&permID))
	roleIDs := make([]int64, n)
	for i := range roleIDs {
		require.NoError(t, db.QueryRow(
			`INSERT INTO role (app_id, code, name) VALUES ($1,$2,$2) RETURNING id`,
			appID, "r"+string(rune('A'+i))).Scan(&roleIDs[i]))
	}

	var wg sync.WaitGroup
	errCh := make(chan error, n)
	for _, rid := range roleIDs {
		wg.Add(1)
		go func(rid int64) {
			defer wg.Done()
			if _, err := m.GrantPermission(context.Background(), appID, rid, permID, "allow"); err != nil {
				errCh <- err
			}
		}(rid)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}

	var ver int64
	require.NoError(t, db.QueryRow(`SELECT current_version FROM application WHERE id=$1`, appID).Scan(&ver))
	require.Equal(t, int64(n), ver)
}
