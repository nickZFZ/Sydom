package console

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// getOK 取页面、断言 200 + 恰一个 <h1> + 含 breadcrumb。
func getOK(t *testing.T, c *http.Client, url string) string {
	t.Helper()
	resp, err := c.Get(url)
	require.NoError(t, err)
	body := readBody(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, url)
	require.Equal(t, 1, strings.Count(body, "<h1>"), url+" 应恰一个 <h1>")
	require.Contains(t, body, `class="breadcrumb"`, url+" 应含 breadcrumb")
	return body
}

func TestOnboarding_SelectAndDone(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	a := strconv.FormatInt(appID, 10)
	sel := getOK(t, c, ts.URL+"/ops/apps/"+a+"/onboarding")
	require.Contains(t, sel, "通用后台管理")
	require.Contains(t, sel, "推荐")
	require.Contains(t, sel, "一键起步") // general-admin intro 片段
	done := getOK(t, c, ts.URL+"/ops/apps/"+a+"/onboarding/done?template_id=general-admin")
	require.Contains(t, done, "接下来")
}

func TestOnboarding_AssignFormAndBind(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	a := strconv.FormatInt(appID, 10)
	// 先 bootstrap 建出角色（直接调既有模板应用路由，幂等）
	_, err := c.PostForm(ts.URL+"/ops/apps/"+a+"/onboarding/apply",
		url.Values{"csrf_token": {csrf}, "template_id": {"general-admin"}})
	require.NoError(t, err)
	// 分配表单：应渲染角色下拉（业务名「管理员」），单 h1 + breadcrumb
	form := getOK(t, c, ts.URL+"/ops/apps/"+a+"/onboarding/assign?template_id=general-admin")
	require.Contains(t, form, "管理员")
	require.Contains(t, form, "跳过") // 可跳过链接
}

func TestOnboarding_BannerWhenEmptyGoneWhenSeeded(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	a := strconv.FormatInt(appID, 10)
	// 空 app：业务角色页应含引导横幅（"开始引导" 是横幅按钮文案，仅来自横幅）
	empty := readBody(t, mustGet(t, c, ts.URL+"/ops/apps/"+a+"/roles"))
	require.Contains(t, empty, "开始引导", "空 app 应显示引导横幅")
	require.Contains(t, empty, "data-onboarding-banner", "空 app 应显示引导横幅锚")
	// bootstrap 后非空：横幅消失
	_, err := c.PostForm(ts.URL+"/ops/apps/"+a+"/onboarding/apply",
		url.Values{"csrf_token": {csrf}, "template_id": {"general-admin"}})
	require.NoError(t, err)
	seeded := readBody(t, mustGet(t, c, ts.URL+"/ops/apps/"+a+"/roles"))
	require.NotContains(t, seeded, "data-onboarding-banner", "非空 app 不应显示引导横幅")
}

// TestOnboarding_AssignBindsAndRedirectsToDone 覆盖分配步骤 POST 主路径（任务3 审查 次要2 补）。
func TestOnboarding_AssignBindsAndRedirectsToDone(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	a := strconv.FormatInt(appID, 10)
	_, err := c.PostForm(ts.URL+"/ops/apps/"+a+"/onboarding/apply",
		url.Values{"csrf_token": {csrf}, "template_id": {"general-admin"}})
	require.NoError(t, err)
	form := getOK(t, c, ts.URL+"/ops/apps/"+a+"/onboarding/assign?template_id=general-admin")
	m := regexp.MustCompile(`<option value="(\d+)"`).FindStringSubmatch(form)
	require.NotNil(t, m, "assign 表单应含至少一个角色 option")
	// loginAndCSRF 的 client 用 ErrUseLastResponse 不跟随重定向；复用同 jar 建跟随 client
	// 落到 PRG 目标页（既有 flash_test.go/confirm_test.go 同惯用法）。
	cf := &http.Client{Jar: c.Jar}
	resp, err := cf.PostForm(ts.URL+"/ops/apps/"+a+"/onboarding/assign",
		url.Values{"csrf_token": {csrf}, "template_id": {"general-admin"}, "user_id": {"alice@corp"}, "role_id": {m[1]}})
	require.NoError(t, err)
	require.Contains(t, resp.Request.URL.Path, "/onboarding/done", "分配成功应重定向到完成页")
	require.Contains(t, readBody(t, resp), "引导完成")
}

func mustGet(t *testing.T, c *http.Client, url string) *http.Response {
	t.Helper()
	resp, err := c.Get(url)
	require.NoError(t, err)
	return resp
}
