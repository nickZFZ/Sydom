package restgw_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// TestREST_ListPermissions_Paginated 验证 GET /v1/apps/{app_id}/permissions 支持分页查询参数：
// - ?limit=2&sort=code&order=asc → 200 + JSON 含 total 键 + permissions 数组长度 ≤2；
// - ?q=find_me → total 减小（仅匹配子串）。
func TestREST_ListPermissions_Paginated(t *testing.T) {
	ts, db := newTestGW(t)
	c := rootClient(t, ts.URL)
	appID := uint64(dbtest.SeedApp(t, db))

	// 种 3 条 permission，其中 2 条 code 含 "find_me"。
	for _, row := range []struct{ code, resource, action, name, source string }{
		{"find_me_a", "order", "read", "读订单A", "manual"},
		{"find_me_b", "order", "write", "写订单B", "manual"},
		{"other_c", "item", "read", "读条目C", "manual"},
	} {
		_, err := db.Exec(
			`INSERT INTO permission(app_id,code,resource,action,type,name,source) VALUES($1,$2,$3,$4,'api',$5,$6)`,
			appID, row.code, row.resource, row.action, row.name, row.source,
		)
		require.NoError(t, err, "seed permission")
	}

	// 分页查询 limit=2，期望 total=3，返回 ≤2 条。
	path := fmt.Sprintf("/v1/apps/%s/permissions?limit=2&sort=code&order=asc", u(appID))
	resp, body := c.do("GET", path, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &m), "响应须是合法 JSON，body=%s", string(body))
	_, hasTotal := m["total"]
	require.True(t, hasTotal, "响应须含 total 键，body=%s", string(body))
	_, hasPerm := m["permissions"]
	require.True(t, hasPerm, "响应须含 permissions 键，body=%s", string(body))

	var perms []json.RawMessage
	require.NoError(t, json.Unmarshal(m["permissions"], &perms))
	require.LessOrEqual(t, len(perms), 2, "limit=2 时 permissions 数组长度须 ≤2")

	// 搜索过滤：q=find_me 只命中 2 条，total 应小于无过滤的 3。
	pathQ := fmt.Sprintf("/v1/apps/%s/permissions?q=find_me", u(appID))
	resp2, body2 := c.do("GET", pathQ, nil)
	require.Equal(t, http.StatusOK, resp2.StatusCode, string(body2))

	var m2 map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body2, &m2))
	var total2 int64
	require.NoError(t, json.Unmarshal(m2["total"], &total2))
	require.Equal(t, int64(2), total2, "q=find_me 应恰好匹配 2 条，total=%d body=%s", total2, string(body2))
}

// TestREST_ListPermissions_NoAuth 验证路由受 HMAC 保护：无凭据 → 401。
func TestREST_ListPermissions_NoAuth(t *testing.T) {
	ts, db := newTestGW(t)
	appID := uint64(dbtest.SeedApp(t, db))

	resp, err := http.Get(ts.URL + "/v1/apps/" + u(appID) + "/permissions")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "非豁免路由无凭据必须 401")
}

// TestREST_ListOperators_Paginated 验证 GET /v1/operators?limit=1 → 200 + JSON 含 total 键。
// root operator 本身已存在，故 total ≥ 1。
func TestREST_ListOperators_Paginated(t *testing.T) {
	ts, _ := newTestGW(t)
	c := rootClient(t, ts.URL)

	resp, body := c.do("GET", "/v1/operators?limit=1", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &m), "响应须是合法 JSON，body=%s", string(body))
	_, hasTotal := m["total"]
	require.True(t, hasTotal, "响应须含 total 键，body=%s", string(body))
}
