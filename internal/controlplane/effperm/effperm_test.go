package effperm_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/nickZFZ/Sydom/internal/controlplane/effperm"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// insertRule 直插一条 casbin_rule（dom 用 dbtest.SeedDomain）。
// p 行: v0=sub, v1=dom, v2=obj, v3=act, v4=eft
// g 行: v0=child, v1=parent, v2=dom
// version=1（测试用固定版本，满足 NOT NULL 约束）。
func insertRule(t *testing.T, db *sql.DB, appID int64, ptype string, v ...string) {
	t.Helper()
	cols := [6]string{}
	copy(cols[:], v)
	_, err := db.Exec(
		`INSERT INTO casbin_rule (app_id, ptype, v0, v1, v2, v3, v4, v5, version)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		appID, ptype, cols[0], cols[1], cols[2], cols[3], cols[4], cols[5], 1)
	require.NoError(t, err)
}

func insertDataPolicy(t *testing.T, db *sql.DB, appID int64, subjType, subjID, resource, effect, condJSON string) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO data_policy (app_id, subject_type, subject_id, resource, condition, effect, version)
		 VALUES ($1,$2,$3,$4,$5::jsonb,$6,1)`,
		appID, subjType, subjID, resource, condJSON, effect)
	require.NoError(t, err)
}

func TestCompute_DirectBindingAndInheritance(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	dom := dbtest.SeedDomain

	// sales 继承 viewer；viewer 可 read orders；sales 可 export orders；alice→sales。
	insertRule(t, db, appID, "p", "viewer", dom, "orders", "read", "allow")
	insertRule(t, db, appID, "p", "sales", dom, "orders", "export", "allow")
	insertRule(t, db, appID, "g", "sales", "viewer", dom) // child=sales 继承 parent=viewer
	insertRule(t, db, appID, "g", "alice", "sales", dom)  // alice→sales

	res, err := effperm.Compute(context.Background(), db, appID, "alice")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"sales", "viewer"}, res.Roles)
	require.ElementsMatch(t, []effperm.Perm{
		{Resource: "orders", Action: "read"},
		{Resource: "orders", Action: "export"},
	}, res.Permissions)
}

func TestCompute_DenyOverride(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	dom := dbtest.SeedDomain

	insertRule(t, db, appID, "p", "sales", dom, "orders", "read", "allow")
	insertRule(t, db, appID, "p", "sales", dom, "orders", "read", "deny")
	insertRule(t, db, appID, "g", "alice", "sales", dom)

	res, err := effperm.Compute(context.Background(), db, appID, "alice")
	require.NoError(t, err)
	require.Empty(t, res.Permissions) // deny 覆盖后不在允许集
}

func TestCompute_DataPolicySymbolicPreview(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	dom := dbtest.SeedDomain

	insertRule(t, db, appID, "g", "alice", "sales", dom)
	insertDataPolicy(t, db, appID, "role", "sales", "orders", "allow",
		`{"op":"EQ","field":"region","value":"$user.region"}`)

	res, err := effperm.Compute(context.Background(), db, appID, "alice")
	require.NoError(t, err)
	require.Len(t, res.DataViews, 1)
	require.Equal(t, "orders", res.DataViews[0].Resource)
	require.Equal(t, "conditional", res.DataViews[0].Match)
	require.Equal(t, "region = $user.region", res.DataViews[0].Predicate)
}

func TestCompute_PoisonedDataPolicyFailClose(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	dom := dbtest.SeedDomain

	insertRule(t, db, appID, "g", "alice", "sales", dom)
	insertDataPolicy(t, db, appID, "role", "sales", "orders", "allow", `{"op":"BOGUS"}`)

	_, err := effperm.Compute(context.Background(), db, appID, "alice")
	require.Error(t, err) // 命中中毒 → fail-close
}

func TestCompute_EmptyAppNoError(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)

	res, err := effperm.Compute(context.Background(), db, appID, "nobody")
	require.NoError(t, err)
	require.Empty(t, res.Roles)
	require.Empty(t, res.Permissions)
	require.Empty(t, res.DataViews)
}

// 新人查自己权限的真实场景：user 无任何绑定，但 app 对其他 role 配了 data_policy。
// 该 resource 仍要出现在预览里、且因主体不命中 → MatchNone（绝不漏报为"无策略"）。
func TestCompute_UserWithoutBindingsSeesDataPolicyAsNone(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)

	// bob 无 g 行；orders 仅对 sales 角色配了行过滤。
	insertDataPolicy(t, db, appID, "role", "sales", "orders", "allow",
		`{"op":"EQ","field":"region","value":"east"}`)

	res, err := effperm.Compute(context.Background(), db, appID, "bob")
	require.NoError(t, err)
	require.Empty(t, res.Roles)
	require.Empty(t, res.Permissions)
	require.Len(t, res.DataViews, 1)
	require.Equal(t, "orders", res.DataViews[0].Resource)
	require.Equal(t, "none", res.DataViews[0].Match) // 配了但无 allow 命中 → 全拒
}

func TestExplain_AllowGrantedViaInheritance(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	dom := dbtest.SeedDomain
	insertRule(t, db, appID, "p", "viewer", dom, "orders", "read", "allow")
	insertRule(t, db, appID, "g", "sales", "viewer", dom) // sales 继承 viewer
	insertRule(t, db, appID, "g", "alice", "sales", dom)  // alice→sales

	exp, err := effperm.Explain(context.Background(), db, appID, "alice", "orders", "read")
	require.NoError(t, err)
	require.True(t, exp.Allowed)
	require.Equal(t, effperm.ReasonAllowGranted, exp.Reason)
	require.NotNil(t, exp.DecidingRule)
	require.Equal(t, "viewer", exp.DecidingRole) // 携权角色 viewer（经 sales 继承）
	require.Equal(t, "allow", exp.DecidingRule.Effect)
	require.ElementsMatch(t, []string{"sales", "viewer"}, exp.Roles)
}

func TestExplain_DenyOverridden(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	dom := dbtest.SeedDomain
	insertRule(t, db, appID, "p", "sales", dom, "orders", "read", "allow")
	insertRule(t, db, appID, "p", "sales", dom, "orders", "read", "deny")
	insertRule(t, db, appID, "g", "alice", "sales", dom)

	exp, err := effperm.Explain(context.Background(), db, appID, "alice", "orders", "read")
	require.NoError(t, err)
	require.False(t, exp.Allowed)
	require.Equal(t, effperm.ReasonDenyOverridden, exp.Reason)
	require.NotNil(t, exp.DecidingRule)
	require.Equal(t, "deny", exp.DecidingRule.Effect)
}

func TestExplain_DenyNoMatch(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	dom := dbtest.SeedDomain
	insertRule(t, db, appID, "g", "alice", "sales", dom) // 有角色但无任何 grant

	exp, err := effperm.Explain(context.Background(), db, appID, "alice", "orders", "read")
	require.NoError(t, err)
	require.False(t, exp.Allowed)
	require.Equal(t, effperm.ReasonDenyNoMatch, exp.Reason)
	require.Nil(t, exp.DecidingRule)
	require.Equal(t, "", exp.DecidingRole)
	require.Contains(t, exp.Roles, "sales") // 仍列出用户角色（帮助排障）
}

func TestExplain_DataScopeSymbolic(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	dom := dbtest.SeedDomain
	insertRule(t, db, appID, "p", "sales", dom, "orders", "read", "allow")
	insertRule(t, db, appID, "g", "alice", "sales", dom)
	insertDataPolicy(t, db, appID, "role", "sales", "orders", "allow",
		`{"op":"EQ","field":"region","value":"$user.region"}`)

	exp, err := effperm.Explain(context.Background(), db, appID, "alice", "orders", "read")
	require.NoError(t, err)
	require.True(t, exp.Allowed)
	require.Equal(t, "conditional", exp.DataScope.Match)
	require.Contains(t, exp.DataScope.Predicate, "$user.region") // 符号谓词保留
}
