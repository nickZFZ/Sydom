package console

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// 渲染有齿：free 套餐/used=1/limit=3，钉死可见数字 + meter 属性 + 未达上限不含告警（双向）。
func TestConsole_UsagePage(t *testing.T) {
	ts, store, db := newConsole(t)
	tid, _ := dbtest.SeedAppInTenant(t, db, "usage-acme", "usage-app", "AK_usage")
	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	resp, err := c.Get(ts.URL + "/tenants/" + strconv.FormatInt(tid, 10) + "/usage")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := readBody(t, resp)

	require.Equal(t, 1, strings.Count(body, "<h1>"), "须单 h1")
	require.Contains(t, body, "免费版")       // planLabel(free)
	require.Contains(t, body, "应用：1 / 3")  // 应用行（used=1 limit=3，有齿）
	require.Contains(t, body, "成员：0 / 3")  // 成员行（SeedAppInTenant 无 membership）
	require.Contains(t, body, "<meter")    // 原生 meter（CSP 安全可视化）
	require.Contains(t, body, `value="1"`) // 有齿：钉死应用用量
	require.Contains(t, body, `max="3"`)   // 有齿：钉死套餐上限
	require.NotContains(t, body, "应用已达上限") // 未达上限：不含告警（双向有齿）
}

// 至上限告警有齿：插满 free 上限（3）→ 出现 at-limit 告警 + value="3"。
func TestConsole_UsagePage_AtLimitWarning(t *testing.T) {
	ts, store, db := newConsole(t)
	tid, _ := dbtest.SeedAppInTenant(t, db, "usage-full", "usage-full-1", "AK_full1")
	// 再插 2 应用达 free 上限 3（distinct domain 避 uq_tenant_domain；app_key 全局唯一）。
	_, err := db.Exec(`INSERT INTO application (tenant_id, domain, name, app_key, app_secret_enc)
	  VALUES ($1,'usage-full-2','app2','AK_full2','\xab'::bytea),
	         ($1,'usage-full-3','app3','AK_full3','\xab'::bytea)`, tid)
	require.NoError(t, err)
	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	resp, err := c.Get(ts.URL + "/tenants/" + strconv.FormatInt(tid, 10) + "/usage")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := readBody(t, resp)

	require.Contains(t, body, "应用：3 / 3")
	require.Contains(t, body, `value="3"`)
	require.Contains(t, body, "应用已达上限")    // 应用至上限告警（有齿）
	require.NotContains(t, body, "成员已达上限") // 成员 0/3 未达上限（跨维度双向有齿）
}

func TestConsole_UsagePage_RequiresSession(t *testing.T) {
	ts, _, db := newConsole(t)
	tid, _ := dbtest.SeedAppInTenant(t, db, "usage-anon", "usage-anon-app", "AK_anon")
	anon := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := anon.Get(ts.URL + "/tenants/" + strconv.FormatInt(tid, 10) + "/usage")
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode) // requireSession 302 /login
}

func TestConsole_UsagePage_UnknownTenant(t *testing.T) {
	ts, store, _ := newConsole(t)
	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	resp, err := c.Get(ts.URL + "/tenants/999999/usage")
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, resp.StatusCode) // 未知租户 → GetTenantUsage NotFound 404
}
