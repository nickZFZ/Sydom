package mgmt_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/controlplane/outbox"
	"github.com/nickZFZ/Sydom/internal/controlplane/policy"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func newGetAppSrv(db *sql.DB) *mgmt.AdminServer {
	return mgmt.NewAdminServer(db, policy.NewPolicyManager(db, outbox.NewSink()), mk())
}

func TestAdminServer_GetApplication(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	srv := newGetAppSrv(db)
	resp, err := srv.GetApplication(context.Background(), &adminv1.GetApplicationRequest{AppId: uint64(appID)})
	require.NoError(t, err)
	// 全部 6 个 Scan 字段精确断言（钉 Scan 参数顺序，防列错位回归）。
	require.Equal(t, uint64(appID), resp.Application.AppId)
	require.Equal(t, dbtest.SeedAppKey, resp.Application.AppKey) // "AK_order" —— 有齿断言
	require.Equal(t, dbtest.SeedDomain, resp.Application.Domain) // "order-system"
	require.Equal(t, "订单系统", resp.Application.Name)              // SeedApp 播种名
	require.Equal(t, uint32(1), resp.Application.Status)         // application.status DEFAULT 1（000002 建表）
	require.Equal(t, uint64(0), resp.Application.CurrentVersion) // application.current_version DEFAULT 0（000002 建表）
	// SD-1：response 绝不含 secret（ApplicationSummary 类型层无 secret 字段；双保险扫序列化）。
	require.NotContains(t, resp.String(), "secret")
}

// TestAdminServer_GetApplication_NotFound 验证 handler 契约本身：直调裸 server
// （绕拦截器）对不存在 app_id 返回 NotFound。与下面的全栈 fail-close 用例互补。
func TestAdminServer_GetApplication_NotFound(t *testing.T) {
	db := dbtest.SetupSchema(t)
	srv := newGetAppSrv(db)
	_, err := srv.GetApplication(context.Background(), &adminv1.GetApplicationRequest{AppId: 999999})
	require.Equal(t, codes.NotFound, status.Code(err))
}

// TestAdminServer_GetApplication_UnknownAppFailClosed 锁住生产路径的反泄露安全不变量：
// 经 gRPC 拦截器全栈时，scopeApp 对不存在 app_id 在鉴权层 fail-close 成 PermissionDenied，
// handler 的 NotFound 分支实际不可达。与 SetApplicationStatus 一致：不借状态码差异泄露
// app 存在性（镜像 TestAdminService_SetStatus_NotFoundOnUnknownId）。
func TestAdminServer_GetApplication_UnknownAppFailClosed(t *testing.T) {
	db := dbtest.SetupSchema(t)
	require.NoError(t, adminauthz.EnsureRootOperator(context.Background(), db, mk(), "root", []byte("root-secret")))
	cli := dialMgmt(t, db, "root", []byte("root-secret"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := cli.GetApplication(ctx, &adminv1.GetApplicationRequest{AppId: 999999})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}
