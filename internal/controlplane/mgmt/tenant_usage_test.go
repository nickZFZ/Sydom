package mgmt_test

import (
	"context"
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

func TestGetTenantUsage(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	var tenantID int64
	require.NoError(t, db.QueryRow(`SELECT tenant_id FROM application WHERE id=$1`, appID).Scan(&tenantID))
	s := mgmt.NewAdminServer(db, policy.NewPolicyManager(db, outbox.NewSink()), mk())
	ctx := cp.WithOperator(context.Background(), "root@sydom")

	resp, err := s.GetTenantUsage(ctx, &adminv1.GetTenantUsageRequest{TenantId: uint64(tenantID)})
	require.NoError(t, err)
	require.Equal(t, "free", resp.PlanName)
	require.Equal(t, uint32(1), resp.Applications.Used)
	require.Equal(t, uint32(3), resp.Applications.Limit)
	require.Equal(t, uint32(0), resp.Members.Used, "SeedApp 租户无 membership")
	require.Equal(t, uint32(3), resp.Members.Limit, "free 成员限 3")

	// 建第二个应用 → used 递增
	_, err = s.CreateApplication(ctx, &adminv1.CreateApplicationRequest{TenantId: uint64(tenantID), Domain: "d2", Name: "n", AppKey: "ak2"})
	require.NoError(t, err)
	resp, err = s.GetTenantUsage(ctx, &adminv1.GetTenantUsageRequest{TenantId: uint64(tenantID)})
	require.NoError(t, err)
	require.Equal(t, uint32(2), resp.Applications.Used)

	// 未知租户 NotFound
	_, err = s.GetTenantUsage(ctx, &adminv1.GetTenantUsageRequest{TenantId: 999999})
	require.Equal(t, codes.NotFound, status.Code(err))
}
