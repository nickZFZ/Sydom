package console

import (
	"net/http"
	"net/url"
	"strconv"
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// 未带 confirmed=1 的删除应渲服务端确认页、不执行删除。
func TestConfirm_DeleteDataPolicy_NoConfirmed_RendersConfirmPage(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	a := strconv.FormatInt(appID, 10)
	resp, err := c.PostForm(ts.URL+"/apps/"+a+"/data-policies/1/delete",
		url.Values{"csrf_token": {csrf}})
	require.NoError(t, err)
	body := readBody(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, body, "确定删除该数据策略吗？此操作不可撤销。")
}
