package obs

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// 路由模板标签：两个不同 app_id 的具体 path 命中同一模板标签（OB-3 基数安全）。
func TestHTTPMiddleware_RouteTemplateLabel(t *testing.T) {
	m := New()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /apps/{app_id}/x", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	h := m.HTTPMiddleware(nil, mux)

	for _, id := range []string{"1", "2"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/apps/"+id+"/x", nil))
		require.Equal(t, 200, rec.Code)
	}
	// 同模板 → 1 series、值 2。
	require.Equal(t, 1, testutil.CollectAndCount(m.httpReqs))
	require.Equal(t, 2.0, testutil.ToFloat64(m.httpReqs.WithLabelValues("GET /apps/{app_id}/x", "GET", "2xx")))
}
