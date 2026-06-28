package console

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// 建角色页的权限点选项必须渲业务名（capabilityName），缺名也不得出现裸 resource:action。
func TestOpsRoleNew_NoNakedPrimitive(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	// 直插一个无 name 的权限点，触发 capabilityName 合成「resource · 动词」。
	_, err := db.Exec(`INSERT INTO permission (app_id, code, resource, action, type, name)
		VALUES ($1,'order:read','order','read','data','')`, appID)
	require.NoError(t, err)
	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	resp, err := c.Get(ts.URL + "/ops/apps/" + strconv.FormatInt(appID, 10) + "/roles/new")
	require.NoError(t, err)
	body := readBody(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NotContains(t, body, "order:read", "缺名权限点不得渲裸 resource:action")
	require.Contains(t, body, "order · ", "应渲 capabilityName 合成的「resource · 动词」")
}

func TestOpsTemplates_SingleH1(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	resp, err := c.Get(ts.URL + "/ops/apps/" + strconv.FormatInt(appID, 10) + "/templates")
	require.NoError(t, err)
	body := readBody(t, resp)
	require.Equal(t, 1, strings.Count(body, "<h1"), "模板库页应恰一个 h1")
}
