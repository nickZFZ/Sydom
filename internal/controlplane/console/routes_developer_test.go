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
	// REST 列确实渲染出真实 method+path（钉死模板 {{if .RESTPath}} 分支，
	// 抓 if/else 写反：UpsertDataPolicy join 到稳定排序首条 POST /v1/apps/{app_id}/data-policies）。
	require.Contains(t, body, "/v1/apps/{app_id}/data-policies")
	// 宽端点表横向滚动容器须键盘可达（axe scrollable-region-focusable，真实浏览器走查捕获）：
	// tabindex+role+aria-label 缺一即回归——此断言无需浏览器即可挡住。
	require.Contains(t, body, `class="table-scroll" tabindex="0" role="region"`)
	// DP-4：不泄露 secret（种子 app 无真实 secret 展示；断言无 secret 字面）。
	require.NotContains(t, body, "app_secret")
}

func TestConsole_DeveloperPage_ShowsCredentials(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	resp, err := c.Get(ts.URL + "/apps/" + strconv.FormatInt(appID, 10) + "/developer")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := readBody(t, resp)

	require.Contains(t, body, "接入凭据")
	require.Contains(t, body, `id="credentials"`)
	// 展示真实 app_key/domain（读回自 GetApplication，有齿：钉死种子值）。
	require.Contains(t, body, dbtest.SeedAppKey) // "AK_order"
	require.Contains(t, body, dbtest.SeedDomain) // "order-system"
	// 轮换凭据入口复用既有 POST /apps/{id}/rotate-secret 流程（含 CSRF；GET-only 链接会 405，故用 POST 表单）。
	require.Contains(t, body, `action="/apps/`+strconv.FormatInt(appID, 10)+`/rotate-secret"`)
	// SD-1：绝不渲染 secret（响应体无 app_secret 字面；ApplicationSummary 类型层无 secret 字段）。
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
