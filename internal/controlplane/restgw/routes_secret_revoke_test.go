package restgw_test

import (
	"net/http"
	"strconv"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// TestREST_RotateApplicationSecret_OK：POST /v1/applications/{app_id}/secret → 200 + appSecret。
func TestREST_RotateApplicationSecret_OK(t *testing.T) {
	ts, db := newTestGW(t)
	c := rootClient(t, ts.URL)
	appID := uint64(dbtest.SeedApp(t, db))

	resp, body := c.do("POST", "/v1/applications/"+u(appID)+"/secret", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var rr adminv1.RotateApplicationSecretResponse
	require.NoError(t, protoUnmarshal(body, &rr))
	require.NotEmpty(t, rr.AppSecret)
}

// TestREST_RotateApplicationSecret_NoAuth：无 HMAC → 401。
func TestREST_RotateApplicationSecret_NoAuth(t *testing.T) {
	ts, db := newTestGW(t)
	appID := uint64(dbtest.SeedApp(t, db))
	resp, err := http.Post(ts.URL+"/v1/applications/"+u(appID)+"/secret", "application/json", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// TestREST_ResetOperatorSecret_OK：建 operator → POST /v1/operators/{operator_id}/secret → 200 + 新 secret。
func TestREST_ResetOperatorSecret_OK(t *testing.T) {
	ts, _ := newTestGW(t)
	c := rootClient(t, ts.URL)
	resp, body := c.do("POST", "/v1/operators", map[string]any{"principal": "rest-op"})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var co adminv1.CreateOperatorResponse
	require.NoError(t, protoUnmarshal(body, &co))

	resp, body = c.do("POST", "/v1/operators/"+i(co.OperatorId)+"/secret", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var rr adminv1.ResetOperatorSecretResponse
	require.NoError(t, protoUnmarshal(body, &rr))
	require.NotEmpty(t, rr.Secret)
	require.NotEqual(t, co.Secret, rr.Secret)
}

// TestREST_RevokeAdminGrant_RoundTripAndStrict404：建角色→授权→DELETE 撤权 200→重复 DELETE 404。
func TestREST_RevokeAdminGrant_RoundTripAndStrict404(t *testing.T) {
	ts, db := newTestGW(t)
	c := rootClient(t, ts.URL)
	appID := uint64(dbtest.SeedApp(t, db))
	domain := strconv.FormatUint(appID, 10)

	resp, body := c.do("POST", "/v1/admin-roles", map[string]any{"code": "rest-role", "name": "n"})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var cr adminv1.CreateAdminRoleResponse
	require.NoError(t, protoUnmarshal(body, &cr))

	resp, body = c.do("POST", "/v1/admin-roles/"+i(cr.RoleId)+"/grants",
		map[string]any{"domain": domain, "resource": "role", "action": "create"})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	// 撤权（DELETE + query）。
	resp, body = c.do("DELETE", "/v1/admin-roles/"+i(cr.RoleId)+"/grants?domain="+domain+"&resource=role&action=create", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	// 重复撤 → 404（严格，不幂等）。
	resp, _ = c.do("DELETE", "/v1/admin-roles/"+i(cr.RoleId)+"/grants?domain="+domain+"&resource=role&action=create", nil)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// TestREST_UnbindOperatorRole_RoundTripAndStrict404：建 op→建角色→绑定→DELETE 解绑 200→重复 404。
func TestREST_UnbindOperatorRole_RoundTripAndStrict404(t *testing.T) {
	ts, db := newTestGW(t)
	c := rootClient(t, ts.URL)
	appID := uint64(dbtest.SeedApp(t, db))
	domain := strconv.FormatUint(appID, 10)

	resp, body := c.do("POST", "/v1/operators", map[string]any{"principal": "rest-op2"})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var co adminv1.CreateOperatorResponse
	require.NoError(t, protoUnmarshal(body, &co))

	resp, body = c.do("POST", "/v1/admin-roles", map[string]any{"code": "rest-role2", "name": "n"})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var cr adminv1.CreateAdminRoleResponse
	require.NoError(t, protoUnmarshal(body, &cr))

	resp, body = c.do("POST", "/v1/operators/"+i(co.OperatorId)+"/roles",
		map[string]any{"roleId": strconv.FormatInt(cr.RoleId, 10), "domain": domain})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))

	resp, _ = c.do("DELETE", "/v1/operators/"+i(co.OperatorId)+"/roles/"+i(cr.RoleId)+"?domain="+domain, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	resp, _ = c.do("DELETE", "/v1/operators/"+i(co.OperatorId)+"/roles/"+i(cr.RoleId)+"?domain="+domain, nil)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}
