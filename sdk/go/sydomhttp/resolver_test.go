package sydomhttp_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nickZFZ/Sydom/sdk/go/sydomhttp"
	"github.com/stretchr/testify/require"
)

func TestPathMethodResolver_UsesPathAndMethod(t *testing.T) {
	r := sydomhttp.PathMethodResolver(func(*http.Request) (string, error) {
		return "alice", nil
	})
	req := httptest.NewRequest(http.MethodPost, "/orders/42", nil)

	sub, obj, act, err := r(req)
	require.NoError(t, err)
	require.Equal(t, "alice", sub)
	require.Equal(t, "/orders/42", obj)
	require.Equal(t, http.MethodPost, act)
}

func TestPathMethodResolver_SubjectErrorPropagates(t *testing.T) {
	want := errors.New("no identity")
	r := sydomhttp.PathMethodResolver(func(*http.Request) (string, error) {
		return "", want
	})
	req := httptest.NewRequest(http.MethodGet, "/x", nil)

	_, _, _, err := r(req)
	require.ErrorIs(t, err, want)
}

func TestContext_RoundTrip(t *testing.T) {
	// FromContext 在未注入时返回 (zero, false)。
	d, ok := sydomhttp.FromContext(context.Background())
	require.False(t, ok)
	require.Equal(t, sydomhttp.Decision{}, d)
}
