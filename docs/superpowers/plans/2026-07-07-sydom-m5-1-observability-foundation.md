# M5.1 可观测性基座 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 给控制面（mgmt/relay gRPC + REST/Console HTTP）与 sidecar 数据面装上 Prometheus `/metrics`（RED + 授权域指标）+ 结构化访问日志关联 + health/ready 全接入，**零触碰判定/求值算法核心**。

**架构：** 新 `internal/obs` 包（自持 `*prometheus.Registry` 无全局态）+ 类型化助手；gRPC 拦截器（最外层，计入 authz 拒绝）+ net/http 中间件（路由模板标签）记 RED + 发访问日志；sidecar 判定 allow/deny 经 gRPC 拦截器读响应、缓存命中经 `kernel.New(…, c cache.Cache, …)` 既有注入缝的指标装饰 cache；每进程既有 `HealthAddr` 端口并入 `/metrics`。观测性 fail-open。

**技术栈：** Go、`github.com/prometheus/client_golang`（经 `GOPROXY=https://mirrors.aliyun.com/goproxy/,direct` 添加——默认 proxy.golang.org 本环境不可达）、gRPC 拦截器、net/http 中间件、`log/slog`、testify、testcontainers。

**规格：** `docs/superpowers/specs/2026-07-07-sydom-m5-1-observability-foundation-design.md`（BASE=main `e31efff`；含 OB-1..7）。

**范式纪律：** 子代理驱动 + 两阶段审查（规格→质量）；TDD；每任务独立 commit；**禁 `--amend`**。**零触碰判定/求值算法核心**：`casbin/`、`internal/controlplane/adminauthz/`、`internal/sidecar/kernel/`（engine/enforce/cache 逻辑）、`internal/sidecar/dataperm/`（filter/render 逻辑）内容 diff=0；埋点只在服务边界（拦截器/中间件）与既有注入缝（`kernel.New` 的 cache 参数）。

---

## 关键环境约束（所有含 `go get`/`go build`/`go test` 的步骤适用）

**默认 `GOPROXY=https://proxy.golang.org,direct` 在本环境不可达（EOF）。** 凡需拉取新模块（仅任务 1 的 `go get`）必须用：
```bash
GOPROXY=https://mirrors.aliyun.com/goproxy/,direct go get <module>
```
`go build`/`go test` 在依赖已入本地 module cache 后**无需网络**（正常跑即可）。`client_golang` 及其传递依赖（beorn7/perks、prometheus/common、procfs、client_model）经上述镜像已验证可下载。

---

## 文件结构（先锁定分解）

| 文件 | 职责 | 任务 |
|---|---|---|
| `go.mod` / `go.sum` | 加 `prometheus/client_golang` 显式依赖 | 1 |
| `internal/obs/metrics.go` | `Metrics{registry, 指标向量}` + `New()` + 类型化助手 + `Handler()` | 1 |
| `internal/obs/metrics_test.go` | registry 含预期指标、助手增量、**OB-3 基数安全** | 1 |
| `internal/obs/ops.go` | `OpsHandler(m, ready)`：`/metrics` + `/healthz` + `/readyz`（复用 health） | 1 |
| `internal/obs/ops_test.go` | ops mux 三路由 | 1 |
| `internal/obs/grpc.go` | `UnaryServerInterceptor(m)`：RED 指标 + 访问日志 + request_id | 2 |
| `internal/obs/grpc_test.go` | 拦截器按 code 增量、访问日志一条、request_id | 2 |
| `internal/obs/logctx.go` | `With`/`From`（nil-safe） | 2 |
| `internal/obs/logctx_test.go` | round-trip + 缺省不 nil | 2 |
| `internal/obs/http.go` | `HTTPMiddleware(m, next)`：路由模板标签 + 访问日志 + status 捕获 | 3 |
| `internal/obs/http_test.go` | 模板标签（非具体 path）、status 捕获、OB-3 基数安全 | 3 |
| `internal/obs/cache.go` | `NewMetricsCache(inner cache.Cache, m)`：装饰 `Get` 计命中/未命中 | 4 |
| `internal/obs/cache_test.go` | 命中/未命中计数、透传语义不变 | 4 |
| `internal/sidecar/kernel/cache.go` | +`NewBoundedCache(n) cache.Cache` 纯加法导出（不改 LRU 逻辑） | 4 |
| `internal/controlplane/mgmt/server.go` | `NewGRPCServer` +`m *obs.Metrics` 参数，prepend obs 拦截器（最外层） | 5 |
| `internal/controlplane/policysync/server.go` | `NewGRPCServer` +`m *obs.Metrics` 参数，prepend obs 拦截器 | 5 |
| mgmt/restgw/其它测试 | 更新 `NewGRPCServer` 调用点传 `nil`（nil-safe） | 5 |
| `internal/controlplane/app/run.go` | 建 `obs.New()`、传拦截器、包 REST/Console 中间件、ops 端口并入 `/metrics` | 5 |
| `internal/sidecar/app/run.go` | 建 `obs.New()`、authz gRPC 经 opts 传拦截器、注入指标 cache、ops 并入 `/metrics` | 5 |
| `docs/superpowers/2026-07-07-m5-1-observability-walkthrough.md` | 抓 `/metrics` 演示 + 核验记录 | 6 |

**关键决策：** obs 拦截器在控制面须**最外层**（prepend 进 `NewGRPCServer` 内建链），才能计入被 authz 拦截器拒绝的请求（`code=PermissionDenied` 是权限系统核心信号）；nil-safe 使测试传 `nil` 即 pass-through，churn 最小。sidecar 数据面 gRPC 无鉴权层，经既有 `opts` 传拦截器即可（无需改 `authz.NewGRPCServer`）。缓存指标经 `kernel.New` 既有 cache 注入缝，`kernel/cache.go` 仅加一个导出构造器、LRU 逻辑零改。

---

## 任务 1：`internal/obs` 包核心（registry + 指标 + 助手 + ops handler）

**文件：**
- 修改：`go.mod`、`go.sum`
- 创建：`internal/obs/metrics.go`、`internal/obs/metrics_test.go`、`internal/obs/ops.go`、`internal/obs/ops_test.go`

参考既有：`internal/health/health.go`（`Handler(ready Checker) http.Handler`，`Checker func(ctx) error`，serves `/healthz`+`/readyz`）。

- [ ] **步骤 1：加 prometheus 依赖**

运行：`GOPROXY=https://mirrors.aliyun.com/goproxy/,direct go get github.com/prometheus/client_golang@v1.19.1`
预期：`go.mod` 出现 `github.com/prometheus/client_golang v1.19.1`（首次为 `// indirect`，import 后 `go mod tidy` 会去掉 indirect 注释）。

- [ ] **步骤 2：写 `internal/obs/metrics.go`**

```go
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
		cacheHits: prometheus.NewCounter(prometheus.CounterOpts{Name: "sydom_cache_hits_total", Help: "sidecar 决策缓存命中"}),
		cacheMiss: prometheus.NewCounter(prometheus.CounterOpts{Name: "sydom_cache_misses_total", Help: "sidecar 决策缓存未命中"}),
		snapApplied: prometheus.NewCounter(prometheus.CounterOpts{Name: "sydom_sidecar_snapshot_applied_total", Help: "sidecar 快照应用次数"}),
		connected: prometheus.NewGauge(prometheus.GaugeOpts{Name: "sydom_sidecar_connected", Help: "sidecar 是否连接控制面(0/1)"}),
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
func (m *Metrics) CacheHit()  { if m != nil { m.cacheHits.Inc() } }
func (m *Metrics) CacheMiss() { if m != nil { m.cacheMiss.Inc() } }
func (m *Metrics) SnapshotApplied() { if m != nil { m.snapApplied.Inc() } }
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
```
> 说明：HTTP `code` 标签用 `statusClass`（2xx/4xx/5xx）进一步降基数；gRPC `code` 用 `codes.Code.String()`（有界枚举，直接用）。所有助手 nil-safe（`m == nil` → no-op），使控制面/测试可在不建 metrics 时传 nil。

- [ ] **步骤 3：写 `internal/obs/metrics_test.go`（TDD——先写测试，含 OB-3 基数安全）**

```go
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
	// 只应有 1 条 series（handler=模板, method=GET, code=2xx），值=2。
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
```
运行确认失败：`go test ./internal/obs/ -run TestMetrics -v`（编译失败：包未建）→ 写完 metrics.go 后 PASS。

- [ ] **步骤 4：写 `internal/obs/ops.go`**

```go
package obs

import (
	"net/http"

	"github.com/nickZFZ/Sydom/internal/health"
)

// OpsHandler 组合内部 ops 端口的路由：/metrics（Prometheus）+ /healthz + /readyz（复用 health）。
// 免鉴权（内部 ops 端口，沿用明文健康探针约定）；指标不含 secret/敏感值。
func OpsHandler(m *Metrics, ready health.Checker) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", m.Handler())
	h := health.Handler(ready)
	mux.Handle("/healthz", h)
	mux.Handle("/readyz", h)
	return mux
}
```

- [ ] **步骤 5：写 `internal/obs/ops_test.go`**

```go
package obs

import (
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
```
> `readAll` 小助手读 body 转 string（`io.ReadAll` + `resp.Body.Close()`）——在本测试文件内定义。

- [ ] **步骤 6：验证 + gofmt + Commit**

运行：`go mod tidy`（去 client_golang 的 indirect 注释）；`go test ./internal/obs/ -v`（全绿）；`gofmt -l internal/obs/`（空）；`go build ./...`。
```bash
git add go.mod go.sum internal/obs/metrics.go internal/obs/metrics_test.go internal/obs/ops.go internal/obs/ops_test.go
git commit -m "feat(obs): M5.1 可观测性基座核心(自持 Prometheus registry+低基数指标+nil-safe 助手+ops handler 并 /metrics+health)"
```
> 提交尾部空行后加 `Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>`。**禁 --amend**。

---

## 任务 2：gRPC 拦截器 + 访问日志 + logctx

**文件：**
- 创建：`internal/obs/grpc.go`、`internal/obs/grpc_test.go`、`internal/obs/logctx.go`、`internal/obs/logctx_test.go`

参考既有：`internal/controlplane/types.go`（`OperatorFromContext(ctx) string` 取 principal，未设返回 "system"）；gRPC `google.golang.org/grpc`、`google.golang.org/grpc/status`、`google.golang.org/grpc/codes`、`google.golang.org/grpc/metadata`。

- [ ] **步骤 1：写 `internal/obs/logctx.go`**

```go
package obs

import (
	"context"
	"log/slog"
)

type logCtxKey struct{}

// With 把请求级 logger 注入 context。
func With(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, logCtxKey{}, l)
}

// From 取请求级 logger；缺省返回 slog.Default()（绝不 nil）。
func From(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(logCtxKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}
```

- [ ] **步骤 2：写 `internal/obs/logctx_test.go`**

```go
package obs

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLogCtx_RoundTrip(t *testing.T) {
	l := slog.New(slog.NewTextHandler(nil, nil))
	ctx := With(context.Background(), l)
	require.Same(t, l, From(ctx))
}

func TestLogCtx_DefaultNotNil(t *testing.T) {
	require.NotNil(t, From(context.Background()))
}
```
> `slog.NewTextHandler(nil, ...)` 会 panic（nil writer）——改用 `io.Discard`：`slog.New(slog.NewTextHandler(io.Discard, nil))`，import `io`。

- [ ] **步骤 3：写失败测试 `internal/obs/grpc_test.go`**

```go
package obs

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestUnaryInterceptor_CountsAndClassifiesCode(t *testing.T) {
	m := New()
	interceptor := m.UnaryServerInterceptor(nil) // logger=nil → 用 slog.Default
	info := &grpc.UnaryServerInfo{FullMethod: "/sydom.admin.v1.AdminService/ListRoles"}

	// OK 一次
	_, err := interceptor(context.Background(), nil, info,
		func(ctx context.Context, req any) (any, error) { return "ok", nil })
	require.NoError(t, err)
	// PermissionDenied 一次（模拟被 authz 拒——obs 在最外层能计入）
	_, err = interceptor(context.Background(), nil, info,
		func(ctx context.Context, req any) (any, error) { return nil, status.Error(codes.PermissionDenied, "no") })
	require.Error(t, err)

	require.Equal(t, 1.0, testutil.ToFloat64(m.grpcReqs.WithLabelValues("AdminService", "ListRoles", "OK")))
	require.Equal(t, 1.0, testutil.ToFloat64(m.grpcReqs.WithLabelValues("AdminService", "ListRoles", "PermissionDenied")))
}
```
运行：`go test ./internal/obs/ -run TestUnaryInterceptor -v` → FAIL（`UnaryServerInterceptor` 未定义）。

- [ ] **步骤 4：写 `internal/obs/grpc.go`**

```go
package obs

import (
	"context"
	"log/slog"
	"strings"
	"time"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// UnaryServerInterceptor 记 gRPC RED 指标 + 每请求一条结构化访问日志（request_id·principal·app·code·耗时）。
// 应挂在拦截器链**最外层**（计入被 auth/authz 拒绝的请求）。m==nil 时退化为纯访问日志（指标 no-op）。
func (m *Metrics) UnaryServerInterceptor(logger *slog.Logger) grpc.UnaryServerInterceptor {
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		rid := requestIDFromMD(ctx)
		ctx = With(ctx, logger.With("request_id", rid))
		resp, err := handler(ctx, req)
		dur := time.Since(start)
		code := status.Code(err)
		service, method := splitFullMethod(info.FullMethod)
		m.ObserveGRPC(service, method, code.String(), dur)
		logger.Info("grpc_request",
			"request_id", rid, "service", service, "method", method,
			"code", code.String(), "duration_ms", dur.Milliseconds(),
			"principal", cp.OperatorFromContext(ctx), "app", appIDOf(req))
		return resp, err
	}
}

// splitFullMethod "/pkg.Svc/Method" → ("Svc","Method")；异常输入退化为 ("unknown", full)。
func splitFullMethod(full string) (service, method string) {
	full = strings.TrimPrefix(full, "/")
	i := strings.LastIndex(full, "/")
	if i < 0 {
		return "unknown", full
	}
	svc := full[:i]
	if j := strings.LastIndex(svc, "."); j >= 0 {
		svc = svc[j+1:] // pkg.AdminService → AdminService
	}
	return svc, full[i+1:]
}

// appIDOf 复用既有 appIDGetter 式鸭子类型：请求含 GetAppId() 则取，否则空（低基数，仅进日志）。
func appIDOf(req any) uint64 {
	if g, ok := req.(interface{ GetAppId() uint64 }); ok {
		return g.GetAppId()
	}
	return 0
}

// requestIDFromMD 入站 metadata x-request-id 有则透传，无则生成。
func requestIDFromMD(ctx context.Context) string {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if v := md.Get("x-request-id"); len(v) > 0 && v[0] != "" {
			return v[0]
		}
	}
	return newRequestID()
}
```
新增 `internal/obs/reqid.go`（供 gRPC + HTTP 共用）：
```go
package obs

import (
	"crypto/rand"
	"encoding/hex"
)

// newRequestID 生成 16 字节随机 hex（无外部依赖；仅作关联标识，非安全令牌）。
func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b[:])
}
```
> `codes` import 未直接用可删（`status.Code` 返回 `codes.Code`，`.String()` 即可，无需显式 import codes——以编译为准，gofmt/vet 会提示）。

- [ ] **步骤 5：运行确认通过 + gofmt + Commit**

运行：`go test ./internal/obs/ -v`（全绿）；`gofmt -l internal/obs/`（空）；`go vet ./internal/obs/`。
```bash
git add internal/obs/grpc.go internal/obs/grpc_test.go internal/obs/logctx.go internal/obs/logctx_test.go internal/obs/reqid.go
git commit -m "feat(obs): M5.1 gRPC 拦截器(最外层记 RED+访问日志+request_id 透传/生成,app 经 GetAppId 鸭子类型)+logctx nil-safe"
```

---

## 任务 3：net/http 中间件（路由模板标签 + 访问日志）

**文件：**
- 创建：`internal/obs/http.go`、`internal/obs/http_test.go`

参考既有：Console/restgw 均返回 `http.Handler` 并挂 `http.Server{Handler: ...}`（`internal/controlplane/app/run.go:117,133`）；Console 用 `http.NewServeMux()` 方法感知模式（`r.Pattern` 在匹配后被填充，Go 1.22）。

- [ ] **步骤 1：写失败测试 `internal/obs/http_test.go`**

```go
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
```
运行：`go test ./internal/obs/ -run TestHTTPMiddleware -v` → FAIL。

- [ ] **步骤 2：写 `internal/obs/http.go`**

```go
package obs

import (
	"log/slog"
	"net/http"
	"time"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
)

// statusRecorder 捕获写出的 status（默认 200）。
type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(c int) {
	s.code = c
	s.ResponseWriter.WriteHeader(c)
}

// HTTPMiddleware 记 HTTP RED（handler=路由模板，非具体 path）+ 每请求一条访问日志。
// next 须是设置 r.Pattern 的路由器（Go 1.22 ServeMux）；未匹配路由 handler 标签退化为 "other"。
func (m *Metrics) HTTPMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rid := r.Header.Get("X-Request-Id")
		if rid == "" {
			rid = newRequestID()
		}
		w.Header().Set("X-Request-Id", rid)
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		r = r.WithContext(With(r.Context(), logger.With("request_id", rid)))
		next.ServeHTTP(rec, r)
		dur := time.Since(start)
		handlerLabel := r.Pattern // Go 1.22 ServeMux 匹配后填充
		if handlerLabel == "" {
			handlerLabel = "other"
		}
		m.ObserveHTTP(handlerLabel, r.Method, rec.code, dur)
		logger.Info("http_request",
			"request_id", rid, "handler", handlerLabel, "method", r.Method,
			"status", rec.code, "duration_ms", dur.Milliseconds(),
			"principal", cp.OperatorFromContext(r.Context()))
	})
}
```
> **restgw 注意**：若 restgw 的路由器不是标准 `http.ServeMux`（不填 `r.Pattern`），`handlerLabel` 会退化为 "other"（基数安全但信息少）。任务 5 接线时确认 restgw 是否 ServeMux；若不是，restgw 的 `route.pattern` 已知，可在 restgw 侧把匹配到的 pattern 写进 `r.Pattern`（`r.SetPathValue` 不适用）或改用 restgw 自有标签注入——**此细节留任务 5 落地时按 restgw 实际路由实现处置，不确定就停下问控制者**。Console 用标准 ServeMux，`r.Pattern` 可用。

- [ ] **步骤 3：运行确认通过 + gofmt + Commit**

运行：`go test ./internal/obs/ -v`；`gofmt -l internal/obs/`（空）；`go vet ./internal/obs/`。
```bash
git add internal/obs/http.go internal/obs/http_test.go
git commit -m "feat(obs): M5.1 net/http 中间件(路由模板标签防基数爆炸+status 捕获+访问日志+X-Request-Id 透传回写)"
```

---

## 任务 4：sidecar 指标 cache 装饰器 + `kernel.NewBoundedCache` 导出

**文件：**
- 创建：`internal/obs/cache.go`、`internal/obs/cache_test.go`
- 修改：`internal/sidecar/kernel/cache.go`（**仅加一个导出构造器，LRU 逻辑零改**）

参考既有：`internal/sidecar/kernel/cache.go`（`boundedCache` 实现 `github.com/casbin/casbin/v3/persist/cache`.`Cache`：`Get(key)(bool,error)`、`Set(key,bool,...)error`、`Delete(key)error`、`Clear()error`；未命中 `Get` 返回非 nil error）；`newBoundedCache(capacity int) *boundedCache`（现为未导出）。

- [ ] **步骤 1：加 `kernel.NewBoundedCache` 导出（cache.go 末尾追加，不改既有任何行）**

```go
// NewBoundedCache 导出有界 LRU 构造器（容量<=0 视为 1），返回 casbin cache.Cache。
// 供 obs 指标装饰器包裹复用（不改缓存/判定逻辑）。
func NewBoundedCache(capacity int) cache.Cache { return newBoundedCache(capacity) }
```
运行：`gofmt -l internal/sidecar/kernel/`（空）；`go build ./internal/sidecar/kernel/`。
> 零触碰核验（本步骤后）：`git diff internal/sidecar/kernel/cache.go` 应只显示**新增** `NewBoundedCache` 一函数，既有 `boundedCache`/`newBoundedCache`/`Get`/`Set` 等 0 改动。

- [ ] **步骤 2：写失败测试 `internal/obs/cache_test.go`**

```go
package obs

import (
	"testing"

	"github.com/nickZFZ/Sydom/internal/sidecar/kernel"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestMetricsCache_HitMiss(t *testing.T) {
	m := New()
	c := NewMetricsCache(kernel.NewBoundedCache(8), m)

	_, err := c.Get("k") // 未命中
	require.Error(t, err)
	require.NoError(t, c.Set("k", true))
	v, err := c.Get("k") // 命中
	require.NoError(t, err)
	require.True(t, v)

	require.Equal(t, 1.0, testutil.ToFloat64(m.cacheMiss))
	require.Equal(t, 1.0, testutil.ToFloat64(m.cacheHits))
}
```
运行：`go test ./internal/obs/ -run TestMetricsCache -v` → FAIL。

- [ ] **步骤 3：写 `internal/obs/cache.go`**

```go
package obs

import "github.com/casbin/casbin/v3/persist/cache"

// metricsCache 装饰任意 casbin cache.Cache：在 Get 处计命中/未命中，其余透传。
// 不改被装饰缓存的任何语义（命中/未命中由 inner.Get 的 error 判定：非 nil=未命中）。
type metricsCache struct {
	inner cache.Cache
	m     *Metrics
}

// NewMetricsCache 用指标装饰 inner；注入 kernel.New 的 cache 参数即可为决策缓存计命中率。
func NewMetricsCache(inner cache.Cache, m *Metrics) cache.Cache {
	return &metricsCache{inner: inner, m: m}
}

func (c *metricsCache) Get(key string) (bool, error) {
	v, err := c.inner.Get(key)
	if err != nil {
		c.m.CacheMiss()
		return v, err
	}
	c.m.CacheHit()
	return v, nil
}

func (c *metricsCache) Set(key string, value bool, extra ...interface{}) error {
	return c.inner.Set(key, value, extra...)
}
func (c *metricsCache) Delete(key string) error { return c.inner.Delete(key) }
func (c *metricsCache) Clear() error            { return c.inner.Clear() }
```
> 核对 casbin `cache.Cache` 接口方法签名（`Get`/`Set`/`Delete`/`Clear`）以 `internal/sidecar/kernel/cache.go` 既有 `boundedCache` 实现为准逐字对齐；若接口有额外方法（如带 TTL 的 Set 变体）一并透传。

- [ ] **步骤 4：运行确认通过 + 零触碰 diff + gofmt + Commit**

运行：`go test ./internal/obs/ -run TestMetricsCache -v`；`gofmt -l internal/obs/ internal/sidecar/kernel/`（空）。
零触碰核验：
```bash
git diff internal/sidecar/kernel/ internal/sidecar/dataperm/ internal/controlplane/adminauthz/ | grep -E '^[+-]' | grep -v '^[+-][+-]' | grep -vE '^\+.*NewBoundedCache|^\+// NewBoundedCache|^\+func NewBoundedCache|^\+\t*return newBoundedCache|^\+}'
```
预期：除 `NewBoundedCache` 新增函数外无任何判定/求值逻辑改动（此 grep 应无输出或仅剩空行）。
```bash
git add internal/obs/cache.go internal/obs/cache_test.go internal/sidecar/kernel/cache.go
git commit -m "feat(obs+kernel): M5.1 决策缓存命中率指标(metricsCache 装饰器经 kernel.New 注入缝;kernel 仅加 NewBoundedCache 导出,LRU 逻辑零改)"
```

---

## 任务 5：接线（控制面 + sidecar 起 /metrics、挂拦截器/中间件、注入指标 cache）

**文件：**
- 修改：`internal/controlplane/mgmt/server.go`、`internal/controlplane/policysync/server.go`（`NewGRPCServer` prepend obs 拦截器 + 新 `m *obs.Metrics` 参数）
- 修改：这两个 `NewGRPCServer` 的**全部调用点**（含测试 `dialMgmt`、restgw `newTestGW`、endtoend 等）传 `nil`（nil-safe）
- 修改：`internal/controlplane/app/run.go`、`internal/sidecar/app/run.go`

参考既有：`mgmt.NewGRPCServer(srv, resolver, enf, db, logger, opts ...grpc.ServerOption)` 内建 `grpc.ChainUnaryInterceptor(sanitize, auth, authz, statuswrite)`（server.go:238）；`policysync.NewGRPCServer(srv, res, opts...)`；`authz.NewGRPCServer(a, relay, opts...)`（sidecar，无鉴权链）；控制面 `app.Run` 装配（run.go:83-153，含 grpcOpts、restgw/console handler、health 服务于 `cfg.HealthAddr`）；sidecar `app.Run`（run.go:29-101，`kernel.New(cfg.Domain, nil, table)`、`authz.NewGRPCServer(authzr, syncCli, grpcOpts...)`、health 服务于 `cfg.HealthAddr`）。

- [ ] **步骤 1：`mgmt.NewGRPCServer` prepend obs 拦截器**

在 `mgmt/server.go` 的 `NewGRPCServer` 签名加参数 `m *obs.Metrics`（放 logger 后），把 obs 拦截器放链**最前**：
```go
func NewGRPCServer(srv *AdminServer, resolver auth.SecretResolver, enf *adminauthz.Enforcer, db *sql.DB, logger *slog.Logger, m *obs.Metrics, opts ...grpc.ServerOption) *grpc.Server {
	chain := grpc.ChainUnaryInterceptor(
		m.UnaryServerInterceptor(logger),                                    // -1. 最外层：RED 指标 + 访问日志（计入下游 auth/authz 拒绝）
		SanitizeErrorUnaryInterceptor(logger),
		auth.UnaryServerInterceptorExempt(resolver, UnauthenticatedMethods),
		AuthzUnaryInterceptor(enf),
		StatusWriteUnaryInterceptor(db),
	)
	// ... 其余不变
}
```
import `"github.com/nickZFZ/Sydom/internal/obs"`。`m.UnaryServerInterceptor(logger)` 对 `m==nil` 仍返回有效拦截器（内部指标 no-op，仍发访问日志）——故测试传 nil 安全。

- [ ] **步骤 2：`policysync.NewGRPCServer` 同样 prepend**

`policysync/server.go` 的 `NewGRPCServer` 加 `m *obs.Metrics` 参数，若其现无 ChainUnaryInterceptor 则新建一条只含 obs 拦截器；若已有链则 prepend。以该文件实际结构为准（读 server.go:161 起）。

- [ ] **步骤 3：更新全部调用点传 nil（编译驱动）**

运行：`go build ./... 2>&1 | grep NewGRPCServer` 找出所有报参数不匹配的调用点。逐个在 `m` 位置传 `nil`（测试与非生产装配均传 nil；仅 `app.Run` 传真实 `obs.New()` 结果——步骤 4/5）。已知调用点至少：`internal/controlplane/mgmt/server_test.go`（dialMgmt）、`internal/controlplane/restgw/handler_test.go`（newTestGW）、`internal/controlplane/console/handler_test.go`（newConsole 若直接调）、`internal/controlplane/mgmt/endtoend_test.go`、`internal/controlplane/app/run.go`。
运行至 `go build ./...` 与 `go vet ./...` 干净。

- [ ] **步骤 4：控制面 `app.Run` 接线**

在 `internal/controlplane/app/run.go` 的 `Run` 内：
1. 建 metrics：`m := obs.New()`（在装配早期）。
2. mgmt：`grpcSrv := mgmt.NewGRPCServer(adminSrv, operatorResolver, enforcer, db, logger, m, grpcOpts...)`。
3. relay：`syncSrv := policysync.NewGRPCServer(syncCore, appResolver, m, grpcOpts...)`。
4. REST：`restSrv = &http.Server{Handler: m.HTTPMiddleware(logger, restgw.NewHandler(adminSrv, operatorResolver, enforcer, db, logger))}`。
5. Console：`consoleSrv = &http.Server{Handler: m.HTTPMiddleware(logger, console.NewHandler(...))}`。
6. ops 端口并入 `/metrics`：把 `health.Handler(cpReadiness(db, rdb))` 换成 `obs.OpsHandler(m, cpReadiness(db, rdb))`：
```go
healthSrv = &http.Server{Handler: obs.OpsHandler(m, cpReadiness(db, rdb))}
logger.Info("control plane ops enabled", "ops_addr", cfg.HealthAddr) // /metrics + /healthz + /readyz
```
> **restgw 路由标签**：若 REST 中间件的 `r.Pattern` 为空（restgw 非标准 ServeMux），按任务 3 步骤 2 的说明处理（确认 restgw 路由实现；不确定停下问控制者）。

- [ ] **步骤 5：sidecar `app.Run` 接线**

在 `internal/sidecar/app/run.go` 的 `Run` 内：
1. `m := obs.New()`。
2. 指标 cache 注入：`engine, err := kernel.New(cfg.Domain, obs.NewMetricsCache(kernel.NewBoundedCache(1024), m), table)`（替换原 `kernel.New(cfg.Domain, nil, table)`；容量 1024 与内建默认一致）。
3. authz gRPC 经 opts 传 obs 拦截器（sidecar 无鉴权链，obs 即最外层）：`grpcOpts = append(grpcOpts, grpc.ChainUnaryInterceptor(m.UnaryServerInterceptor(logger)))` 然后 `authSrv := authz.NewGRPCServer(authzr, syncCli, grpcOpts...)`。
   - 但 sidecar 判定 allow/deny 需从**响应**读——obs 通用拦截器只记 RED，不读 CheckResponse。**判定计数用一个 sidecar 专用小拦截器**（在 sidecar app 内联，读 `resp.(*authv1.CheckResponse).GetAllowed()` / BatchCheck 逐条 → `m.AuthzDecision(...)` + `m.ObserveCheck(dur)`），与 obs.UnaryServerInterceptor 链在一起。此拦截器约 20 行，放 `internal/sidecar/app/metrics_interceptor.go`（新建），只读响应不改判定。
4. 快照/连接指标：在 sync 客户端应用快照、连接状态变化的回调处调 `m.SnapshotApplied()`/`m.SetConnected(...)`——以 `syncclient` 既有回调/事件点为准（读 syncclient 接口；若无干净事件点，本项可留 TODO 注释并在汇报中标注为 DONE_WITH_CONCERNS，勿臆造）。
5. ops 并入 `/metrics`：`healthSrv = &http.Server{Handler: obs.OpsHandler(m, func(ctx) error { return authzr.Ready() })}`。
> sidecar 判定拦截器与快照/连接 hook 的落点需读 `internal/sidecar/authz/server.go`、`internal/sidecar/syncclient/` 实际结构——**不确定就停下问控制者，勿臆造 gRPC 响应类型或事件回调**。

- [ ] **步骤 6：验证 + 零触碰 + gofmt + Commit**

运行：`gofmt -l internal/`（空）；`go vet ./...`；`go build ./...`；`go test ./internal/controlplane/... ./internal/sidecar/... ./internal/obs/ -count=1`（全绿——既有测试因 `NewGRPCServer` 加参已在步骤 3 修好）。
零触碰核验：
```bash
git diff main..HEAD -- internal/sidecar/kernel/ internal/sidecar/dataperm/ internal/sidecar/authz/authorizer.go internal/controlplane/adminauthz/ casbin/ | grep -E '^\+' | grep -v NewBoundedCache | grep -vE '^\+\+\+' | wc -l
```
预期：0（除 kernel 的 NewBoundedCache 导出外，判定/求值逻辑零改；`sidecar/authz/server.go` 判定分发不改——判定计数经 app 层拦截器读响应）。
```bash
git add -A
git commit -m "feat(cp+sidecar): M5.1 接线 obs(mgmt/relay 拦截器最外层+REST/Console 中间件+ops 端口并 /metrics+sidecar 判定拦截器读响应+指标 cache 注入)"
```

---

## 任务 6：整体核验 OB-1..7 + 抓 /metrics 演示 + 最终评审 + FF

**文件：** 无代码改动（除演示涌现修复）；产出 `docs/superpowers/2026-07-07-m5-1-observability-walkthrough.md`。

- [ ] **步骤 1：OB-1 零触碰机器核验**

```bash
BASE=$(git merge-base main HEAD)
git diff $BASE..HEAD -- internal/sidecar/kernel/ internal/sidecar/dataperm/ internal/controlplane/adminauthz/ casbin/ | grep -E '^\+' | grep -v '^\+\+\+' | grep -v NewBoundedCache
```
预期：无判定/求值逻辑行（仅 kernel `NewBoundedCache` 导出）。`sidecar/authz/authorizer.go`（判定分发）与 `sidecar/authz/server.go` 的判定语句不改（判定计数在 app 层拦截器）。

- [ ] **步骤 2：全量验证**

```bash
gofmt -l internal/          # 空
go vet ./...                # 干净
go test ./...               # 0 FAIL（含 obs + 既有全量，NewGRPCServer 加参调用点全修）
```

- [ ] **步骤 3：抓 `/metrics` 演示（OB-7，无 UI 故非 axe 走查）**

起控制面 + sidecar（用既有 `deploy/docker-compose.yaml` 或 build-tag 脚手架/直接 `go run cmd/...` 配 ops 端口），驱动若干 mgmt gRPC + REST/Console HTTP + sidecar Check（allow 与 deny 各若干），`curl http://<ops-addr>/metrics`，核验：
- `sydom_grpc_requests_total{...,code="OK"}` 与 `{code="PermissionDenied"}`（证明最外层计入拒绝）；
- `sydom_http_requests_total{handler="/apps/{app_id}/...",...}`（模板标签、低基数）；
- `sydom_authz_decisions_total{decision="allow"}` 与 `{decision="deny"}` 随 Check 增长；
- `sydom_cache_hits_total`/`misses_total`、`go_*`/`process_*` 存在；
- **OB-3**：多个 app_id 请求后 `sydom_http_requests_total` 的 series 数不随 app_id 增长（标签无 app_id）；
- **OB-4**：`/metrics` 全文与访问日志绝不含 secret/会话值（grep 扫）。
记录到 walkthrough.md 并 commit。**演示纪律**：停后台按确切 PID；脚手架/产物走查后删除未提交。

- [ ] **步骤 4：最终整体评审**

派子代理逐条核验 OB-1..7：OB-1 零触碰（diff 证明）、OB-2 fail-open（指标/日志失败不阻断主路径——读代码确认助手 nil-safe/不返 error）、OB-3 低基数（标签无业务维度 + 测试断言）、OB-4 无 secret（/metrics + 日志扫）、OB-5 ops 端口隔离免鉴权、OB-6 request_id 关联、OB-7 抓 /metrics 见关键 series。产出 READY 或阻断清单。

- [ ] **步骤 5：更新记忆**

`project_detailed_design_progress.md` 加 M5.1 节；`MEMORY.md` M5 索引起头标 M5.1 ✅。

- [ ] **步骤 6：FF 并入本地 main + 问用户 push**

```bash
git -C /home/tongyu/codes/Sydom merge --ff-only worktree-feat+m5-1-observability-foundation
```
核实 main==feature tip；push origin 与否问用户；清理 worktree。

---

## 自检（写完计划后，对照规格）

**1. 规格覆盖度：**
- §2.1 obs 包 registry+助手+ServeOps/OpsHandler → 任务 1 ✅
- §2.2 gRPC 拦截器 + §2.5 logctx → 任务 2 ✅
- §2.3 net/http 中间件（路由模板标签）→ 任务 3 ✅
- §2.4 sidecar 缓存指标（注入缝）+ 判定/快照/连接 → 任务 4（缓存）+ 任务 5（判定拦截器/快照/连接接线）✅
- §3 指标契约（低基数）→ 任务 1 指标定义 + 任务 3 模板标签测试 ✅
- §5 配置（ops 端口复用 HealthAddr）→ 任务 5 ✅
- §6 零触碰 → 任务 4/5/6 diff 核验 ✅
- OB-1..7 → 各任务 + 任务 6 ✅
- §8 测试策略 → 各任务 TDD + 任务 6 ✅；§9 任务分解 → 6 任务 ✅

**2. 占位符扫描：** 无 TODO。两处刻意标注「以实际结构为准/不确定就停下问控制者」（任务 3 restgw 路由标签、任务 5 sidecar 判定拦截器响应类型 + syncclient 快照/连接事件点）——因这些依赖 restgw 路由实现与 syncclient/authz server 的真实结构，实现者须对齐真实代码、勿臆造；这是刻意的实现期核对点，非计划缺口。

**3. 类型一致性：**
- `obs.Metrics` 助手签名（`ObserveGRPC/ObserveHTTP/AuthzDecision/ObserveCheck/CacheHit/CacheMiss/SetConnected/SnapshotApplied`）任务 1 定义 → 任务 2/3/4/5 一致引用 ✅
- `obs.New() *Metrics`、`(*Metrics).UnaryServerInterceptor(logger) grpc.UnaryServerInterceptor`、`(*Metrics).HTTPMiddleware(logger, next) http.Handler`、`OpsHandler(m, ready) http.Handler`、`NewMetricsCache(cache.Cache, *Metrics) cache.Cache`、`kernel.NewBoundedCache(int) cache.Cache`、`With/From` → 全计划一致 ✅
- `NewGRPCServer(...对应包..., m *obs.Metrics, opts...)` 新签名 → 任务 5 全调用点（含测试传 nil）一致 ✅

对照无缺口。
