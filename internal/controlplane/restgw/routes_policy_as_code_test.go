package restgw_test

import (
	"context"
	"net/http"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// minimalPolicyYAML 是最小合法策略文件（apiVersion sydom.policy/v1）。
const minimalPolicyYAML = `apiVersion: sydom.policy/v1
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

// TestREST_PolicyExport_200 验证 GET /v1/apps/{app_id}/policy/export?format=yaml：
// 鉴权放行（root super-admin）→ 200 + Content 非空且含注入的权限点 code。
func TestREST_PolicyExport_200(t *testing.T) {
	ts, db := newTestGW(t)
	c := rootClient(t, ts.URL)
	appID := uint64(dbtest.SeedApp(t, db))

	// 注入 1 个权限点让 export 内容有料。
	ctx := context.Background()
	_, err := store.UpsertPermissionWithSource(ctx, db, int64(appID), "doc.read", "doc", "read", "api", "读文档", "", "iac")
	require.NoError(t, err)

	resp, body := c.do("GET", "/v1/apps/"+u(appID)+"/policy/export?format=yaml", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	var er adminv1.ExportAppPolicyResponse
	require.NoError(t, protoUnmarshal(body, &er))
	require.NotEmpty(t, er.Content, "Content 必须非空")
	require.Contains(t, er.Content, "doc.read", "Content 须含注入的权限点 code")
}

// TestREST_PolicyImport_DryRun 验证 POST /v1/apps/{app_id}/policy/import?dry_run=true：
// body = 原始 YAML → 200 + Applied=false、Creates>0、DB 无副作用（permission/role 行数均为 0）。
func TestREST_PolicyImport_DryRun(t *testing.T) {
	ts, db := newTestGW(t)
	c := rootClient(t, ts.URL)
	appID := uint64(dbtest.SeedApp(t, db))

	resp, body := c.do("POST", "/v1/apps/"+u(appID)+"/policy/import?dry_run=true", []byte(minimalPolicyYAML))
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	var ir adminv1.ImportAppPolicyResponse
	require.NoError(t, protoUnmarshal(body, &ir))
	require.False(t, ir.Applied, "dry_run=true 时 Applied 必须为 false")
	require.Greater(t, len(ir.Diff), 0, "Diff 须非空")
	require.Greater(t, ir.Creates, int32(0), "须有 creates（新增权限/角色）")

	// dry-run 零副作用：DB 不应写入任何行。
	var permCnt, roleCnt int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM permission WHERE app_id=$1`, int64(appID)).Scan(&permCnt))
	require.Equal(t, 0, permCnt, "dry_run 后 permission 表应无记录")
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM role WHERE app_id=$1`, int64(appID)).Scan(&roleCnt))
	require.Equal(t, 0, roleCnt, "dry_run 后 role 表应无记录")
}

// TestREST_PolicyImport_Apply 验证 POST /v1/apps/{app_id}/policy/import（apply，dry_run=false/默认）：
// body = 原始 YAML → 200 + Applied=true、Version>0；库落入 permission code=doc.read 与 role code=iac:reader 各 1 行。
func TestREST_PolicyImport_Apply(t *testing.T) {
	ts, db := newTestGW(t)
	c := rootClient(t, ts.URL)
	appID := uint64(dbtest.SeedApp(t, db))

	// dry_run 不带参数默认 false → apply。
	resp, body := c.do("POST", "/v1/apps/"+u(appID)+"/policy/import", []byte(minimalPolicyYAML))
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	var ir adminv1.ImportAppPolicyResponse
	require.NoError(t, protoUnmarshal(body, &ir))
	require.True(t, ir.Applied, "apply 后 Applied 必须为 true")
	require.Greater(t, ir.Version, int64(0), "apply 后 Version 须 >0")

	// 往返 parity：DB 已落入 permission 和 role。
	var permCnt, roleCnt int
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM permission WHERE app_id=$1 AND code=$2`, int64(appID), "doc.read").Scan(&permCnt))
	require.Equal(t, 1, permCnt, "permission code=doc.read 须落库")
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM role WHERE app_id=$1 AND code=$2`, int64(appID), "iac:reader").Scan(&roleCnt))
	require.Equal(t, 1, roleCnt, "role code=iac:reader 须落库")
}

// TestREST_PolicyExport_UnknownApp_FailClose 验证未知 app_id 经 AuthorizeRule→TenantDomainOf fail-close
// → PermissionDenied → HTTP 403（不泄露存在性差异）。
func TestREST_PolicyExport_UnknownApp_FailClose(t *testing.T) {
	ts, _ := newTestGW(t)
	c := rootClient(t, ts.URL)

	// 使用极大的、肯定不存在的 app_id。
	resp, body := c.do("GET", "/v1/apps/999999999/policy/export", nil)
	require.Equal(t, http.StatusForbidden, resp.StatusCode, string(body))
}

// TestREST_PolicyImport_NoAuth 验证 import 路由受 HMAC 保护：无凭据 → 401。
func TestREST_PolicyImport_NoAuth(t *testing.T) {
	ts, db := newTestGW(t)
	appID := uint64(dbtest.SeedApp(t, db))

	// 直接 POST 无任何 HMAC 头（镜像 routes_audit_test.go NoAuth 范式）。
	resp, err := http.Post(ts.URL+"/v1/apps/"+u(appID)+"/policy/import", "text/plain", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "无 HMAC 凭据须返回 401")
}
