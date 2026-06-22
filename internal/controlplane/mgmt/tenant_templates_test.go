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

// seedAppInTenant 在既有 tenantID 下新建第二个 app（dbtest 无同租户多 app 助手）。
func seedAppInTenant(t *testing.T, db *sql.DB, tenantID int64, domain, appKey string) int64 {
	t.Helper()
	var appID int64
	require.NoError(t, db.QueryRow(
		`INSERT INTO application (tenant_id, domain, name, app_key, app_secret_enc)
		 VALUES ($1, $2, $3, $4, '\xab'::bytea) RETURNING id`,
		tenantID, domain, "second-app", appKey).Scan(&appID))
	return appID
}

func TestApplyTenantTemplate_ReusesEngine(t *testing.T) {
	db := dbtest.SetupSchema(t)
	tID, srcApp := dbtest.SeedAppInTenant(t, db, "t-a", "dom-a", "AK_a")
	dstApp := seedAppInTenant(t, db, tID, "dom-a2", "AK_a2") // 同租户第二 app
	srv := accountsSrv(db)
	ctx := context.Background()
	seedConfiguredApp(t, db, srcApp)

	ref, err := srv.SaveAppAsTemplate(ctx, &adminv1.SaveAppAsTemplateRequest{AppId: uint64(srcApp), Name: "标准后台"})
	require.NoError(t, err)

	// apply 到同租户目标 app：复用引擎，角色+数据范围随模板种入。
	resp, err := srv.ApplyTenantTemplate(ctx, &adminv1.ApplyTenantTemplateRequest{AppId: uint64(dstApp), TemplateId: ref.Id})
	require.NoError(t, err)
	require.GreaterOrEqual(t, resp.RolesCreated, uint32(1))
	require.GreaterOrEqual(t, resp.DataScopesCreated, uint32(1))

	// re-apply 幂等：角色已存在→跳过。
	resp2, err := srv.ApplyTenantTemplate(ctx, &adminv1.ApplyTenantTemplateRequest{AppId: uint64(dstApp), TemplateId: ref.Id})
	require.NoError(t, err)
	require.Equal(t, uint32(0), resp2.RolesCreated)
	require.Equal(t, uint32(0), resp2.DataScopesCreated)

	// 跨租户 fail-close：模板属 t-a，目标 app 属 t-b → store.GetTenantTemplate 自然 NotFound。
	_, appB := dbtest.SeedAppInTenant(t, db, "t-b", "dom-b", "AK_b")
	_, err = srv.ApplyTenantTemplate(ctx, &adminv1.ApplyTenantTemplateRequest{AppId: uint64(appB), TemplateId: ref.Id})
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
