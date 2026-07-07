package mgmt_test

import (
	"context"
	"database/sql"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/controlplane/outbox"
	"github.com/nickZFZ/Sydom/internal/controlplane/policy"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func newGetAppSrv(t *testing.T, db *sql.DB) *mgmt.AdminServer {
	return mgmt.NewAdminServer(db, policy.NewPolicyManager(db, outbox.NewSink()), mk())
}

func TestAdminServer_GetApplication(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	srv := newGetAppSrv(t, db)
	resp, err := srv.GetApplication(context.Background(), &adminv1.GetApplicationRequest{AppId: uint64(appID)})
	require.NoError(t, err)
	require.Equal(t, uint64(appID), resp.Application.AppId)
	require.Equal(t, dbtest.SeedAppKey, resp.Application.AppKey) // "AK_order" —— 有齿断言
	require.Equal(t, dbtest.SeedDomain, resp.Application.Domain) // "order-system"
	// SD-1：response 绝不含 secret（ApplicationSummary 类型层无 secret 字段；双保险扫序列化）。
	require.NotContains(t, resp.String(), "secret")
}

func TestAdminServer_GetApplication_NotFound(t *testing.T) {
	db := dbtest.SetupSchema(t)
	srv := newGetAppSrv(t, db)
	_, err := srv.GetApplication(context.Background(), &adminv1.GetApplicationRequest{AppId: 999999})
	require.Equal(t, codes.NotFound, status.Code(err))
}
