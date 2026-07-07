package obs

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestMetrics_GRPCCounterIncrements(t *testing.T) {
	m := New()
	m.ObserveGRPC("AdminService", "ListRoles", "OK", 0)
	m.ObserveGRPC("AdminService", "ListRoles", "OK", 0)
	require.Equal(t, 2.0, testutil.ToFloat64(m.grpcReqs.WithLabelValues("AdminService", "ListRoles", "OK")))
}

func TestMetrics_AuthzDecisionLabels(t *testing.T) {
	m := New()
	m.AuthzDecision(true)
	m.AuthzDecision(false)
	m.AuthzDecision(false)
	require.Equal(t, 1.0, testutil.ToFloat64(m.authzDec.WithLabelValues("allow")))
	require.Equal(t, 2.0, testutil.ToFloat64(m.authzDec.WithLabelValues("deny")))
}

// OB-3 基数安全：不同 app_id 的 HTTP 请求命中同一 handler 标签（模板），series 不随 app_id 增长。
func TestMetrics_HTTPLowCardinality(t *testing.T) {
	m := New()
	m.ObserveHTTP("/v1/apps/{app_id}/roles", "GET", 200, 0) // app 1
	m.ObserveHTTP("/v1/apps/{app_id}/roles", "GET", 200, 0) // app 2 —— 同模板标签
	require.Equal(t, 1, testutil.CollectAndCount(m.httpReqs))
	require.Equal(t, 2.0, testutil.ToFloat64(m.httpReqs.WithLabelValues("/v1/apps/{app_id}/roles", "GET", "2xx")))
}

func TestMetrics_NilSafe(t *testing.T) {
	var m *Metrics
	require.NotPanics(t, func() {
		m.ObserveGRPC("s", "x", "OK", 0)
		m.AuthzDecision(true)
		m.CacheHit()
		m.SetConnected(true)
	})
}
