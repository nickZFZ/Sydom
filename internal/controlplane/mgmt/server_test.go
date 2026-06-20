package mgmt_test

import (
	"context"
	"database/sql"
	"log/slog"
	"net"
	"testing"
	"time"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/auth"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/controlplane/outbox"
	"github.com/nickZFZ/Sydom/internal/controlplane/policy"
	"github.com/nickZFZ/Sydom/internal/crypto"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func mk() []byte {
	k := make([]byte, crypto.KeySize)
	for i := range k {
		k[i] = 0x2a
	}
	return k
}

// dialMgmt 起带三拦截器的 AdminService，用给定 principal/secret 拨号返回客户端。
func dialMgmt(t *testing.T, db *sql.DB, principal string, secret []byte) adminv1.AdminServiceClient {
	t.Helper()
	resolver, err := adminauthz.NewOperatorResolver(db, mk())
	require.NoError(t, err)
	enf, err := adminauthz.NewEnforcer(db)
	require.NoError(t, err)
	mgr := policy.NewPolicyManager(db, outbox.NewSink())
	g := mgmt.NewGRPCServer(mgmt.NewAdminServer(db, mgr, mk()), resolver, enf, db, slog.Default())
	lis := bufconn.Listen(1 << 20)
	go func() { _ = g.Serve(lis) }()
	t.Cleanup(g.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(auth.NewPerRPCCredentials(principal, secret, false)),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return adminv1.NewAdminServiceClient(conn)
}

func TestAdminService_CreateRoleAndGrant_WritesVersionAndOutbox(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	require.NoError(t, adminauthz.EnsureRootOperator(context.Background(), db, mk(), "root", []byte("root-secret")))
	cli := dialMgmt(t, db, "root", []byte("root-secret"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cr, err := cli.CreateRole(ctx, &adminv1.CreateRoleRequest{AppId: uint64(appID), Code: "manager", Name: "经理"})
	require.NoError(t, err)
	up, err := cli.UpsertPermission(ctx, &adminv1.UpsertPermissionRequest{
		AppId: uint64(appID), Code: "order.read", Resource: "order", Action: "read", Ptype: "p", Name: "读订单"})
	require.NoError(t, err)
	w, err := cli.GrantPermission(ctx, &adminv1.GrantPermissionRequest{
		AppId: uint64(appID), RoleId: cr.RoleId, PermissionId: up.PermissionId, Eft: "allow"})
	require.NoError(t, err)
	require.True(t, w.Changed)
	require.Greater(t, w.Version, uint64(0))

	var n int
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM policy_outbox WHERE app_id=$1 AND version=$2`, appID, w.Version).Scan(&n))
	require.Equal(t, 1, n)
}

func TestAdminService_UnauthenticatedRejected(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	require.NoError(t, adminauthz.EnsureRootOperator(context.Background(), db, mk(), "root", []byte("root-secret")))
	bad := dialMgmt(t, db, "root", []byte("WRONG"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := bad.CreateRole(ctx, &adminv1.CreateRoleRequest{AppId: uint64(appID), Code: "x", Name: "y"})
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}
