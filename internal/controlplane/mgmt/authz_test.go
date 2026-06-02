package mgmt_test

import (
	"context"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/auth"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestAuthzInterceptor_EnforcesPerAppDomain(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	ctx := context.Background()

	opID, _ := adminauthz.InsertOperator(ctx, db, "alice", []byte("x"))
	roleID, _ := adminauthz.InsertRole(ctx, db, "r", "n")
	domain := mgmt.DomainOfAppID(appID)
	require.NoError(t, adminauthz.InsertRoleGrant(ctx, db, roleID, domain, "grant", "create"))
	require.NoError(t, adminauthz.InsertSubjectRole(ctx, db, opID, roleID, domain))

	enf, err := adminauthz.NewEnforcer(db)
	require.NoError(t, err)
	interceptor := mgmt.AuthzUnaryInterceptor(enf)

	authedCtx := auth.WithAppID(ctx, "alice")
	called := false
	_, err = interceptor(authedCtx,
		&adminv1.GrantPermissionRequest{AppId: uint64(appID), RoleId: roleID, PermissionId: 1, Eft: "allow"},
		&grpc.UnaryServerInfo{FullMethod: "/sydom.admin.v1.AdminService/GrantPermission"},
		func(ctx context.Context, req any) (any, error) { called = true; return nil, nil })
	require.NoError(t, err)
	require.True(t, called)

	_, err = interceptor(authedCtx,
		&adminv1.GrantPermissionRequest{AppId: 999, RoleId: roleID, PermissionId: 1, Eft: "allow"},
		&grpc.UnaryServerInfo{FullMethod: "/sydom.admin.v1.AdminService/GrantPermission"},
		func(ctx context.Context, req any) (any, error) { return nil, nil })
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

func TestAuthzInterceptor_SystemDomainRPC(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()

	opID, _ := adminauthz.InsertOperator(ctx, db, "root", []byte("x"))
	roleID, _ := adminauthz.InsertRole(ctx, db, "admin", "n")
	require.NoError(t, adminauthz.InsertRoleGrant(ctx, db, roleID, "*", "admin", "create"))
	require.NoError(t, adminauthz.InsertSubjectRole(ctx, db, opID, roleID, "*"))

	enf, err := adminauthz.NewEnforcer(db)
	require.NoError(t, err)
	interceptor := mgmt.AuthzUnaryInterceptor(enf)

	const method = "/sydom.admin.v1.AdminService/CreateOperator"

	// 有 * 域 admin/create grant 的 root：放行；CreateOperatorRequest 无 app_id，验证 system 路径不取 app_id。
	rootCtx := auth.WithAppID(ctx, "root")
	called := false
	_, err = interceptor(rootCtx,
		&adminv1.CreateOperatorRequest{Principal: "newop"},
		&grpc.UnaryServerInfo{FullMethod: method},
		func(ctx context.Context, req any) (any, error) { called = true; return nil, nil })
	require.NoError(t, err)
	require.True(t, called)

	// 无任何 grant 的 operator：拒绝。
	called = false
	_, err = interceptor(auth.WithAppID(ctx, "nobody"),
		&adminv1.CreateOperatorRequest{Principal: "newop"},
		&grpc.UnaryServerInfo{FullMethod: method},
		func(ctx context.Context, req any) (any, error) { called = true; return nil, nil })
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.False(t, called)
}

func TestAuthzInterceptor_MissingIdentityUnauthenticated(t *testing.T) {
	db := dbtest.SetupSchema(t)
	enf, err := adminauthz.NewEnforcer(db)
	require.NoError(t, err)
	interceptor := mgmt.AuthzUnaryInterceptor(enf)

	// 裸 ctx（未经 auth.WithAppID）：在取 principal 时即返回 Unauthenticated，不触达 enforcer。
	called := false
	_, err = interceptor(context.Background(),
		&adminv1.GrantPermissionRequest{AppId: 1},
		&grpc.UnaryServerInfo{FullMethod: "/sydom.admin.v1.AdminService/GrantPermission"},
		func(ctx context.Context, req any) (any, error) { called = true; return nil, nil })
	require.Equal(t, codes.Unauthenticated, status.Code(err))
	require.False(t, called)
}

func TestAuthzInterceptor_UnknownMethodDenied(t *testing.T) {
	db := dbtest.SetupSchema(t)
	enf, err := adminauthz.NewEnforcer(db)
	require.NoError(t, err)
	interceptor := mgmt.AuthzUnaryInterceptor(enf)

	// 合法身份但 FullMethod 不在 ruleTable：拒绝。
	called := false
	_, err = interceptor(auth.WithAppID(context.Background(), "alice"),
		&adminv1.GrantPermissionRequest{AppId: 1},
		&grpc.UnaryServerInfo{FullMethod: "/sydom.admin.v1.AdminService/Bogus"},
		func(ctx context.Context, req any) (any, error) { called = true; return nil, nil })
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.False(t, called)
}

func TestStatusInterceptor_BlocksWriteOnDisabledApp(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	_, err := db.Exec(`UPDATE application SET status=2 WHERE id=$1`, appID)
	require.NoError(t, err)

	interceptor := mgmt.StatusWriteUnaryInterceptor(db)
	_, err = interceptor(context.Background(),
		&adminv1.GrantPermissionRequest{AppId: uint64(appID)},
		&grpc.UnaryServerInfo{FullMethod: "/sydom.admin.v1.AdminService/GrantPermission"},
		func(ctx context.Context, req any) (any, error) { return nil, nil })
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}
