package restgw_test

import (
	"net/http"
	"strconv"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// TestREST_RoleGraph_And_Simulate 端到端验证角色全景 + 决策模拟 2 路由：
// 经 REST 真实写路径（GrantPermission 同时写 role_permission+casbin_rule），
// GetRoleGraph 能拿到 Capabilities，SimulateRoleChange bind_user 能拿到 AddedPermissions。
func TestREST_RoleGraph_And_Simulate(t *testing.T) {
	ts, db := newTestGW(t)
	c := rootClient(t, ts.URL)
	appID := uint64(dbtest.SeedApp(t, db))

	// 经 REST 建角色 + 权限 + 授权。
	resp, body := c.do("POST", "/v1/apps/"+u(appID)+"/roles", map[string]any{"code": "viewer", "name": "查看员"})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var cr adminv1.CreateRoleResponse
	require.NoError(t, protoUnmarshal(body, &cr))

	resp, body = c.do("PUT", "/v1/apps/"+u(appID)+"/permissions/order:read", map[string]any{
		"resource": "order", "action": "read", "ptype": "api", "name": "查看订单"})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var up adminv1.UpsertPermissionResponse
	require.NoError(t, protoUnmarshal(body, &up))

	resp, _ = c.do("POST", "/v1/apps/"+u(appID)+"/roles/"+i(cr.RoleId)+"/grants", map[string]any{
		"permissionId": strconv.FormatInt(up.PermissionId, 10), "eft": "allow"})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// GetRoleGraph — 验证路由注册、app_id/role_id path 权威、返回 Capabilities。
	resp, body = c.do("GET", "/v1/apps/"+u(appID)+"/roles/"+i(cr.RoleId)+"/graph", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var rg adminv1.GetRoleGraphResponse
	require.NoError(t, protoUnmarshal(body, &rg))
	require.Equal(t, "viewer", rg.RoleCode)
	require.NotEmpty(t, rg.Capabilities)

	// SimulateRoleChange（bind_user）— 验证路由注册、query 参数解析、AddedPermissions 非空。
	resp, body = c.do("GET", "/v1/apps/"+u(appID)+"/roles/"+i(cr.RoleId)+"/simulation?change_type=bind_user&user_id=bob", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var sim adminv1.SimulateRoleChangeResponse
	require.NoError(t, protoUnmarshal(body, &sim))
	require.Len(t, sim.Subjects, 1)
	require.NotEmpty(t, sim.Subjects[0].AddedPermissions)
}
