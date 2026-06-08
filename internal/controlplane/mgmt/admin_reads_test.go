package mgmt_test

import (
	"context"
	"testing"
	"time"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
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
