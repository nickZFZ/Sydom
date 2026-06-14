package console

import (
	"fmt"
	"net/url"
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// TestEffectivePermissions_NoUser_ShowsForm：GET /apps/{app_id}/effective（无 user_id 参数）
// → 200，body 含「有效权限」标题与 user_id 输入框（提示查询用户）。
func TestEffectivePermissions_NoUser_ShowsForm(t *testing.T) {
	ts, _, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c := loginClient(t, ts, "root@sydom", "rootsecret")

	resp, err := c.Get(ts.URL + fmt.Sprintf("/apps/%d/effective", appID))
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)
	body := readBody(t, resp)
	require.Contains(t, body, "有效权限")
	require.Contains(t, body, "user_id")
}

// TestEffectivePermissions_WithUser_ShowsResult：绑定 alice 到角色后，GET ?user_id=alice
// → 200，body 含被查 user_id 与「有效权限」相关标题。
func TestEffectivePermissions_WithUser_ShowsResult(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	roleID := mustCreateRole(t, c, ts, db, csrf, uint64(appID), "sales-mgr", "销售经理")

	// 绑定 alice 到 sales-mgr。
	form := url.Values{"csrf_token": {csrf}, "user_id": {"alice@corp"}, "role_id": {fmt.Sprint(roleID)}}
	resp, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/bindings", appID), form)
	require.NoError(t, err)
	require.Equal(t, 303, resp.StatusCode)

	// 查有效权限页。
	page, err := c.Get(ts.URL + fmt.Sprintf("/apps/%d/effective?user_id=alice@corp", appID))
	require.NoError(t, err)
	require.Equal(t, 200, page.StatusCode)
	body := readBody(t, page)
	require.Contains(t, body, "alice@corp")
	require.Contains(t, body, "有效权限")
}

// TestEffectivePermissions_WrongApp_NoEnumerate：用越权/不存在的 app_id 访问 effective 页
// → HTTP 非 200（403/404/500），body 不暴露跨域资源细节。
// 此处以 appID=999999999（不存在）模拟越权，AuthorizeRule 会拒绝（无该域授权）→ 403/error 页。
// 降级无枚举：body 绝不含 "alice@corp"（真实用户数据）或 "sales-mgr"（角色串）。
func TestEffectivePermissions_WrongApp_NoEnumerate(t *testing.T) {
	ts, store, db := newConsole(t)
	// 建两个 app；以受限操作员身份登录——只有 appA 授权，访问 appB 有效权限页。
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	// 在 appID 建角色绑 alice（只要数据存在即可）。
	roleID := mustCreateRole(t, c, ts, db, csrf, uint64(appID), "secret-role", "秘密角色")
	form := url.Values{"csrf_token": {csrf}, "user_id": {"alice@corp"}, "role_id": {fmt.Sprint(roleID)}}
	resp, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/bindings", appID), form)
	require.NoError(t, err)
	require.Equal(t, 303, resp.StatusCode)

	// 用无效/不存在的 appID（9999999999）访问 effective 页——不在任何租户域内。
	badID := uint64(9999999999)
	page, err := c.Get(ts.URL + fmt.Sprintf("/apps/%d/effective?user_id=alice@corp", badID))
	require.NoError(t, err)
	// 非 200（鉴权失败或 not found）。
	require.NotEqual(t, 200, page.StatusCode)
	body := readBody(t, page)
	// 降级无枚举：不泄露跨域资源。
	require.NotContains(t, body, "secret-role")
}

// TestEffectivePermissions_BindOnEffective_PRG：通过 effective 页绑定角色 → 303 PRG，
// 重定向到同一 effective 页面（含 user_id）。
func TestEffectivePermissions_BindOnEffective_PRG(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	roleID := mustCreateRole(t, c, ts, db, csrf, uint64(appID), "clerk", "店员")

	form := url.Values{
		"csrf_token": {csrf},
		"user_id":    {"bob@corp"},
		"role_id":    {fmt.Sprint(roleID)},
	}
	resp, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/effective/bind", appID), form)
	require.NoError(t, err)
	require.Equal(t, 303, resp.StatusCode)
	loc := resp.Header.Get("Location")
	require.Contains(t, loc, "effective")
	require.Contains(t, loc, "bob%40corp")
}

// TestEffectivePermissions_UnbindOnEffective_PRG：通过 effective 页解绑角色 → 303 PRG。
func TestEffectivePermissions_UnbindOnEffective_PRG(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	roleID := mustCreateRole(t, c, ts, db, csrf, uint64(appID), "clerk", "店员")

	// 先绑定。
	bindForm := url.Values{"csrf_token": {csrf}, "user_id": {"carol@corp"}, "role_id": {fmt.Sprint(roleID)}}
	r1, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/effective/bind", appID), bindForm)
	require.NoError(t, err)
	require.Equal(t, 303, r1.StatusCode)

	// 再解绑。
	unbindForm := url.Values{"csrf_token": {csrf}, "user_id": {"carol@corp"}, "role_id": {fmt.Sprint(roleID)}}
	r2, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/effective/unbind", appID), unbindForm)
	require.NoError(t, err)
	require.Equal(t, 303, r2.StatusCode)
	loc := r2.Header.Get("Location")
	require.Contains(t, loc, "effective")
}
