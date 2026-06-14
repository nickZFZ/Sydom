package mgmt_test

import (
	"bytes"
	"context"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/controlplane/outbox"
	"github.com/nickZFZ/Sydom/internal/controlplane/policy"
	"github.com/nickZFZ/Sydom/internal/crypto"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// effSrv 构造一个 in-process *AdminServer，供 GetEffectivePermissions 单测直调（不经 gRPC 拦截器）。
func effSrv(t *testing.T) (*mgmt.AdminServer, func() []byte) {
	t.Helper()
	db := dbtest.SetupSchema(t)
	srv := mgmt.NewAdminServer(db, policy.NewPolicyManager(db, outbox.NewSink()), mk())
	return srv, func() []byte { return nil } // db 关闭由 dbtest 的 t.Cleanup 处理，此处仅返回 srv
}

// TestGetEffectivePermissions_UserIDRequired：UserId 为空时返回 InvalidArgument（不需鉴权，直接调 handler）。
func TestGetEffectivePermissions_UserIDRequired(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	srv := mgmt.NewAdminServer(db, policy.NewPolicyManager(db, outbox.NewSink()), mk())

	ctx := context.Background()
	_, err := srv.GetEffectivePermissions(ctx, &adminv1.GetEffectivePermissionsRequest{AppId: uint64(appID)})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// TestGetEffectivePermissions_AuthorizeRule_OwnTenant：本租户管理员查本租户 app → AuthorizeRule 放行。
func TestGetEffectivePermissions_AuthorizeRule_OwnTenant(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	mk2 := bytes.Repeat([]byte{0x11}, crypto.KeySize)

	tA, appA := dbtest.SeedAppInTenant(t, db, "tenant-a", "domain-a", "AK_a")
	require.NoError(t, adminauthz.EnsureTenantAdmin(ctx, db, mk2, tA, "alice", []byte("sa")))

	enf, err := adminauthz.NewEnforcer(db)
	require.NoError(t, err)

	const method = "/sydom.admin.v1.AdminService/GetEffectivePermissions"
	req := &adminv1.GetEffectivePermissionsRequest{AppId: uint64(appA), UserId: "some-user"}

	// 本租户管理员 alice 访问本租户 app → 放行（无 err）。
	_, err = mgmt.AuthorizeRule(ctx, enf, method, "alice", req)
	require.NoError(t, err, "alice 读本租户 app 有效权限必须放行")
}

// TestGetEffectivePermissions_AuthorizeRule_CrossTenant：租户 A 管理员查租户 B 的 app → PermissionDenied。
func TestGetEffectivePermissions_AuthorizeRule_CrossTenant(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	mk2 := bytes.Repeat([]byte{0x11}, crypto.KeySize)

	tA, _ := dbtest.SeedAppInTenant(t, db, "tenant-a", "domain-a", "AK_a")
	_, appB := dbtest.SeedAppInTenant(t, db, "tenant-b", "domain-b", "AK_b")
	require.NoError(t, adminauthz.EnsureTenantAdmin(ctx, db, mk2, tA, "alice", []byte("sa")))

	enf, err := adminauthz.NewEnforcer(db)
	require.NoError(t, err)

	const method = "/sydom.admin.v1.AdminService/GetEffectivePermissions"
	req := &adminv1.GetEffectivePermissionsRequest{AppId: uint64(appB), UserId: "some-user"}

	// alice（租户 A 管理员）尝试读租户 B 的 app → 跨租户 403，不泄露存在性差异。
	_, err = mgmt.AuthorizeRule(ctx, enf, method, "alice", req)
	require.Equal(t, codes.PermissionDenied, status.Code(err), "alice 读跨租户 app 有效权限必须 403")
}

// TestGetEffectivePermissions_DisabledApp_ReadPassesThrough：停用 app 后，只读 GetEffectivePermissions 仍放行（isWrite=false）。
func TestGetEffectivePermissions_DisabledApp_ReadPassesThrough(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()

	// 用 SeedApp 建 app，再 SetApplicationStatus 停用。
	appID := dbtest.SeedApp(t, db)
	_, err := db.Exec(`UPDATE application SET status=2 WHERE id=$1`, appID)
	require.NoError(t, err)

	// CheckStatusWrite 对只读（isWrite=false）规则直接放行。
	err = mgmt.CheckStatusWrite(ctx, db, "/sydom.admin.v1.AdminService/GetEffectivePermissions",
		&adminv1.GetEffectivePermissionsRequest{AppId: uint64(appID), UserId: "u"})
	require.NoError(t, err, "只读 GetEffectivePermissions 不受 status 写拦截")

	// handler 本身也能处理停用 app（能读到 domain 即可，不校验 status）。
	srv := mgmt.NewAdminServer(db, policy.NewPolicyManager(db, outbox.NewSink()), mk())
	resp, err := srv.GetEffectivePermissions(ctx, &adminv1.GetEffectivePermissionsRequest{
		AppId: uint64(appID), UserId: "u",
	})
	require.NoError(t, err, "停用 app 仍可调用只读 handler")
	require.NotNil(t, resp)
}

// 确保 effSrv helper 真的存在；避免 unused import。
var _ = effSrv
