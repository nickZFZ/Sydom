package console

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// 有会话、无查询参数 → 200 渲染表单（单 h1 + breadcrumb + 表单三控件）。
func TestConsole_DataSandbox_RendersForm(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	resp, err := c.Get(ts.URL + "/apps/" + strconv.FormatInt(appID, 10) + "/data-sandbox")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := readBody(t, resp)

	require.Equal(t, 1, strings.Count(body, "<h1"), "须单 h1")
	require.Contains(t, body, "数据权限沙箱")
	require.Contains(t, body, `name="subject"`)
	require.Contains(t, body, `name="resource"`)
	require.Contains(t, body, `name="attrs"`)
}

// 有会话 + 三参齐备 → 200 渲染数据面同一渲染器产出的参数化 WHERE + args。
func TestConsole_DataSandbox_PreviewsSQL(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	dom := dbtest.SeedDomain
	insertCasbinRuleC(t, db, appID, "g", "alice", "viewer", dom) // alice→viewer
	_, err := db.Exec(
		`INSERT INTO data_policy (app_id,subject_type,subject_id,resource,condition,effect,version)
		 VALUES ($1,'role','viewer','order',$2::jsonb,'allow',1)`,
		appID, `{"op":"EQ","field":"dept","value":"$user.dept"}`)
	require.NoError(t, err)
	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	resp, err := c.Get(ts.URL + "/apps/" + strconv.FormatInt(appID, 10) +
		"/data-sandbox?subject=alice&resource=order&attrs=dept%3Dshanghai")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := readBody(t, resp)
	require.Contains(t, body, "dept = ?") // 渲染出的参数化 WHERE
	require.Contains(t, body, "shanghai") // 值进 args
}

// 无会话 → 303 去登录。
func TestConsole_DataSandbox_RequiresSession(t *testing.T) {
	ts, _, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	anon := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := anon.Get(ts.URL + "/apps/" + strconv.FormatInt(appID, 10) + "/data-sandbox")
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
}
