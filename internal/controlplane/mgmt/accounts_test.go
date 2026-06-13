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

func TestInviteMember_NewAndExisting(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	srv := accountsSrv(db)

	reg, err := srv.RegisterTenant(ctx, &adminv1.RegisterTenantRequest{TenantName: "t1", OwnerPrincipal: "owner"})
	require.NoError(t, err)

	// 新 principal → 返回一次性 secret + admin 档 membership。
	inv, err := srv.InviteMember(ctx, &adminv1.InviteMemberRequest{TenantId: reg.TenantId, Principal: "alice"})
	require.NoError(t, err)
	require.NotEmpty(t, inv.Secret)
	require.NotZero(t, inv.OperatorId)

	// ListMembers 含 owner + alice。
	lm, err := srv.ListMembers(ctx, &adminv1.ListMembersRequest{TenantId: reg.TenantId})
	require.NoError(t, err)
	require.Len(t, lm.Members, 2)

	// 重复邀请同 principal 同租户 → AlreadyExists。
	_, err = srv.InviteMember(ctx, &adminv1.InviteMemberRequest{TenantId: reg.TenantId, Principal: "alice"})
	require.Equal(t, codes.AlreadyExists, status.Code(err))

	// 既有 operator 被邀到另一租户 → 成功但不返回新 secret（复用既有凭据）。
	reg2, err := srv.RegisterTenant(ctx, &adminv1.RegisterTenantRequest{TenantName: "t2", OwnerPrincipal: "owner2"})
	require.NoError(t, err)
	inv2, err := srv.InviteMember(ctx, &adminv1.InviteMemberRequest{TenantId: reg2.TenantId, Principal: "alice"})
	require.NoError(t, err)
	require.Empty(t, inv2.Secret) // 既有 operator：无新 secret
}

func TestListApplications_TenantFilter(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	srv := accountsSrv(db)

	rA, err := srv.RegisterTenant(ctx, &adminv1.RegisterTenantRequest{TenantName: "ta", OwnerPrincipal: "oa"})
	require.NoError(t, err)
	rB, err := srv.RegisterTenant(ctx, &adminv1.RegisterTenantRequest{TenantName: "tb", OwnerPrincipal: "ob"})
	require.NoError(t, err)
	_, err = srv.CreateApplication(ctx, &adminv1.CreateApplicationRequest{TenantId: rA.TenantId, Domain: "da", Name: "a", AppKey: "AK_a"})
	require.NoError(t, err)
	_, err = srv.CreateApplication(ctx, &adminv1.CreateApplicationRequest{TenantId: rB.TenantId, Domain: "db", Name: "b", AppKey: "AK_b"})
	require.NoError(t, err)

	a, err := srv.ListApplications(ctx, &adminv1.ListApplicationsRequest{TenantId: rA.TenantId})
	require.NoError(t, err)
	require.Len(t, a.Applications, 1) // 仅 A 的 app

	all, err := srv.ListApplications(ctx, &adminv1.ListApplicationsRequest{TenantId: 0})
	require.NoError(t, err)
	require.Len(t, all.Applications, 2) // 0=列全量
}
