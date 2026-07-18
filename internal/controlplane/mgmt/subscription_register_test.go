package mgmt_test

import (
	"context"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// RegisterTenant 须在同事务内建一条 active 订阅（无订阅孤儿租户）。
func TestRegisterTenant_CreatesSubscription(t *testing.T) {
	db := dbtest.SetupSchema(t)
	srv := accountsSrv(db)
	resp, err := srv.RegisterTenant(context.Background(), &adminv1.RegisterTenantRequest{
		TenantName: "sub-tenant", OwnerPrincipal: "owner@sub",
	})
	require.NoError(t, err)

	var status string
	require.NoError(t, db.QueryRow(
		`SELECT status FROM subscription WHERE tenant_id=$1`, int64(resp.TenantId)).Scan(&status))
	require.Equal(t, "active", status)
}
