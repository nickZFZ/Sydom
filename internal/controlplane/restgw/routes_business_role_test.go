package restgw_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// TestREST_CreateBusinessRole_OK 验证 POST /v1/apps/{app_id}/business-roles 路由已注册、
// 鉴权放行（root super-admin）、命中 CreateBusinessRole、返回 200 + 含 roleId 的合法 JSON。
func TestREST_CreateBusinessRole_OK(t *testing.T) {
	ts, db := newTestGW(t)
	c := rootClient(t, ts.URL)
	appID := uint64(dbtest.SeedApp(t, db))

	resp, body := c.do("POST", "/v1/apps/"+u(appID)+"/business-roles",
		map[string]any{"name": "销售经理", "permission_ids": []int64{}})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &m), "响应必须是合法 JSON")
	_, hasRoleID := m["roleId"]
	require.True(t, hasRoleID, "响应须含 roleId，body=%s", string(body))
}

// TestREST_CreateBusinessRole_PathAuthority 验证 app_id 取自 path 并权威覆写 body。
// body 伪造 app_id="999999"（不存在），path 为真实 appID → 覆写后建在真实 app → 200；
// 若 path 权威失效（误用 body 999999）则 InsertRole FK 违反 → 500。故 200 证明覆写生效。
// 注：proto3 JSON 中 uint64 字段编码为字符串。
func TestREST_CreateBusinessRole_PathAuthority(t *testing.T) {
	ts, db := newTestGW(t)
	c := rootClient(t, ts.URL)
	appID := uint64(dbtest.SeedApp(t, db))

	resp, body := c.do("POST", "/v1/apps/"+u(appID)+"/business-roles",
		map[string]any{"app_id": "999999", "name": "经理", "permission_ids": []int64{}})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
}
