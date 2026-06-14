package console

import (
	"fmt"
	"net/url"
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// TestDataPolicyForm_DescriptionPersisted：提交数据策略时带 description，
// PRG 后 GET 列表页应回显该业务说明文本。
func TestDataPolicyForm_DescriptionPersisted(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	form := url.Values{
		"csrf_token":   {csrf},
		"id":           {"0"},
		"subject_type": {"role"},
		"subject_id":   {"sales"},
		"resource":     {"orders"},
		"effect":       {"allow"},
		"condition":    {`{"op":"EQ","field":"region","value":"east"}`},
		"description":  {"仅限本人区域的订单"},
	}
	resp, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/data-policies", appID), form)
	require.NoError(t, err)
	require.Equal(t, 303, resp.StatusCode)

	page, err := c.Get(ts.URL + fmt.Sprintf("/apps/%d/data-policies", appID))
	require.NoError(t, err)
	require.Contains(t, readBody(t, page), "仅限本人区域的订单")
}
