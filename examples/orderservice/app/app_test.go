package app_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	oapp "github.com/nickZFZ/Sydom/examples/orderservice/app"
	"github.com/nickZFZ/Sydom/sdk/go/sydom"
	"github.com/stretchr/testify/require"
)

// fakeGW 是 AuthGateway 假实现：Check/FilterSQL 走注入闭包。
type fakeGW struct {
	check  func(sub, obj, act string) (bool, error)
	filter func(sub, res string, attrs map[string]any) (sydom.FilterResult, error)
}

func (f fakeGW) Check(_ context.Context, sub, obj, act string) (bool, error) {
	return f.check(sub, obj, act)
}
func (f fakeGW) FilterSQL(_ context.Context, sub, res string, attrs map[string]any) (sydom.FilterResult, error) {
	return f.filter(sub, res, attrs)
}

func newTestServer(t *testing.T, gw oapp.AuthGateway) *httptest.Server {
	t.Helper()
	db := setupOrders(t) // 复用任务 1 helper（同包 app_test）
	h, err := oapp.New(context.Background(), db, gw)
	require.NoError(t, err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func do(t *testing.T, srv *httptest.Server, method, path, user string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, nil)
	require.NoError(t, err)
	if user != "" {
		req.AddCookie(&http.Cookie{Name: "demo_user", Value: user})
	}
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	return resp
}

func TestHandler_Landing_OK(t *testing.T) {
	srv := newTestServer(t, fakeGW{
		check:  func(_, _, _ string) (bool, error) { return false, nil },
		filter: func(_, _ string, _ map[string]any) (sydom.FilterResult, error) { return sydom.FilterResult{}, nil },
	})
	resp := do(t, srv, http.MethodGet, "/", "")
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestHandler_OrdersList_FilteredRender(t *testing.T) {
	srv := newTestServer(t, fakeGW{
		check: func(_, _, _ string) (bool, error) { return true, nil },
		filter: func(_, _ string, _ map[string]any) (sydom.FilterResult, error) {
			return sydom.FilterResult{SQL: "dept = ?", Args: []any{"shanghai"}}, nil
		},
	})
	resp := do(t, srv, http.MethodGet, "/orders", "bob")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := readBody(t, resp)
	require.Contains(t, body, "上海客户A")
	require.NotContains(t, body, "北京客户A") // 数据权限过滤生效
}

func TestHandler_DeleteDenied_FriendlyForbidden(t *testing.T) {
	srv := newTestServer(t, fakeGW{
		check:  func(_, _, act string) (bool, error) { return act != "delete", nil }, // 删除一律拒
		filter: func(_, _ string, _ map[string]any) (sydom.FilterResult, error) { return sydom.FilterResult{}, nil },
	})
	resp := do(t, srv, http.MethodPost, "/orders/1/delete", "bob")
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
	require.Contains(t, readBody(t, resp), "无权") // 友好 403 页，非堆栈
}

func TestHandler_Unavailable_Friendly503(t *testing.T) {
	srv := newTestServer(t, fakeGW{
		check:  func(_, _, _ string) (bool, error) { return false, sydom.ErrUnavailable },
		filter: func(_, _ string, _ map[string]any) (sydom.FilterResult, error) { return sydom.FilterResult{}, nil },
	})
	resp := do(t, srv, http.MethodGet, "/orders", "bob")
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
}
