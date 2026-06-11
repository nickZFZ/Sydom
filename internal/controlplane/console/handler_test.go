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
