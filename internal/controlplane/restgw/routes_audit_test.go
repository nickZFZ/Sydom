package restgw_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// TestREST_AppAudit 验证 GET /v1/apps/{app_id}/audit 路由：
// - 已授权 root 签名 → 200 + 合法 JSON（含 entries 键）；
// - 无 HMAC → 401。
func TestREST_AppAudit(t *testing.T) {
	ts, db := newTestGW(t)
	c := rootClient(t, ts.URL)

	appID := uint64(dbtest.SeedApp(t, db))

	// 直插一条 policy_audit_log（store.InsertAudit）。
	err := store.InsertAudit(context.Background(), db, int64(appID),
		"root", "create", "role", "1", []byte(`{}`), 1)
	require.NoError(t, err)

	// 已授权 operator（root = super-admin）可查 app 域审计。
	resp, body := c.do("GET", "/v1/apps/"+u(appID)+"/audit", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	// 响应必须是合法 JSON，且含 entries 键（EmitDefaultValues=true 保证空切片也输出）。
	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &m), "响应必须是合法 JSON，body=%s", string(body))
	_, hasEntries := m["entries"]
	require.True(t, hasEntries, "响应 JSON 须含 entries 键，body=%s", string(body))
}

// TestREST_AppAudit_NoAuth 验证路由受 HMAC 保护：无凭据 → 401。
func TestREST_AppAudit_NoAuth(t *testing.T) {
	ts, db := newTestGW(t)
	appID := uint64(dbtest.SeedApp(t, db))

	resp, err := http.Get(ts.URL + "/v1/apps/" + u(appID) + "/audit")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "非豁免路由无凭据必须 401")
}

// TestREST_AdminAudit 验证 GET /v1/admin/audit 路由：
// - root 超管签名 → 200 + 合法 JSON（含 entries 键）；
// - 无 HMAC → 401。
func TestREST_AdminAudit(t *testing.T) {
	ts, db := newTestGW(t)
	c := rootClient(t, ts.URL)

	// 直插一条 admin_audit_log（adminauthz.InsertAdminAudit）。
	err := adminauthz.InsertAdminAudit(context.Background(), db,
		sql.NullInt64{}, "root", "create", "operator", "1", nil, sql.NullInt64{})
	require.NoError(t, err)

	resp, body := c.do("GET", "/v1/admin/audit?tenant_id=0", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(body, &m), "响应必须是合法 JSON，body=%s", string(body))
	_, hasEntries := m["entries"]
	require.True(t, hasEntries, "响应 JSON 须含 entries 键，body=%s", string(body))
}

// TestREST_AdminAudit_NoAuth 验证路由受 HMAC 保护：无凭据 → 401。
func TestREST_AdminAudit_NoAuth(t *testing.T) {
	ts, _ := newTestGW(t)

	resp, err := http.Get(ts.URL + "/v1/admin/audit")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "非豁免路由无凭据必须 401")
}
