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

// TestConsole_PreviewCondition 验证 POST /apps/{id}/data-policies/preview-condition：
// 服务端复用 dataperm.ValidateCondition 校验 + conditionPredicate 渲染，单一真相源
// （与写时校验、数据面 eval 同文法），且预览幂等只读——不落库/不 bump/不写审计。
func TestConsole_PreviewCondition(t *testing.T) {
	ts, rstore, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, rstore, "root@sydom", "rootsecret")
	path := ts.URL + fmt.Sprintf("/apps/%d/data-policies/preview-condition", appID)

	// ① 合法条件 → 200 + body 含渲染后的符号谓词。
	resp, err := c.PostForm(path, url.Values{
		"csrf_token": {csrf},
		"condition":  {`{"op":"AND","children":[{"field":"dept","op":"EQ","value":"$user.dept"}]}`},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, readBody(t, resp), "dept = $user.dept")

	// ② 非法条件（未知算子）→ 200（业务性错误内联展示，非服务器错误）+ body 含错误信息。
	resp2, err := c.PostForm(path, url.Values{
		"csrf_token": {csrf},
		"condition":  {`{"op":"ALL"}`},
	})
	require.NoError(t, err)
	require.NotEqual(t, http.StatusInternalServerError, resp2.StatusCode)
	require.Contains(t, readBody(t, resp2), "算子")

	// ③ 无 csrf_token → CSRF 校验失败（PermissionDenied → 403，同 TestRoles_CSRFMissing_Forbidden 断言风格）。
	resp3, err := c.PostForm(path, url.Values{
		"condition": {`{"op":"ALL"}`},
	})
	require.NoError(t, err)
	require.NotEqual(t, http.StatusOK, resp3.StatusCode)
	require.Equal(t, http.StatusForbidden, resp3.StatusCode)

	// ④ 未登录会话 → requireSession 挡，302 去 /login（同 TestDashboard_NoSession_RedirectsLogin 断言风格）。
	anon := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp4, err := anon.PostForm(path, url.Values{
		"csrf_token": {"whatever"},
		"condition":  {`{"op":"ALL"}`},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp4.StatusCode)
	require.Equal(t, "/login", resp4.Header.Get("Location"))

	// ⑤ 只读不落库：以上多次预览请求后，该 app 的数据策略表仍为空。
	after, err := store.ReadAppDataPolicies(context.Background(), db, appID)
	require.NoError(t, err)
	require.Len(t, after, 0)
}
