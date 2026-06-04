package store_test

import (
	"context"
	"testing"

	"github.com/lib/pq"
	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// TestDataPolicyEffectColumn 验证 000015 迁移：effect 列默认 allow、CHECK 拒非法值、接受 deny。
func TestDataPolicyEffectColumn(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)

	var eff string
	// 不指定 effect → 默认 allow
	require.NoError(t, db.QueryRow(`
		INSERT INTO data_policy (app_id, subject_type, subject_id, resource, condition, version)
		VALUES ($1,'role','m','order','{}'::jsonb,1) RETURNING effect`, appID).Scan(&eff))
	require.Equal(t, "allow", eff)

	// 显式 deny 接受
	require.NoError(t, db.QueryRow(`
		INSERT INTO data_policy (app_id, subject_type, subject_id, resource, condition, effect, version)
		VALUES ($1,'user','a','order','{}'::jsonb,'deny',1) RETURNING effect`, appID).Scan(&eff))
	require.Equal(t, "deny", eff)

	// 非法值被 CHECK 拒
	_, err := db.Exec(`
		INSERT INTO data_policy (app_id, subject_type, subject_id, resource, condition, effect, version)
		VALUES ($1,'role','m','order','{}'::jsonb,'bogus',1)`, appID)
	require.Error(t, err)
	var pqErr *pq.Error
	require.ErrorAs(t, err, &pqErr)
	require.Equal(t, "23514", string(pqErr.Code)) // check_violation
}

func TestUpsertDataPolicy_EffectRoundTrip(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	ctx := context.Background()

	// 新增 deny
	denyID, created, err := store.UpsertDataPolicy(ctx, db, appID, cp.DataPolicy{
		SubjectType: "role", SubjectID: "manager", Resource: "order", Condition: "{}", Effect: "deny",
	}, 1)
	require.NoError(t, err)
	require.True(t, created)

	// 空串归一为 allow
	_, _, err = store.UpsertDataPolicy(ctx, db, appID, cp.DataPolicy{
		SubjectType: "user", SubjectID: "alice", Resource: "invoice", Condition: "{}", Effect: "",
	}, 2)
	require.NoError(t, err)

	got, err := store.ReadAppDataPolicies(ctx, db, appID)
	require.NoError(t, err)
	byID := map[int64]string{}
	for _, p := range got {
		byID[p.ID] = p.Effect
	}
	require.Equal(t, "deny", byID[denyID])
	require.Len(t, got, 2)
	for _, p := range got {
		require.Contains(t, []string{"allow", "deny"}, p.Effect)
	}

	// UPDATE 改 effect deny→allow
	_, _, err = store.UpsertDataPolicy(ctx, db, appID, cp.DataPolicy{
		ID: denyID, SubjectType: "role", SubjectID: "manager", Resource: "order", Condition: "{}", Effect: "allow",
	}, 3)
	require.NoError(t, err)
	got2, err := store.ReadAppDataPolicies(ctx, db, appID)
	require.NoError(t, err)
	for _, p := range got2 {
		if p.ID == denyID {
			require.Equal(t, "allow", p.Effect)
		}
	}
}
