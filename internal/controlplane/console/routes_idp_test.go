package console

import (
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func idpGetBody(t *testing.T, c *http.Client, url string) string {
	t.Helper()
	resp, err := c.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(b)
}

func TestIdPConfigPage_RenderAndSave(t *testing.T) {
	ts, store, db := newConsole(t)
	var tid int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant (name) VALUES ('idp-ui') RETURNING id`).Scan(&tid))
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	base := ts.URL + "/tenants/" + strconv.FormatInt(tid, 10) + "/idp"

	// 未配置 GET：200，表单无 secret。
	body := idpGetBody(t, c, base)
	require.Contains(t, body, "client_id")
	require.NotContains(t, strings.ToLower(body), "s3cr3t")

	// POST 新建（带 secret）。client c 不跟随重定向（loginAndCSRF 已设 ErrUseLastResponse）。
	form := url.Values{"csrf_token": {csrf}, "issuer": {"https://idp"}, "client_id": {"cid"},
		"client_secret": {"s3cr3t"}, "domains": {"acme.com\n  \nfoo.com"}, "enabled": {"on"}, "jit_enabled": {"on"}}
	resp, err := c.PostForm(base, form)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)

	var enc1 []byte
	var jit bool
	require.NoError(t, db.QueryRow(`SELECT client_secret_enc, jit_enabled FROM tenant_idp WHERE tenant_id=$1`, tid).Scan(&enc1, &jit))
	require.True(t, jit)
	var domainCount int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM tenant_idp_domain WHERE tenant_id=$1`, tid).Scan(&domainCount))
	require.Equal(t, 2, domainCount, "空行须被丢弃（acme.com/foo.com）")

	// GET 已配置：预填 issuer/域，仍无 secret。
	body2 := idpGetBody(t, c, base)
	require.Contains(t, body2, "https://idp")
	require.Contains(t, body2, "acme.com")
	require.NotContains(t, strings.ToLower(body2), "s3cr3t")

	// POST 编辑（空 secret，关 jit）→ 密文保留、jit 关。
	form2 := url.Values{"csrf_token": {csrf}, "issuer": {"https://idp"}, "client_id": {"cid"},
		"client_secret": {""}, "domains": {"acme.com\nfoo.com"}, "enabled": {"on"}}
	resp2, err := c.PostForm(base, form2)
	require.NoError(t, err)
	resp2.Body.Close()
	require.Equal(t, http.StatusSeeOther, resp2.StatusCode)

	var enc2 []byte
	require.NoError(t, db.QueryRow(`SELECT client_secret_enc, jit_enabled FROM tenant_idp WHERE tenant_id=$1`, tid).Scan(&enc2, &jit))
	require.Equal(t, enc1, enc2, "空 secret 编辑须保留密文")
	require.False(t, jit, "jit 应被关闭")
}
