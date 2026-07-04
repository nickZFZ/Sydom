package mgmt_test

import (
	"context"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/controlplane/outbox"
	"github.com/nickZFZ/Sydom/internal/controlplane/policy"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// 各测试直调 mgmt.NewAdminServer(db, ...) 构造裸 AdminServer 调 handler（鉴权矩阵另用
// AuthorizeRule 覆盖），与 policy_as_code_test.go 的 export/import 直调范式一致。
//
// 种子数据一律经 PolicyManager 的单数 versioned-write 方法（mgr.CreateRole/GrantPermission/
// BindUserRole/AddRoleInheritance），而非绕过 casbin_rule 同步的裸 store.Insert* —— 若种子只写
// 业务表不经 versioned write，casbin_rule 表永远不会被同步，之后批量删除时 runVersionedWrite
// 重投影 diff 的"current"侧仍是空表，会让 Changed 断言因不相关的原因巧合通过（这里踩过一次坑，
// 已改正）。与 internal/controlplane/policy/bulk_test.go 的种子范式保持一致。

// ── BatchDeleteRole ──────────────────────────────────────────────────────

func TestBatchDeleteRole_OK(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	appID := dbtest.SeedApp(t, db)
	mgr := policy.NewPolicyManager(db, outbox.NewSink())
	srv := mgmt.NewAdminServer(db, mgr, mk())

	permID, err := store.UpsertPermission(ctx, db, appID, "p.read", "res", "read", "api", "读")
	require.NoError(t, err)
	r1, _, err := mgr.CreateRole(ctx, appID, "iac:a", "A")
	require.NoError(t, err)
	r2, _, err := mgr.CreateRole(ctx, appID, "iac:b", "B")
	require.NoError(t, err)
	// 裸角色（无授权/绑定/继承）不投影任何 casbin 规则；删除裸角色 diff 为空、Changed 应为
	// false（与单数 DeleteRole 语义一致）。故各挂一份授权，使删除产生可观测的 RuleRemoves。
	_, err = mgr.GrantPermission(ctx, appID, r1, permID, cp.EffectAllow)
	require.NoError(t, err)
	_, err = mgr.GrantPermission(ctx, appID, r2, permID, cp.EffectAllow)
	require.NoError(t, err)

	resp, err := srv.BatchDeleteRole(ctx, &adminv1.BatchDeleteRoleRequest{
		AppId: uint64(appID), RoleIds: []int64{r1, r2, 999999},
	})
	require.NoError(t, err)
	require.EqualValues(t, 3, resp.Requested, "3 个 id 被请求(含 1 个不存在)")
	require.EqualValues(t, 2, resp.Applied, "仅 r1,r2 实际存在")
	require.True(t, resp.Changed)
	require.Greater(t, resp.Version, uint64(0))

	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM role WHERE app_id=$1`, appID).Scan(&n))
	require.Equal(t, 0, n)
}

func TestBatchDeleteRole_Empty_InvalidArgument(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	srv := mgmt.NewAdminServer(db, policy.NewPolicyManager(db, outbox.NewSink()), mk())

	_, err := srv.BatchDeleteRole(context.Background(), &adminv1.BatchDeleteRoleRequest{AppId: uint64(appID)})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestBatchDeleteRole_OverLimit_InvalidArgument(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	srv := mgmt.NewAdminServer(db, policy.NewPolicyManager(db, outbox.NewSink()), mk())

	ids := make([]int64, 1001)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	_, err := srv.BatchDeleteRole(context.Background(), &adminv1.BatchDeleteRoleRequest{AppId: uint64(appID), RoleIds: ids})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestBatchDeleteRole_AllNoOp_NotChanged(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	srv := mgmt.NewAdminServer(db, policy.NewPolicyManager(db, outbox.NewSink()), mk())

	resp, err := srv.BatchDeleteRole(context.Background(), &adminv1.BatchDeleteRoleRequest{
		AppId: uint64(appID), RoleIds: []int64{111, 222},
	})
	require.NoError(t, err)
	require.EqualValues(t, 2, resp.Requested)
	require.EqualValues(t, 0, resp.Applied)
	require.False(t, resp.Changed, "全 no-op 不应 bump/广播")
}

// ── BatchUnbindUserRole ──────────────────────────────────────────────────

func TestBatchUnbindUserRole_OK(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	appID := dbtest.SeedApp(t, db)
	mgr := policy.NewPolicyManager(db, outbox.NewSink())
	srv := mgmt.NewAdminServer(db, mgr, mk())

	r1, _, err := mgr.CreateRole(ctx, appID, "iac:a", "A")
	require.NoError(t, err)
	_, err = mgr.BindUserRole(ctx, appID, "u1", r1)
	require.NoError(t, err)
	_, err = mgr.BindUserRole(ctx, appID, "u2", r1)
	require.NoError(t, err)

	resp, err := srv.BatchUnbindUserRole(ctx, &adminv1.BatchUnbindUserRoleRequest{
		AppId: uint64(appID),
		Items: []*adminv1.UserRoleRef{
			{UserId: "u1", RoleId: r1},
			{UserId: "nobody", RoleId: r1},
		},
	})
	require.NoError(t, err)
	require.EqualValues(t, 2, resp.Requested)
	require.EqualValues(t, 1, resp.Applied)
	require.True(t, resp.Changed)

	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM user_role_binding WHERE app_id=$1 AND user_id='u1'`, appID).Scan(&n))
	require.Equal(t, 0, n)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM user_role_binding WHERE app_id=$1 AND user_id='u2'`, appID).Scan(&n))
	require.Equal(t, 1, n, "u2 未被请求,应保留")
}

func TestBatchUnbindUserRole_Empty_InvalidArgument(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	srv := mgmt.NewAdminServer(db, policy.NewPolicyManager(db, outbox.NewSink()), mk())

	_, err := srv.BatchUnbindUserRole(context.Background(), &adminv1.BatchUnbindUserRoleRequest{AppId: uint64(appID)})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// ── BatchRevokePermission ────────────────────────────────────────────────

func TestBatchRevokePermission_OK(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	appID := dbtest.SeedApp(t, db)
	mgr := policy.NewPolicyManager(db, outbox.NewSink())
	srv := mgmt.NewAdminServer(db, mgr, mk())

	r, _, err := mgr.CreateRole(ctx, appID, "iac:a", "A")
	require.NoError(t, err)
	p1, err := store.UpsertPermission(ctx, db, appID, "p.read", "res", "read", "api", "读")
	require.NoError(t, err)
	p2, err := store.UpsertPermission(ctx, db, appID, "p.write", "res", "write", "api", "写")
	require.NoError(t, err)
	_, err = mgr.GrantPermission(ctx, appID, r, p1, cp.EffectAllow)
	require.NoError(t, err)
	_, err = mgr.GrantPermission(ctx, appID, r, p2, cp.EffectAllow)
	require.NoError(t, err)

	resp, err := srv.BatchRevokePermission(ctx, &adminv1.BatchRevokePermissionRequest{
		AppId: uint64(appID),
		Items: []*adminv1.GrantRef{
			{RoleId: r, PermissionId: p1},
			{RoleId: r, PermissionId: 999999},
		},
	})
	require.NoError(t, err)
	require.EqualValues(t, 2, resp.Requested)
	require.EqualValues(t, 1, resp.Applied)
	require.True(t, resp.Changed)

	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM role_permission WHERE app_id=$1`, appID).Scan(&n))
	require.Equal(t, 1, n)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM role_permission WHERE app_id=$1 AND permission_id=$2`, appID, p2).Scan(&n))
	require.Equal(t, 1, n, "p2 未被请求,应保留")
}

func TestBatchRevokePermission_OverLimit_InvalidArgument(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	srv := mgmt.NewAdminServer(db, policy.NewPolicyManager(db, outbox.NewSink()), mk())

	items := make([]*adminv1.GrantRef, 1001)
	for i := range items {
		items[i] = &adminv1.GrantRef{RoleId: int64(i + 1), PermissionId: int64(i + 1)}
	}
	_, err := srv.BatchRevokePermission(context.Background(), &adminv1.BatchRevokePermissionRequest{AppId: uint64(appID), Items: items})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// ── BatchRemoveRoleInheritance ───────────────────────────────────────────

func TestBatchRemoveRoleInheritance_OK(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	appID := dbtest.SeedApp(t, db)
	mgr := policy.NewPolicyManager(db, outbox.NewSink())
	srv := mgmt.NewAdminServer(db, mgr, mk())

	parent, _, err := mgr.CreateRole(ctx, appID, "iac:parent", "Parent")
	require.NoError(t, err)
	childA, _, err := mgr.CreateRole(ctx, appID, "iac:childA", "ChildA")
	require.NoError(t, err)
	childB, _, err := mgr.CreateRole(ctx, appID, "iac:childB", "ChildB")
	require.NoError(t, err)
	_, err = mgr.AddRoleInheritance(ctx, appID, childA, parent)
	require.NoError(t, err)
	_, err = mgr.AddRoleInheritance(ctx, appID, childB, parent)
	require.NoError(t, err)

	resp, err := srv.BatchRemoveRoleInheritance(ctx, &adminv1.BatchRemoveRoleInheritanceRequest{
		AppId: uint64(appID),
		Items: []*adminv1.InheritanceRef{
			{ChildRoleId: childA, ParentRoleId: parent},
			{ChildRoleId: 999999, ParentRoleId: parent},
		},
	})
	require.NoError(t, err)
	require.EqualValues(t, 2, resp.Requested)
	require.EqualValues(t, 1, resp.Applied)
	require.True(t, resp.Changed)

	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM role_inheritance WHERE app_id=$1`, appID).Scan(&n))
	require.Equal(t, 1, n)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM role_inheritance WHERE app_id=$1 AND child_role_id=$2`, appID, childB).Scan(&n))
	require.Equal(t, 1, n, "childB 未被请求,应保留")
}

func TestBatchRemoveRoleInheritance_Empty_InvalidArgument(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	srv := mgmt.NewAdminServer(db, policy.NewPolicyManager(db, outbox.NewSink()), mk())

	_, err := srv.BatchRemoveRoleInheritance(context.Background(), &adminv1.BatchRemoveRoleInheritanceRequest{AppId: uint64(appID)})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// ── BatchDeleteDataPolicy ────────────────────────────────────────────────

func TestBatchDeleteDataPolicy_OK(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	appID := dbtest.SeedApp(t, db)
	mgr := policy.NewPolicyManager(db, outbox.NewSink())
	srv := mgmt.NewAdminServer(db, mgr, mk())

	d1, err := mgr.UpsertDataPolicy(ctx, appID, cp.DataPolicy{
		SubjectType: "role", SubjectID: "manager", Resource: "order", Condition: `{"op":"ALL"}`,
	})
	require.NoError(t, err)
	require.NotNil(t, d1)
	id1 := d1.DataChanges[0].Policy.ID

	d2, err := mgr.UpsertDataPolicy(ctx, appID, cp.DataPolicy{
		SubjectType: "role", SubjectID: "clerk", Resource: "order", Condition: `{"op":"ALL"}`,
	})
	require.NoError(t, err)
	id2 := d2.DataChanges[0].Policy.ID

	resp, err := srv.BatchDeleteDataPolicy(ctx, &adminv1.BatchDeleteDataPolicyRequest{
		AppId: uint64(appID), DataPolicyIds: []int64{id1, 999999},
	})
	require.NoError(t, err)
	require.EqualValues(t, 2, resp.Requested)
	require.EqualValues(t, 1, resp.Applied)
	require.True(t, resp.Changed)

	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM data_policy WHERE app_id=$1`, appID).Scan(&n))
	require.Equal(t, 1, n)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM data_policy WHERE app_id=$1 AND id=$2`, appID, id2).Scan(&n))
	require.Equal(t, 1, n, "id2 未被请求,应保留")
}

func TestBatchDeleteDataPolicy_Empty_InvalidArgument(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	srv := mgmt.NewAdminServer(db, policy.NewPolicyManager(db, outbox.NewSink()), mk())

	_, err := srv.BatchDeleteDataPolicy(context.Background(), &adminv1.BatchDeleteDataPolicyRequest{AppId: uint64(appID)})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestBatchDeleteDataPolicy_OverLimit_InvalidArgument(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	srv := mgmt.NewAdminServer(db, policy.NewPolicyManager(db, outbox.NewSink()), mk())

	ids := make([]int64, 1001)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	_, err := srv.BatchDeleteDataPolicy(context.Background(), &adminv1.BatchDeleteDataPolicyRequest{AppId: uint64(appID), DataPolicyIds: ids})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// ── 跨租户矩阵（镜像 TestPolicyAsCode_CrossTenantDenied：直调 AuthorizeRule,不绕拦截器语义）───

func TestBatchOps_CrossTenant_PermissionDenied(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()

	tA, appA := dbtest.SeedAppInTenant(t, db, "tenant-bulk-a", "bulk-a-domain", "AK_bulk_a")
	tB, _ := dbtest.SeedAppInTenant(t, db, "tenant-bulk-b", "bulk-b-domain", "AK_bulk_b")

	require.NoError(t, adminauthz.EnsureTenantAdmin(ctx, db, mk(), tA, "alice-bulk", []byte("sa")))
	require.NoError(t, adminauthz.EnsureTenantAdmin(ctx, db, mk(), tB, "bob-bulk", []byte("sb")))

	enf, err := adminauthz.NewEnforcer(db)
	require.NoError(t, err)

	cases := []struct {
		method string
		req    any
	}{
		{"/sydom.admin.v1.AdminService/BatchUnbindUserRole", &adminv1.BatchUnbindUserRoleRequest{AppId: uint64(appA)}},
		{"/sydom.admin.v1.AdminService/BatchRevokePermission", &adminv1.BatchRevokePermissionRequest{AppId: uint64(appA)}},
		{"/sydom.admin.v1.AdminService/BatchRemoveRoleInheritance", &adminv1.BatchRemoveRoleInheritanceRequest{AppId: uint64(appA)}},
		{"/sydom.admin.v1.AdminService/BatchDeleteRole", &adminv1.BatchDeleteRoleRequest{AppId: uint64(appA)}},
		{"/sydom.admin.v1.AdminService/BatchDeleteDataPolicy", &adminv1.BatchDeleteDataPolicyRequest{AppId: uint64(appA)}},
	}
	for _, c := range cases {
		// bob-bulk 是租户 B 管理员,访问租户 A 的 app → PermissionDenied(fail-close,不泄露存在性)。
		_, err := mgmt.AuthorizeRule(ctx, enf, c.method, "bob-bulk", c.req)
		require.Equal(t, codes.PermissionDenied, status.Code(err), "bob-bulk 跨租户 %s 必须 403", c.method)

		// alice-bulk 是租户 A 管理员,访问自己的 app → OK(证明规则本身未被误配置为处处拒绝)。
		_, err = mgmt.AuthorizeRule(ctx, enf, c.method, "alice-bulk", c.req)
		require.Equal(t, codes.OK, status.Code(err), "alice-bulk 访问本租户 %s 必须放行", c.method)
	}
}
