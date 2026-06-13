package mgmt_test

import (
	"context"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestAccountLayer_CrossTenantIsolation 是 M1.2 退风险验收矩阵：
// 自助注册 + 邀请产出真实主体，证明账户层跨租户隔离在共用 AuthorizeRule 层正确。
func TestAccountLayer_CrossTenantIsolation(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	srv := accountsSrv(db)
	require.NoError(t, adminauthz.EnsureRootOperator(ctx, db, mk(), "root", []byte("sr")))

	rA, err := srv.RegisterTenant(ctx, &adminv1.RegisterTenantRequest{TenantName: "ta", OwnerPrincipal: "alice"})
	require.NoError(t, err)
	rB, err := srv.RegisterTenant(ctx, &adminv1.RegisterTenantRequest{TenantName: "tb", OwnerPrincipal: "carol"})
	require.NoError(t, err)
	// alice 邀 bob 进 A。
	_, err = srv.InviteMember(ctx, &adminv1.InviteMemberRequest{TenantId: rA.TenantId, Principal: "bob"})
	require.NoError(t, err)

	enf, err := adminauthz.NewEnforcer(db) // RegisterTenant/Invite 后已 bump，重建加载最新策略
	require.NoError(t, err)
	code := func(principal, method string, req any) codes.Code {
		_, err := mgmt.AuthorizeRule(ctx, enf, method, principal, req)
		return status.Code(err)
	}
	const (
		createApp = "/sydom.admin.v1.AdminService/CreateApplication"
		listApps  = "/sydom.admin.v1.AdminService/ListApplications"
		invite    = "/sydom.admin.v1.AdminService/InviteMember"
		members   = "/sydom.admin.v1.AdminService/ListMembers"
		createOp  = "/sydom.admin.v1.AdminService/CreateOperator"
	)
	appReq := func(tid uint64) *adminv1.CreateApplicationRequest { return &adminv1.CreateApplicationRequest{TenantId: tid} }
	listReq := func(tid uint64) *adminv1.ListApplicationsRequest { return &adminv1.ListApplicationsRequest{TenantId: tid} }
	invReq := func(tid uint64) *adminv1.InviteMemberRequest {
		return &adminv1.InviteMemberRequest{TenantId: tid, Principal: "x"}
	}
	memReq := func(tid uint64) *adminv1.ListMembersRequest { return &adminv1.ListMembersRequest{TenantId: tid} }

	// owner alice：本租户全放行。
	require.Equal(t, codes.OK, code("alice", createApp, appReq(rA.TenantId)))
	require.Equal(t, codes.OK, code("alice", listApps, listReq(rA.TenantId)))
	require.Equal(t, codes.OK, code("alice", invite, invReq(rA.TenantId)))
	require.Equal(t, codes.OK, code("alice", members, memReq(rA.TenantId)))
	// alice 跨租户 B：全 403。
	require.Equal(t, codes.PermissionDenied, code("alice", createApp, appReq(rB.TenantId)))
	require.Equal(t, codes.PermissionDenied, code("alice", listApps, listReq(rB.TenantId)))
	require.Equal(t, codes.PermissionDenied, code("alice", invite, invReq(rB.TenantId)))
	require.Equal(t, codes.PermissionDenied, code("alice", members, memReq(rB.TenantId)))
	// alice 列全量(0) → 403（非运营平面）。
	require.Equal(t, codes.PermissionDenied, code("alice", listApps, listReq(0)))
	// alice 碰 system RPC（CreateOperator）→ 403。
	require.Equal(t, codes.PermissionDenied, code("alice", createOp, &adminv1.CreateOperatorRequest{Principal: "z"}))

	// 被邀 admin bob：与 owner 同权（A 放行、B 403）。
	require.Equal(t, codes.OK, code("bob", createApp, appReq(rA.TenantId)))
	require.Equal(t, codes.PermissionDenied, code("bob", createApp, appReq(rB.TenantId)))

	// carol（B owner）：B 放行、A 403。
	require.Equal(t, codes.OK, code("carol", members, memReq(rB.TenantId)))
	require.Equal(t, codes.PermissionDenied, code("carol", members, memReq(rA.TenantId)))

	// root 超管：两租户 + 列全量(0) 均放行。
	require.Equal(t, codes.OK, code("root", createApp, appReq(rA.TenantId)))
	require.Equal(t, codes.OK, code("root", createApp, appReq(rB.TenantId)))
	require.Equal(t, codes.OK, code("root", listApps, listReq(0)))

	// I-1 锁步：bob 在 A 既有 membership 行，也有 t:<A> 域 casbin 绑定。
	var nm, ng int
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM tenant_membership m JOIN admin_operator o ON o.id=m.operator_id
		 WHERE o.principal='bob' AND m.tenant_id=$1`, rA.TenantId).Scan(&nm))
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM admin_subject_role sr JOIN admin_operator o ON o.id=sr.operator_id
		 WHERE o.principal='bob' AND sr.domain=$1`, adminauthz.TenantDomain(int64(rA.TenantId))).Scan(&ng))
	require.Equal(t, 1, nm)
	require.Equal(t, 1, ng)
}
