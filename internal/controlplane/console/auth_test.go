package console

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeResolver map[string][]byte

func (f fakeResolver) ResolveSecret(_ context.Context, p string) ([]byte, error) {
	s, ok := f[p]
	if !ok {
		return nil, ErrNoSession // 任意 error 即可（登录不区分原因）
	}
	return s, nil
}

func newAuthHandler(t *testing.T) (*Handler, *RedisStore) {
	t.Helper()
	store := newTestStore(t, time.Minute)
	h := &Handler{
		sessions:     store,
		resolver:     fakeResolver{"root@sydom": []byte("s3cr3t")},
		cookieSecure: false,
		logger:       testLogger(t),   // 必加：渲染路径需要
		templates:    mustTemplates(), // 必加：渲染 login.html 需要
	}
	return h, store
}

func TestLogin_Success_SetsCookie(t *testing.T) {
	h, _ := newAuthHandler(t)
	form := url.Values{"principal": {"root@sydom"}, "secret": {"s3cr3t"}}
	r := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.handleLoginPost(w, r)
	require.Equal(t, http.StatusSeeOther, w.Code)
	require.Equal(t, "/", w.Header().Get("Location"))
	cookies := w.Result().Cookies()
	var sc *http.Cookie
	for _, c := range cookies {
		if c.Name == sessionCookieName {
			sc = c
		}
	}
	require.NotNil(t, sc, "session cookie should be set")
	require.True(t, sc.HttpOnly)
	require.NotEmpty(t, sc.Value)
}

func TestLogin_WrongSecret_Generic401(t *testing.T) {
	h, _ := newAuthHandler(t)
	form := url.Values{"principal": {"root@sydom"}, "secret": {"WRONG"}}
	r := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.handleLoginPost(w, r)
	require.Equal(t, http.StatusUnauthorized, w.Code)
	// 无 cookie
	require.Empty(t, w.Result().Cookies())
	// body 不含提交的 secret
	require.NotContains(t, w.Body.String(), "WRONG")
}

func TestLogin_UnknownPrincipal_SameGeneric(t *testing.T) {
	h, _ := newAuthHandler(t)
	form := url.Values{"principal": {"nobody@sydom"}, "secret": {"s3cr3t"}}
	r := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.handleLoginPost(w, r)
	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Empty(t, w.Result().Cookies())
}

func TestRequireSession_NoCookie_Redirects(t *testing.T) {
	h, _ := newAuthHandler(t)
	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	_, _, ok := h.requireSession(w, r)
	require.False(t, ok)
	require.Equal(t, http.StatusSeeOther, w.Code)
	require.Equal(t, "/login", w.Header().Get("Location"))
}

func TestRequireSession_ValidCookie_OK(t *testing.T) {
	h, store := newAuthHandler(t)
	id, _, err := store.Create(context.Background(), "root@sydom")
	require.NoError(t, err)
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: id})
	w := httptest.NewRecorder()
	principal, sess, ok := h.requireSession(w, r)
	require.True(t, ok)
	require.Equal(t, "root@sydom", principal)
	require.NotEmpty(t, sess.CSRF)
}

func TestCheckCSRF(t *testing.T) {
	h, store := newAuthHandler(t)
	_, csrf, err := store.Create(context.Background(), "root@sydom")
	require.NoError(t, err)
	sess := Session{CSRF: csrf}

	// 匹配
	r := httptest.NewRequest("POST", "/", strings.NewReader(url.Values{"csrf_token": {csrf}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	require.True(t, h.checkCSRF(r, sess))

	// 不匹配
	r2 := httptest.NewRequest("POST", "/", strings.NewReader(url.Values{"csrf_token": {"wrong"}}.Encode()))
	r2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	require.False(t, h.checkCSRF(r2, sess))
}
