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

// TestAuthorizeRule_CrossTenantIsolation 是 M1.1 退风险验收矩阵：
// 经共用 AuthorizeRule，证明租户隔离在鉴权核心层正确。
func TestAuthorizeRule_CrossTenantIsolation(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	mk := bytes.Repeat([]byte{0x11}, crypto.KeySize)

	tA, appA := dbtest.SeedAppInTenant(t, db, "tenant-a", "app-a", "AK_a")
	tB, appB := dbtest.SeedAppInTenant(t, db, "tenant-b", "app-b", "AK_b")

	require.NoError(t, adminauthz.EnsureTenantAdmin(ctx, db, mk, tA, "alice", []byte("sa")))
	require.NoError(t, adminauthz.EnsureTenantAdmin(ctx, db, mk, tB, "bob", []byte("sb")))
	require.NoError(t, adminauthz.EnsureRootOperator(ctx, db, mk, "root", []byte("sr")))

	enf, err := adminauthz.NewEnforcer(db)
	require.NoError(t, err)

	// code 返回 AuthorizeRule 的 gRPC 状态码（nil→codes.OK）。
	code := func(principal, method string, req any) codes.Code {
		_, err := mgmt.AuthorizeRule(ctx, enf, method, principal, req)
		return status.Code(err)
	}
	const (
		createRole = "/sydom.admin.v1.AdminService/CreateRole"
		listRoles  = "/sydom.admin.v1.AdminService/ListRoles"
		createOp   = "/sydom.admin.v1.AdminService/CreateOperator"
	)
	roleReq := func(appID int64) *adminv1.CreateRoleRequest { return &adminv1.CreateRoleRequest{AppId: uint64(appID)} }
	listReq := func(appID int64) *adminv1.ListRolesRequest { return &adminv1.ListRolesRequest{AppId: uint64(appID)} }

	// alice = 租户 A 管理员：本租户放行，跨租户 403（写 + 读）。
	require.Equal(t, codes.OK, code("alice", createRole, roleReq(appA)))
	require.Equal(t, codes.PermissionDenied, code("alice", createRole, roleReq(appB)), "alice 写跨租户 app 必须 403")
	require.Equal(t, codes.OK, code("alice", listRoles, listReq(appA)))
	require.Equal(t, codes.PermissionDenied, code("alice", listRoles, listReq(appB)), "alice 读跨租户 app 必须 403")

	// bob = 租户 B 管理员：对称。
	require.Equal(t, codes.OK, code("bob", createRole, roleReq(appB)))
	require.Equal(t, codes.PermissionDenied, code("bob", createRole, roleReq(appA)), "bob 写跨租户 app 必须 403")

	// root = super-admin：两租户均放行。
	require.Equal(t, codes.OK, code("root", createRole, roleReq(appA)))
	require.Equal(t, codes.OK, code("root", createRole, roleReq(appB)))

	// 租户管理员碰不到 SaaS 级 system RPC；root 可以。
	require.Equal(t, codes.PermissionDenied, code("alice", createOp, &adminv1.CreateOperatorRequest{Principal: "x"}), "租户管理员不得创建 operator")
	require.Equal(t, codes.OK, code("root", createOp, &adminv1.CreateOperatorRequest{Principal: "x"}))
}
