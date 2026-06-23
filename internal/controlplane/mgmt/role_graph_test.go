package mgmt_test

import (
	"context"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"database/sql"
)

// ─── 本测试文件内的小 helper ───────────────────────────────────────────────────

func mustRole(t *testing.T, db *sql.DB, appID int64, code, name string) int64 {
	t.Helper()
	id, err := store.InsertRole(context.Background(), db, appID, code, name)
	require.NoError(t, err)
	return id
}

func mustPerm(t *testing.T, db *sql.DB, appID int64, code, resource, action, name string) int64 {
	t.Helper()
	id, err := store.UpsertPermission(context.Background(), db, appID, code, resource, action, "api", name)
	require.NoError(t, err)
	return id
}

func mustGrant(t *testing.T, db *sql.DB, appID, roleID, permID int64) {
	t.Helper()
	require.NoError(t, store.InsertRolePermission(context.Background(), db, appID, roleID, permID, "allow"))
}

// mustInherit 添加角色继承关系。child 先、parent 后（与 store.InsertRoleInheritance 参数顺序一致）。
func mustInherit(t *testing.T, db *sql.DB, appID, childID, parentID int64) {
	t.Helper()
	require.NoError(t, store.InsertRoleInheritance(context.Background(), db, appID, childID, parentID))
}

func mustBind(t *testing.T, db *sql.DB, appID int64, userID string, roleID int64) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO user_role_binding(app_id,user_id,role_id) VALUES($1,$2,$3)`,
		appID, userID, roleID)
	require.NoError(t, err)
}

func mustDataPolicy(t *testing.T, db *sql.DB, appID int64, subjectID, resource, conditionJSON string) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO data_policy(app_id,subject_type,subject_id,resource,condition,effect,version)
		 VALUES($1,'role',$2,$3,$4::jsonb,'allow',1)`,
		appID, subjectID, resource, conditionJSON)
	require.NoError(t, err)
}

// mustCasbinP 直插一条 casbin p 规则（与 Sidecar 快照同源）。effperm 经 casbin_rule 求值，
// 而非 relational role_permission——故 effperm 背书的 handler（SimulateRoleChange）须经此播种授权。
// p 行：v0=sub, v1=dom, v2=obj, v3=act, v4=eft。
func mustCasbinP(t *testing.T, db *sql.DB, appID int64, sub, dom, obj, act, eft string) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO casbin_rule(app_id,ptype,v0,v1,v2,v3,v4,v5,version)
		 VALUES($1,'p',$2,$3,$4,$5,$6,'',1)`,
		appID, sub, dom, obj, act, eft)
	require.NoError(t, err)
}

// capSource 在 caps 里找匹配 resource+action 的 capability，返回其 Source。未找到返回空串。
func capSource(caps []*adminv1.RoleGraphCapability, resource, action string) string {
	for _, c := range caps {
		if c.Resource == resource && c.Action == action {
			return c.Source
		}
	}
	return ""
}

// ─── 测试 ──────────────────────────────────────────────────────────────────────

// TestGetRoleGraph_AggregatesAndInheritance 播种：
//   - role viewer 授 order:read；
//   - role admin 继承 viewer 且授 order:write；
//   - 绑 alice→admin；
//   - viewer 持 order 数据范围（$user.tenant_id 符号谓词）。
//
// 断言聚合结果和 NotFound 防泄露。
func TestGetRoleGraph_AggregatesAndInheritance(t *testing.T) {
	db := dbtest.SetupSchema(t)
	_, appID := dbtest.SeedAppInTenant(t, db, "t-a", "dom-a", "AK_a")
	srv := accountsSrv(db)
	ctx := context.Background()

	viewerID := mustRole(t, db, appID, "viewer", "查看员")
	adminID := mustRole(t, db, appID, "admin", "管理员")
	permR := mustPerm(t, db, appID, "order:read", "order", "read", "查看订单")
	permW := mustPerm(t, db, appID, "order:write", "order", "write", "修改订单")
	mustGrant(t, db, appID, viewerID, permR)
	mustGrant(t, db, appID, adminID, permW)
	mustInherit(t, db, appID, adminID /*child*/, viewerID /*parent*/)
	mustBind(t, db, appID, "alice", adminID)
	mustDataPolicy(t, db, appID, "viewer", "order",
		`{"field":"tenant_id","op":"EQ","value":"$user.tenant_id"}`)

	resp, err := srv.GetRoleGraph(ctx, &adminv1.GetRoleGraphRequest{
		AppId: uint64(appID), RoleId: adminID})
	require.NoError(t, err)
	require.Equal(t, "admin", resp.RoleCode)
	// BoundUsers 已是 []string，直接断言。
	require.Contains(t, resp.BoundUsers, "alice")
	// 能力：直接 order/write(source="direct") + 继承 order/read(source="查看员")。
	require.Equal(t, "direct", capSource(resp.Capabilities, "order", "write"))
	require.Equal(t, "查看员", capSource(resp.Capabilities, "order", "read"))
	// 父角色应含 viewer。
	require.Len(t, resp.Parents, 1)
	require.Equal(t, "viewer", resp.Parents[0].Code)
	// admin 自身无直接数据策略。
	require.Empty(t, resp.DataScopes)

	// viewer 自身有 order 数据范围。
	vresp, err := srv.GetRoleGraph(ctx, &adminv1.GetRoleGraphRequest{
		AppId: uint64(appID), RoleId: viewerID})
	require.NoError(t, err)
	require.Len(t, vresp.DataScopes, 1)
	require.Equal(t, "order", vresp.DataScopes[0].Resource)
	require.Equal(t, "allow", vresp.DataScopes[0].Effect)
	require.JSONEq(t, `{"field":"tenant_id","op":"EQ","value":"$user.tenant_id"}`, vresp.DataScopes[0].Condition)

	// 跨 App 隔离（RG-6）：app B 的 app_id 配 app A 的 role_id → NotFound。
	_, appIDB := dbtest.SeedAppInTenant(t, db, "t-b", "dom-b", "AK_b")
	_, err = srv.GetRoleGraph(ctx, &adminv1.GetRoleGraphRequest{
		AppId: uint64(appIDB), RoleId: viewerID})
	require.Equal(t, codes.NotFound, status.Code(err))

	// 未知 role → NotFound（不泄露存在性）。
	_, err = srv.GetRoleGraph(ctx, &adminv1.GetRoleGraphRequest{
		AppId: uint64(appID), RoleId: 999999})
	require.Equal(t, codes.NotFound, status.Code(err))
}

func TestSimulateRoleChange_BindUserDiff(t *testing.T) {
	db := dbtest.SetupSchema(t)
	_, appID := dbtest.SeedAppInTenant(t, db, "t-a", "dom-a", "AK_a")
	srv := accountsSrv(db)
	ctx := context.Background()

	viewerID := mustRole(t, db, appID, "viewer", "查看员")
	// effperm 经 casbin_rule 求值（与 Sidecar 同源），故授权须播种 casbin p 行；dom 取 app 域 "dom-a"。
	mustCasbinP(t, db, appID, "viewer", "dom-a", "order", "read", "allow")
	mustDataPolicy(t, db, appID, "viewer", "order", `{"field":"tenant_id","op":"EQ","value":"$user.tenant_id"}`)

	resp, err := srv.SimulateRoleChange(ctx, &adminv1.SimulateRoleChangeRequest{
		AppId: uint64(appID), RoleId: viewerID,
		ChangeType: adminv1.RoleChangeType_BIND_USER, UserId: "bob"})
	require.NoError(t, err)
	require.Len(t, resp.Subjects, 1)
	require.Equal(t, "bob", resp.Subjects[0].UserId)
	require.NotEmpty(t, resp.Subjects[0].AddedPermissions)
	// 精确断言字段映射（防 Resource/Action 错位的静默 bug）。
	require.Equal(t, "order", resp.Subjects[0].AddedPermissions[0].Resource)
	require.Equal(t, "read", resp.Subjects[0].AddedPermissions[0].Action)
	require.NotEmpty(t, resp.Subjects[0].AddedDataPreviews)
	require.Contains(t, resp.Subjects[0].AddedDataPreviews[0].Predicate, "$user.")

	// 未知 role → NotFound。
	_, err = srv.SimulateRoleChange(ctx, &adminv1.SimulateRoleChangeRequest{
		AppId: uint64(appID), RoleId: 999, ChangeType: adminv1.RoleChangeType_BIND_USER, UserId: "x"})
	require.Equal(t, codes.NotFound, status.Code(err))
}
