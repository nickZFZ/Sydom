package console

import (
	"fmt"
	"net/url"
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// TestConsole_Templates_ListAndPreview 验证运营台模板库列表：
// 返回 200；body 含内置模板业务名（通用后台管理、电商运营）；
// 含权限点业务名（查看订单）；不漏技术原语（order:read）。
func TestConsole_Templates_ListAndPreview(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	page, err := c.Get(ts.URL + fmt.Sprintf("/ops/apps/%d/templates", appID))
	require.NoError(t, err)
	require.Equal(t, 200, page.StatusCode)
	body := readBody(t, page)

	// 内置模板名（bizterm 业务名）
	require.Contains(t, body, "通用后台管理")
	require.Contains(t, body, "电商运营")

	// 权限点业务名（内置模板含「查看订单」能力名）
	require.Contains(t, body, "查看订单")

	// 绝不渲染技术原语（TP-8）
	require.NotContains(t, body, "order:read")
}

// TestConsole_ApplyTemplate_CSRF 验证 CSRF 管线：
// 缺 csrf_token → 403；带正确 token + template_id → 200，body 含摘要计数。
func TestConsole_ApplyTemplate_CSRF(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	applyURL := ts.URL + fmt.Sprintf("/ops/apps/%d/templates/apply", appID)

	// 缺 csrf_token → 403
	badForm := url.Values{"template_id": {"general-admin"}}
	resp, err := c.PostForm(applyURL, badForm)
	require.NoError(t, err)
	require.Equal(t, 403, resp.StatusCode)

	// 正确 csrf_token → 200，body 含摘要计数关键词
	goodForm := url.Values{
		"csrf_token":  {csrf},
		"template_id": {"general-admin"},
	}
	resp2, err := c.PostForm(applyURL, goodForm)
	require.NoError(t, err)
	require.Equal(t, 200, resp2.StatusCode)
	body := readBody(t, resp2)
	// 摘要页含「新建」（RolesCreated > 0）或计数关键词
	require.Contains(t, body, "新建")
}

// TestConsole_ApplyTemplate_Idempotent 验证幂等语义：
// 连续 apply 两次同 template_id；第二次摘要显示角色全部「跳过」（已存在）。
func TestConsole_ApplyTemplate_Idempotent(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	applyURL := ts.URL + fmt.Sprintf("/ops/apps/%d/templates/apply", appID)
	form := url.Values{
		"csrf_token":  {csrf},
		"template_id": {"general-admin"},
	}

	// 第一次 apply
	resp1, err := c.PostForm(applyURL, form)
	require.NoError(t, err)
	require.Equal(t, 200, resp1.StatusCode)
	readBody(t, resp1) // 消费 body

	// 第二次 apply：同一 client（csrf 不变，幂等）
	resp2, err := c.PostForm(applyURL, form)
	require.NoError(t, err)
	require.Equal(t, 200, resp2.StatusCode)
	body2 := readBody(t, resp2)

	// 第二次摘要：角色应全部跳过（已存在），新建数为 0
	require.Contains(t, body2, "跳过")
	require.NotContains(t, body2, "新建 1") // RolesCreated=0，不应出现「新建 1」
}
