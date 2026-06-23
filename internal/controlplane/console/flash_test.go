package console

import (
	"net/http"
	"net/url"
	"strconv"
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestConsole_Flash_ShownOnceAfterWrite(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	// 建角色（doWrite + flash）→ PRG 到角色列表。
	// loginAndCSRF 返回的 client 使用默认 CheckRedirect（自动跟随重定向）。
	jar := c.Jar
	c2 := &http.Client{Jar: jar} // 自动跟随重定向，PRG 后落到目标页
	form := url.Values{"code": {"viewer"}, "name": {"查看员"}, "csrf_token": {csrf}}
	resp, err := c2.PostForm(ts.URL+"/apps/"+strconv.FormatInt(appID, 10)+"/roles", form)
	require.NoError(t, err)
	body := readBody(t, resp) // 跟随 PRG 后的页
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, body, "已创建") // flash 业务语言成功提示

	// 再访问同页 → flash 不再出现（一次性）。
	resp2, err := c2.Get(ts.URL + "/apps/" + strconv.FormatInt(appID, 10) + "/roles")
	require.NoError(t, err)
	body2 := readBody(t, resp2)
	require.NotContains(t, body2, "已创建", "flash 读后即清")
}

// TestFlashFor_FallbackAndKnown 纯函数测（不需 redis）：未知方法回退通用语，
// 已知方法命中专有业务文案。给回退契约加齿。
func TestFlashFor_FallbackAndKnown(t *testing.T) {
	require.Equal(t, "操作成功", flashFor(svc+"UnknownMethod"))
	require.Equal(t, "角色已创建", flashFor(svc+"CreateRole"))
}
