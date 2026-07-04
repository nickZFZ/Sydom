package restgw_test

import (
	"net/http"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// —— M4.2 批量操作 5 路由测试专用 helper（经 REST 真实写路径建夹具，非直插 DB）——

func createRole(t *testing.T, c *restClient, appID uint64, code, name string) int64 {
	t.Helper()
	resp, body := c.do("POST", "/v1/apps/"+u(appID)+"/roles", map[string]any{"code": code, "name": name})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var cr adminv1.CreateRoleResponse
	require.NoError(t, protoUnmarshal(body, &cr))
	return cr.RoleId
}

func upsertPermission(t *testing.T, c *restClient, appID uint64, code string) int64 {
	t.Helper()
	resp, body := c.do("PUT", "/v1/apps/"+u(appID)+"/permissions/"+code, map[string]any{
		"resource": "order", "action": "read", "ptype": "api", "name": "读订单"})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var up adminv1.UpsertPermissionResponse
	require.NoError(t, protoUnmarshal(body, &up))
	return up.PermissionId
}

func grantPermission(t *testing.T, c *restClient, appID uint64, roleID, permID int64) {
	t.Helper()
	resp, body := c.do("POST", "/v1/apps/"+u(appID)+"/roles/"+i(roleID)+"/grants", map[string]any{
		"permissionId": permID, "eft": "allow"})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
}

func addInheritance(t *testing.T, c *restClient, appID uint64, childID, parentID int64) {
	t.Helper()
	resp, body := c.do("POST", "/v1/apps/"+u(appID)+"/roles/"+i(childID)+"/parents", map[string]any{
		"parentRoleId": parentID})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
}

func bindUserRole(t *testing.T, c *restClient, appID uint64, userID string, roleID int64) {
	t.Helper()
	resp, body := c.do("POST", "/v1/apps/"+u(appID)+"/users/"+userID+"/roles", map[string]any{"roleId": roleID})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
}

func createDataPolicy(t *testing.T, c *restClient, appID uint64, subjectID string) int64 {
	t.Helper()
	resp, body := c.do("POST", "/v1/apps/"+u(appID)+"/data-policies", map[string]any{
		"subjectType": "role", "subjectId": subjectID, "resource": "order",
		"condition": `{"field":"dept","op":"EQ","value":"x"}`, "effect": "allow"})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var up adminv1.UpsertDataPolicyResponse
	require.NoError(t, protoUnmarshal(body, &up))
	return up.DataPolicyId
}

// TestREST_BatchDeleteRole_OK 验证 POST /v1/apps/{app_id}/roles/batch-delete：
// 200 + Requested==Applied==len(role_ids)，且两角色确已从列表消失。
// 两角色各授一条权限：使删除产生真实 casbin 投影 diff（Changed=true）——若角色本身
// 无任何 grant/inheritance/binding，关系表行虽被删，但无投影变化，Changed 会是 false
// （BatchDeleteRole 依赖 diff 是否为空判定要不要 bump，这是既有约定，非本路由 bug）。
func TestREST_BatchDeleteRole_OK(t *testing.T) {
	ts, db := newTestGW(t)
	c := rootClient(t, ts.URL)
	appID := uint64(dbtest.SeedApp(t, db))

	r1 := createRole(t, c, appID, "r1", "角色一")
	r2 := createRole(t, c, appID, "r2", "角色二")
	permID := upsertPermission(t, c, appID, "order:read")
	grantPermission(t, c, appID, r1, permID)
	grantPermission(t, c, appID, r2, permID)

	resp, body := c.do("POST", "/v1/apps/"+u(appID)+"/roles/batch-delete", map[string]any{"roleIds": []int64{r1, r2}})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var br adminv1.BatchWriteResponse
	require.NoError(t, protoUnmarshal(body, &br))
	require.Equal(t, uint32(2), br.Requested)
	require.Equal(t, uint32(2), br.Applied)
	require.True(t, br.Changed)

	resp, body = c.do("GET", "/v1/apps/"+u(appID)+"/roles", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var lr adminv1.ListRolesResponse
	require.NoError(t, protoUnmarshal(body, &lr))
	require.Empty(t, lr.Roles, "两角色须均已被批量删除")
}

// TestREST_BatchDeleteRole_UnknownApp_403 验证未知 app_id 经 AuthorizeRule→TenantDomainOf fail-close → 403（不泄露存在性）。
func TestREST_BatchDeleteRole_UnknownApp_403(t *testing.T) {
	ts, _ := newTestGW(t)
	c := rootClient(t, ts.URL)

	resp, body := c.do("POST", "/v1/apps/999999999/roles/batch-delete", map[string]any{"roleIds": []int64{1}})
	require.Equal(t, http.StatusForbidden, resp.StatusCode, string(body))
}

// TestREST_BatchDeleteRole_PathAuthority 验证 app_id 恒取 path：body 里塞一个不存在的假 appId 也不影响——
// 实际作用于 path 的 app，而非 body 的假 app_id（若读了 body 的 app_id，TenantDomainOf 会 fail-close 致 403）。
func TestREST_BatchDeleteRole_PathAuthority(t *testing.T) {
	ts, db := newTestGW(t)
	c := rootClient(t, ts.URL)
	appID := uint64(dbtest.SeedApp(t, db))
	r1 := createRole(t, c, appID, "r1", "角色一")

	resp, body := c.do("POST", "/v1/apps/"+u(appID)+"/roles/batch-delete", map[string]any{
		"appId": "999999999", "roleIds": []int64{r1},
	})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var br adminv1.BatchWriteResponse
	require.NoError(t, protoUnmarshal(body, &br))
	require.Equal(t, uint32(1), br.Applied, "须作用于路径 app（body 假 app_id 被忽略）")

	resp, body = c.do("GET", "/v1/apps/"+u(appID)+"/roles", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var lr adminv1.ListRolesResponse
	require.NoError(t, protoUnmarshal(body, &lr))
	require.Empty(t, lr.Roles, "路径 app 的角色须已被删除")
}

// TestREST_BatchUnbindUserRole_OK 验证 POST /v1/apps/{app_id}/user-bindings/batch-delete。
func TestREST_BatchUnbindUserRole_OK(t *testing.T) {
	ts, db := newTestGW(t)
	c := rootClient(t, ts.URL)
	appID := uint64(dbtest.SeedApp(t, db))
	roleID := createRole(t, c, appID, "mgr", "经理")
	bindUserRole(t, c, appID, "alice", roleID)

	resp, body := c.do("POST", "/v1/apps/"+u(appID)+"/user-bindings/batch-delete", map[string]any{
		"items": []map[string]any{{"userId": "alice", "roleId": roleID}},
	})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var br adminv1.BatchWriteResponse
	require.NoError(t, protoUnmarshal(body, &br))
	require.Equal(t, uint32(1), br.Requested)
	require.Equal(t, uint32(1), br.Applied)

	resp, body = c.do("GET", "/v1/apps/"+u(appID)+"/user-bindings", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var lb adminv1.ListUserBindingsResponse
	require.NoError(t, protoUnmarshal(body, &lb))
	require.Empty(t, lb.Bindings, "绑定须已被批量解绑")
}

// TestREST_BatchRevokePermission_OK 验证 POST /v1/apps/{app_id}/grants/batch-delete。
func TestREST_BatchRevokePermission_OK(t *testing.T) {
	ts, db := newTestGW(t)
	c := rootClient(t, ts.URL)
	appID := uint64(dbtest.SeedApp(t, db))
	roleID := createRole(t, c, appID, "mgr", "经理")
	permID := upsertPermission(t, c, appID, "order:read")
	grantPermission(t, c, appID, roleID, permID)

	resp, body := c.do("POST", "/v1/apps/"+u(appID)+"/grants/batch-delete", map[string]any{
		"items": []map[string]any{{"roleId": roleID, "permissionId": permID}},
	})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var br adminv1.BatchWriteResponse
	require.NoError(t, protoUnmarshal(body, &br))
	require.Equal(t, uint32(1), br.Requested)
	require.Equal(t, uint32(1), br.Applied)

	resp, body = c.do("GET", "/v1/apps/"+u(appID)+"/grants?role_id="+i(roleID), nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var lg adminv1.ListGrantsResponse
	require.NoError(t, protoUnmarshal(body, &lg))
	require.Empty(t, lg.Grants, "授权须已被批量撤销")
}

// TestREST_BatchRemoveRoleInheritance_OK 验证 POST /v1/apps/{app_id}/role-inheritances/batch-delete。
func TestREST_BatchRemoveRoleInheritance_OK(t *testing.T) {
	ts, db := newTestGW(t)
	c := rootClient(t, ts.URL)
	appID := uint64(dbtest.SeedApp(t, db))
	parentID := createRole(t, c, appID, "mgr", "经理")
	childID := createRole(t, c, appID, "clerk", "职员")
	addInheritance(t, c, appID, childID, parentID)

	resp, body := c.do("POST", "/v1/apps/"+u(appID)+"/role-inheritances/batch-delete", map[string]any{
		"items": []map[string]any{{"childRoleId": childID, "parentRoleId": parentID}},
	})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var br adminv1.BatchWriteResponse
	require.NoError(t, protoUnmarshal(body, &br))
	require.Equal(t, uint32(1), br.Requested)
	require.Equal(t, uint32(1), br.Applied)

	resp, body = c.do("GET", "/v1/apps/"+u(appID)+"/role-inheritances", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var li adminv1.ListRoleInheritancesResponse
	require.NoError(t, protoUnmarshal(body, &li))
	require.Empty(t, li.Inheritances, "继承边须已被批量移除")
}

// TestREST_BatchDeleteDataPolicy_OK 验证 POST /v1/apps/{app_id}/data-policies/batch-delete。
func TestREST_BatchDeleteDataPolicy_OK(t *testing.T) {
	ts, db := newTestGW(t)
	c := rootClient(t, ts.URL)
	appID := uint64(dbtest.SeedApp(t, db))
	dpID := createDataPolicy(t, c, appID, "manager")

	resp, body := c.do("POST", "/v1/apps/"+u(appID)+"/data-policies/batch-delete", map[string]any{
		"dataPolicyIds": []int64{dpID},
	})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var br adminv1.BatchWriteResponse
	require.NoError(t, protoUnmarshal(body, &br))
	require.Equal(t, uint32(1), br.Requested)
	require.Equal(t, uint32(1), br.Applied)

	resp, body = c.do("GET", "/v1/apps/"+u(appID)+"/data-policies", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var ld adminv1.ListDataPoliciesResponse
	require.NoError(t, protoUnmarshal(body, &ld))
	require.Empty(t, ld.DataPolicies, "数据策略须已被批量删除")
}

// TestREST_BatchDeleteRole_NoAuth_401 验证批量路由受 HMAC 保护：无凭据 → 401（镜像 routes_policy_as_code_test.go NoAuth 范式）。
func TestREST_BatchDeleteRole_NoAuth_401(t *testing.T) {
	ts, db := newTestGW(t)
	appID := uint64(dbtest.SeedApp(t, db))

	resp, err := http.Post(ts.URL+"/v1/apps/"+u(appID)+"/roles/batch-delete", "application/json", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "无 HMAC 凭据须返回 401")
}
