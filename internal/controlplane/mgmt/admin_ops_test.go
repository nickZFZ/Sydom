package mgmt_test

import (
	"context"
	"testing"
	"time"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/crypto"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestAdminService_ApplicationLifecycle(t *testing.T) {
	db := dbtest.SetupSchema(t)
	require.NoError(t, adminauthz.EnsureRootOperator(context.Background(), db, mk(), "root", []byte("root-secret")))
	cli := dialMgmt(t, db, "root", []byte("root-secret"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cr, err := cli.CreateApplication(ctx, &adminv1.CreateApplicationRequest{
		TenantName: "acme", Domain: "order-system", Name: "订单", AppKey: "AK_x"})
	require.NoError(t, err)
	require.NotEmpty(t, cr.AppSecret)
	require.Greater(t, cr.AppId, uint64(0))

	// disable 后业务写被 status 拦截器拦截
	_, err = cli.SetApplicationStatus(ctx, &adminv1.SetApplicationStatusRequest{AppId: cr.AppId, Status: 2})
	require.NoError(t, err)
	_, err = cli.CreateRole(ctx, &adminv1.CreateRoleRequest{AppId: cr.AppId, Code: "r", Name: "n"})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))

	apps, err := cli.ListApplications(ctx, &adminv1.ListApplicationsRequest{})
	require.NoError(t, err)
	require.NotEmpty(t, apps.Applications)
}

func TestAdminService_OperatorSelfManagement(t *testing.T) {
	db := dbtest.SetupSchema(t)
	require.NoError(t, adminauthz.EnsureRootOperator(context.Background(), db, mk(), "root", []byte("root-secret")))
	cli := dialMgmt(t, db, "root", []byte("root-secret"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	co, err := cli.CreateOperator(ctx, &adminv1.CreateOperatorRequest{Principal: "bob"})
	require.NoError(t, err)
	require.NotEmpty(t, co.Secret)
	require.Greater(t, co.OperatorId, int64(0))

	role, err := cli.CreateAdminRole(ctx, &adminv1.CreateAdminRoleRequest{Code: "app1-admin", Name: "n"})
	require.NoError(t, err)
	_, err = cli.GrantAdminRole(ctx, &adminv1.GrantAdminRoleRequest{
		RoleId: role.RoleId, Domain: "1", Resource: "role", Action: "create"})
	require.NoError(t, err)
	_, err = cli.BindOperatorRole(ctx, &adminv1.BindOperatorRoleRequest{
		OperatorId: co.OperatorId, RoleId: role.RoleId, Domain: "1"})
	require.NoError(t, err)
}

// TestAdminService_CreateApplication_SecretEncryptedRoundTrip 证明安全红线：
// 明文 AppSecret 不入库，库里存的是密文，且密文可用 masterKey 解回原文。
func TestAdminService_CreateApplication_SecretEncryptedRoundTrip(t *testing.T) {
	db := dbtest.SetupSchema(t)
	require.NoError(t, adminauthz.EnsureRootOperator(context.Background(), db, mk(), "root", []byte("root-secret")))
	cli := dialMgmt(t, db, "root", []byte("root-secret"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cr, err := cli.CreateApplication(ctx, &adminv1.CreateApplicationRequest{
		TenantName: "acme", Domain: "order-system", Name: "订单", AppKey: "AK_x"})
	require.NoError(t, err)
	require.NotEmpty(t, cr.AppSecret)

	var enc []byte
	require.NoError(t, db.QueryRow(
		`SELECT app_secret_enc FROM application WHERE id=$1`, int64(cr.AppId)).Scan(&enc))
	// 密文不等于明文字节：证明没存明文。
	require.NotEqual(t, []byte(cr.AppSecret), enc)
	// 密文可解回明文，且用的就是 masterKey：证明加密往返正确。
	plain, err := crypto.Decrypt(mk(), enc)
	require.NoError(t, err)
	require.Equal(t, []byte(cr.AppSecret), plain)
}

// TestAdminService_DisabledOperatorDenied 证明安全契约：停用 operator 后其后续请求被即时拒绝。
// resolver 每次实时读库，status≠1 即 fail-close，认证拦截器返回 Unauthenticated。
func TestAdminService_DisabledOperatorDenied(t *testing.T) {
	db := dbtest.SetupSchema(t)
	require.NoError(t, adminauthz.EnsureRootOperator(context.Background(), db, mk(), "root", []byte("root-secret")))
	rootCli := dialMgmt(t, db, "root", []byte("root-secret"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	co, err := rootCli.CreateOperator(ctx, &adminv1.CreateOperatorRequest{Principal: "bob"})
	require.NoError(t, err)
	require.NotEmpty(t, co.Secret)

	bobCli := dialMgmt(t, db, "bob", []byte(co.Secret))
	// 停用前：bob 能通过认证但无任何 grant，写请求被鉴权拒为 PermissionDenied（非 Unauthenticated）。
	_, err = bobCli.CreateAdminRole(ctx, &adminv1.CreateAdminRoleRequest{Code: "x", Name: "n"})
	require.Equal(t, codes.PermissionDenied, status.Code(err))

	// root 停用 bob。
	_, err = rootCli.SetOperatorStatus(ctx, &adminv1.SetOperatorStatusRequest{OperatorId: co.OperatorId, Status: 2})
	require.NoError(t, err)

	// 停用后：bob 任意请求被认证拦截器即时拒绝（resolver fail-close）。
	_, err = bobCli.CreateAdminRole(ctx, &adminv1.CreateAdminRoleRequest{Code: "y", Name: "n"})
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}

// TestAdminService_SetStatus_NotFoundOnUnknownId 证明对未知 id 的 set status 行为符合安全策略。
// 租户隔离引入后：SetApplicationStatus 在鉴权层做 tenantOf 查询，未知 app_id → fail-close
// PermissionDenied（不借 NotFound 差异泄露 app 存在性）；SetOperatorStatus 是 system 域，
// 跳过 tenantOf，由 handler 返回 NotFound。
func TestAdminService_SetStatus_NotFoundOnUnknownId(t *testing.T) {
	db := dbtest.SetupSchema(t)
	require.NoError(t, adminauthz.EnsureRootOperator(context.Background(), db, mk(), "root", []byte("root-secret")))
	cli := dialMgmt(t, db, "root", []byte("root-secret"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// SetApplicationStatus：未知 app_id 在鉴权层 tenantOf 失败 → fail-close PermissionDenied。
	// 不返回 NotFound，以防通过状态码差异泄露 app 存在性。
	_, err := cli.SetApplicationStatus(ctx, &adminv1.SetApplicationStatusRequest{AppId: 999999, Status: 2})
	require.Equal(t, codes.PermissionDenied, status.Code(err))

	// SetOperatorStatus 是 system 域（不经 tenantOf），handler 层返回 NotFound。
	_, err = cli.SetOperatorStatus(ctx, &adminv1.SetOperatorStatusRequest{OperatorId: 999999, Status: 2})
	require.Equal(t, codes.NotFound, status.Code(err))
}
