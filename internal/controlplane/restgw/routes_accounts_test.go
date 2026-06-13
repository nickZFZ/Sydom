package restgw_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

// TestREST_RegisterTenant_NoAuth 验证 /v1/tenants（RegisterTenant）免鉴权可达：无 HMAC 头也返回 200 + 一次性凭据。
func TestREST_RegisterTenant_NoAuth(t *testing.T) {
	ts, _ := newTestGW(t)

	body := `{"tenantName":"acme","ownerPrincipal":"owner"}`
	resp, err := http.Post(ts.URL+"/v1/tenants", "application/json", strings.NewReader(body)) // 无 HMAC 头
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "RegisterTenant 必须免鉴权可达")

	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var out adminv1.RegisterTenantResponse
	require.NoError(t, protojson.Unmarshal(raw, &out))
	require.NotZero(t, out.TenantId)
	require.NotEmpty(t, out.OwnerSecret) // 一次性明文
}

// TestREST_ListMembers_RequiresAuth 验证已认证路由仍需 HMAC：无凭据 → 401。
func TestREST_ListMembers_RequiresAuth(t *testing.T) {
	ts, _ := newTestGW(t)
	resp, err := http.Get(ts.URL + "/v1/tenants/1/members") // 无 HMAC 头
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "非豁免路由无凭据必须 401")
}
