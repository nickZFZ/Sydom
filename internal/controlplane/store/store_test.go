package store_test

import (
	"context"
	"testing"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestCasbinRule_ApplyDiffAndRead(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	ctx := context.Background()

	adds := []cp.Rule{
		{Ptype: "p", V: [6]string{"admin", "d", "order", "read", "allow", ""}},
		{Ptype: "g", V: [6]string{"alice", "admin", "d", "", "", ""}},
	}
	require.NoError(t, store.ApplyDiff(ctx, db, appID, adds, nil, 1))

	got, err := store.ReadAppRules(ctx, db, appID)
	require.NoError(t, err)
	require.ElementsMatch(t, adds, got)

	// 删一条、加一条，version=2
	require.NoError(t, store.ApplyDiff(ctx, db, appID,
		[]cp.Rule{{Ptype: "p", V: [6]string{"admin", "d", "order", "write", "allow", ""}}},
		[]cp.Rule{{Ptype: "g", V: [6]string{"alice", "admin", "d", "", "", ""}}}, 2))

	got, err = store.ReadAppRules(ctx, db, appID)
	require.NoError(t, err)
	require.ElementsMatch(t, []cp.Rule{
		{Ptype: "p", V: [6]string{"admin", "d", "order", "read", "allow", ""}},
		{Ptype: "p", V: [6]string{"admin", "d", "order", "write", "allow", ""}},
	}, got)
}

func TestLockAndBumpVersion(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	ctx := context.Background()

	tx, err := db.Begin()
	require.NoError(t, err)
	defer tx.Rollback()

	cur, err := store.LockAppVersion(ctx, tx, appID)
	require.NoError(t, err)
	require.Equal(t, int64(0), cur)
	require.NoError(t, store.BumpAppVersion(ctx, tx, appID, cur+1))
	require.NoError(t, tx.Commit())

	var v int64
	require.NoError(t, db.QueryRow(
		`SELECT current_version FROM application WHERE id=$1`, appID).Scan(&v))
	require.Equal(t, int64(1), v)
}
