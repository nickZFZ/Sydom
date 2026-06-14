package console

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// mustCreatePermissionFull 经 HTTP 建权限点（带自定义 resource/action/name），查 DB 取 id。
// 与 handler_test.go 的 mustCreatePermission（简化版）区分，此版支持全参数。
func mustCreatePermissionFull(t *testing.T, c *http.Client, ts *httptest.Server, db *sql.DB,
	csrf string, appID uint64, code, resource, action, name string) int64 {
	t.Helper()
	form := url.Values{
		"csrf_token": {csrf},
		"code":       {code},
		"resource":   {resource},
		"action":     {action},
		"ptype":      {"act"},
		"name":       {name},
	}
	resp, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/permissions", appID), form)
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	var id int64
	require.NoError(t, db.QueryRow(`SELECT id FROM permission WHERE app_id=$1 AND code=$2`, appID, code).Scan(&id))
	return id
}

// mustGrant 经 HTTP 把权限点授给角色（eft=allow）。
func mustGrant(t *testing.T, c *http.Client, ts *httptest.Server, csrf string, appID uint64, roleID, permID int64) {
	t.Helper()
	form := url.Values{
		"csrf_token":    {csrf},
		"role_id":       {fmt.Sprint(roleID)},
		"permission_id": {fmt.Sprint(permID)},
		"eft":           {"allow"},
	}
	resp, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/grants", appID), form)
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
}

// mustBind 经 HTTP 把用户绑到角色。
func mustBind(t *testing.T, c *http.Client, ts *httptest.Server, csrf string, appID uint64, userID string, roleID int64) {
	t.Helper()
	form := url.Values{
		"csrf_token": {csrf},
		"user_id":    {userID},
		"role_id":    {fmt.Sprint(roleID)},
	}
	resp, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/bindings", appID), form)
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
}

// TestOps_PersonView_BusinessLanguage 验证运营台人员旅程核心：
// 渲染业务角色名/能力名；绝不泄露技术原语（resource:action）。
func TestOps_PersonView_BusinessLanguage(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	// 建模台播种：权限点（带业务名）+ 角色 + 授权 + 绑定 alice。
	permID := mustCreatePermissionFull(t, c, ts, db, csrf, uint64(appID), "p_read", "orders", "read", "查看订单")
	roleID := mustCreateRole(t, c, ts, db, csrf, uint64(appID), "sales", "销售经理")
	mustGrant(t, c, ts, csrf, uint64(appID), roleID, permID)
	mustBind(t, c, ts, csrf, uint64(appID), "alice", roleID)

	page, err := c.Get(ts.URL + fmt.Sprintf("/ops/apps/%d/people/view?user_id=alice", appID))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, page.StatusCode)
	body := readBody(t, page)
	require.Contains(t, body, "销售经理")           // 业务角色名（来自 ListRoles name 映射）
	require.Contains(t, body, "查看订单")           // 能力业务名（来自 ListPermissions name 映射）
	require.NotContains(t, body, "orders:read") // 不漏技术原语（权限点 resource:action）
	require.NotContains(t, body, "sales")       // 不漏角色 code（防 roleNameMap 回归）
}

// TestOps_PersonView_DegradeNoEnumerate 验证越权访问运营台时降级无枚举：
// 不存在的 app_id 被 AuthorizeRule 拦截，返回非 200，且 body 不含任何业务数据。
func TestOps_PersonView_DegradeNoEnumerate(t *testing.T) {
	ts, store, db := newConsole(t)
	_ = dbtest.SeedApp(t, db)
	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	page, err := c.Get(ts.URL + "/ops/apps/9999999999/people/view?user_id=alice")
	require.NoError(t, err)
	require.NotEqual(t, http.StatusOK, page.StatusCode)
	require.False(t, strings.Contains(readBody(t, page), "查看订单"))
}

// TestOps_CreateBusinessRole_Then_ShowInList 验证运营台业务角色旅程：
// 建角色勾能力 → 角色列表显示业务名 → 不漏原语（code/role_id）。
func TestOps_CreateBusinessRole_Then_ShowInList(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	permID := mustCreatePermissionFull(t, c, ts, db, csrf, uint64(appID), "p_read", "orders", "read", "查看订单")

	form := url.Values{
		"csrf_token":     {csrf},
		"name":           {"销售经理"},
		"permission_ids": {fmt.Sprint(permID)},
	}
	resp, err := c.PostForm(ts.URL+fmt.Sprintf("/ops/apps/%d/roles", appID), form)
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)

	page, err := c.Get(ts.URL + fmt.Sprintf("/ops/apps/%d/roles", appID))
	require.NoError(t, err)
	body := readBody(t, page)
	require.Contains(t, body, "销售经理")   // 业务角色名可见
	require.NotContains(t, body, "br-") // 不漏系统生成的 code 前缀（generateRoleCode = "br-"+hex）
}

// TestOps_AssignUnassign_Role 验证分配/移除业务角色闭环：
// assign → 人员视图含角色名；unassign → 人员视图无该角色名。
func TestOps_AssignUnassign_Role(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	// 建模台播种：权限点 + 业务角色（via CreateBusinessRole）。
	permID := mustCreatePermissionFull(t, c, ts, db, csrf, uint64(appID), "p_write", "orders", "write", "创建订单")
	createForm := url.Values{
		"csrf_token":     {csrf},
		"name":           {"运营专员"},
		"permission_ids": {fmt.Sprint(permID)},
	}
	resp, err := c.PostForm(ts.URL+fmt.Sprintf("/ops/apps/%d/roles", appID), createForm)
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)

	// 取刚建的角色 id（查 DB）。
	var roleID int64
	require.NoError(t, db.QueryRow(`SELECT id FROM role WHERE app_id=$1 AND name=$2`, appID, "运营专员").Scan(&roleID))

	// 分配给 bob。
	assignForm := url.Values{
		"csrf_token": {csrf},
		"user_id":    {"bob"},
		"role_id":    {fmt.Sprint(roleID)},
	}
	resp, err = c.PostForm(ts.URL+fmt.Sprintf("/ops/apps/%d/people/assign", appID), assignForm)
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)

	// 人员视图验证角色可见。
	page, err := c.Get(ts.URL + fmt.Sprintf("/ops/apps/%d/people/view?user_id=bob", appID))
	require.NoError(t, err)
	body := readBody(t, page)
	require.Contains(t, body, "运营专员")   // 角色名渲染
	require.Contains(t, body, "当前直接分配") // bob 已有直绑角色 → 可移除区渲染

	// 移除角色。
	unassignForm := url.Values{
		"csrf_token": {csrf},
		"user_id":    {"bob"},
		"role_id":    {fmt.Sprint(roleID)},
	}
	resp, err = c.PostForm(ts.URL+fmt.Sprintf("/ops/apps/%d/people/unassign", appID), unassignForm)
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)

	// 人员视图验证角色消失。
	page2, err := c.Get(ts.URL + fmt.Sprintf("/ops/apps/%d/people/view?user_id=bob", appID))
	require.NoError(t, err)
	body2 := readBody(t, page2)
	// 注：角色名"运营专员"仍在分配下拉的 option 中，故不能用整页 NotContains。
	require.NotContains(t, body2, "当前直接分配") // 可移除区不再渲染（bob 无直绑角色）
	require.Contains(t, body2, "暂无角色绑定")    // bob 已无任何有效角色
}
