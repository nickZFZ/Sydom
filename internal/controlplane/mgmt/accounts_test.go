package mgmt_test

import (
	"context"
	"database/sql"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/controlplane/outbox"
	"github.com/nickZFZ/Sydom/internal/controlplane/policy"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// accountsSrv 构造一个 in-process *AdminServer，供账户层 handler 单测直调（不经 gRPC 拦截器）。
func accountsSrv(db *sql.DB) *mgmt.AdminServer {
	return mgmt.NewAdminServer(db, policy.NewPolicyManager(db, outbox.NewSink()), mk())
}

func TestRegisterTenant_CreatesOwnerAndMembership(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	srv := accountsSrv(db)

	resp, err := srv.RegisterTenant(ctx, &adminv1.RegisterTenantRequest{
		TenantName: "acme", OwnerPrincipal: "owner1"})
	require.NoError(t, err)
	require.NotZero(t, resp.TenantId)
	require.NotEmpty(t, resp.OwnerSecret) // 一次性明文返回

	// membership(owner) 已写（tier=1=owner）。
	var tier int16
	require.NoError(t, db.QueryRow(
		`SELECT m.tier FROM tenant_membership m JOIN admin_operator o ON o.id=m.operator_id
		 WHERE o.principal='owner1' AND m.tenant_id=$1`, resp.TenantId).Scan(&tier))
	require.Equal(t, int16(1), tier)

	// 重复租户名 → AlreadyExists。
	_, err = srv.RegisterTenant(ctx, &adminv1.RegisterTenantRequest{TenantName: "acme", OwnerPrincipal: "owner2"})
	require.Equal(t, codes.AlreadyExists, status.Code(err))

	// 非法 principal → InvalidArgument。
	_, err = srv.RegisterTenant(ctx, &adminv1.RegisterTenantRequest{TenantName: "z", OwnerPrincipal: "bad principal!"})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestListMyTenants_ReturnsOwnMemberships(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	srv := accountsSrv(db)

	r1, err := srv.RegisterTenant(ctx, &adminv1.RegisterTenantRequest{TenantName: "t1", OwnerPrincipal: "u"})
	require.NoError(t, err)

	out, err := srv.ListMyTenants(cp.WithOperator(ctx, "u"), &adminv1.ListMyTenantsRequest{})
	require.NoError(t, err)
	require.Len(t, out.Memberships, 1)
	require.Equal(t, r1.TenantId, out.Memberships[0].TenantId)
	require.False(t, out.IsOperatingPlane)
}
