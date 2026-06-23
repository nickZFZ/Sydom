package effperm_test

import (
	"context"
	"strings"
	"testing"

	"github.com/nickZFZ/Sydom/internal/controlplane/effperm"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// permPairs 把 []Perm 转为 "resource/action" 字符串列表，便于断言。
func permPairs(ps []effperm.Perm) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Resource + "/" + p.Action
	}
	return out
}

// TestSimulate_BindUser_GainsRolePerms：绑 u-1 → viewer，u-1 新获 order:read + 数据范围。
func TestSimulate_BindUser_GainsRolePerms(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	dom := dbtest.SeedDomain

	// viewer 可 read order（allow）
	insertRule(t, db, appID, "p", "viewer", dom, "order", "read", "allow")
	// viewer 对 order 有符号化数据范围
	insertDataPolicy(t, db, appID, "role", "viewer", "order", "allow",
		`{"op":"EQ","field":"tenant_id","value":"$user.tenant_id"}`)

	tx, err := db.Begin()
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()

	diffs, err := effperm.Simulate(context.Background(), tx, appID, "viewer",
		effperm.Change{Type: "bind_user", UserID: "u-1"})
	require.NoError(t, err)
	require.Len(t, diffs, 1)

	d := diffs[0]
	require.Equal(t, "u-1", d.UserID)
	require.Contains(t, permPairs(d.AddedPermissions), "order/read")
	require.Empty(t, d.RemovedPermissions)
	require.NotEmpty(t, d.AddedDataViews)
	// 数据范围应含符号谓词（$user.）
	hasSymbol := false
	for _, dv := range d.AddedDataViews {
		if strings.Contains(dv.Predicate, "$user.") {
			hasSymbol = true
		}
	}
	require.True(t, hasSymbol, "AddedDataViews 应含 $user. 符号谓词")
}

// TestSimulate_NoSideEffects：Simulate 不写 casbin_rule，不 bump version。
func TestSimulate_NoSideEffects(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	dom := dbtest.SeedDomain

	insertRule(t, db, appID, "p", "viewer", dom, "order", "read", "allow")
	insertDataPolicy(t, db, appID, "role", "viewer", "order", "allow",
		`{"op":"EQ","field":"tenant_id","value":"$user.tenant_id"}`)

	// 记录调用前的行数与版本
	var rulesBefore int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM casbin_rule WHERE app_id=$1`, appID).Scan(&rulesBefore))
	var versionBefore int64
	require.NoError(t, db.QueryRow(`SELECT current_version FROM application WHERE id=$1`, appID).Scan(&versionBefore))

	tx, err := db.Begin()
	require.NoError(t, err)

	_, err = effperm.Simulate(context.Background(), tx, appID, "viewer",
		effperm.Change{Type: "bind_user", UserID: "u-noside"})
	require.NoError(t, err)

	// Commit tx 后查库：行数与版本不变
	require.NoError(t, tx.Commit())

	var rulesAfter int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM casbin_rule WHERE app_id=$1`, appID).Scan(&rulesAfter))
	var versionAfter int64
	require.NoError(t, db.QueryRow(`SELECT current_version FROM application WHERE id=$1`, appID).Scan(&versionAfter))

	require.Equal(t, rulesBefore, rulesAfter, "Simulate 不得写入 casbin_rule")
	require.Equal(t, versionBefore, versionAfter, "Simulate 不得 bump current_version")
}

// TestSimulate_BindUser_DenyOverrideRemoves：u-2 已有 r1(allow)，绑到 r2(deny) 后失去 order/read。
// 这是「双向 diff 有齿」用例：若实现只算 added 则此用例 FAIL。
func TestSimulate_BindUser_DenyOverrideRemoves(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	dom := dbtest.SeedDomain

	// r1 allow order:read；u-2 已绑 r1
	insertRule(t, db, appID, "p", "r1", dom, "order", "read", "allow")
	insertRule(t, db, appID, "g", "u-2", "r1", dom)

	// r2 deny order:read（覆盖 r1 的 allow）
	insertRule(t, db, appID, "p", "r2", dom, "order", "read", "deny")

	tx, err := db.Begin()
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()

	// 模拟：把 u-2 绑到 r2（同时仍有 r1 → 但 deny 覆盖 allow）
	diffs, err := effperm.Simulate(context.Background(), tx, appID, "r2",
		effperm.Change{Type: "bind_user", UserID: "u-2"})
	require.NoError(t, err)
	require.Len(t, diffs, 1)

	d := diffs[0]
	require.Equal(t, "u-2", d.UserID)
	require.Contains(t, permPairs(d.RemovedPermissions), "order/read",
		"deny 覆盖后 order/read 应出现在 RemovedPermissions")
}
