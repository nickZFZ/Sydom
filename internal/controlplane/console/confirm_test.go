package console

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"testing"

	storepkg "github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestConsole_ConfirmGate(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)

	// 直接建一个角色供删除（不经 HTTP，避免 createRole 的确认门干扰）。
	roleID, err := storepkg.InsertRole(context.Background(), db, appID, "viewer", "查看员")
	require.NoError(t, err)

	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	base := ts.URL + "/apps/" + strconv.FormatInt(appID, 10) + "/roles/" + strconv.FormatInt(roleID, 10) + "/delete"

	// ---- 不带 confirmed → 渲确认页（200），角色仍在 ----
	resp, err := c.PostForm(base, url.Values{"csrf_token": {csrf}})
	require.NoError(t, err)
	body := readBody(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, body, "确认")
	require.Contains(t, body, `name="confirmed" value="1"`)
	require.Contains(t, body, `name="csrf_token"`)

	// 确认角色仍在库中（确认门阻止了删除）。
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM role WHERE id=$1`, roleID).Scan(&count))
	require.Equal(t, 1, count, "无 confirmed 时角色不应被删除")

	// ---- 带 confirmed=1 → 执行 + PRG(303)，再跟随重定向看 flash「已删除」 ----
	// 同 cookie jar 建新 client（nil CheckRedirect）自动跟随 303。
	jar := c.Jar
	c2 := &http.Client{Jar: jar}
	resp2, err := c2.PostForm(base, url.Values{
		"csrf_token": {csrf},
		"confirmed":  {"1"},
	})
	require.NoError(t, err)
	body2 := readBody(t, resp2)
	require.Equal(t, http.StatusOK, resp2.StatusCode) // 跟随 PRG 后落到目标页
	require.Contains(t, body2, "已删除")                 // flash 成功文案

	// 确认角色已从库中删除（真正执行了操作）。
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM role WHERE id=$1`, roleID).Scan(&count))
	require.Equal(t, 0, count, "带 confirmed=1 时角色应被删除")
}

// TestConfirmGate_CSRF_Missing：无 csrf_token 直接返回 403（门守在 CSRF 校验处）。
func TestConfirmGate_CSRF_Missing(t *testing.T) {
	ts, _, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)

	roleID, err := storepkg.InsertRole(context.Background(), db, appID, "viewer", "查看员")
	require.NoError(t, err)

	// 登录但不带 csrf_token。
	c, _ := loginAndCSRF(t, ts, nil, "root@sydom", "rootsecret")

	base := ts.URL + "/apps/" + strconv.FormatInt(appID, 10) + "/roles/" + strconv.FormatInt(roleID, 10) + "/delete"
	resp, err := c.PostForm(base, url.Values{}) // 无 csrf_token
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
}
