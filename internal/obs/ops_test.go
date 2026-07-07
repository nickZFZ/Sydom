package obs

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpsHandler_Routes(t *testing.T) {
	m := New()
	m.AuthzDecision(true)
	ts := httptest.NewServer(OpsHandler(m, nil))
	defer ts.Close()
	for path, wantBodySub := range map[string]string{
		"/healthz": "ok",
		"/readyz":  "ok",
		"/metrics": "sydom_authz_decisions_total",
	} {
		resp, err := http.Get(ts.URL + path)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode, path)
		b := readAll(t, resp)
		require.Contains(t, b, wantBodySub, path)
	}
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(b)
}
