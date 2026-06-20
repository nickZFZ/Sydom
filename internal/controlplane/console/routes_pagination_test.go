package console

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// TestConsole_Permissions_Paginated 验证 GET /apps/{id}/permissions?page=1：
// - 有会话 + 已 seed 权限点 → 200 且 body 含分页条文案（"共"）与搜索框（"搜索"）。
func TestConsole_Permissions_Paginated(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)

	// 直插 3 个权限点（少于 limit=50，避免与默认 limit 歧义）。
	for i := 0; i < 3; i++ {
		code := "perm-pg-" + strconv.Itoa(i)
		_, err := db.Exec(
			`INSERT INTO permission(app_id, code, resource, action, type, name, source)
			 VALUES($1, $2, 'order', 'read', 'act', $2, 'manual')`,
			appID, code,
		)
		require.NoError(t, err)
	}

	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	resp, err := c.Get(ts.URL + "/apps/" + strconv.FormatInt(appID, 10) + "/permissions?page=1")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := readBody(t, resp)
	require.Contains(t, body, "显示 1-3 / 共 3") // pager 渲染完整区间 + total（seed 3 条，SeedApp 不预置权限）
	require.Contains(t, body, "搜索")           // searchbox 模板含"搜索"按钮
}

// TestConsole_Operators_Paginated 验证 GET /operators?page=1：有分页条和搜索框。
func TestConsole_Operators_Paginated(t *testing.T) {
	ts, store, db := newConsole(t)

	// seed 3 个额外 operator（NOT NULL 列：principal/secret_enc/status）
	for i := 0; i < 3; i++ {
		principal := fmt.Sprintf("op-pg-%d@test", i)
		_, err := db.Exec(
			`INSERT INTO admin_operator(principal, secret_enc, status) VALUES($1, '\xab'::bytea, 1)`,
			principal,
		)
		require.NoError(t, err)
	}

	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	resp, err := c.Get(ts.URL + "/operators?page=1")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := readBody(t, resp)
	require.Contains(t, body, "显示 1-4 / 共 4") // root@sydom(EnsureRootOperator) + 3 seed = 4，不足一页
	require.Contains(t, body, "搜索")           // searchbox 含"搜索"按钮
	require.Contains(t, body, "op-pg-0@test") // seed operator 出现在列表
}

// TestConsole_Permissions_PagerLinkNotDoubleEncoded 锁定 pagerData 的 template.URL 修复：
// 翻页链接保留的 query（sort/order）必须是原始 "sort=code"，不能被 html/template 在 URL
// context 二次 percent-encode 成 "sort%3Dcode"（那会破坏链接参数）。需 >50 行触发"下一页"。
func TestConsole_Permissions_PagerLinkNotDoubleEncoded(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)

	// seed 51 个权限点（> consolePageSize=50），第 1 页才会出现"下一页"链接。
	for i := 0; i < 51; i++ {
		code := fmt.Sprintf("perm-de-%02d", i)
		_, err := db.Exec(
			`INSERT INTO permission(app_id, code, resource, action, type, name, source)
			 VALUES($1, $2, 'order', 'read', 'act', $2, 'manual')`,
			appID, code,
		)
		require.NoError(t, err)
	}

	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	resp, err := c.Get(ts.URL + "/apps/" + strconv.FormatInt(appID, 10) + "/permissions?sort=code&order=asc")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := readBody(t, resp)
	require.Contains(t, body, "page=2", "应渲染下一页链接（total 51 > 50）")
	// 回归守卫：双重编码 bug 会让翻页链接含 sort%3dcode（html/template URL escaper 输出小写 %xx，
	// 已实测确认）；template.URL 修复后应为原始 sort=code。小写化 body 以兼容大小写差异。
	lower := strings.ToLower(body)
	require.NotContains(t, lower, "sort%3dcode", "翻页链接的 query 不得被二次 percent-encode")
	require.Contains(t, body, "sort=code", "翻页链接应保留原始 sort=code 参数")
}

// TestConsole_SortLink_PreservesQuery 验证表头排序链接保留当前 q（搜索）与过滤（source），
// 修复"点排序丢失搜索/过滤"的 UX 债。破坏前排序链接是裸 ?sort=X&order=Y，body 不含 "q=...".
func TestConsole_SortLink_PreservesQuery(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	for i := 0; i < 3; i++ {
		_, err := db.Exec(
			`INSERT INTO permission(app_id, code, resource, action, type, name, source)
			 VALUES($1, $2, 'order', 'read', 'act', $2, 'manual')`,
			appID, fmt.Sprintf("perm-sl-%d", i))
		require.NoError(t, err)
	}
	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	resp, err := c.Get(ts.URL + "/apps/" + strconv.FormatInt(appID, 10) + "/permissions?q=perm-sl&source=manual")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := readBody(t, resp)
	// 排序链接保留搜索词 + 过滤（裸链接 bug 下 body 不含 "q="；searchbox 用 value= 不会贡献 "q="）。
	require.Contains(t, body, "q=perm-sl", "排序链接须保留当前搜索 q")
	require.Contains(t, body, "source=manual", "排序链接须保留当前过滤 source")
	// template.URL 不二次编码（无 sort%3d）；点排序重置到第 1 页（不带 page=）。
	require.NotContains(t, strings.ToLower(body), "sort%3d", "排序链接 query 不得二次 percent-encode")
}

// TestConsole_Permissions_NoSession 验证无会话时重定向到登录页。
func TestConsole_Permissions_NoSession(t *testing.T) {
	ts, _, _ := newConsole(t)
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(ts.URL + "/apps/1/permissions")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
}
