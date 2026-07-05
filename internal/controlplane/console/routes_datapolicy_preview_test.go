package console

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// TestDataPolicy_PreviewCondition 验证 POST /apps/{id}/data-policies/preview-condition：
// 服务端复用 dataperm.ValidateCondition 校验 + conditionPredicate 渲染，单一真相源
// （与写时校验、数据面 eval 同文法）；且本端点被前端 fetch → resp.json() 消费，
// 故所有分支（鉴权/校验成功/校验失败）都必须返回 JSON（锁 Content-Type + 状态码契约）。
// 预览幂等只读——不落库/不 bump/不写审计。
func TestDataPolicy_PreviewCondition(t *testing.T) {
	ts, rstore, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, rstore, "root@sydom", "rootsecret")
	path := ts.URL + fmt.Sprintf("/apps/%d/data-policies/preview-condition", appID)

	// ① 合法条件 → 200 + JSON body 含渲染后的符号谓词。
	resp, err := c.PostForm(path, url.Values{
		"csrf_token": {csrf},
		"condition":  {`{"op":"AND","children":[{"field":"dept","op":"EQ","value":"$user.dept"}]}`},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Content-Type"), "application/json")
	require.Contains(t, readBody(t, resp), "dept = $user.dept")

	// ② 非法条件（未知算子）→ 恒 200（校验失败是业务结果内联展示，非服务器错误）+ body 含错误信息。
	resp2, err := c.PostForm(path, url.Values{
		"csrf_token": {csrf},
		"condition":  {`{"op":"ALL"}`},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp2.StatusCode)
	require.Contains(t, readBody(t, resp2), "算子")

	// ③ 无 csrf_token → 403 且响应仍是 JSON（不走 renderError 回 HTML；契约保护）。
	resp3, err := c.PostForm(path, url.Values{
		"condition": {`{"op":"ALL"}`},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, resp3.StatusCode)
	require.Contains(t, resp3.Header.Get("Content-Type"), "application/json")
	require.Contains(t, readBody(t, resp3), "error")

	// ④ 未登录会话 → 401 且响应仍是 JSON（不再 302 重定向到 HTML 登录页；fetch 消费契约的关键）。
	anon := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp4, err := anon.PostForm(path, url.Values{
		"csrf_token": {"whatever"},
		"condition":  {`{"op":"ALL"}`},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, resp4.StatusCode)
	require.Contains(t, resp4.Header.Get("Content-Type"), "application/json")
	require.Contains(t, readBody(t, resp4), "error")

	// ⑤ 只读不落库：以上多次预览请求后，该 app 的数据策略表仍为空。
	after, err := store.ReadAppDataPolicies(context.Background(), db, appID)
	require.NoError(t, err)
	require.Len(t, after, 0)
}
