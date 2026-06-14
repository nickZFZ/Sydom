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

func TestCreateBusinessRole_EmptyNameRejected(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	srv := accountsSrv(db)
	_, err := srv.CreateBusinessRole(context.Background(),
		&adminv1.CreateBusinessRoleRequest{AppId: uint64(appID), Name: ""})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestCreateBusinessRole_OwnTenantCreatesRole(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	srv := accountsSrv(db)
	resp, err := srv.CreateBusinessRole(context.Background(),
		&adminv1.CreateBusinessRoleRequest{AppId: uint64(appID), Name: "销售经理"})
	require.NoError(t, err)
	require.NotZero(t, resp.RoleId)
}

func TestCreateBusinessRole_CrossTenant403(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	mk := bytes.Repeat([]byte{0x11}, crypto.KeySize)
	tA, _ := dbtest.SeedAppInTenant(t, db, "tenant-a", "domain-a", "AK_a")
	_, appB := dbtest.SeedAppInTenant(t, db, "tenant-b", "domain-b", "AK_b")
	require.NoError(t, adminauthz.EnsureTenantAdmin(ctx, db, mk, tA, "alice", []byte("sa")))
	enf, err := adminauthz.NewEnforcer(db)
	require.NoError(t, err)
	const method = "/sydom.admin.v1.AdminService/CreateBusinessRole"
	req := &adminv1.CreateBusinessRoleRequest{AppId: uint64(appB), Name: "x"}
	_, err = mgmt.AuthorizeRule(ctx, enf, method, "alice", req)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}
