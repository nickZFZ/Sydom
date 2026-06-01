package policy_test

import (
	"context"
	"testing"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/policy"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestUpsertDataPolicy(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	m := policy.NewPolicyManager(db)

	d, err := m.UpsertDataPolicy(context.Background(), appID, cp.DataPolicy{
		SubjectType: "role", SubjectID: "manager", Resource: "order",
		Condition: `{"op":"EQ","field":"dept","value":"HR"}`,
	})
	require.NoError(t, err)
	require.NotNil(t, d)
	require.Equal(t, int64(1), d.Version)
	require.Len(t, d.DataChanges, 1)
	require.Equal(t, cp.ChangeAdd, d.DataChanges[0].Op)

	var rules, ver, dpVer int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM casbin_rule WHERE app_id=$1`, appID).Scan(&rules))
	require.Equal(t, 0, rules)
	require.NoError(t, db.QueryRow(`SELECT current_version FROM application WHERE id=$1`, appID).Scan(&ver))
	require.Equal(t, 1, ver)
	require.NoError(t, db.QueryRow(`SELECT version FROM data_policy WHERE app_id=$1`, appID).Scan(&dpVer))
	require.Equal(t, 1, dpVer)
}

func TestUpsertDataPolicy_UpdateExisting(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	m := policy.NewPolicyManager(db)

	// 先新增
	d, err := m.UpsertDataPolicy(context.Background(), appID, cp.DataPolicy{
		SubjectType: "role", SubjectID: "manager", Resource: "order", Condition: `{"op":"ALL"}`,
	})
	require.NoError(t, err)
	id := d.DataChanges[0].Policy.ID
	require.Positive(t, id)

	// 带 ID 再次 Upsert → 走 UPDATE 分支，Op=ChangeUpdate、版本=2、data_policy.version=2
	d2, err := m.UpsertDataPolicy(context.Background(), appID, cp.DataPolicy{
		ID: id, SubjectType: "role", SubjectID: "manager", Resource: "order",
		Condition: `{"op":"EQ","field":"dept","value":"HR"}`,
	})
	require.NoError(t, err)
	require.NotNil(t, d2)
	require.Equal(t, int64(2), d2.Version)
	require.Equal(t, cp.ChangeUpdate, d2.DataChanges[0].Op)
	require.Equal(t, id, d2.DataChanges[0].Policy.ID)

	var dpVer, n int
	require.NoError(t, db.QueryRow(`SELECT version FROM data_policy WHERE id=$1`, id).Scan(&dpVer))
	require.Equal(t, 2, dpVer)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM data_policy WHERE app_id=$1`, appID).Scan(&n))
	require.Equal(t, 1, n) // 仍只有一条，UPDATE 未新增
}

// fail-close：对不存在的 data_policy id 做 Upsert/Delete 必须报错，且不 bump 版本。
func TestDataPolicy_NotFoundFailClose(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	m := policy.NewPolicyManager(db)

	_, err := m.UpsertDataPolicy(context.Background(), appID, cp.DataPolicy{
		ID: 999999, SubjectType: "role", SubjectID: "x", Resource: "order", Condition: `{"op":"ALL"}`,
	})
	require.Error(t, err)

	err2 := errFromDelete(t, m, appID, 999999)
	require.Error(t, err2)

	// 两次失败都应整体回滚：版本号仍为 0
	var ver int
	require.NoError(t, db.QueryRow(`SELECT current_version FROM application WHERE id=$1`, appID).Scan(&ver))
	require.Equal(t, 0, ver)
}

func errFromDelete(t *testing.T, m *policy.PolicyManager, appID, id int64) error {
	t.Helper()
	_, err := m.DeleteDataPolicy(context.Background(), appID, id)
	return err
}

func TestDeleteDataPolicy(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	m := policy.NewPolicyManager(db)

	d, err := m.UpsertDataPolicy(context.Background(), appID, cp.DataPolicy{
		SubjectType: "role", SubjectID: "manager", Resource: "order", Condition: `{"op":"ALL"}`,
	})
	require.NoError(t, err)
	id := d.DataChanges[0].Policy.ID
	require.Positive(t, id)

	d2, err := m.DeleteDataPolicy(context.Background(), appID, id)
	require.NoError(t, err)
	require.NotNil(t, d2)
	require.Equal(t, int64(2), d2.Version)
	require.Equal(t, cp.ChangeRemove, d2.DataChanges[0].Op)

	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM data_policy WHERE app_id=$1`, appID).Scan(&n))
	require.Equal(t, 0, n)
}
