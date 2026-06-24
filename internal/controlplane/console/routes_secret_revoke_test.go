package console

import (
	"net/http"
	"net/url"
	"strconv"
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// 撤权走 doWrite：缺 CSRF → 403。
func TestConsole_RevokeAdminGrant_CSRFMissing_Forbidden(t *testing.T) {
	ts, _, _ := newConsole(t)
	c, _ := loginAndCSRF(t, ts, nil, "root@sydom", "rootsecret")
	resp, err := c.PostForm(ts.URL+"/admin-roles/1/revoke-grant",
		url.Values{"domain": {"*"}, "resource": {"admin"}, "action": {"update"}})
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
}

// 解绑走 doWrite：缺 CSRF → 403。
func TestConsole_UnbindOperatorRole_CSRFMissing_Forbidden(t *testing.T) {
	ts, _, _ := newConsole(t)
	c, _ := loginAndCSRF(t, ts, nil, "root@sydom", "rootsecret")
	resp, err := c.PostForm(ts.URL+"/operators/1/unbind-role",
		url.Values{"role_id": {"1"}, "domain": {"*"}})
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
}

// 轮换 app secret：缺 CSRF → 403。
func TestConsole_RotateAppSecret_CSRFMissing_Forbidden(t *testing.T) {
	ts, _, _ := newConsole(t)
	c, _ := loginAndCSRF(t, ts, nil, "root@sydom", "rootsecret")
	resp, err := c.PostForm(ts.URL+"/apps/1/rotate-secret", url.Values{})
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
}

// 重置 operator secret：带 CSRF → 200 一次性展示新 secret（非 PRG）。
func TestConsole_ResetOperatorSecret_ShowsSecretOnce(t *testing.T) {
	ts, store, db := newConsole(t)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	// 经 Console 建 operator（一次性 secret 页，非 PRG）。
	resp, err := c.PostForm(ts.URL+"/operators", url.Values{"csrf_token": {csrf}, "principal": {"erin"}})
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	// 查 erin 的 operator_id。
	var opID int64
	require.NoError(t, db.QueryRow(`SELECT id FROM admin_operator WHERE principal=$1`, "erin").Scan(&opID))
	// 重置其 secret（带 confirmed=1 过确认门）。
	resp, err = c.PostForm(ts.URL+"/operators/"+strconv.FormatInt(opID, 10)+"/reset-secret",
		url.Values{"csrf_token": {csrf}, "confirmed": {"1"}})
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode) // 非 PRG（PRG 会是 303→GET /operators，丢失 secret）
	body := readBody(t, resp)
	require.Contains(t, body, "已重置")
	require.Contains(t, body, `class="secret"`) // 一次性展示新 secret
}

// 轮换 app secret：带 CSRF → 200 一次性展示新 App Secret（非 PRG）。
func TestConsole_RotateAppSecret_ShowsSecretOnce(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	resp, err := c.PostForm(ts.URL+"/apps/"+strconv.FormatInt(appID, 10)+"/rotate-secret",
		url.Values{"csrf_token": {csrf}, "confirmed": {"1"}})
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := readBody(t, resp)
	require.Contains(t, body, "已轮换")
	require.Contains(t, body, `class="secret"`)
}
