package mgmt_test

import (
	"bytes"
	"context"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/crypto"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestChangeTenantPlan_SuperAdminChanges(t *testing.T) {
	db := dbtest.SetupSchema(t)
	dbtest.SeedApp(t, db)
	var tenantID, proID int64
	require.NoError(t, db.QueryRow(`SELECT id FROM tenant WHERE name='acme'`).Scan(&tenantID))
	require.NoError(t, db.QueryRow(`SELECT id FROM plan WHERE name='pro'`).Scan(&proID))

	srv := accountsSrv(db)
	ctx := cp.WithOperator(context.Background(), "root") // 直调 handler，授权由拦截器（此处跳过）；operator 供审计
	resp, err := srv.ChangeTenantPlan(ctx, &adminv1.ChangeTenantPlanRequest{
		TenantId: uint64(tenantID), PlanId: uint64(proID),
	})
	require.NoError(t, err)
	require.Equal(t, uint64(proID), resp.PlanId)
	require.Equal(t, "active", resp.Status)

	var got int64
	require.NoError(t, db.QueryRow(`SELECT plan_id FROM tenant WHERE id=$1`, tenantID).Scan(&got))
	require.Equal(t, proID, got)
}

func TestChangeTenantPlan_UnknownPlan_FailedPrecondition(t *testing.T) {
	db := dbtest.SetupSchema(t)
	dbtest.SeedApp(t, db)
	var tenantID int64
	require.NoError(t, db.QueryRow(`SELECT id FROM tenant WHERE name='acme'`).Scan(&tenantID))
	srv := accountsSrv(db)
	ctx := cp.WithOperator(context.Background(), "root")
	_, err := srv.ChangeTenantPlan(ctx, &adminv1.ChangeTenantPlanRequest{
		TenantId: uint64(tenantID), PlanId: 999999,
	})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}

func TestChangeTenantPlan_UnknownTenant_NotFound(t *testing.T) {
	db := dbtest.SetupSchema(t)
	srv := accountsSrv(db)
	var proID int64
	require.NoError(t, db.QueryRow(`SELECT id FROM plan WHERE name='pro'`).Scan(&proID))
	ctx := cp.WithOperator(context.Background(), "root")
	_, err := srv.ChangeTenantPlan(ctx, &adminv1.ChangeTenantPlanRequest{
		TenantId: 999999, PlanId: uint64(proID),
	})
	require.Equal(t, codes.NotFound, status.Code(err))
}

// 授权门：租户管理员（非超管）经 AuthorizeRule 应 PermissionDenied（scopeSystem）。
func TestChangeTenantPlan_NonSuperAdmin_Denied(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	mk := bytes.Repeat([]byte{0x11}, crypto.KeySize)
	tA, _ := dbtest.SeedAppInTenant(t, db, "tenant-a", "domain-a", "AK_a")
	require.NoError(t, adminauthz.EnsureTenantAdmin(ctx, db, mk, tA, "alice", []byte("sa")))
	enf, err := adminauthz.NewEnforcer(db)
	require.NoError(t, err)
	const method = "/sydom.admin.v1.AdminService/ChangeTenantPlan"
	req := &adminv1.ChangeTenantPlanRequest{TenantId: uint64(tA), PlanId: 2}
	_, err = mgmt.AuthorizeRule(ctx, enf, method, "alice", req)
	require.Equal(t, codes.PermissionDenied, status.Code(err), "租户管理员非超管，改套餐须拒")
}
