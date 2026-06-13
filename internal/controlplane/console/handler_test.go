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
	"regexp"
	"strconv"
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
	ts, store, db := newConsole(t)
	var tID int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant(name) VALUES('acme') RETURNING id`).Scan(&tID))
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	form := url.Values{
		"csrf_token": {csrf},
		"tenant_id":  {fmt.Sprint(tID)},
		"domain":     {"acme"},
		"name":       {"acme-app"},
		"app_key":    {"ak_acme"},
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

// domainOf：appID → casbin 域串（与 mgmt.DomainOfAppID 同语义）。
func domainOf(id int64) string { return strconv.FormatInt(id, 10) }

// opSecretRe：从 operator_created.html 的稳定锚 id="op-secret" 中确定性抽取一次性明文密钥。
var opSecretRe = regexp.MustCompile(`id="op-secret">([^<]+)<`)

// TestOperators_CreateThenList：建操作员走「一次性 secret」专管线（非 PRG）。
// POST /operators → 200（不是 303）且页面渲染一次性密钥锚与强警示文案；再 GET /operators 回显 principal。
func TestOperators_CreateThenList(t *testing.T) {
	ts, store, _ := newConsole(t)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	form := url.Values{"csrf_token": {csrf}, "principal": {"alice@ops"}}
	resp, err := c.PostForm(ts.URL+"/operators", form)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode) // 一次性 secret 页：非 PRG
	body := readBody(t, resp)
	require.Contains(t, body, `id="op-secret"`) // 稳定锚，便于确定性抽取
	require.Contains(t, body, "仅显示一次")          // 强警示文案

	page, err := c.Get(ts.URL + "/operators")
	require.NoError(t, err)
	require.Contains(t, readBody(t, page), "alice@ops")
}

// TestAdminRoles_CreateThenList：建管理角色走 doWrite（PRG）。POST /admin-roles → 303；
// 再 GET /admin-roles 回显 code。
func TestAdminRoles_CreateThenList(t *testing.T) {
	ts, store, _ := newConsole(t)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	form := url.Values{"csrf_token": {csrf}, "code": {"app-admin"}, "name": {"应用管理员"}}
	resp, err := c.PostForm(ts.URL+"/admin-roles", form)
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode) // PRG

	page, err := c.Get(ts.URL + "/admin-roles")
	require.NoError(t, err)
	require.Contains(t, readBody(t, page), "app-admin")
}

// mustCreateAppViaConsole 经 console POST /apps 建应用（一次性 secret 页，200），返回该 app 自增 id。
func mustCreateAppViaConsole(t *testing.T, c *http.Client, ts *httptest.Server, db *sql.DB,
	csrf, tenant, domain, name, appKey string) int64 {
	t.Helper()
	var tID int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant(name) VALUES($1) RETURNING id`, tenant).Scan(&tID))
	form := url.Values{
		"csrf_token": {csrf},
		"tenant_id":  {fmt.Sprint(tID)},
		"domain":     {domain},
		"name":       {name},
		"app_key":    {appKey},
	}
	resp, err := c.PostForm(ts.URL+"/apps", form)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode) // createApp 渲染 app_created.html
	var id int64
	require.NoError(t, db.QueryRow(`SELECT id FROM application WHERE app_key=$1`, appKey).Scan(&id))
	return id
}

// TestSecurityMatrix_LimitedOperator 是冠石用例：完全经 console 写端点构造一个受限操作员，
// 证明跨域隔离的四象限：
//  1. 降级仪表盘——无 *-域 ListApplications 授权 → 退化为「App ID 直达」表单，绝不枚举 app。
//  2. 本应用域内允许——在 strconv(appA) 域有 role:create → POST /apps/{appA}/roles 成功 303。
//  3. 跨应用 403——同动作打到 appB 域无授权 → 403。
//  4. 系统域闸——GET /operators 系统页无 * 域授权 → 403（绝不降级，否则泄露存在性）。
//
// 受限操作员的授权即时生效：GrantAdminRole/BindOperatorRole 自增 admin_policy_version，
// 进程内共享 Enforcer 在下次 Enforce 时重载——无需 sleep/retry。
func TestSecurityMatrix_LimitedOperator(t *testing.T) {
	ts, store, db := newConsole(t)
	root, csrfRoot := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	// 两个不同租户/域/key 的应用（root 建）。
	appA := mustCreateAppViaConsole(t, root, ts, db, csrfRoot, "ta", "dom-a", "应用A", "ak-a")
	appB := mustCreateAppViaConsole(t, root, ts, db, csrfRoot, "tb", "dom-b", "应用B", "ak-b")

	// a. 建操作员，从一次性密钥页确定性抽取明文密钥。
	form := url.Values{"csrf_token": {csrfRoot}, "principal": {"ops@a"}}
	resp, err := root.PostForm(ts.URL+"/operators", form)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	m := opSecretRe.FindStringSubmatch(readBody(t, resp))
	require.Len(t, m, 2, "应能从 op-secret 锚抽取一次性密钥")
	opsSecret := m[1]
	require.NotEmpty(t, opsSecret)

	// b. 建管理角色，读其自增 id。
	resp, err = root.PostForm(ts.URL+"/admin-roles", url.Values{"csrf_token": {csrfRoot}, "code": {"a-admin"}, "name": {"n"}})
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	var roleID int64
	require.NoError(t, db.QueryRow(`SELECT id FROM admin_role WHERE code=$1`, "a-admin").Scan(&roleID))

	// c. 在 appA 的 casbin 域（=strconv(appA)）授 role:create + role:read。
	for _, action := range []string{"create", "read"} {
		g := url.Values{"csrf_token": {csrfRoot}, "domain": {domainOf(appA)}, "resource": {"role"}, "action": {action}}
		resp, err = root.PostForm(ts.URL+fmt.Sprintf("/admin-roles/%d/grants", roleID), g)
		require.NoError(t, err)
		require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	}

	// d. 读操作员 id，绑角色到 appA 域。
	var opID int64
	require.NoError(t, db.QueryRow(`SELECT id FROM admin_operator WHERE principal=$1`, "ops@a").Scan(&opID))
	bind := url.Values{"csrf_token": {csrfRoot}, "role_id": {fmt.Sprint(roleID)}, "domain": {domainOf(appA)}}
	resp, err = root.PostForm(ts.URL+fmt.Sprintf("/operators/%d/roles", opID), bind)
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)

	// 以受限操作员身份登录。
	c, csrf := loginAndCSRF(t, ts, store, "ops@a", opsSecret)

	// 象限 1：降级仪表盘——无 * 域 ListApplications 授权 → 降级直达表单，无枚举。
	page, err := c.Get(ts.URL + "/")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, page.StatusCode)
	homeBody := readBody(t, page)
	require.Contains(t, homeBody, "App ID")   // 降级直达表单
	require.NotContains(t, homeBody, "dom-a") // 绝不泄露 app 列表
	require.NotContains(t, homeBody, "dom-b")

	// 象限 2：本应用域内允许——POST /apps/{appA}/roles → 303。
	r1 := url.Values{"csrf_token": {csrf}, "code": {"r1"}, "name": {"n"}}
	resp, err = c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/roles", appA), r1)
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)

	// 象限 3：跨应用 403——POST /apps/{appB}/roles → 403。
	r2 := url.Values{"csrf_token": {csrf}, "code": {"r2"}, "name": {"n"}}
	resp, err = c.PostForm(ts.URL+fmt.Sprintf("/apps/%d/roles", appB), r2)
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)

	// 象限 4：系统域闸——GET /operators → 403（绝不降级）。
	resp, err = c.Get(ts.URL + "/operators")
	require.NoError(t, err)
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
}
