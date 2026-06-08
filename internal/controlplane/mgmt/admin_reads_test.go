package mgmt_test

import (
	"context"
	"testing"
	"time"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestAdminReads_AppDomain_RoundTrip(t *testing.T) {
	db := dbtest.SetupSchema(t)
	require.NoError(t, adminauthz.EnsureRootOperator(context.Background(), db, mk(), "root", []byte("root-secret")))
	cli := dialMgmt(t, db, "root", []byte("root-secret"))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	app, err := cli.CreateApplication(ctx, &adminv1.CreateApplicationRequest{
		TenantName: "t", Domain: "d", Name: "n", AppKey: "k-roundtrip"})
	require.NoError(t, err)
	appID := app.AppId

	mgrRole, err := cli.CreateRole(ctx, &adminv1.CreateRoleRequest{AppId: appID, Code: "manager", Name: "经理"})
	require.NoError(t, err)
	clerkRole, err := cli.CreateRole(ctx, &adminv1.CreateRoleRequest{AppId: appID, Code: "clerk", Name: "店员"})
	require.NoError(t, err)
	perm, err := cli.UpsertPermission(ctx, &adminv1.UpsertPermissionRequest{
		AppId: appID, Code: "order:read", Resource: "order", Action: "read", Ptype: "api", Name: "读订单"})
	require.NoError(t, err)
	_, err = cli.GrantPermission(ctx, &adminv1.GrantPermissionRequest{
		AppId: appID, RoleId: mgrRole.RoleId, PermissionId: perm.PermissionId, Eft: "allow"})
	require.NoError(t, err)
	_, err = cli.AddRoleInheritance(ctx, &adminv1.RoleInheritanceRequest{
		AppId: appID, ChildRoleId: clerkRole.RoleId, ParentRoleId: mgrRole.RoleId})
	require.NoError(t, err)
	_, err = cli.BindUserRole(ctx, &adminv1.UserRoleRequest{AppId: appID, UserId: "alice", RoleId: mgrRole.RoleId})
	require.NoError(t, err)
	_, err = cli.UpsertDataPolicy(ctx, &adminv1.UpsertDataPolicyRequest{
		AppId: appID, Id: 0, SubjectType: "role", SubjectId: "manager", Resource: "order",
		Condition: `{"field":"dept","op":"EQ","value":"x"}`, Effect: "allow"})
	require.NoError(t, err)

	roles, err := cli.ListRoles(ctx, &adminv1.ListRolesRequest{AppId: appID})
	require.NoError(t, err)
	require.Len(t, roles.Roles, 2)

	perms, err := cli.ListPermissions(ctx, &adminv1.ListPermissionsRequest{AppId: appID})
	require.NoError(t, err)
	require.Len(t, perms.Permissions, 1)
	require.Equal(t, "order:read", perms.Permissions[0].Code)
	require.Equal(t, "manual", perms.Permissions[0].Source)

	grants, err := cli.ListGrants(ctx, &adminv1.ListGrantsRequest{AppId: appID})
	require.NoError(t, err)
	require.Len(t, grants.Grants, 1)
	require.Equal(t, "allow", grants.Grants[0].Eft)
	g2, err := cli.ListGrants(ctx, &adminv1.ListGrantsRequest{AppId: appID, RoleId: clerkRole.RoleId})
	require.NoError(t, err)
	require.Empty(t, g2.Grants)
	g3, err := cli.ListGrants(ctx, &adminv1.ListGrantsRequest{AppId: appID, RoleId: mgrRole.RoleId})
	require.NoError(t, err)
	require.Len(t, g3.Grants, 1)
	require.Equal(t, mgrRole.RoleId, g3.Grants[0].RoleId)

	inh, err := cli.ListRoleInheritances(ctx, &adminv1.ListRoleInheritancesRequest{AppId: appID})
	require.NoError(t, err)
	require.Len(t, inh.Inheritances, 1)
	require.Equal(t, mgrRole.RoleId, inh.Inheritances[0].ParentRoleId)
	require.Equal(t, clerkRole.RoleId, inh.Inheritances[0].ChildRoleId)

	binds, err := cli.ListUserBindings(ctx, &adminv1.ListUserBindingsRequest{AppId: appID})
	require.NoError(t, err)
	require.Len(t, binds.Bindings, 1)
	require.Equal(t, "alice", binds.Bindings[0].UserId)
	b2, err := cli.ListUserBindings(ctx, &adminv1.ListUserBindingsRequest{AppId: appID, UserId: "nobody"})
	require.NoError(t, err)
	require.Empty(t, b2.Bindings)
	b3, err := cli.ListUserBindings(ctx, &adminv1.ListUserBindingsRequest{AppId: appID, UserId: "alice"})
	require.NoError(t, err)
	require.Len(t, b3.Bindings, 1)
	require.Equal(t, "alice", b3.Bindings[0].UserId)

	dps, err := cli.ListDataPolicies(ctx, &adminv1.ListDataPoliciesRequest{AppId: appID})
	require.NoError(t, err)
	require.Len(t, dps.DataPolicies, 1)
	require.Equal(t, "order", dps.DataPolicies[0].Resource)
	require.Equal(t, "allow", dps.DataPolicies[0].Effect)
	require.Greater(t, dps.DataPolicies[0].Version, uint64(0))
	require.JSONEq(t, `{"field":"dept","op":"EQ","value":"x"}`, dps.DataPolicies[0].Condition)
	dpEmpty, err := cli.ListDataPolicies(ctx, &adminv1.ListDataPoliciesRequest{AppId: appID, Resource: "other"})
	require.NoError(t, err)
	require.Empty(t, dpEmpty.DataPolicies)
	dpOrder, err := cli.ListDataPolicies(ctx, &adminv1.ListDataPoliciesRequest{AppId: appID, Resource: "order"})
	require.NoError(t, err)
	require.Len(t, dpOrder.DataPolicies, 1)
	require.Equal(t, "order", dpOrder.DataPolicies[0].Resource)

	app2, err := cli.CreateApplication(ctx, &adminv1.CreateApplicationRequest{
		TenantName: "t2", Domain: "d2", Name: "n2", AppKey: "k-empty"})
	require.NoError(t, err)
	empty, err := cli.ListRoles(ctx, &adminv1.ListRolesRequest{AppId: app2.AppId})
	require.NoError(t, err)
	require.Empty(t, empty.Roles)
}

func TestAdminReads_SystemDomain_NoSecret(t *testing.T) {
	db := dbtest.SetupSchema(t)
	require.NoError(t, adminauthz.EnsureRootOperator(context.Background(), db, mk(), "root", []byte("root-secret")))
	cli := dialMgmt(t, db, "root", []byte("root-secret"))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	_, err := cli.CreateOperator(ctx, &adminv1.CreateOperatorRequest{Principal: "op-2"})
	require.NoError(t, err)

	ops, err := cli.ListOperators(ctx, &adminv1.ListOperatorsRequest{})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(ops.Operators), 2) // root + op-2
	var principals []string
	for _, o := range ops.Operators {
		require.NotZero(t, o.OperatorId)
		require.NotEmpty(t, o.Principal)
		require.Contains(t, []uint32{1, 2}, o.Status)
		principals = append(principals, o.Principal)
	}
	require.Contains(t, principals, "root")
	require.Contains(t, principals, "op-2")
	// secret 不泄露由 OperatorSummary 结构保证（无 secret 字段）；此处再断言 principal/status 正确即可。

	roles, err := cli.ListAdminRoles(ctx, &adminv1.ListAdminRolesRequest{})
	require.NoError(t, err)
	var roleCodes []string
	for _, r := range roles.Roles {
		roleCodes = append(roleCodes, r.Code)
	}
	require.Contains(t, roleCodes, "super-admin") // 000013 内置
}

func TestAdminReads_CrossAppDomainDenied(t *testing.T) {
	db := dbtest.SetupSchema(t)
	require.NoError(t, adminauthz.EnsureRootOperator(context.Background(), db, mk(), "root", []byte("root-secret")))
	root := dialMgmt(t, db, "root", []byte("root-secret"))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	appA, err := root.CreateApplication(ctx, &adminv1.CreateApplicationRequest{
		TenantName: "ta", Domain: "da", Name: "na", AppKey: "k-a"})
	require.NoError(t, err)
	appB, err := root.CreateApplication(ctx, &adminv1.CreateApplicationRequest{
		TenantName: "tb", Domain: "db", Name: "nb", AppKey: "k-b"})
	require.NoError(t, err)

	// reader：仅在域 A 有 role/read（细粒度）
	op, err := root.CreateOperator(ctx, &adminv1.CreateOperatorRequest{Principal: "reader"})
	require.NoError(t, err)
	ar, err := root.CreateAdminRole(ctx, &adminv1.CreateAdminRoleRequest{Code: "reader-role", Name: "只读"})
	require.NoError(t, err)
	domainA := mgmt.DomainOfAppID(int64(appA.AppId))
	_, err = root.GrantAdminRole(ctx, &adminv1.GrantAdminRoleRequest{
		RoleId: ar.RoleId, Domain: domainA, Resource: "role", Action: "read"})
	require.NoError(t, err)
	_, err = root.BindOperatorRole(ctx, &adminv1.BindOperatorRoleRequest{
		OperatorId: op.OperatorId, RoleId: ar.RoleId, Domain: domainA})
	require.NoError(t, err)

	// 绑定/授权完成后再拨号 reader（dialMgmt 此刻构造的 enforcer 已含上述策略）
	reader := dialMgmt(t, db, "reader", []byte(op.Secret))

	// 域 A 的 role：放行
	_, err = reader.ListRoles(ctx, &adminv1.ListRolesRequest{AppId: appA.AppId})
	require.NoError(t, err)
	// 域 B 的 role：跨域拒绝
	_, err = reader.ListRoles(ctx, &adminv1.ListRolesRequest{AppId: appB.AppId})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	// 域 A 的 permission（reader 只有 role/read）：细粒度资源拒绝
	_, err = reader.ListPermissions(ctx, &adminv1.ListPermissionsRequest{AppId: appA.AppId})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	// system 域 ListOperators（reader 无 admin/read）：拒绝
	_, err = reader.ListOperators(ctx, &adminv1.ListOperatorsRequest{})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}
