package console

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// TestConsole_AppAudit_RequiresSessionThenRenders 验证 GET /apps/{id}/audit：
// - 无会话 → 302 去登录；
// - 有会话 + 直插一条审计记录 → 200 且 body 含动作文本。
func TestConsole_AppAudit_RequiresSessionThenRenders(t *testing.T) {
	// 无会话 → 302
	ts, _, _ := newConsole(t)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(ts.URL + "/apps/1/audit")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)

	// 有会话 + 审计记录 → 200
	ts2, store2, db2 := newConsole(t)
	appID := dbtest.SeedApp(t, db2)
	// 直插一条审计记录（diff 传 []byte("{}") 不传 nil，pq JSONB 不接受 nil）
	err = store.InsertAudit(context.Background(), db2, appID,
		"root@sydom", "create", "role", "1", []byte(`{}`), 1)
	require.NoError(t, err)
	c, _ := loginAndCSRF(t, ts2, store2, "root@sydom", "rootsecret")
	resp2, err := c.Get(ts2.URL + "/apps/" + strconv.FormatInt(appID, 10) + "/audit")
	require.NoError(t, err)
	defer resp2.Body.Close()
	require.Equal(t, http.StatusOK, resp2.StatusCode)
	require.Contains(t, readBody(t, resp2), "create")
}

// TestConsole_AdminAudit_Renders 验证 GET /admin/audit：超管会话 → 200。
func TestConsole_AdminAudit_Renders(t *testing.T) {
	ts, store, _ := newConsole(t)
	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	resp, err := c.Get(ts.URL + "/admin/audit")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, readBody(t, resp), "管理审计")
}
