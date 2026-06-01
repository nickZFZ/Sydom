package store_test

import (
	"context"
	"testing"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestResolveAppIDByKey(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	ctx := context.Background()

	got, err := store.ResolveAppIDByKey(ctx, db, dbtest.SeedAppKey)
	require.NoError(t, err)
	require.Equal(t, appID, got)

	_, err = store.ResolveAppIDByKey(ctx, db, "AK_nope")
	require.Error(t, err) // 未知 app_key → fail-close 报错
}

func TestReadCurrentVersion(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	ctx := context.Background()

	v, err := store.ReadCurrentVersion(ctx, db, appID)
	require.NoError(t, err)
	require.Equal(t, int64(0), v) // 种子 app 初始版本 0

	_, err = store.ReadCurrentVersion(ctx, db, 9999999)
	require.Error(t, err) // 未知 app id → fail-close 报错
}

func TestReadAppDataPolicies(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	ctx := context.Background()

	var id int64
	require.NoError(t, db.QueryRow(`
		INSERT INTO data_policy (app_id, subject_type, subject_id, resource, condition, version)
		VALUES ($1,'role','manager','order','{"op":"ALL"}'::jsonb,1) RETURNING id`, appID).Scan(&id))

	got, err := store.ReadAppDataPolicies(ctx, db, appID)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, cp.DataPolicy{
		ID: id, SubjectType: "role", SubjectID: "manager", Resource: "order", Condition: `{"op": "ALL"}`,
	}, got[0])
}
