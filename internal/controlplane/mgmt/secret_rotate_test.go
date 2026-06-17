package mgmt_test

import (
	"context"
	"testing"
	"time"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/secret"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// MS-3 + MS-4（operator 路径，经真实 HMAC）：重置后旧 secret 认证即 401、新 secret 通过；secret 一次性。
func TestResetOperatorSecret_OldFailsNewWorks(t *testing.T) {
	db := dbtest.SetupSchema(t)
	require.NoError(t, adminauthz.EnsureRootOperator(context.Background(), db, mk(), "root", []byte("root-secret")))
	root := dialMgmt(t, db, "root", []byte("root-secret"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	op, err := root.CreateOperator(ctx, &adminv1.CreateOperatorRequest{Principal: "dave"})
	require.NoError(t, err)
	old := op.Secret

	// 旧 secret 可认证（ListMyTenants 是 scopeSelf，认证通过即放行）。
	daveOld := dialMgmt(t, db, "dave", []byte(old))
	_, err = daveOld.ListMyTenants(ctx, &adminv1.ListMyTenantsRequest{})
	require.NoError(t, err)

	// root 重置 dave 的 secret，返回新明文（一次性）。
	rr, err := root.ResetOperatorSecret(ctx, &adminv1.ResetOperatorSecretRequest{OperatorId: op.OperatorId})
	require.NoError(t, err)
	require.NotEmpty(t, rr.Secret)
	require.NotEqual(t, old, rr.Secret)

	// MS-3：旧 secret 客户端下次认证即 Unauthenticated（resolver 每请求查库，无缓存）。
	_, err = daveOld.ListMyTenants(ctx, &adminv1.ListMyTenantsRequest{})
	require.Equal(t, codes.Unauthenticated, status.Code(err))

	// 新 secret 通过。
	daveNew := dialMgmt(t, db, "dave", []byte(rr.Secret))
	_, err = daveNew.ListMyTenants(ctx, &adminv1.ListMyTenantsRequest{})
	require.NoError(t, err)
}

// scopeSystem：重置未知 operator → NotFound。
func TestResetOperatorSecret_UnknownNotFound(t *testing.T) {
	db := dbtest.SetupSchema(t)
	require.NoError(t, adminauthz.EnsureRootOperator(context.Background(), db, mk(), "root", []byte("root-secret")))
	root := dialMgmt(t, db, "root", []byte("root-secret"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := root.ResetOperatorSecret(ctx, &adminv1.ResetOperatorSecretRequest{OperatorId: 999999})
	require.Equal(t, codes.NotFound, status.Code(err))
}

// MS-3 + MS-4（app 路径，经 sidecar 同源 secret.Resolver）：硬切换后库里密文换新，
// resolver 解出新明文；ApplicationSummary 结构上不含 secret。
func TestRotateApplicationSecret_HardCutover(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	require.NoError(t, adminauthz.EnsureRootOperator(context.Background(), db, mk(), "root", []byte("root-secret")))
	root := dialMgmt(t, db, "root", []byte("root-secret"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 取 SeedApp 的 app_key（resolver 按 app_key 解析，与 sidecar 同源）。
	var appKey string
	var oldEnc []byte
	require.NoError(t, db.QueryRow(`SELECT app_key, app_secret_enc FROM application WHERE id=$1`, appID).Scan(&appKey, &oldEnc))

	rr, err := root.RotateApplicationSecret(ctx, &adminv1.RotateApplicationSecretRequest{AppId: uint64(appID)})
	require.NoError(t, err)
	require.NotEmpty(t, rr.AppSecret)

	// 库里密文已换（硬切换）。
	var newEnc []byte
	require.NoError(t, db.QueryRow(`SELECT app_secret_enc FROM application WHERE id=$1`, appID).Scan(&newEnc))
	require.NotEqual(t, oldEnc, newEnc)

	// sidecar 同源 resolver 解出的就是新明文（MS-3：新 secret 即时生效）。
	res, err := secret.NewResolver(db, mk())
	require.NoError(t, err)
	plain, err := res.ResolveSecret(ctx, appKey)
	require.NoError(t, err)
	require.Equal(t, []byte(rr.AppSecret), plain)
}

// scopeApp fail-close：轮换未知 app → PermissionDenied（不泄露存在性，非 NotFound）。
func TestRotateApplicationSecret_UnknownApp_PermissionDenied(t *testing.T) {
	db := dbtest.SetupSchema(t)
	require.NoError(t, adminauthz.EnsureRootOperator(context.Background(), db, mk(), "root", []byte("root-secret")))
	root := dialMgmt(t, db, "root", []byte("root-secret"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := root.RotateApplicationSecret(ctx, &adminv1.RotateApplicationSecretRequest{AppId: 888888})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

// isWrite=false：停用的 app 仍可轮换 secret（不受 status 写拦截）。
func TestRotateApplicationSecret_DisabledAppStillRotates(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	require.NoError(t, adminauthz.EnsureRootOperator(context.Background(), db, mk(), "root", []byte("root-secret")))
	root := dialMgmt(t, db, "root", []byte("root-secret"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := root.SetApplicationStatus(ctx, &adminv1.SetApplicationStatusRequest{AppId: uint64(appID), Status: 2})
	require.NoError(t, err)
	rr, err := root.RotateApplicationSecret(ctx, &adminv1.RotateApplicationSecretRequest{AppId: uint64(appID)})
	require.NoError(t, err)
	require.NotEmpty(t, rr.AppSecret)
}
