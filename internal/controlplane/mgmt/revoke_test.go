package mgmt_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// domainStr 把 app_id 转成 casbin domain 字符串（与 mgmt.DomainOfAppID 一致）。
func domainStr(appID int64) string { return strconv.FormatInt(appID, 10) }

// MS-1 + MS-2：撤管理授权后，被撤特权经真实 Enforce 即刻消失；再撤 → NotFound。
func TestRevokeAdminGrant_PrivilegeGoneAndStrictNotFound(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	require.NoError(t, adminauthz.EnsureRootOperator(context.Background(), db, mk(), "root", []byte("root-secret")))
	root := dialMgmt(t, db, "root", []byte("root-secret"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	domain := domainStr(appID)
	op, err := root.CreateOperator(ctx, &adminv1.CreateOperatorRequest{Principal: "alice"})
	require.NoError(t, err)
	role, err := root.CreateAdminRole(ctx, &adminv1.CreateAdminRoleRequest{Code: "app-admin", Name: "n"})
	require.NoError(t, err)
	_, err = root.GrantAdminRole(ctx, &adminv1.GrantAdminRoleRequest{
		RoleId: role.RoleId, Domain: domain, Resource: "role", Action: "create"})
	require.NoError(t, err)
	_, err = root.BindOperatorRole(ctx, &adminv1.BindOperatorRoleRequest{
		OperatorId: op.OperatorId, RoleId: role.RoleId, Domain: domain})
	require.NoError(t, err)

	// alice 现在能在该 app 建角色（特权生效）。
	alice := dialMgmt(t, db, "alice", []byte(op.Secret))
	_, err = alice.CreateRole(ctx, &adminv1.CreateRoleRequest{AppId: uint64(appID), Code: "c1", Name: "n"})
	require.NoError(t, err)

	// root 撤销该授权。
	_, err = root.RevokeAdminGrant(ctx, &adminv1.RevokeAdminGrantRequest{
		RoleId: role.RoleId, Domain: domain, Resource: "role", Action: "create"})
	require.NoError(t, err)

	// MS-1：bump 已触发 enforcer 重载，alice 同一特权下次即被拒。
	_, err = alice.CreateRole(ctx, &adminv1.CreateRoleRequest{AppId: uint64(appID), Code: "c2", Name: "n"})
	require.Equal(t, codes.PermissionDenied, status.Code(err))

	// MS-2：再撤已不存在的授权 → NotFound（严格，不幂等）。
	_, err = root.RevokeAdminGrant(ctx, &adminv1.RevokeAdminGrantRequest{
		RoleId: role.RoleId, Domain: domain, Resource: "role", Action: "create"})
	require.Equal(t, codes.NotFound, status.Code(err))
}

// MS-1 + MS-2：解绑操作员角色后特权即刻消失；再解绑 → NotFound。
func TestUnbindOperatorRole_PrivilegeGoneAndStrictNotFound(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	require.NoError(t, adminauthz.EnsureRootOperator(context.Background(), db, mk(), "root", []byte("root-secret")))
	root := dialMgmt(t, db, "root", []byte("root-secret"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	domain := domainStr(appID)

	op, err := root.CreateOperator(ctx, &adminv1.CreateOperatorRequest{Principal: "bob"})
	require.NoError(t, err)
	role, err := root.CreateAdminRole(ctx, &adminv1.CreateAdminRoleRequest{Code: "app-admin2", Name: "n"})
	require.NoError(t, err)
	_, err = root.GrantAdminRole(ctx, &adminv1.GrantAdminRoleRequest{
		RoleId: role.RoleId, Domain: domain, Resource: "role", Action: "create"})
	require.NoError(t, err)
	_, err = root.BindOperatorRole(ctx, &adminv1.BindOperatorRoleRequest{
		OperatorId: op.OperatorId, RoleId: role.RoleId, Domain: domain})
	require.NoError(t, err)

	bob := dialMgmt(t, db, "bob", []byte(op.Secret))
	_, err = bob.CreateRole(ctx, &adminv1.CreateRoleRequest{AppId: uint64(appID), Code: "x1", Name: "n"})
	require.NoError(t, err)

	_, err = root.UnbindOperatorRole(ctx, &adminv1.UnbindOperatorRoleRequest{
		OperatorId: op.OperatorId, RoleId: role.RoleId, Domain: domain})
	require.NoError(t, err)

	_, err = bob.CreateRole(ctx, &adminv1.CreateRoleRequest{AppId: uint64(appID), Code: "x2", Name: "n"})
	require.Equal(t, codes.PermissionDenied, status.Code(err))

	_, err = root.UnbindOperatorRole(ctx, &adminv1.UnbindOperatorRoleRequest{
		OperatorId: op.OperatorId, RoleId: role.RoleId, Domain: domain})
	require.Equal(t, codes.NotFound, status.Code(err))
}

// MS-6：非超管操作员无 system 域授权 → 撤权被拒 403（PermissionDenied）。
func TestRevoke_NonSuperAdminDenied(t *testing.T) {
	db := dbtest.SetupSchema(t)
	require.NoError(t, adminauthz.EnsureRootOperator(context.Background(), db, mk(), "root", []byte("root-secret")))
	root := dialMgmt(t, db, "root", []byte("root-secret"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	op, err := root.CreateOperator(ctx, &adminv1.CreateOperatorRequest{Principal: "mallory"})
	require.NoError(t, err)
	mallory := dialMgmt(t, db, "mallory", []byte(op.Secret))
	_, err = mallory.RevokeAdminGrant(ctx, &adminv1.RevokeAdminGrantRequest{
		RoleId: 1, Domain: "*", Resource: "admin", Action: "update"})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}
