package restgw_test

import (
	"net/http"
	"strings"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// TestREST_ListTemplates_OK 验证 GET /v1/apps/{app_id}/templates 路由已注册、鉴权放行
// （root super-admin）、命中 ListTemplates、返回 200 + 含内置包（业务中文名）的合法 protojson。
func TestREST_ListTemplates_OK(t *testing.T) {
	ts, db := newTestGW(t)
	c := rootClient(t, ts.URL)
	appID := uint64(dbtest.SeedApp(t, db))

	resp, body := c.do("GET", "/v1/apps/"+u(appID)+"/templates", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	var lt adminv1.ListTemplatesResponse
	require.NoError(t, protoUnmarshal(body, &lt))
	require.GreaterOrEqual(t, len(lt.Templates), 2, "须返回 >=2 内置包")
	var names []string
	for _, tpl := range lt.Templates {
		names = append(names, tpl.Name)
		require.NotEmpty(t, tpl.Permissions, "模板 %q 须有权限点", tpl.Id)
		require.NotEmpty(t, tpl.Roles, "模板 %q 须有角色", tpl.Id)
	}
	found := false
	for _, n := range names {
		if strings.Contains(n, "通用后台管理") || strings.Contains(n, "电商运营") {
			found = true
		}
	}
	require.True(t, found, "须含内置中文业务名，got: %v", names)
}

// TestREST_ApplyTemplate_OK 验证 POST /v1/apps/{app_id}/templates/{template_id}/apply 路由
// 已注册、鉴权放行、命中 ApplyTemplate、返回 200 + 正确计数；template_id 取自 path 权威。
func TestREST_ApplyTemplate_OK(t *testing.T) {
	ts, db := newTestGW(t)
	c := rootClient(t, ts.URL)
	appID := uint64(dbtest.SeedApp(t, db))

	resp, body := c.do("POST", "/v1/apps/"+u(appID)+"/templates/general-admin/apply", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	var ar adminv1.ApplyTemplateResponse
	require.NoError(t, protoUnmarshal(body, &ar))
	// general-admin: 5 权限点、3 角色。
	require.EqualValues(t, 5, ar.PermissionsUpserted)
	require.EqualValues(t, 3, ar.RolesCreated)

	// 确认角色确定性 code 落库（template_id 取自 path 生效）。
	var cnt int
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM role WHERE app_id=$1 AND code=$2`, int64(appID), "tpl:general-admin:admin").Scan(&cnt))
	require.Equal(t, 1, cnt)

	// re-apply 幂等：角色全部跳过。
	resp, body = c.do("POST", "/v1/apps/"+u(appID)+"/templates/general-admin/apply", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	require.NoError(t, protoUnmarshal(body, &ar))
	require.EqualValues(t, 3, ar.RolesSkipped)
	require.EqualValues(t, 0, ar.RolesCreated)
}

// TestREST_ApplyTemplate_DataScopesCreated 验证 POST apply ecommerce-ops 返回 DataScopesCreated >= 1。
func TestREST_ApplyTemplate_DataScopesCreated(t *testing.T) {
	ts, db := newTestGW(t)
	c := rootClient(t, ts.URL)
	appID := uint64(dbtest.SeedApp(t, db))

	resp, body := c.do("POST", "/v1/apps/"+u(appID)+"/templates/ecommerce-ops/apply", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	var ar adminv1.ApplyTemplateResponse
	require.NoError(t, protoUnmarshal(body, &ar))
	require.GreaterOrEqual(t, ar.DataScopesCreated, uint32(1))
}

// TestREST_ApplyTemplate_UnknownTemplate 验证未知 template_id → InvalidArgument → HTTP 400
// （fail-close 不泄露存在性）。
func TestREST_ApplyTemplate_UnknownTemplate(t *testing.T) {
	ts, db := newTestGW(t)
	c := rootClient(t, ts.URL)
	appID := uint64(dbtest.SeedApp(t, db))

	resp, body := c.do("POST", "/v1/apps/"+u(appID)+"/templates/nope/apply", nil)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, string(body))
}
