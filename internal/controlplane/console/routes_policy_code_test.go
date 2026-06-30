package console

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// minimalPolicyYAML 是测试用最小合法策略文档。
const minimalPolicyYAML = `apiVersion: sydom.policy/v1
permissions:
  - code: doc.read
    resource: doc
    action: read
    type: api
    name: 读文档
roles:
  - key: reader
    name: 阅读者
    permission_codes: [doc.read]
`

// TestPolicyCode_GetPage 验证 GET /apps/{app_id}/policy-code → 200，
// body 含策略即代码标题、导出 action、import textarea、恰好一个 <h1、breadcrumb。
func TestPolicyCode_GetPage(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	resp, err := c.Get(ts.URL + fmt.Sprintf("/apps/%d/policy-code", appID))
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)

	body := readBody(t, resp)
	require.Contains(t, body, "策略即代码")
	require.Contains(t, body, fmt.Sprintf("/apps/%d/policy-code/export", appID))
	require.Contains(t, body, `<textarea`)
	require.Contains(t, body, `name="content"`)
	require.Contains(t, body, "breadcrumb")

	// 恰好一个 <h1
	count := strings.Count(body, "<h1")
	require.Equal(t, 1, count, "页面应恰好有一个 <h1 元素，实际有 %d 个", count)
}

// TestPolicyCode_ImportPreview 验证 POST import dry-run → 200，body 含变更预览特征。
func TestPolicyCode_ImportPreview(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	form := url.Values{
		"csrf_token": {csrf},
		"content":    {minimalPolicyYAML},
		// 不带 confirmed → dry_run=true → 预览
	}
	resp, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/policy-code/import", appID), form)
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)

	body := readBody(t, resp)
	require.Contains(t, body, "变更预览")
	// diff 表中应有 doc.read 或 reader 之类的内容（新建条目）
	require.True(t,
		strings.Contains(body, "doc.read") || strings.Contains(body, "reader") || strings.Contains(body, "新建") || strings.Contains(body, "create"),
		"预览页应含 diff 条目相关内容，实际 body：%s", body)
}

// TestPolicyCode_ImportConfirm_PRG 验证 POST import confirmed=1 → 303，Location 指向 policy-code 页；
// 随后查库确认 permission 落库。
func TestPolicyCode_ImportConfirm_PRG(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	form := url.Values{
		"csrf_token": {csrf},
		"content":    {minimalPolicyYAML},
		"confirmed":  {"1"},
	}
	resp, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/policy-code/import", appID), form)
	require.NoError(t, err)
	require.Equal(t, 303, resp.StatusCode)
	require.Equal(t, fmt.Sprintf("/apps/%d/policy-code", appID), resp.Header.Get("Location"))

	// 查库确认 permission 已落库
	var count int
	require.NoError(t, db.QueryRow(
		`SELECT COUNT(*) FROM permission WHERE app_id=$1 AND code='doc.read'`, appID).Scan(&count))
	require.Equal(t, 1, count, "doc.read 权限点应已落库")
}

// TestPolicyCode_Export_Download 验证 GET export → 200，
// Content-Disposition 含 attachment，body 非空且不含 app 的 access key（PC-2）。
func TestPolicyCode_Export_Download(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	resp, err := c.Get(ts.URL + fmt.Sprintf("/apps/%d/policy-code/export?format=yaml", appID))
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)

	cd := resp.Header.Get("Content-Disposition")
	require.Contains(t, cd, "attachment", "Content-Disposition 应含 attachment")

	body := readBody(t, resp)
	require.NotEmpty(t, body, "导出内容不应为空")
	// PC-2：不含 app 的 access key
	require.NotContains(t, body, dbtest.SeedAppKey, "导出内容不应包含 app access key")
}
