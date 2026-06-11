package console

import (
	"bytes"
	"context"
	"database/sql"
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
func newConsole(t *testing.T) (*httptest.Server, *RedisStore) {
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
	return ts, store
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

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(b)
}

func TestDashboard_SuperAdmin_ListsApps(t *testing.T) {
	ts, _ := newConsole(t)
	c := loginClient(t, ts, "root@sydom", "rootsecret")
	resp, err := c.Get(ts.URL + "/")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := readBody(t, resp)
	require.Contains(t, body, "应用")
}

func TestDashboard_NoSession_RedirectsLogin(t *testing.T) {
	ts, _ := newConsole(t)
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(ts.URL + "/")
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	require.Equal(t, "/login", resp.Header.Get("Location"))
}
