package app

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nickZFZ/Sydom/sdk/go/sydomhttp"
	"github.com/stretchr/testify/require"
)

func reqWithUser(method, path, user string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	if user != "" {
		r.AddCookie(&http.Cookie{Name: "demo_user", Value: user})
	}
	return r
}

func TestResolver_PublicRoutesSkip(t *testing.T) {
	for _, p := range []string{"/", "/login", "/logout"} {
		_, _, _, err := resolver(reqWithUser(http.MethodGet, p, ""))
		require.ErrorIs(t, err, sydomhttp.ErrSkipAuth, p)
	}
}

func TestResolver_ListMapsToRead(t *testing.T) {
	sub, obj, act, err := resolver(reqWithUser(http.MethodGet, "/orders", "alice"))
	require.NoError(t, err)
	require.Equal(t, "alice", sub)
	require.Equal(t, "order", obj)
	require.Equal(t, "read", act)
}

func TestResolver_DeleteMapsToDelete(t *testing.T) {
	sub, obj, act, err := resolver(reqWithUser(http.MethodPost, "/orders/5/delete", "bob"))
	require.NoError(t, err)
	require.Equal(t, "bob", sub)
	require.Equal(t, "order", obj)
	require.Equal(t, "delete", act)
}

func TestResolver_ProtectedWithoutUser_FailClose(t *testing.T) {
	_, _, _, err := resolver(reqWithUser(http.MethodGet, "/orders", ""))
	require.Error(t, err)
	require.False(t, errors.Is(err, sydomhttp.ErrSkipAuth)) // 非 skip → 中间件 fail-close 拒绝
}

func TestUserDept(t *testing.T) {
	require.Equal(t, "shanghai", userDept("bob"))
	require.Equal(t, "", userDept("alice")) // manager 不按部门过滤
}
