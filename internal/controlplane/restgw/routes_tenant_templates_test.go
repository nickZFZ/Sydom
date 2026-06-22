package restgw_test

import (
	"net/http"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// TestREST_TenantTemplate_SaveListApply 端到端走通租户自有模板 5 路由中的 4 条
// （Save/List/Apply/Delete），验证路由已注册、鉴权放行（root super-admin "*" 域覆盖所有
// app/tenant 域）、path 权威填域字段使 AuthorizeRule 正确鉴权、捕获→应用链路连通。
func TestREST_TenantTemplate_SaveListApply(t *testing.T) {
	ts, db := newTestGW(t)
	c := rootClient(t, ts.URL)

	// 源 app（带租户）。
	tID, srcApp := dbtest.SeedAppInTenant(t, db, "t-a", "dom-a", "AK_a")

	// 在 srcApp 配置至少 1 角色，使 apply 后 RolesCreated>=1。
	resp, body := c.do("POST", "/v1/apps/"+u(uint64(srcApp))+"/roles",
		map[string]any{"code": "viewer", "name": "查看员"})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	// 存为模板（SaveAppAsTemplate，scopeApp，读 path app_id）。
	resp, body = c.do("POST", "/v1/apps/"+u(uint64(srcApp))+"/template-captures",
		map[string]any{"name": "标准后台", "description": "通用"})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var ref adminv1.TenantTemplateRef
	require.NoError(t, protoUnmarshal(body, &ref))
	require.NotZero(t, ref.Id)
	require.Equal(t, "标准后台", ref.Name)

	// 列表（ListTenantTemplates，scopeTenant，读 path tenant_id）。
	resp, body = c.do("GET", "/v1/tenants/"+u(uint64(tID))+"/templates", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var lt adminv1.ListTenantTemplatesResponse
	require.NoError(t, protoUnmarshal(body, &lt))
	require.GreaterOrEqual(t, lt.Total, uint32(1))
	var names []string
	for _, s := range lt.Templates {
		names = append(names, s.Name)
	}
	require.Contains(t, names, "标准后台")

	// 同租户第二 app（dbtest 无同租户多 app 助手→直接 INSERT，复用 SeedAppInTenant 形态）。
	var dstApp int64
	require.NoError(t, db.QueryRow(
		`INSERT INTO application (tenant_id, domain, name, app_key, app_secret_enc)
		 VALUES ($1,$2,$3,$4,'\xab'::bytea) RETURNING id`,
		tID, "dom-a2", "second", "AK_a2").Scan(&dstApp))

	// 应用到第二 app（ApplyTenantTemplate，scopeApp，path app_id+template_id 权威，无 body 字段可伪造）。
	resp, body = c.do("POST", "/v1/apps/"+u(uint64(dstApp))+"/tenant-templates/"+u(ref.Id)+"/apply", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var ar adminv1.ApplyTemplateResponse
	require.NoError(t, protoUnmarshal(body, &ar))
	require.GreaterOrEqual(t, ar.RolesCreated, uint32(1))

	// 删除（DeleteTenantTemplate，scopeTenant，path tenant_id+template_id 权威）。
	resp, body = c.do("DELETE", "/v1/tenants/"+u(uint64(tID))+"/templates/"+u(ref.Id), nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
}
