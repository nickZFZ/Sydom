package mgmt_test

import (
	"context"
	"database/sql"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// seedConfiguredApp 在给定 app 内种 1 权限点+1 角色+role_permission 授权+1 条 role 主体 data_policy。
func seedConfiguredApp(t *testing.T, db *sql.DB, appID int64) {
	t.Helper()
	ctx := context.Background()
	permID, err := store.UpsertPermission(ctx, db, appID, "order:read", "order", "read", "app", "查看订单")
	require.NoError(t, err)
	roleID, err := store.InsertRole(ctx, db, appID, "viewer", "查看员")
	require.NoError(t, err)
	err = store.InsertRolePermission(ctx, db, appID, roleID, permID, "allow")
	require.NoError(t, err)
	_, _, err = store.UpsertDataPolicy(ctx, db, appID, cp.DataPolicy{
		SubjectType: "role",
		SubjectID:   "viewer", // 角色数据策略 subject_id=role.code（与 casbin g 绑定/dataperm 主体匹配一致，见 policy/manager.go:346、filter.go:99）
		Resource:    "order",
		Condition:   `{"field":"tenant_id","op":"eq","value":"$user.tenant_id"}`,
		Effect:      "allow",
		Description: "仅限本租户订单",
	}, 1)
	require.NoError(t, err)
}

func TestListAndGetTenantTemplate(t *testing.T) {
	db := dbtest.SetupSchema(t)
	tID, appID := dbtest.SeedAppInTenant(t, db, "t-a", "dom-a", "AK_a")
	srv := accountsSrv(db)
	ctx := context.Background()
	seedConfiguredApp(t, db, appID)
	ref, _ := srv.SaveAppAsTemplate(ctx, &adminv1.SaveAppAsTemplateRequest{AppId: uint64(appID), Name: "标准后台"})

	lst, err := srv.ListTenantTemplates(ctx, &adminv1.ListTenantTemplatesRequest{TenantId: uint64(tID)})
	require.NoError(t, err)
	require.Equal(t, uint32(1), lst.Total)
	require.Equal(t, "标准后台", lst.Templates[0].Name)

	got, err := srv.GetTenantTemplate(ctx, &adminv1.GetTenantTemplateRequest{TenantId: uint64(tID), TemplateId: ref.Id})
	require.NoError(t, err)
	require.NotEmpty(t, got.Permissions)
	require.NotEmpty(t, got.Roles)
	require.NotEmpty(t, got.Roles[0].DataScopes)
	require.Equal(t, "order", got.Roles[0].DataScopes[0].Resource)

	// 跨租户 Get→NotFound（fail-close，不泄露存在性）。
	tB, _ := dbtest.SeedAppInTenant(t, db, "t-b", "dom-b", "AK_b")
	_, err = srv.GetTenantTemplate(ctx, &adminv1.GetTenantTemplateRequest{TenantId: uint64(tB), TemplateId: ref.Id})
	require.Equal(t, codes.NotFound, status.Code(err))
}

func TestSaveAppAsTemplate_CapturesAndDelete(t *testing.T) {
	db := dbtest.SetupSchema(t)
	tID, appID := dbtest.SeedAppInTenant(t, db, "t-a", "dom-a", "AK_a")
	srv := accountsSrv(db)
	ctx := context.Background()
	seedConfiguredApp(t, db, appID)

	ref, err := srv.SaveAppAsTemplate(ctx, &adminv1.SaveAppAsTemplateRequest{
		AppId: uint64(appID), Name: "标准后台", Description: "通用"})
	require.NoError(t, err)
	require.NotZero(t, ref.Id)

	// 重名→AlreadyExists。
	_, err = srv.SaveAppAsTemplate(ctx, &adminv1.SaveAppAsTemplateRequest{AppId: uint64(appID), Name: "标准后台"})
	require.Equal(t, codes.AlreadyExists, status.Code(err))

	// Delete（tenant-scoped）。
	_, err = srv.DeleteTenantTemplate(ctx, &adminv1.DeleteTenantTemplateRequest{TenantId: uint64(tID), TemplateId: ref.Id})
	require.NoError(t, err)
	// 再删→NotFound。
	_, err = srv.DeleteTenantTemplate(ctx, &adminv1.DeleteTenantTemplateRequest{TenantId: uint64(tID), TemplateId: ref.Id})
	require.Equal(t, codes.NotFound, status.Code(err))
}
