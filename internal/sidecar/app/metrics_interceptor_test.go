package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	authv1 "github.com/nickZFZ/Sydom/gen/sydom/auth/v1"
	"github.com/nickZFZ/Sydom/internal/obs"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// TestDecisionInterceptor_CountsFromResponse 核验 sidecar 判定拦截器**只读响应**得到 allow/deny 计数：
// Check → 一条判定；BatchCheck → 逐条判定。经真实 obs.OpsHandler /metrics 抓取断言，不触碰判定分发。
func TestDecisionInterceptor_CountsFromResponse(t *testing.T) {
	m := obs.New()
	interceptor := decisionInterceptor(m)

	// Check：allow 一次。
	checkInfo := &grpc.UnaryServerInfo{FullMethod: "/sydom.auth.v1.Authz/Check"}
	_, err := interceptor(context.Background(), &authv1.CheckRequest{}, checkInfo,
		func(ctx context.Context, req any) (any, error) {
			return &authv1.CheckResponse{Allowed: true}, nil
		})
	require.NoError(t, err)

	// BatchCheck：逐条 [true,false,true] → 再 +2 allow / +1 deny。
	batchInfo := &grpc.UnaryServerInfo{FullMethod: "/sydom.auth.v1.Authz/BatchCheck"}
	_, err = interceptor(context.Background(), &authv1.BatchCheckRequest{}, batchInfo,
		func(ctx context.Context, req any) (any, error) {
			return &authv1.BatchCheckResponse{Allowed: []bool{true, false, true}}, nil
		})
	require.NoError(t, err)

	// 抓 /metrics 断言累计：allow=3、deny=1、Check 耗时直方图出现。
	ts := httptest.NewServer(obs.OpsHandler(m, nil))
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/metrics")
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	_ = resp.Body.Close()
	scrape := string(body)

	require.Contains(t, scrape, `sydom_authz_decisions_total{decision="allow"} 3`)
	require.Contains(t, scrape, `sydom_authz_decisions_total{decision="deny"} 1`)
	require.Contains(t, scrape, "sydom_authz_check_duration_seconds_count 2") // 每 RPC 一次 → 2 次
}
