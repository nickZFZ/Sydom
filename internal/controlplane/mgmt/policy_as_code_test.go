package mgmt_test

import (
	"context"
	"strings"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/controlplane/outbox"
	"github.com/nickZFZ/Sydom/internal/controlplane/policy"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// validYAML 是一份最小的合法策略文档（1 权限点 + 1 引用它的角色）。
const validYAML = `apiVersion: sydom.policy/v1
permissions:
  - code: doc.read
    resource: doc
    action: read
    type: api
    name: 读文档
roles:
  - key: reader
    name: 阅读者
    permission_codes: [doc.read]
`

// TestPolicyAsCode_ExportNonEmptyAndNoSecret 验证 export 返回非空内容且不含 app 密钥（PC-2）。
func TestPolicyAsCode_ExportNonEmptyAndNoSecret(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	srv := mgmt.NewAdminServer(db, policy.NewPolicyManager(db, outbox.NewSink()), mk())

	const accessKey = "AK_export_test"
	_, appID := dbtest.SeedAppInTenant(t, db, "tenant-export", "export-domain", accessKey)

	// 给 app 注入 1 个 iac 权限点，让 export 内容非空。
	_, err := store.UpsertPermissionWithSource(ctx, db, appID, "doc.read", "doc", "read", "api", "读文档", "", "iac")
	require.NoError(t, err)

	resp, err := srv.ExportAppPolicy(ctx, &adminv1.ExportAppPolicyRequest{
		AppId:  uint64(appID),
		Format: "yaml",
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.Content, "export 内容不应为空")
	require.False(t, strings.Contains(resp.Content, accessKey),
		"export 内容不应包含 app access key（PC-2 脱敏）")
}

// TestPolicyAsCode_ImportDryRun 验证 dry_run=true 返回 diff 但不改库（PC-4）。
func TestPolicyAsCode_ImportDryRun(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	srv := mgmt.NewAdminServer(db, policy.NewPolicyManager(db, outbox.NewSink()), mk())

	_, appID := dbtest.SeedAppInTenant(t, db, "tenant-dryrun", "dryrun-domain", "AK_dryrun")

	resp, err := srv.ImportAppPolicy(ctx, &adminv1.ImportAppPolicyRequest{
		AppId:   uint64(appID),
		Content: validYAML,
		DryRun:  true,
	})
	require.NoError(t, err)
	require.False(t, resp.Applied, "dry-run 时 Applied 必须为 false")
	require.Greater(t, len(resp.Diff), 0, "dry-run 应返回非空 diff")
	require.Greater(t, resp.Creates, int32(0), "应含 create 项（1 权限点 + 1 角色）")

	// 验证库内没有落入任何权限点或角色。
	var permCnt, roleCnt int
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM permission WHERE app_id=$1`, appID).Scan(&permCnt))
	require.Equal(t, 0, permCnt, "dry-run 后权限点不应落库")
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM role WHERE app_id=$1`, appID).Scan(&roleCnt))
	require.Equal(t, 0, roleCnt, "dry-run 后角色不应落库")
}

// TestPolicyAsCode_ImportApply 验证 apply（dry_run=false）改库（PC-8 往返）。
func TestPolicyAsCode_ImportApply(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	srv := mgmt.NewAdminServer(db, policy.NewPolicyManager(db, outbox.NewSink()), mk())

	_, appID := dbtest.SeedAppInTenant(t, db, "tenant-apply", "apply-domain", "AK_apply")

	resp, err := srv.ImportAppPolicy(ctx, &adminv1.ImportAppPolicyRequest{
		AppId:   uint64(appID),
		Content: validYAML,
		DryRun:  false,
	})
	require.NoError(t, err)
	require.True(t, resp.Applied, "apply 时 Applied 必须为 true")
	require.Greater(t, resp.Version, int64(0), "apply 后版本应 bump")

	// 验证权限点和角色已落库。
	var permCnt, roleCnt int
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM permission WHERE app_id=$1 AND code=$2`, appID, "doc.read").Scan(&permCnt))
	require.Equal(t, 1, permCnt, "apply 后权限点应在库中")
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM role WHERE app_id=$1 AND code=$2`, appID, "iac:reader").Scan(&roleCnt))
	require.Equal(t, 1, roleCnt, "apply 后角色应在库中（code=iac:reader）")

	// 再次 export 确认往返幂等（无报错、内容非空）。
	exportResp, err := srv.ExportAppPolicy(ctx, &adminv1.ExportAppPolicyRequest{
		AppId:  uint64(appID),
		Format: "yaml",
	})
	require.NoError(t, err)
	require.NotEmpty(t, exportResp.Content)
}

// TestPolicyAsCode_CrossTenantDenied 验证跨租户鉴权 fail-close（TenantDomainOf fail-close）。
func TestPolicyAsCode_CrossTenantDenied(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()

	tA, appA := dbtest.SeedAppInTenant(t, db, "tenant-ct-a", "ct-a-domain", "AK_ct_a")
	tB, _ := dbtest.SeedAppInTenant(t, db, "tenant-ct-b", "ct-b-domain", "AK_ct_b")

	require.NoError(t, adminauthz.EnsureTenantAdmin(ctx, db, mk(), tA, "alice-ct", []byte("sa")))
	require.NoError(t, adminauthz.EnsureTenantAdmin(ctx, db, mk(), tB, "bob-ct", []byte("sb")))

	enf, err := adminauthz.NewEnforcer(db)
	require.NoError(t, err)

	const (
		exportMethod = "/sydom.admin.v1.AdminService/ExportAppPolicy"
		importMethod = "/sydom.admin.v1.AdminService/ImportAppPolicy"
	)

	// bob-ct 是租户 B 管理员，访问租户 A 的 app → PermissionDenied。
	_, errExport := mgmt.AuthorizeRule(ctx, enf, exportMethod, "bob-ct",
		&adminv1.ExportAppPolicyRequest{AppId: uint64(appA)})
	require.Equal(t, codes.PermissionDenied, status.Code(errExport),
		"bob-ct 跨租户 export 必须 403")

	_, errImport := mgmt.AuthorizeRule(ctx, enf, importMethod, "bob-ct",
		&adminv1.ImportAppPolicyRequest{AppId: uint64(appA)})
	require.Equal(t, codes.PermissionDenied, status.Code(errImport),
		"bob-ct 跨租户 import 必须 403")

	// alice-ct 是租户 A 管理员，访问自己的 app → OK。
	_, errAliceExport := mgmt.AuthorizeRule(ctx, enf, exportMethod, "alice-ct",
		&adminv1.ExportAppPolicyRequest{AppId: uint64(appA)})
	require.Equal(t, codes.OK, status.Code(errAliceExport),
		"alice-ct 访问本租户 export 必须放行")

	_, errAliceImport := mgmt.AuthorizeRule(ctx, enf, importMethod, "alice-ct",
		&adminv1.ImportAppPolicyRequest{AppId: uint64(appA)})
	require.Equal(t, codes.OK, status.Code(errAliceImport),
		"alice-ct 访问本租户 import 必须放行")
}

// TestPolicyAsCode_DisabledApp_CheckStatusWrite 验证停用 app 时 import（写）被拦，export（只读）放行。
func TestPolicyAsCode_DisabledApp_CheckStatusWrite(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()

	_, appID := dbtest.SeedAppInTenant(t, db, "tenant-disabled", "disabled-domain", "AK_disabled")

	// 停用 app（status=2）。
	_, err := db.ExecContext(ctx, `UPDATE application SET status=2 WHERE id=$1`, appID)
	require.NoError(t, err)

	const (
		exportMethod = "/sydom.admin.v1.AdminService/ExportAppPolicy"
		importMethod = "/sydom.admin.v1.AdminService/ImportAppPolicy"
	)

	// import 是 isWrite=true，停用后应被拦。
	errImport := mgmt.CheckStatusWrite(ctx, db, importMethod,
		&adminv1.ImportAppPolicyRequest{AppId: uint64(appID)})
	require.Equal(t, codes.FailedPrecondition, status.Code(errImport),
		"停用 app 的 import 必须被 CheckStatusWrite 拦截")

	// export 是 isWrite=false，停用后应放行。
	errExport := mgmt.CheckStatusWrite(ctx, db, exportMethod,
		&adminv1.ExportAppPolicyRequest{AppId: uint64(appID)})
	require.NoError(t, errExport, "export 只读不应被 CheckStatusWrite 拦截")
}

// TestPolicyAsCode_ConflictReturnsFailedPrecondition 验证 iac 角色有用户绑定被文件省略时返回 FailedPrecondition（PC-6）。
func TestPolicyAsCode_ConflictReturnsFailedPrecondition(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	srv := mgmt.NewAdminServer(db, policy.NewPolicyManager(db, outbox.NewSink()), mk())

	_, appID := dbtest.SeedAppInTenant(t, db, "tenant-conflict", "conflict-domain", "AK_conflict")

	// 插入 source='iac' 的角色（code 须用 iac: 命名空间）并给它一个用户绑定。
	roleID, err := store.InsertRoleWithSource(ctx, db, appID, "iac:editor", "编辑者", "iac")
	require.NoError(t, err)
	require.NoError(t, store.InsertUserRoleBinding(ctx, db, appID, "user-bound", roleID))

	// Import 一份省略该角色的文档（dry_run=false）→ conflict → handler 返回 FailedPrecondition。
	const docWithoutEditor = `apiVersion: sydom.policy/v1
permissions: []
roles: []
`
	_, err = srv.ImportAppPolicy(ctx, &adminv1.ImportAppPolicyRequest{
		AppId:   uint64(appID),
		Content: docWithoutEditor,
		DryRun:  false,
	})
	require.Equal(t, codes.FailedPrecondition, status.Code(err),
		"存在有绑定的 iac 角色被省略时应返回 FailedPrecondition（conflict）")
}

// TestPolicyAsCode_InvalidDocReturnsInvalidArgument 验证非法文档返回 InvalidArgument。
func TestPolicyAsCode_InvalidDocReturnsInvalidArgument(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	srv := mgmt.NewAdminServer(db, policy.NewPolicyManager(db, outbox.NewSink()), mk())

	_, appID := dbtest.SeedAppInTenant(t, db, "tenant-invalid", "invalid-domain", "AK_invalid")

	// Case 1: 语法错误。首字符 '{' → Parse 路由到 JSON 分支 → json 解析失败 → ErrImportInvalid。
	_, err := srv.ImportAppPolicy(ctx, &adminv1.ImportAppPolicyRequest{
		AppId:   uint64(appID),
		Content: `{invalid json not parseable`,
		DryRun:  false,
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err),
		"语法错误的文档应返回 InvalidArgument")

	// Case 2: 非法 apiVersion（Validate 失败 → ErrImportInvalid）。
	_, err2 := srv.ImportAppPolicy(ctx, &adminv1.ImportAppPolicyRequest{
		AppId:   uint64(appID),
		Content: "apiVersion: sydom.policy/v999\npermissions: []\nroles: []\n",
		DryRun:  false,
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err2),
		"非法 apiVersion 的文档应返回 InvalidArgument")
}
