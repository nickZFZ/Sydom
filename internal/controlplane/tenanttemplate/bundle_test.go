package tenanttemplate_test

import (
	"context"
	"database/sql"
	"testing"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/controlplane/tenanttemplate"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestCapture_FullModel(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	ctx := context.Background()

	// 配好一个 app：权限点 + 角色 + 授权 + 角色数据范围（用既有 store 写入）。
	// 并写一条 user 主体 data_policy + 一条 user_role_binding 作为「应被排除」的对照。
	seedConfiguredApp(t, db, appID)

	b, err := tenanttemplate.Capture(ctx, db, appID)
	require.NoError(t, err)
	require.Len(t, b.Permissions, 1)
	require.Equal(t, "order.read", b.Permissions[0].Code)
	require.Len(t, b.Roles, 1)
	r := b.Roles[0]
	require.NotContains(t, r.Key, ":") // 安全 key：去 ':'（ApplyTemplate 拒 key 含 ':'）
	require.Contains(t, r.PermissionCodes, "order.read")
	require.Len(t, r.DataScopes, 1) // TT-4 齿：删过滤行→user 主体泄漏→Len==2→FAIL
	require.Equal(t, "order", r.DataScopes[0].Resource)
	require.JSONEq(t, `{"field":"department","op":"EQ","value":"$user.department"}`, string(r.DataScopes[0].Condition))
	// 显式排除 user 主体泄漏资源：若过滤行被删，user 那条会归入此 role 的 DataScopes
	for _, ds := range r.DataScopes {
		require.NotEqual(t, "user-only-leak", ds.Resource, "user 主体 data_policy 不应出现在角色数据范围中")
	}
}

func TestSafeKey_DropsColonAndDedups(t *testing.T) {
	seen := map[string]bool{}
	require.Equal(t, "tpl_x_editor", tenanttemplate.SafeKey("tpl:x:editor", seen))
	require.Equal(t, "tpl_x_editor_2", tenanttemplate.SafeKey("tpl:x:editor", seen)) // 同名去重
}

// seedConfiguredApp 种入：1 条 order.read 权限点、1 个 cs 角色（code 含 ':' 验证安全 key）、
// role_permission 把 order.read 授给 cs 角色、1 条 subject_type='role' 的 order 资源
// data_policy（condition allow）。外加对照排除项：1 条 subject_type='user' 的 data_policy
// + 1 条 user_role_binding（验证 Capture 不把它们捕获进 bundle）。
//
// TT-4 齿说明：user 主体 data_policy 的 subject_id 故意与角色 code「tpl:x:cs」相同，
// 但 resource 用「user-only-leak」以区分。data_policy 表无唯一约束，两条可共存（仅
// subject_type 不同）。若 bundle.go 删除 `if dp.SubjectType != "role" { continue }` 过滤行，
// user 那条会被错误归入 scopesByRole["tpl:x:cs"]，使 DataScopes 变 2 条 → 断言 Len==1 FAIL。
func seedConfiguredApp(t *testing.T, db *sql.DB, appID int64) {
	t.Helper()
	ctx := context.Background()

	// 1. 权限点
	permID, err := store.UpsertPermission(ctx, db, appID, "order.read", "order", "read", "api", "读订单")
	require.NoError(t, err)

	// 2. 角色（code 含 ':' 以验证安全 key 去冒号）
	roleID, err := store.InsertRole(ctx, db, appID, "tpl:x:cs", "客服")
	require.NoError(t, err)

	// 3. role_permission 授权
	require.NoError(t, store.InsertRolePermission(ctx, db, appID, roleID, permID, "allow"))

	// 4. 角色数据范围（subject_type='role'，subject_id=role.code）
	cond := `{"field":"department","op":"EQ","value":"$user.department"}`
	_, _, err = store.UpsertDataPolicy(ctx, db, appID, cp.DataPolicy{
		SubjectType: "role",
		SubjectID:   "tpl:x:cs",
		Resource:    "order",
		Condition:   cond,
		Effect:      "allow",
	}, 1)
	require.NoError(t, err)

	// 对照排除项 A：user 主体 data_policy，subject_id 故意与角色 code 相同（TT-4 真实泄漏路径）。
	// subject_type='user' vs 'role' 是区分键；resource 用 'user-only-leak' 与角色那条不同。
	// data_policy 无唯一约束，两条可共存。若过滤行被删，此条错误归入该角色的 DataScopes。
	_, _, err = store.UpsertDataPolicy(ctx, db, appID, cp.DataPolicy{
		SubjectType: "user",
		SubjectID:   "tpl:x:cs", // 与角色 code 相同，制造真实泄漏路径
		Resource:    "user-only-leak",
		Condition:   "{}",
		Effect:      "allow",
	}, 2)
	require.NoError(t, err)

	// 对照排除项 B：user_role_binding（Capture 应忽略）
	require.NoError(t, store.InsertUserRoleBinding(ctx, db, appID, "alice", roleID))
}
