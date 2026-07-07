// Package obs 是可观测性基座：自持 Prometheus registry（无全局态，测试隔离）+ 类型化指标助手。
// 严守低基数：tenant/app/user/resource/action/具体 path 一律进日志、绝不进指标标签。
package obs

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics 自持一个 registry 与全部 Sydom 指标向量。每进程一个（New）。
type Metrics struct {
	reg *prometheus.Registry

	grpcReqs    *prometheus.CounterVec   // service, method, code
	grpcDur     *prometheus.HistogramVec // service, method
	httpReqs    *prometheus.CounterVec   // handler, method, code
	httpDur     *prometheus.HistogramVec // handler, method
	authzDec    *prometheus.CounterVec   // decision(allow/deny)
	checkDur    prometheus.Histogram
	cacheHits   prometheus.Counter
	cacheMiss   prometheus.Counter
	snapApplied prometheus.Counter
	connected   prometheus.Gauge
}

// New 构造并注册全部指标 + Go/process 采集器。
func New() *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		reg: reg,
		grpcReqs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "sydom_grpc_requests_total", Help: "gRPC 请求总数"}, []string{"service", "method", "code"}),
		grpcDur: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "sydom_grpc_request_duration_seconds", Help: "gRPC 请求耗时", Buckets: prometheus.DefBuckets}, []string{"service", "method"}),
		httpReqs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "sydom_http_requests_total", Help: "HTTP 请求总数"}, []string{"handler", "method", "code"}),
		httpDur: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "sydom_http_request_duration_seconds", Help: "HTTP 请求耗时", Buckets: prometheus.DefBuckets}, []string{"handler", "method"}),
		authzDec: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "sydom_authz_decisions_total", Help: "sidecar 授权判定数"}, []string{"decision"}),
		checkDur: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "sydom_authz_check_duration_seconds", Help: "sidecar Check 耗时", Buckets: prometheus.DefBuckets}),
		cacheHits:   prometheus.NewCounter(prometheus.CounterOpts{Name: "sydom_cache_hits_total", Help: "sidecar 决策缓存命中"}),
		cacheMiss:   prometheus.NewCounter(prometheus.CounterOpts{Name: "sydom_cache_misses_total", Help: "sidecar 决策缓存未命中"}),
		snapApplied: prometheus.NewCounter(prometheus.CounterOpts{Name: "sydom_sidecar_snapshot_applied_total", Help: "sidecar 快照应用次数"}),
		connected:   prometheus.NewGauge(prometheus.GaugeOpts{Name: "sydom_sidecar_connected", Help: "sidecar 是否连接控制面(0/1)"}),
	}
	reg.MustRegister(m.grpcReqs, m.grpcDur, m.httpReqs, m.httpDur, m.authzDec,
		m.checkDur, m.cacheHits, m.cacheMiss, m.snapApplied, m.connected)
	reg.MustRegister(collectors.NewGoCollector())
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}

// Handler 暴露 registry。
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}

// —— 类型化助手（隐藏向量细节；nil 接收者安全，便于测试/关闭路径）——

func (m *Metrics) ObserveGRPC(service, method, code string, d time.Duration) {
	if m == nil {
		return
	}
	m.grpcReqs.WithLabelValues(service, method, code).Inc()
	m.grpcDur.WithLabelValues(service, method).Observe(d.Seconds())
}

func (m *Metrics) ObserveHTTP(handler, method string, code int, d time.Duration) {
	if m == nil {
		return
	}
	cs := statusClass(code)
	m.httpReqs.WithLabelValues(handler, method, cs).Inc()
	m.httpDur.WithLabelValues(handler, method).Observe(d.Seconds())
}

func (m *Metrics) AuthzDecision(allow bool) {
	if m == nil {
		return
	}
	d := "deny"
	if allow {
		d = "allow"
	}
	m.authzDec.WithLabelValues(d).Inc()
}

func (m *Metrics) ObserveCheck(d time.Duration) {
	if m == nil {
		return
	}
	m.checkDur.Observe(d.Seconds())
}

func (m *Metrics) CacheHit() {
	if m != nil {
		m.cacheHits.Inc()
	}
}

func (m *Metrics) CacheMiss() {
	if m != nil {
		m.cacheMiss.Inc()
	}
}

func (m *Metrics) SnapshotApplied() {
	if m != nil {
		m.snapApplied.Inc()
	}
}

func (m *Metrics) SetConnected(c bool) {
	if m == nil {
		return
	}
	v := 0.0
	if c {
		v = 1
	}
	m.connected.Set(v)
}

// statusClass 把 HTTP 状态码归一为低基数类别（2xx/3xx/4xx/5xx），防每码一 series。
func statusClass(code int) string {
	switch {
	case code >= 500:
		return "5xx"
	case code >= 400:
		return "4xx"
	case code >= 300:
		return "3xx"
	case code >= 200:
		return "2xx"
	default:
		return "other"
	}
}
