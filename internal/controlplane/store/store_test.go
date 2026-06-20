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

func TestUpsertAutoPermission_InsertAndRefresh(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	ctx := context.Background()

	applied, err := store.UpsertAutoPermission(ctx, db, appID, "p.read", "order", "read", "api", "读订单", "")
	require.NoError(t, err)
	require.True(t, applied)
	var src, name string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT source, name FROM permission WHERE app_id=$1 AND code=$2`, appID, "p.read").Scan(&src, &name))
	require.Equal(t, "auto", src)
	require.Equal(t, "读订单", name)

	applied, err = store.UpsertAutoPermission(ctx, db, appID, "p.read", "order", "read", "api", "读订单V2", "desc")
	require.NoError(t, err)
	require.True(t, applied)
	var desc string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT name, description FROM permission WHERE app_id=$1 AND code=$2`, appID, "p.read").Scan(&name, &desc))
	require.Equal(t, "读订单V2", name)
	require.Equal(t, "desc", desc) // 固化空串→非空串刷新口径（DB 真相源存空串非 NULL）
}

func TestUpsertAutoPermission_NeverClobbersManual(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	ctx := context.Background()

	_, err := db.ExecContext(ctx,
		`INSERT INTO permission (app_id, code, resource, action, type, name, source)
		 VALUES ($1,$2,$3,$4,$5,$6,'manual')`,
		appID, "p.manual", "order", "write", "api", "人工写订单")
	require.NoError(t, err)

	applied, err := store.UpsertAutoPermission(ctx, db, appID, "p.manual", "CHANGED", "CHANGED", "x", "篡改", "x")
	require.NoError(t, err)
	require.False(t, applied)
	var src, resource, name string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT source, resource, name FROM permission WHERE app_id=$1 AND code=$2`, appID, "p.manual").Scan(&src, &resource, &name))
	require.Equal(t, "manual", src)
	require.Equal(t, "order", resource)
	require.Equal(t, "人工写订单", name)
}

func TestPermissionIDsByCode(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	ctx := context.Background()
	id1, err := store.UpsertPermission(ctx, db, appID, "a.read", "a", "read", "act", "查看A")
	require.NoError(t, err)
	_, err = store.UpsertPermission(ctx, db, appID, "b.read", "b", "read", "act", "查看B")
	require.NoError(t, err)

	m, err := store.PermissionIDsByCode(ctx, db, appID, []string{"a.read", "b.read", "missing"})
	require.NoError(t, err)
	require.Equal(t, id1, m["a.read"])
	require.Contains(t, m, "b.read")
	require.NotContains(t, m, "missing") // 不存在的 code 不入 map

	// 空 codes 短路：返回空 map 无错，不打 DB。
	empty, err := store.PermissionIDsByCode(ctx, db, appID, nil)
	require.NoError(t, err)
	require.Empty(t, empty)
}

func TestUpsertTemplateRole_Idempotent(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	ctx := context.Background()

	id1, created1, err := store.UpsertTemplateRole(ctx, db, appID, "tpl:x:admin", "管理员")
	require.NoError(t, err)
	require.True(t, created1)

	id2, created2, err := store.UpsertTemplateRole(ctx, db, appID, "tpl:x:admin", "改了的名") // re-apply
	require.NoError(t, err)
	require.False(t, created2) // 已存在 → 跳过
	require.Equal(t, id1, id2) // 同一行
	// 名称不被覆盖（不改人工后续编辑）。
	var name string
	require.NoError(t, db.QueryRow(`SELECT name FROM role WHERE id=$1`, id1).Scan(&name))
	require.Equal(t, "管理员", name)
}
