package console

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestConsole_RegisterPage_PublicGET：注册页公开可达（无需会话）。
func TestConsole_RegisterPage_PublicGET(t *testing.T) {
	ts, _, _ := newConsole(t)
	resp, err := http.Get(ts.URL + "/register")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode) // 公开，无需会话
}

// TestConsole_RegisterPost_CreatesTenant：POST /register 免鉴权建租户，渲染一次性凭据页（非 PRG）。
func TestConsole_RegisterPost_CreatesTenant(t *testing.T) {
	ts, _, _ := newConsole(t)
	form := url.Values{"tenant_name": {"acme"}, "owner_principal": {"owner1"}}
	resp, err := http.Post(ts.URL+"/register", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := readBody(t, resp)
	require.Contains(t, body, "owner1") // 渲染管理员标识
	require.Contains(t, body, "仅显示这一次") // 一次性凭据强警示文案
}

// TestConsole_Members_RequiresSession：成员页无会话 → 302 去 /login。
func TestConsole_Members_RequiresSession(t *testing.T) {
	ts, _, _ := newConsole(t)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(ts.URL + "/tenants/1/members")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
}
