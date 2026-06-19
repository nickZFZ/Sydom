package console

import (
	"fmt"
	"net/http"
	"strconv"
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// TestConsole_Permissions_Paginated 验证 GET /apps/{id}/permissions?page=1：
// - 有会话 + 已 seed 权限点 → 200 且 body 含分页条文案（"共"）与搜索框（"搜索"）。
func TestConsole_Permissions_Paginated(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)

	// 直插 3 个权限点（少于 limit=50，避免与默认 limit 歧义）。
	for i := 0; i < 3; i++ {
		code := "perm-pg-" + strconv.Itoa(i)
		_, err := db.Exec(
			`INSERT INTO permission(app_id, code, resource, action, type, name, source)
			 VALUES($1, $2, 'order', 'read', 'act', $2, 'manual')`,
			appID, code,
		)
		require.NoError(t, err)
	}

	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	resp, err := c.Get(ts.URL + "/apps/" + strconv.FormatInt(appID, 10) + "/permissions?page=1")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := readBody(t, resp)
	require.Contains(t, body, "显示 1-3 / 共 3") // pager 渲染完整区间 + total（seed 3 条，SeedApp 不预置权限）
	require.Contains(t, body, "搜索")           // searchbox 模板含"搜索"按钮
}

// TestConsole_Operators_Paginated 验证 GET /operators?page=1：有分页条和搜索框。
func TestConsole_Operators_Paginated(t *testing.T) {
	ts, store, db := newConsole(t)

	// seed 3 个额外 operator（NOT NULL 列：principal/secret_enc/status）
	for i := 0; i < 3; i++ {
		principal := fmt.Sprintf("op-pg-%d@test", i)
		_, err := db.Exec(
			`INSERT INTO admin_operator(principal, secret_enc, status) VALUES($1, '\xab'::bytea, 1)`,
			principal,
		)
		require.NoError(t, err)
	}

	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	resp, err := c.Get(ts.URL + "/operators?page=1")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := readBody(t, resp)
	require.Contains(t, body, "共 ")           // pager 的 "共 N" 文案
	require.Contains(t, body, "搜索")           // searchbox 含"搜索"按钮
	require.Contains(t, body, "op-pg-0@test") // seed operator 出现在列表
}

// TestConsole_Permissions_NoSession 验证无会话时重定向到登录页。
func TestConsole_Permissions_NoSession(t *testing.T) {
	ts, _, _ := newConsole(t)
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(ts.URL + "/apps/1/permissions")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
}
