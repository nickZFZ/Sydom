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
