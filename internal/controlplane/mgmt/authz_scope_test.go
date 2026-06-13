package mgmt_test

import (
	"bytes"
	"context"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/crypto"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestAuthorizeRule_TenantScope(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	mk := bytes.Repeat([]byte{0x11}, crypto.KeySize)

	tA, _ := dbtest.SeedAppInTenant(t, db, "ta", "appa", "AK_a")
	tB, _ := dbtest.SeedAppInTenant(t, db, "tb", "appb", "AK_b")
	require.NoError(t, adminauthz.EnsureTenantAdmin(ctx, db, mk, tA, "alice", []byte("sa")))
	require.NoError(t, adminauthz.EnsureTenantAdmin(ctx, db, mk, tB, "bob", []byte("sb")))
	require.NoError(t, adminauthz.EnsureRootOperator(ctx, db, mk, "root", []byte("sr")))
	enf, err := adminauthz.NewEnforcer(db)
	require.NoError(t, err)

	code := func(principal, method string, req any) codes.Code {
		_, err := mgmt.AuthorizeRule(ctx, enf, method, principal, req)
		return status.Code(err)
	}
	const (
		createApp = "/sydom.admin.v1.AdminService/CreateApplication"
		listApps  = "/sydom.admin.v1.AdminService/ListApplications"
		listMine  = "/sydom.admin.v1.AdminService/ListMyTenants"
		invite    = "/sydom.admin.v1.AdminService/InviteMember"
	)
	// tenant-target：本租户放行，跨租户 / 列全量(0) 拒绝。
	require.Equal(t, codes.OK, code("alice", createApp, &adminv1.CreateApplicationRequest{TenantId: uint64(tA)}))
	require.Equal(t, codes.PermissionDenied, code("alice", createApp, &adminv1.CreateApplicationRequest{TenantId: uint64(tB)}))
	require.Equal(t, codes.OK, code("alice", listApps, &adminv1.ListApplicationsRequest{TenantId: uint64(tA)}))
	require.Equal(t, codes.PermissionDenied, code("alice", listApps, &adminv1.ListApplicationsRequest{TenantId: uint64(tB)}))
	require.Equal(t, codes.PermissionDenied, code("alice", listApps, &adminv1.ListApplicationsRequest{TenantId: 0}), "租户管理员列全量(0)必须 403")
	require.Equal(t, codes.PermissionDenied, code("alice", invite, &adminv1.InviteMemberRequest{TenantId: uint64(tB)}))

	// super-admin：列全量(0) 与任一租户均放行。
	require.Equal(t, codes.OK, code("root", listApps, &adminv1.ListApplicationsRequest{TenantId: 0}))
	require.Equal(t, codes.OK, code("root", createApp, &adminv1.CreateApplicationRequest{TenantId: uint64(tA)}))

	// self：任一已认证 principal 放行（不 enforce）。
	require.Equal(t, codes.OK, code("alice", listMine, &adminv1.ListMyTenantsRequest{}))
	require.Equal(t, codes.OK, code("root", listMine, &adminv1.ListMyTenantsRequest{}))
}

func TestUnauthenticatedMethods_RegisterTenant(t *testing.T) {
	require.True(t, mgmt.UnauthenticatedMethods["/sydom.admin.v1.AdminService/RegisterTenant"])
	require.False(t, mgmt.UnauthenticatedMethods["/sydom.admin.v1.AdminService/CreateApplication"])
}
