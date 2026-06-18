package restgw_test

import (
	"net/http"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestREST_ExplainDecision_OK(t *testing.T) {
	ts, db := newTestGW(t)
	c := rootClient(t, ts.URL)
	appID := uint64(dbtest.SeedApp(t, db))
	// 直插 grant：manager 可 read orders；alice→manager。
	dom := dbtest.SeedDomain
	_, err := db.Exec(`INSERT INTO casbin_rule (app_id,ptype,v0,v1,v2,v3,v4,v5,version) VALUES ($1,'p','manager',$2,'orders','read','allow','',1)`, int64(appID), dom)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO casbin_rule (app_id,ptype,v0,v1,v2,v3,v4,v5,version) VALUES ($1,'g','alice','manager',$2,'','','',1)`, int64(appID), dom)
	require.NoError(t, err)

	resp, body := c.do("GET", "/v1/apps/"+u(appID)+"/decision?user_id=alice&resource=orders&action=read", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var out adminv1.ExplainDecisionResponse
	require.NoError(t, protoUnmarshal(body, &out))
	require.True(t, out.Allowed)
	require.Equal(t, "ALLOW_GRANTED", out.Reason)
	require.Equal(t, "manager", out.DecidingRole)
}

// app_id 取自 path（路径权威）；user_id 空 → 400 InvalidArgument。
func TestREST_ExplainDecision_MissingUser_400(t *testing.T) {
	ts, db := newTestGW(t)
	c := rootClient(t, ts.URL)
	appID := uint64(dbtest.SeedApp(t, db))
	resp, body := c.do("GET", "/v1/apps/"+u(appID)+"/decision?resource=orders&action=read", nil)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, string(body))
}

// 受 HMAC 保护：无凭据 → 401。
func TestREST_ExplainDecision_NoAuth(t *testing.T) {
	ts, db := newTestGW(t)
	appID := uint64(dbtest.SeedApp(t, db))
	resp, err := http.Get(ts.URL + "/v1/apps/" + u(appID) + "/decision?user_id=alice&resource=orders&action=read")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}
