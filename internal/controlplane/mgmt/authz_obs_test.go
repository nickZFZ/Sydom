package mgmt_test

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/nickZFZ/Sydom/internal/obs"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Enforce 内部错误（关 DB → ReadPolicyVersion 失败）须【仍 fail-close 为 PermissionDenied】（行为不变）
// 且记一条 warn（新日志有齿）。用 scopeTenant 规则(ListApplications)——域由 tenant_id 纯字符串算出，
// Enforce 前不碰 DB，故错误确由 Enforce 内部触发。
func TestAuthorizeRule_EnforceInternalError_FailsClosedAndLogs(t *testing.T) {
	db := dbtest.SetupSchema(t)
	enf, err := adminauthz.NewEnforcer(db)
	require.NoError(t, err)
	require.NoError(t, db.Close()) // 关 DB → Enforce 内 ReadPolicyVersion 必失败

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil)) // 默认 Info 级，Warn 会输出
	ctx := obs.With(context.Background(), logger)

	msg := &adminv1.ListApplicationsRequest{TenantId: 1}
	_, err = mgmt.AuthorizeRule(ctx, enf, "/sydom.admin.v1.AdminService/ListApplications", "someprincipal", msg)
	require.Equal(t, codes.PermissionDenied, status.Code(err), "内部错误须仍 fail-close 为 PermissionDenied（行为不变）")
	require.Contains(t, buf.String(), "authz enforce internal error", "内部错误须记 warn（新日志有齿）")
}
