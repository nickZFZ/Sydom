package console

import (
	"database/sql"
	"net/http"
	"strconv"
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func insertCasbinRuleC(t *testing.T, db *sql.DB, appID int64, ptype string, v ...string) {
	t.Helper()
	var c [6]string
	copy(c[:], v)
	_, err := db.Exec(
		`INSERT INTO casbin_rule (app_id, ptype, v0, v1, v2, v3, v4, v5, version)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,1)`,
		appID, ptype, c[0], c[1], c[2], c[3], c[4], c[5])
	require.NoError(t, err)
}

// 无会话 → 302/303 去登录。
func TestConsole_Decision_NoSession_Redirects(t *testing.T) {
	ts, _, _ := newConsole(t)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(ts.URL + "/apps/1/decision")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
}

// 有会话、无查询参数 → 200 渲染表单。
func TestConsole_Decision_RendersForm(t *testing.T) {
	ts, store, _ := newConsole(t)
	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	resp, err := c.Get(ts.URL + "/apps/1/decision")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, readBody(t, resp), "决策解释器")
}

// 有会话 + 有效查询 → 200 显示 ALLOW + reason。
func TestConsole_Decision_ShowsAllow(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	dom := dbtest.SeedDomain
	insertCasbinRuleC(t, db, appID, "p", "manager", dom, "orders", "read", "allow")
	insertCasbinRuleC(t, db, appID, "g", "alice", "manager", dom)
	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	resp, err := c.Get(ts.URL + "/apps/" + strconv.FormatInt(appID, 10) + "/decision?user_id=alice&resource=orders&action=read")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := readBody(t, resp)
	require.Contains(t, body, "ALLOW")
	require.Contains(t, body, "ALLOW_GRANTED")
}
