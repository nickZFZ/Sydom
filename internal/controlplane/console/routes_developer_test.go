package console

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestConsole_DeveloperPage(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	resp, err := c.Get(ts.URL + "/apps/" + strconv.FormatInt(appID, 10) + "/developer")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := readBody(t, resp)

	require.Equal(t, 1, strings.Count(body, "<h1"), "须单 h1")
	require.Contains(t, body, "开发者文档")
	// 四块锚点。
	for _, anchor := range []string{"quickstart", "concepts", "sdk", "api-reference"} {
		require.Contains(t, body, `id="`+anchor+`"`, "缺锚点 %s", anchor)
	}
	// DP-3：quickstart 含真实 SDK 符号。
	require.Contains(t, body, "sydom.New")
	require.Contains(t, body, ".Check(")
	// 管理面参考含已知 RPC。
	require.Contains(t, body, "UpsertDataPolicy")
	// DP-4：不泄露 secret（种子 app 无真实 secret 展示；断言无 secret 字面）。
	require.NotContains(t, body, "app_secret")
}

func TestConsole_DeveloperPage_RequiresSession(t *testing.T) {
	ts, _, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	anon := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := anon.Get(ts.URL + "/apps/" + strconv.FormatInt(appID, 10) + "/developer")
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode) // requireSession 302 /login
}
