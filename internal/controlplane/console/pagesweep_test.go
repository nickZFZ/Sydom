package console

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// assertSweptPage 断言迁后页：恰一个 <h1> + 含 breadcrumb（error 页 wantCrumb=false）。
func assertSweptPage(t *testing.T, c *http.Client, url string, wantCrumb bool) {
	t.Helper()
	resp, err := c.Get(url)
	require.NoError(t, err)
	body := readBody(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, url)
	require.Equal(t, 1, strings.Count(body, "<h1>"), url+" 应恰一个 <h1>")
	if wantCrumb {
		require.Contains(t, body, `class="breadcrumb"`, url+" 应含 breadcrumb")
	}
}

func TestPageSweep_System(t *testing.T) {
	ts, store, db := newConsole(t)
	// members 需要真实 tenant_id；root 是 super-admin，可访问任意租户。
	tid, _ := dbtest.SeedAppInTenant(t, db, "sweep-tenant", "sweep-app", "AK_sweep")
	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	for _, p := range []string{
		"/admin-roles",
		"/admin/audit",
		"/operators",
		"/tenants",
		"/tenants/" + strconv.FormatInt(tid, 10) + "/members",
	} {
		assertSweptPage(t, c, ts.URL+p, true)
	}
}

func TestPageSweep_Forms(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	a := strconv.FormatInt(appID, 10)
	for _, p := range []string{
		"/apps/new",
		"/operators/new",
		"/register",
		"/ops/apps/" + a + "/roles/new",
	} {
		assertSweptPage(t, c, ts.URL+p, true)
	}
}

func TestPageSweep_Modeling(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	a := strconv.FormatInt(appID, 10)
	for _, p := range []string{
		"/apps/" + a + "/roles",
		"/apps/" + a + "/grants",
		"/apps/" + a + "/bindings",
		"/apps/" + a + "/inheritances",
		"/apps/" + a + "/data-policies",
		"/apps/" + a + "/audit",
		"/apps/" + a + "/decision",
		"/apps/" + a + "/effective",
	} {
		assertSweptPage(t, c, ts.URL+p, true)
	}
}

func TestPageSweep_OpsAndError(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	a := strconv.FormatInt(appID, 10)
	assertSweptPage(t, c, ts.URL+"/ops/apps/"+a+"/people", true)
	assertSweptPage(t, c, ts.URL+"/ops/apps/"+a+"/roles", true)
	// error 页：越权 app 的 effective → PermissionDenied → error.html（403），单一 h1、无 breadcrumb
	badID := strconv.FormatInt(appID+999999, 10)
	resp, err := c.Get(ts.URL + "/apps/" + badID + "/effective")
	require.NoError(t, err)
	body := readBody(t, resp)
	require.Equal(t, http.StatusForbidden, resp.StatusCode, "越权 app 应 403 error.html")
	require.Equal(t, 1, strings.Count(body, "<h1>"), "error 页应恰一个 <h1>")
	require.NotContains(t, body, `class="breadcrumb"`, "error 页不应含 breadcrumb")
}
