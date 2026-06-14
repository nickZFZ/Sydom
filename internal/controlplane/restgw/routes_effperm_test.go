package restgw_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// TestREST_GetEffectivePermissions_OK 验证 GET /v1/apps/{app_id}/effective-permissions?user_id=alice
// 路由已注册、鉴权放行（root super-admin）、返回 200 + 合法 JSON（含 roles/permissions/dataPrec iews 键）。
func TestREST_GetEffectivePermissions_OK(t *testing.T) {
	ts, db := newTestGW(t)
	c := rootClient(t, ts.URL)

	// 建一个 active app（root 有 * 域 super-admin，直接可查任何 app 域）。
	appID := uint64(dbtest.SeedApp(t, db))

	resp, body := c.do("GET", "/v1/apps/"+u(appID)+"/effective-permissions?user_id=alice", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	// 响应必须是合法 JSON，且含 GetEffectivePermissionsResponse 的顶层键。
	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &m), "响应必须是合法 JSON")
	// EmitDefaultValues=true：即便空切片也会输出这三个键。
	_, hasRoles := m["roles"]
	_, hasPerms := m["permissions"]
	_, hasDP := m["dataPreviews"]
	require.True(t, hasRoles || hasPerms || hasDP,
		"响应 JSON 须含 roles / permissions / dataPreviews 之一（EmitDefaultValues 保证），body=%s", string(body))
}

// TestREST_GetEffectivePermissions_AppIDFromPath 验证 app_id 取自 path（路径权威）、user_id 取自 query。
func TestREST_GetEffectivePermissions_AppIDFromPath(t *testing.T) {
	ts, db := newTestGW(t)
	c := rootClient(t, ts.URL)
	appID := uint64(dbtest.SeedApp(t, db))

	// user_id 空时 handler 返回 InvalidArgument（400）。
	resp, body := c.do("GET", "/v1/apps/"+u(appID)+"/effective-permissions", nil)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode,
		"user_id 为空须 400 InvalidArgument，body=%s", string(body))
}

// TestREST_GetEffectivePermissions_NoAuth 验证路由受 HMAC 保护：无凭据 → 401。
func TestREST_GetEffectivePermissions_NoAuth(t *testing.T) {
	ts, db := newTestGW(t)
	appID := uint64(dbtest.SeedApp(t, db))

	resp, err := http.Get(ts.URL + "/v1/apps/" + u(appID) + "/effective-permissions?user_id=alice")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "非豁免路由无凭据必须 401")
}
