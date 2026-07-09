package secheaders

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// okHandler 是被包裹的下游：写一个可辨识的 body + 200，用于验证透传。
func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("downstream-ok"))
	})
}

func do(h http.Handler) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	return rec
}

func TestConsole_HeadersSecure(t *testing.T) {
	rec := do(Console(true)(okHandler()))
	h := rec.Header()
	require.Equal(t, "nosniff", h.Get("X-Content-Type-Options"))
	require.Equal(t, "DENY", h.Get("X-Frame-Options"))
	require.Equal(t, "no-referrer", h.Get("Referrer-Policy"))
	require.Equal(t, "geolocation=(), camera=(), microphone=()", h.Get("Permissions-Policy"))
	csp := h.Get("Content-Security-Policy")
	require.Contains(t, csp, "default-src 'self'")
	require.Contains(t, csp, "script-src 'self'")
	require.Contains(t, csp, "style-src 'self'")
	require.Contains(t, csp, "object-src 'none'")
	require.Contains(t, csp, "frame-ancestors 'none'")
	require.Contains(t, csp, "base-uri 'self'")
	require.Contains(t, csp, "form-action 'self'")
	require.NotContains(t, csp, "unsafe-inline")
	require.NotContains(t, csp, "unsafe-eval")
	require.Equal(t, "max-age=63072000; includeSubDomains", h.Get("Strict-Transport-Security"))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "downstream-ok", rec.Body.String())
}

func TestConsole_NoHSTSWhenInsecure(t *testing.T) {
	rec := do(Console(false)(okHandler()))
	require.Empty(t, rec.Header().Get("Strict-Transport-Security"))
	require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	require.Contains(t, rec.Header().Get("Content-Security-Policy"), "default-src 'self'")
}

func TestAPI_HeadersLockedDown(t *testing.T) {
	rec := do(API(true)(okHandler()))
	h := rec.Header()
	require.Equal(t, "default-src 'none'; frame-ancestors 'none'", h.Get("Content-Security-Policy"))
	require.Equal(t, "nosniff", h.Get("X-Content-Type-Options"))
	require.Equal(t, "DENY", h.Get("X-Frame-Options"))
	require.Equal(t, "no-referrer", h.Get("Referrer-Policy"))
	require.Equal(t, "max-age=63072000; includeSubDomains", h.Get("Strict-Transport-Security"))
	require.Empty(t, h.Get("Permissions-Policy"))
	require.Equal(t, "downstream-ok", rec.Body.String())
}

func TestAPI_NoHSTSWhenInsecure(t *testing.T) {
	rec := do(API(false)(okHandler()))
	require.Empty(t, rec.Header().Get("Strict-Transport-Security"))
	require.Equal(t, "default-src 'none'; frame-ancestors 'none'", rec.Header().Get("Content-Security-Policy"))
}

func TestSurfaceHeadersDoNotBleed(t *testing.T) {
	con := do(Console(true)(okHandler())).Header()
	api := do(API(true)(okHandler())).Header()
	require.NotEqual(t, con.Get("Content-Security-Policy"), api.Get("Content-Security-Policy"))
	require.NotEmpty(t, con.Get("Permissions-Policy"))
	require.Empty(t, api.Get("Permissions-Policy"))
	require.False(t, strings.Contains(api.Get("Content-Security-Policy"), "script-src"))
}
