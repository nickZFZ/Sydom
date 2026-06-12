package console

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/controlplane/outbox"
	"github.com/nickZFZ/Sydom/internal/controlplane/policy"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// newConsole 起一套真依赖的 Console（root 已播种为超管）+ httptest server。
func newConsole(t *testing.T) (*httptest.Server, *RedisStore, *sql.DB) {
	t.Helper()
	dsn := dbtest.MigratedDSN(t) // 注：dbtest 已 blank-import lib/pq，驱动已注册
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	mk := bytes.Repeat([]byte{7}, 32)
	require.NoError(t, adminauthz.EnsureRootOperator(context.Background(), db, mk, "root@sydom", []byte("rootsecret")))
	resolver, err := adminauthz.NewOperatorResolver(db, mk)
	require.NoError(t, err)
	enf, err := adminauthz.NewEnforcer(db)
	require.NoError(t, err)
	srv := mgmt.NewAdminServer(db, policy.NewPolicyManager(db, outbox.NewSink()), mk)

	rdb := redis.NewClient(&redis.Options{Addr: dbtest.StartRedis(t)})
	t.Cleanup(func() { _ = rdb.Close() })
	store := NewRedisStore(rdb, time.Minute)

	h := NewHandler(srv, resolver, enf, db, store, testLogger(t), false)
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts, store, db
}

// loginClient 返回带会话 cookie 的 client。
func loginClient(t *testing.T, ts *httptest.Server, principal, secret string) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	form := url.Values{"principal": {principal}, "secret": {secret}}
	resp, err := c.PostForm(ts.URL+"/login", form)
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	return c
}

// loginAndCSRF 登录并返回 (client, csrf)：从 jar 取会话 id，再向 store 拿该会话的 CSRF。
// store==nil 时只返回 client（用于「故意不带 csrf」的否定用例）。
func loginAndCSRF(t *testing.T, ts *httptest.Server, store *RedisStore, principal, secret string) (*http.Client, string) {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	form := url.Values{"principal": {principal}, "secret": {secret}}
	resp, err := c.PostForm(ts.URL+"/login", form)
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)

	u, _ := url.Parse(ts.URL)
	var sessionID string
	for _, ck := range jar.Cookies(u) {
		if ck.Name == sessionCookieName {
			sessionID = ck.Value
		}
	}
	require.NotEmpty(t, sessionID)
	if store == nil {
		return c, ""
	}
	sess, err := store.Get(context.Background(), sessionID)
	require.NoError(t, err)
	return c, sess.CSRF
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(b)
}

func TestDashboard_SuperAdmin_ListsApps(t *testing.T) {
	ts, _, _ := newConsole(t)
	c := loginClient(t, ts, "root@sydom", "rootsecret")
	resp, err := c.Get(ts.URL + "/")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := readBody(t, resp)
	require.Contains(t, body, "应用")
}

func TestDashboard_NoSession_RedirectsLogin(t *testing.T) {
	ts, _, _ := newConsole(t)
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(ts.URL + "/")
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/login", resp.Header.Get("Location"))
}

func TestRoles_CreateThenList(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db) // 每个测试库只可调一次
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	form := url.Values{"csrf_token": {csrf}, "code": {"manager"}, "name": {"经理"}}
	resp, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/roles", appID), form)
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode) // PRG
	page, err := c.Get(ts.URL + fmt.Sprintf("/apps/%d/roles", appID))
	require.NoError(t, err)
	body := readBody(t, page)
	require.Contains(t, body, "manager")
	require.Contains(t, body, "经理")
}

func TestRoles_CSRFMissing_Forbidden(t *testing.T) {
	ts, _, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, _ := loginAndCSRF(t, ts, nil, "root@sydom", "rootsecret")
	form := url.Values{"code": {"x"}} // 无 csrf_token
	resp, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/roles", appID), form)
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, resp.StatusCode) // CSRF 失败 → PermissionDenied → 403
}

// mustCreateRole 经 HTTP 建角色后查 DB 取自增 id（role 表 (app_id, code) 唯一）。
func mustCreateRole(t *testing.T, c *http.Client, ts *httptest.Server, db *sql.DB, csrf string, appID uint64, code, name string) int64 {
	t.Helper()
	form := url.Values{"csrf_token": {csrf}, "code": {code}, "name": {name}}
	resp, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/roles", appID), form)
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	var id int64
	require.NoError(t, db.QueryRow(`SELECT id FROM role WHERE app_id=$1 AND code=$2`, appID, code).Scan(&id))
	return id
}

// mustCreatePermission 经 HTTP 建权限点后查 DB 取自增 id（permission 表 (app_id, code) 唯一）。
func mustCreatePermission(t *testing.T, c *http.Client, ts *httptest.Server, db *sql.DB, csrf string, appID uint64, code string) int64 {
	t.Helper()
	form := url.Values{"csrf_token": {csrf}, "code": {code}, "resource": {"order"}, "action": {"read"}, "ptype": {"act"}, "name": {code}}
	resp, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/permissions", appID), form)
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	var id int64
	require.NoError(t, db.QueryRow(`SELECT id FROM permission WHERE app_id=$1 AND code=$2`, appID, code).Scan(&id))
	return id
}

func TestGrants_GrantThenList(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	roleID := mustCreateRole(t, c, ts, db, csrf, uint64(appID), "manager", "经理")
	permID := mustCreatePermission(t, c, ts, db, csrf, uint64(appID), "order.read")

	form := url.Values{"csrf_token": {csrf}, "role_id": {fmt.Sprint(roleID)}, "permission_id": {fmt.Sprint(permID)}, "eft": {"allow"}}
	resp, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/grants", appID), form)
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode) // PRG

	page, err := c.Get(ts.URL + fmt.Sprintf("/apps/%d/grants", appID))
	require.NoError(t, err)
	body := readBody(t, page)
	require.Contains(t, body, fmt.Sprint(roleID))
	require.Contains(t, body, fmt.Sprint(permID))
}

func TestBindings_BindThenList(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	roleID := mustCreateRole(t, c, ts, db, csrf, uint64(appID), "manager", "经理")

	form := url.Values{"csrf_token": {csrf}, "user_id": {"alice@corp"}, "role_id": {fmt.Sprint(roleID)}}
	resp, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/bindings", appID), form)
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)

	page, err := c.Get(ts.URL + fmt.Sprintf("/apps/%d/bindings", appID))
	require.NoError(t, err)
	body := readBody(t, page)
	require.Contains(t, body, "alice@corp")
}

func TestInheritances_AddThenList(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	childID := mustCreateRole(t, c, ts, db, csrf, uint64(appID), "clerk", "店员")
	parentID := mustCreateRole(t, c, ts, db, csrf, uint64(appID), "manager", "经理")

	form := url.Values{"csrf_token": {csrf}, "child_role_id": {fmt.Sprint(childID)}, "parent_role_id": {fmt.Sprint(parentID)}}
	resp, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/inheritances", appID), form)
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)

	page, err := c.Get(ts.URL + fmt.Sprintf("/apps/%d/inheritances", appID))
	require.NoError(t, err)
	body := readBody(t, page)
	require.Contains(t, body, fmt.Sprint(childID))
	require.Contains(t, body, fmt.Sprint(parentID))
}

func TestGrants_CSRFMissing_Forbidden(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	roleID := mustCreateRole(t, c, ts, db, csrf, uint64(appID), "manager", "经理")
	permID := mustCreatePermission(t, c, ts, db, csrf, uint64(appID), "order.read")

	form := url.Values{"role_id": {fmt.Sprint(roleID)}, "permission_id": {fmt.Sprint(permID)}, "eft": {"allow"}} // 无 csrf_token
	resp, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/grants", appID), form)
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
}

// TestCreateApp_ShowsOneTimeSecret：建应用走「一次性 secret」专管线（非 PRG）。
// 断言返回 200（不是 303）且页面渲染了 App Secret 一次性提示与明文密钥标签。
func TestCreateApp_ShowsOneTimeSecret(t *testing.T) {
	ts, store, _ := newConsole(t)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	form := url.Values{
		"csrf_token":  {csrf},
		"tenant_name": {"acme"},
		"domain":      {"acme"},
		"name":        {"acme-app"},
		"app_key":     {"ak_acme"},
	}
	resp, err := c.PostForm(ts.URL+"/apps", form)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode) // 一次性 secret 页：非 PRG
	body := readBody(t, resp)
	require.Contains(t, body, "App Secret") // 一次性密钥标签
	require.Contains(t, body, "仅显示这一次")     // 强警示文案，确认这是 app_created 页
	require.Contains(t, body, "/roles")     // 前往工作台链接
}

// TestSetAppStatus_Disable：状态切换走 doWrite（PRG）。先确认新建 app 为「启用」，
// POST status=2 后 303，再 GET 列表确认状态列变「停用」。
func TestSetAppStatus_Disable(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	// 起始状态：新建 app 默认启用。
	page, err := c.Get(ts.URL + "/")
	require.NoError(t, err)
	require.Contains(t, readBody(t, page), "启用")

	form := url.Values{"csrf_token": {csrf}, "status": {"2"}}
	resp, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/status", appID), form)
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode) // doWrite PRG

	page2, err := c.Get(ts.URL + "/")
	require.NoError(t, err)
	require.Contains(t, readBody(t, page2), "停用") // 状态列已变停用
}

// TestDataPolicy_UpsertRawJSON_ThenList：condition 作为「原始 JSON 串」走无 JS 基线 textarea
// 提交（id=0 即插入），断言 PRG(303)，再 GET 列表确认 resource 与 condition 内容均回显。
func TestDataPolicy_UpsertRawJSON_ThenList(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	cond := `{"op":"and","children":[{"field":"dept","op":"eq","value":"$user.dept"}]}`
	form := url.Values{
		"csrf_token":   {csrf},
		"id":           {"0"},
		"subject_type": {"role"},
		"subject_id":   {"clerk"},
		"resource":     {"order"},
		"condition":    {cond},
		"effect":       {"allow"},
	}
	resp, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/data-policies", appID), form)
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode) // PRG

	page, err := c.Get(ts.URL + fmt.Sprintf("/apps/%d/data-policies", appID))
	require.NoError(t, err)
	body := readBody(t, page)
	require.Contains(t, body, "order")
	require.Contains(t, body, "$user.dept")
}

// TestDataPolicy_InvalidCondition_FailClose：condition 列为 JSONB NOT NULL，store 以 $5::jsonb
// 插入。非法 JSON 在 INSERT 处 cast 失败，AdminServer.UpsertDataPolicy 把该错误一律包成
// codes.Internal（server.go:107；仅 effect 字段在 server.go:101 特判为 InvalidArgument；
// server.go:35-38 的 TODO 已声明此错误码映射偏粗）。Console 忠实透传 gRPC code，故返回
// HTTP 500——不是 plan 假设的 400。Console 按红线绝不预解析 condition，因此无法也不应强行
// 凑出 400。本用例真正担保的是「fail-close」：非法策略绝不落库（事务回滚）。
func TestDataPolicy_InvalidCondition_FailClose(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	form := url.Values{
		"csrf_token":   {csrf},
		"id":           {"0"},
		"subject_type": {"role"},
		"subject_id":   {"clerk"},
		"resource":     {"order_badcond"}, // 唯一标识，便于后续 NotContains 断言
		"condition":    {"{not valid"},    // 非法 JSON → JSONB cast 失败
		"effect":       {"allow"},
	}
	resp, err := c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/data-policies", appID), form)
	require.NoError(t, err)
	require.Equal(t, http.StatusInternalServerError, resp.StatusCode) // jsonb cast → codes.Internal → 500

	// fail-close：该行绝不应落库（事务回滚），列表里查不到。
	page, err := c.Get(ts.URL + fmt.Sprintf("/apps/%d/data-policies", appID))
	require.NoError(t, err)
	body := readBody(t, page)
	require.NotContains(t, body, "order_badcond")
}
