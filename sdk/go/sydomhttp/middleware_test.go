package sydomhttp_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nickZFZ/Sydom/sdk/go/sydom"
	"github.com/nickZFZ/Sydom/sdk/go/sydomhttp"
	"github.com/stretchr/testify/require"
)

// mockChecker 固定返回注入的 (allowed, err)，并捕获最后一次 (sub,obj,act)。
type mockChecker struct {
	allowed                bool
	err                    error
	called                 bool
	gotSub, gotObj, gotAct string
}

func (m *mockChecker) Check(_ context.Context, sub, obj, act string) (bool, error) {
	m.called = true
	m.gotSub, m.gotObj, m.gotAct = sub, obj, act
	return m.allowed, m.err
}

// fixedResolver 总是返回 (alice, order, read)。
func fixedResolver(*http.Request) (string, string, string, error) {
	return "alice", "order", "read", nil
}

// nextRecorder 是被保护的下游 handler，记录是否被调用并写 200。
func nextRecorder(called *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*called = true
		w.WriteHeader(http.StatusOK)
	})
}

func serve(mw func(http.Handler) http.Handler, next http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mw(next).ServeHTTP(rec, req)
	return rec
}

func TestMiddleware_Allow_CallsNextAndInjectsContext(t *testing.T) {
	chk := &mockChecker{allowed: true}
	var gotDecision sydomhttp.Decision
	var ok bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotDecision, ok = sydomhttp.FromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	rec := serve(sydomhttp.New(chk, fixedResolver), next, httptest.NewRequest(http.MethodGet, "/x", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, ok)
	require.Equal(t, sydomhttp.Decision{Subject: "alice", Object: "order", Action: "read"}, gotDecision)
}

func TestMiddleware_Deny_403_NextNotCalled(t *testing.T) {
	chk := &mockChecker{allowed: false}
	var nextCalled bool

	rec := serve(sydomhttp.New(chk, fixedResolver), nextRecorder(&nextCalled), httptest.NewRequest(http.MethodGet, "/x", nil))

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.False(t, nextCalled)
}

func TestMiddleware_Unavailable_DefaultFailClose_503(t *testing.T) {
	chk := &mockChecker{err: sydom.ErrUnavailable}
	var nextCalled bool

	rec := serve(sydomhttp.New(chk, fixedResolver), nextRecorder(&nextCalled), httptest.NewRequest(http.MethodGet, "/x", nil))

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.False(t, nextCalled)
}

func TestMiddleware_Unavailable_FailOpen_CallsNext(t *testing.T) {
	chk := &mockChecker{err: sydom.ErrUnavailable}
	var nextCalled bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		_, ok := sydomhttp.FromContext(r.Context())
		require.False(t, ok) // fail-open 放行但不注入 Decision（放行≠已鉴权）
		w.WriteHeader(http.StatusOK)
	})

	rec := serve(sydomhttp.New(chk, fixedResolver, sydomhttp.WithFailOpen()), next, httptest.NewRequest(http.MethodGet, "/x", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, nextCalled)
}

func TestMiddleware_HardError_500_FailOpenNotHonored(t *testing.T) {
	chk := &mockChecker{err: errors.New("internal boom")}
	var nextCalled bool

	// 即便配了 WithFailOpen，硬错误也不放行。
	rec := serve(sydomhttp.New(chk, fixedResolver, sydomhttp.WithFailOpen()), nextRecorder(&nextCalled), httptest.NewRequest(http.MethodGet, "/x", nil))

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.False(t, nextCalled)
}

func TestMiddleware_ResolverSkip_CallsNext_NoCheck(t *testing.T) {
	chk := &mockChecker{allowed: false} // 即使会拒，skip 也应放行且不调 Check
	var nextCalled bool
	skipResolver := func(*http.Request) (string, string, string, error) {
		return "", "", "", sydomhttp.ErrSkipAuth
	}

	rec := serve(sydomhttp.New(chk, skipResolver), nextRecorder(&nextCalled), httptest.NewRequest(http.MethodGet, "/public", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, nextCalled)
	require.False(t, chk.called)
}

func TestMiddleware_ResolverError_403_NoCheck(t *testing.T) {
	chk := &mockChecker{allowed: true}
	var nextCalled bool
	errResolver := func(*http.Request) (string, string, string, error) {
		return "", "", "", errors.New("no identity")
	}

	rec := serve(sydomhttp.New(chk, errResolver), nextRecorder(&nextCalled), httptest.NewRequest(http.MethodGet, "/x", nil))

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.False(t, nextCalled)
	require.False(t, chk.called)
}

func TestMiddleware_CustomHandlersAndErrorLog(t *testing.T) {
	chk := &mockChecker{allowed: false}
	var logged bool
	deny := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })

	mw := sydomhttp.New(chk, fixedResolver,
		sydomhttp.WithDenyHandler(deny),
		sydomhttp.WithErrorLog(func(*http.Request, error) { logged = true }))
	rec := serve(mw, nextRecorder(new(bool)), httptest.NewRequest(http.MethodGet, "/x", nil))

	require.Equal(t, http.StatusTeapot, rec.Code) // 自定义 deny handler 生效
	require.False(t, logged)                      // 纯 deny（非错误）不触发 errorLog
}

func TestMiddleware_PathMethodResolver_FeedsCheck(t *testing.T) {
	chk := &mockChecker{allowed: true}
	resolver := sydomhttp.PathMethodResolver(func(*http.Request) (string, error) { return "alice", nil })

	serve(sydomhttp.New(chk, resolver), nextRecorder(new(bool)), httptest.NewRequest(http.MethodPut, "/orders/7", nil))

	require.Equal(t, "alice", chk.gotSub)
	require.Equal(t, "/orders/7", chk.gotObj)
	require.Equal(t, http.MethodPut, chk.gotAct)
}
