package obs

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nickZFZ/Sydom/internal/sidecar/kernel"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestScrape_EndToEnd 是 M5.1 的可演示核验（OB-7）：驱动真实的 gRPC 拦截器（含被拒请求）、
// HTTP 中间件（两个 app_id → 同模板标签）、决策计数与缓存装饰器后，从真正的 OpsHandler /metrics
// 抓取暴露文本，断言关键 series 出现、低基数（OB-3）、无 secret 泄露（OB-4）。
// 一个可复现的测试胜过一次性 curl。
func TestScrape_EndToEnd(t *testing.T) {
	m := New()

	// —— 驱动 gRPC 拦截器：OK 一次 + PermissionDenied 一次（证明最外层能计入被 authz 拒绝的请求）——
	gi := m.UnaryServerInterceptor(nil)
	info := &grpc.UnaryServerInfo{FullMethod: "/sydom.admin.v1.AdminService/ListRoles"}
	_, err := gi(context.Background(), nil, info,
		func(ctx context.Context, req any) (any, error) { return "ok", nil })
	require.NoError(t, err)
	_, err = gi(context.Background(), nil, info,
		func(ctx context.Context, req any) (any, error) {
			return nil, status.Error(codes.PermissionDenied, "denied")
		})
	require.Error(t, err)

	// —— 驱动 HTTP 中间件：两个不同 app_id 命中同一路由模板（OB-3 基数安全）+ 一个 5xx ——
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/apps/{app_id}/roles", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("GET /v1/boom", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) })
	hm := m.HTTPMiddleware(nil, mux)
	for _, id := range []string{"7", "42"} {
		rec := httptest.NewRecorder()
		hm.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/apps/"+id+"/roles", nil))
		require.Equal(t, 200, rec.Code)
	}
	rec := httptest.NewRecorder()
	hm.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/boom", nil))
	require.Equal(t, 500, rec.Code)

	// —— 驱动 sidecar 判定计数 + Check 耗时（decisionInterceptor 就是调这两个助手）——
	m.AuthzDecision(true)
	m.AuthzDecision(false)

	// —— 驱动决策缓存命中率装饰器（经真实 kernel LRU）——
	c := NewMetricsCache(kernel.NewBoundedCache(8), m)
	_, _ = c.Get("k") // miss
	require.NoError(t, c.Set("k", true))
	_, _ = c.Get("k") // hit

	// OB-3：两个 app_id 后 http 计数只有 2 条 series（roles 模板 + boom），不随 app_id 增长。
	require.Equal(t, 2, testutil.CollectAndCount(m.httpReqs))

	// —— 抓真正的 ops /metrics 端点 ——
	ts := httptest.NewServer(OpsHandler(m, nil))
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/metrics")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	_ = resp.Body.Close()
	scrape := string(body)

	// 关键 series 均出现在暴露文本中（OB-7）。
	wantSeries := []string{
		`sydom_grpc_requests_total{code="OK",method="ListRoles",service="AdminService"} 1`,
		`sydom_grpc_requests_total{code="PermissionDenied",method="ListRoles",service="AdminService"} 1`,
		`sydom_http_requests_total{code="2xx",handler="GET /v1/apps/{app_id}/roles",method="GET"} 2`,
		`sydom_http_requests_total{code="5xx",handler="GET /v1/boom",method="GET"} 1`,
		`sydom_authz_decisions_total{decision="allow"} 1`,
		`sydom_authz_decisions_total{decision="deny"} 1`,
		`sydom_cache_hits_total 1`,
		`sydom_cache_misses_total 1`,
	}
	for _, s := range wantSeries {
		require.Contains(t, scrape, s, "关键 series 缺失: %s", s)
	}
	// 标准采集器族存在。
	require.Contains(t, scrape, "go_goroutines")
	require.Contains(t, scrape, "process_")
	// 直方图族出现。
	require.Contains(t, scrape, "sydom_grpc_request_duration_seconds_bucket")
	require.Contains(t, scrape, "sydom_authz_check_duration_seconds")

	// OB-4：/metrics 暴露文本绝不含 secret/会话敏感词（指标只有计数/延迟/有界枚举标签）。
	for _, banned := range []string{"secret", "Secret", "password", "app_secret", "session"} {
		require.NotContains(t, scrape, banned, "OB-4 违规：/metrics 含敏感词 %q", banned)
	}
}
