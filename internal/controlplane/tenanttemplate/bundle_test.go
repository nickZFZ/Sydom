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
	require.Len(t, r.DataScopes, 1)
	require.Equal(t, "order", r.DataScopes[0].Resource)
	require.JSONEq(t, `{"field":"department","op":"EQ","value":"$user.department"}`, string(r.DataScopes[0].Condition))
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

	// 对照排除项 A：user 主体 data_policy（Capture 应忽略）
	_, _, err = store.UpsertDataPolicy(ctx, db, appID, cp.DataPolicy{
		SubjectType: "user",
		SubjectID:   "alice",
		Resource:    "order",
		Condition:   "{}",
		Effect:      "allow",
	}, 2)
	require.NoError(t, err)

	// 对照排除项 B：user_role_binding（Capture 应忽略）
	require.NoError(t, store.InsertUserRoleBinding(ctx, db, appID, "alice", roleID))
}
