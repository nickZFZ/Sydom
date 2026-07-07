package restgw_test

import (
	"net/http"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// GET /v1/applications/{app_id} → 200，含 app_key，绝不含 secret。
func TestREST_GetApplication_OK(t *testing.T) {
	ts, db := newTestGW(t)
	c := rootClient(t, ts.URL)
	appID := uint64(dbtest.SeedApp(t, db))
	resp, body := c.do("GET", "/v1/applications/"+u(appID), nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var out adminv1.GetApplicationResponse
	require.NoError(t, protoUnmarshal(body, &out))
	require.Equal(t, appID, out.Application.AppId)
	require.Equal(t, dbtest.SeedAppKey, out.Application.AppKey) // "AK_order"
	require.Equal(t, dbtest.SeedDomain, out.Application.Domain) // "order-system"
	require.NotContains(t, string(body), "secret")              // SD-1：响应体绝不含 secret
}

func TestREST_GetApplication_NoAuth(t *testing.T) {
	ts, db := newTestGW(t)
	appID := uint64(dbtest.SeedApp(t, db))
	resp, err := http.Get(ts.URL + "/v1/applications/" + u(appID))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// POST /v1/apps/{app_id}/data-filter/preview → 200，含 sql/args。
func TestREST_PreviewDataFilter_OK(t *testing.T) {
	ts, db := newTestGW(t)
	c := rootClient(t, ts.URL)
	appID := uint64(dbtest.SeedApp(t, db))
	dom := dbtest.SeedDomain
	_, err := db.Exec(`INSERT INTO casbin_rule (app_id,ptype,v0,v1,v2,v3,v4,v5,version) VALUES ($1,'g','alice','viewer',$2,'','','',1)`, int64(appID), dom)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO data_policy (app_id,subject_type,subject_id,resource,condition,effect,version) VALUES ($1,'role','viewer','order',$2::jsonb,'allow',1)`,
		int64(appID), `{"op":"EQ","field":"dept","value":"$user.dept"}`)
	require.NoError(t, err)

	reqBody := map[string]any{"subject": "alice", "resource": "order", "attrs": map[string]string{"dept": "shanghai"}}
	resp, body := c.do("POST", "/v1/apps/"+u(appID)+"/data-filter/preview", reqBody)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var out adminv1.PreviewDataFilterResponse
	require.NoError(t, protoUnmarshal(body, &out))
	require.Equal(t, "dept = ?", out.Sql)
	require.Equal(t, []string{"shanghai"}, out.Args)
}

func TestREST_PreviewDataFilter_NoAuth(t *testing.T) {
	ts, db := newTestGW(t)
	appID := uint64(dbtest.SeedApp(t, db))
	resp, err := http.Post(ts.URL+"/v1/apps/"+u(appID)+"/data-filter/preview", "application/json", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}
