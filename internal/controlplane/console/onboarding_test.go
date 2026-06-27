package console

import (
	"net/http"
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
