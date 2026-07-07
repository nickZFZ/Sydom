# M5.1 可观测性基座（Observability Foundation）— 设计

> 里程碑：M5.1（M5「运维就绪 + 生产硬化」第 1 子项目）。BASE=main `e31efff`（M4.5 tip，M4 全部完结）。
> 路线图定位：M5 经范围评估拆 5 子项目：**M5.1 可观测性基座**（本节）/ M5.2 安全硬化 / M5.3 部署硬化 / M5.4 HA / M5.5 性能。策略 A（可演示纵向切片）：M5.1 最基础、后续 HA/性能都要靠它度量。

## 1. 目标与范围

给司域控制面 + sidecar 数据面装上**生产级可观测性基座**——三件事，都在服务边界/缓存层做加法，**零触碰授权判定与数据面求值逻辑**：

- **件① Prometheus `/metrics`**：控制面（mgmt gRPC / relay gRPC / REST 网关 / Console BFF）+ sidecar 数据面（Check/BatchCheck/FilterSQL 热路径 + LRU 缓存）各出一个 `/metrics` 端点，暴露 RED 指标（请求量/错误/延迟）+ 授权域指标（allow/deny 决策、缓存命中、快照）。
- **件② 结构化访问日志 + 关联**：一层访问日志中间层（gRPC 拦截器 + net/http 中间件），每请求出**一条**结构化日志（`method`·`code`·`duration_ms`·`request_id`·`tenant`·`app`·`principal`）；`logctx` 助手把请求级 `*slog.Logger` 注入 context 供 handler 取用。**不横扫改写既有 `h.logger` 调用**。
- **件③ health·ready 全接入**：复用既有 `internal/health`，把 `/healthz`（存活）+ `/readyz`（就绪）接入每进程的 ops 监听器（与 `/metrics` 同端口）。

**为什么这样切**：可观测性是运维就绪的地基——你要先能看见（决策速率、延迟、缓存命中、错误分布），才能谈 HA/性能/告警。M5.1 纯加法、不碰授权真相，风险最低、可演示（真实抓 `/metrics`）。

**非目标 / 裁剪（YAGNI，留后续 M5.x）**：分布式 tracing（OpenTelemetry；otel 现仅间接依赖，需全链埋点 + collector）；Grafana 仪表盘/告警规则打包；全 handler 日志横扫改写；指标持久化/远程写；SLO 定义（M5 验收级，非 M5.1）。

## 2. 架构

新增 `internal/obs` 包（无全局状态，便于测试隔离），三处接线点复用它：

```
                          ┌────────────── internal/obs ──────────────┐
                          │ Metrics{registry, 指标向量}  New()        │
                          │ ServeOps(addr, m, ready) → /metrics       │
                          │   + /healthz + /readyz（复用 internal/health）│
                          │ UnaryServerInterceptor(m) [gRPC]          │
                          │ HTTPMiddleware(m, next)   [net/http]       │
                          └───────────────────────────────────────────┘
控制面进程：mgmt gRPC 链 + relay gRPC 链 挂 UnaryServerInterceptor；
           REST + Console 外层包 HTTPMiddleware；起一个 ops 监听器。
sidecar 进程：Check/BatchCheck/FilterSQL 服务路径 + LRU 缓存层加指标自增；起一个 ops 监听器。
logctx：访问日志层构建关联 logger 注入 ctx；handler 用 logctx.From(ctx) 取用（可选）。
```

### 2.1 `internal/obs` 包
- `type Metrics struct` 自持 `*prometheus.Registry`（**非全局默认 registry**——每进程一个，测试可独立断言），注册 Go/process 采集器（`collectors.NewGoCollector()`、`NewProcessCollector()`）+ 全部 Sydom 指标向量。
- `New() *Metrics`：构造并注册所有指标。
- `(m *Metrics) Handler() http.Handler`：`promhttp.HandlerFor(m.registry, ...)`。
- `ServeOps(ctx, addr string, m *Metrics, ready health.Checker) (stop func(), err error)`：起 ops HTTP 服务，mux 挂 `/metrics`（`m.Handler()`）+ `/healthz` + `/readyz`（`health.Handler(ready)`）。
- 类型化记录助手（供拦截器/sidecar 调用，隐藏指标向量细节）：`ObserveGRPC(service, method, code string, dur time.Duration)`、`ObserveHTTP(handler, method string, code int, dur time.Duration)`、`AuthzDecision(allow bool)`、`ObserveCheck(dur)`、`CacheHit()` / `CacheMiss()`、`SnapshotApplied()`、`SetConnected(bool)`。

### 2.2 gRPC 拦截器（mgmt + relay）
`UnaryServerInterceptor(m)` 包裹每次调用：计时 → 调 handler → 从返回 err 取 `status.Code()` → `m.ObserveGRPC(...)` + 发访问日志（`request_id`：入站 metadata 有则透传、无则生成；`principal`/`tenant`/`app` 从已认证 ctx 取）。挂在 mgmt/relay 既有拦截器链**尾部**（authz 之后，确保 `code`/principal 已定）。

### 2.3 net/http 中间件（REST + Console）
`HTTPMiddleware(m, next)` 包裹 handler：包 `ResponseWriter` 捕获 status → 计时 → `m.ObserveHTTP(handler, method, code, dur)` + 访问日志。**`handler` 标签取路由模板**（Go 1.22 `http.Request.Pattern`，如 `/v1/apps/{app_id}/roles`），**绝不用具体 path**（防基数爆炸）。

### 2.4 sidecar 数据面（全部经既有注入缝/服务边界，不改判定与缓存逻辑）
- **判定 allow/deny + Check 延迟**：sidecar gRPC 服务本就支持 `grpc.ServerOption`（`authz.NewGRPCServer(a, relay, opts...)`）——用一个 sidecar gRPC `UnaryServerInterceptor` **从响应**读判定（`CheckResponse.Allowed`；BatchCheck 逐条）得到 `AuthzDecision(allow/deny)` + `ObserveCheck(dur)`。**`authz/server.go`、`authorizer.go` 均不改**。
- **缓存命中/未命中**：`kernel.New(domain, c cache.Cache, applier)` 本就**接受注入的 `cache.Cache`**（当前 sidecar 传 `nil` 走内置 `newBoundedCache`）。改由 sidecar 启动时注入一个**指标装饰 cache**（`obs` 提供，包裹一个有界 LRU，在 `Get` 命中/未命中处计数）经此缝传入。**`kernel/cache.go`（LRU 逻辑）不改**；若需复用其有界实现，至多加一个**纯加法导出构造器** `kernel.NewBoundedCache(n) cache.Cache`（不改任何缓存/判定逻辑）。
- **快照应用 / 连接状态**：在 sidecar app 的同步客户端「应用快照」「连接状态变化」回调处加 `SnapshotApplied()`/`SetConnected(...)`（sync 客户端编排层，非求值核心）。

### 2.5 logctx
`logctx.With(ctx, *slog.Logger) ctx` / `logctx.From(ctx) *slog.Logger`（缺省返回一个 no-attr 的进程 logger，绝不返回 nil）。访问日志中间层在计时前构建带关联字段的 logger 注入 ctx。

## 3. 指标契约（严守低基数）

**铁律：`tenant`/`app`/`user`/`resource`/`action`/具体 path 一律进日志，绝不进指标标签**（否则基数爆炸）。标签仅限有界枚举集。

| 指标 | 类型 | 标签（低基数） | 面 |
|---|---|---|---|
| `sydom_grpc_requests_total` | Counter | `service`,`method`,`code` | 控制面 gRPC |
| `sydom_grpc_request_duration_seconds` | Histogram | `service`,`method` | 控制面 gRPC |
| `sydom_http_requests_total` | Counter | `handler`(路由模板),`method`,`code` | REST+Console |
| `sydom_http_request_duration_seconds` | Histogram | `handler`,`method` | REST+Console |
| `sydom_authz_decisions_total` | Counter | `decision`(allow/deny) | sidecar |
| `sydom_authz_check_duration_seconds` | Histogram | （无标签） | sidecar |
| `sydom_cache_hits_total` / `sydom_cache_misses_total` | Counter | （无标签） | sidecar LRU |
| `sydom_sidecar_snapshot_applied_total` | Counter | （无标签） | sidecar |
| `sydom_sidecar_connected` | Gauge | （无标签，0/1） | sidecar |
| `go_*` / `process_*` | — | — | 两面（标准采集器） |

Histogram bucket 用 Prometheus 默认 `DefBuckets`（0.005..10s），够覆盖 gRPC/HTTP/Check 常见延迟；不过早定制。

## 4. 数据流与错误处理

- **观测性 fail-open（与授权 fail-close 相反且正确）**：指标记录/日志失败绝不影响主请求路径——助手内不返回 error、不 panic；ops 端口起不来只记 error 不阻断主服务（可配是否强制）。
- **ops 端口免鉴权**：`/metrics`·`/healthz`·`/readyz` 在**内部 ops 端口**明文免鉴权（沿用 M1.5 明文健康探针约定；ops 端口不对公网暴露，由部署层网络策略隔离）。指标不含 secret/敏感值（只有计数/延迟/枚举标签）。
- **request_id**：入站有（gRPC metadata `x-request-id` / HTTP header `X-Request-Id`）则透传，无则生成（ULID/UUID）；写入访问日志与响应（HTTP 回写 header 便于跨系统关联）。

## 5. 配置

- 每进程 ops 监听地址可配（控制面 cmd + sidecar cmd 各加 flag/config，如 `ops_addr`，默认给一个合理端口）。未配则不起 ops 服务（不破坏既有启动）。
- 复用既有 `internal/health.Checker` 作 `/readyz` 就绪判据（控制面：DB 可达；sidecar：已收到首个快照）。

## 6. 零触碰硬约束（OB-1）

**零触碰边界 = 判定/求值算法核心**（这些路径内容 diff=0，机器验证）：
- `casbin/`、`internal/controlplane/adminauthz/`、`internal/sidecar/kernel/`（engine/enforce/cache **逻辑**）、`internal/sidecar/dataperm/`（filter/render **逻辑**）。

**埋点只在服务边界与既有注入缝**（不进入 enforce/filter 算法路径）：
- 控制面：gRPC 拦截器（mgmt/relay 链）、net/http 中间件（REST/Console）——包裹层。
- sidecar：gRPC 拦截器读响应得判定；缓存指标经 `kernel.New(…, c cache.Cache, …)` 注入缝（指标装饰 cache）；快照/连接经 sync 客户端编排层回调。
- **允许的至多加法**：`kernel.NewBoundedCache(n)` 导出构造器（若复用其有界实现所需）——纯新增导出、不改任何逻辑，仍在「逻辑 diff=0」内（新增导出行不改既有语句）。

即：`sidecar/authz/server.go`/`authorizer.go` 的判定分发**不改**（判定经拦截器观测）；`kernel/cache.go` 的 LRU **不改**（经注入装饰）。任一判定/求值语句零改动。

## 7. 安全与正确性不变量（OB-1..OB-7）

- **OB-1 零触碰授权/求值**：决策核心内容 diff=0（§6，机器验证）。
- **OB-2 观测性 fail-open**：指标/日志失败不影响主路径（授权仍 fail-close，二者不冲突）。
- **OB-3 低基数**：指标标签仅有界枚举；业务维度（tenant/app/user/resource/action/具体 path）只进日志——**测试断言 app_id=1 与 =2 命中同一 `handler` 标签**。
- **OB-4 无 secret 泄露**：指标/访问日志绝不含 secret（app_secret、operator secret、会话值）——访问日志字段白名单化。
- **OB-5 ops 端口隔离**：`/metrics`·health 仅在内部 ops 端口、免鉴权、不含敏感值；主服务端口不暴露 `/metrics`。
- **OB-6 request_id 关联**：每请求一条访问日志带 request_id，入站透传/无则生成。
- **OB-7 可演示**：真实抓 `/metrics` 见关键 series（无 UI，故非 axe 走查；用集成测试 + 手动 curl 验证）。

## 8. 测试策略

- **`obs` 单测**：`New()` registry 含预期指标；助手增量用 `prometheus/testutil.ToFloat64`/`CollectAndCount` 断言；**OB-3 基数安全**（两个不同 app_id 的 HTTP 请求命中同一 `handler` 标签值、series 数不随 app_id 增长）。
- **gRPC 拦截器测**：mock handler，断言 `sydom_grpc_requests_total{...}` 按 code 增量、duration 被观测、访问日志出一条带 request_id。
- **HTTP 中间件测**：断言 `handler` 标签=路由模板（非具体 path）、status 捕获正确。
- **sidecar 数据面测**：一次 Check(allow) 增 `decisions_total{decision="allow"}`；一次 deny 增 deny；缓存命中/未命中计数；**判定结果与既有测试逐字节一致**（证明加指标不改行为）。
- **零触碰 diff 核验**：`git diff BASE..HEAD -- <决策核心路径>` = 0。
- **集成/可演示**：起控制面 + sidecar，驱动若干 gRPC/HTTP + 授权调用，抓两个 `/metrics`，断言关键 series 存在、`authz_decisions_total` 随 Check 增长、访问日志带关联字段。

## 9. 任务分解（留给 writing-plans）

1. `internal/obs` 包：registry + 指标向量 + 助手 + `ServeOps`（复用 health）+ 单测（含 OB-3 基数安全）。
2. gRPC `UnaryServerInterceptor` + 访问日志 + `logctx`；挂 mgmt + relay 链；单测。
3. net/http `HTTPMiddleware`（路由模板标签）+ 访问日志；包 REST + Console；单测。
4. sidecar 数据面指标（decisions/check duration/cache/snapshot/connected）+ 单测（判定行为零变）；零触碰 diff 核验。
5. 控制面 + sidecar cmd 接线：起 ops 监听器（`/metrics`+health）、配置 flag；就绪判据接 health。
6. 整体核验 OB-1..7 + 集成抓 `/metrics` 演示 + 最终评审 + FF。

## 10. 自检小结

- **占位符**：无 TODO；指标契约、标签基数、接线点、零触碰路径均具体。
- **一致性**：延续 effperm/M1.5「可托管运维底座」纪律；metrics 加法与授权 fail-close 不冲突（观测性 fail-open 是正确的相反面）。
- **范围**：单一实现计划可覆盖（6 任务，同 M4.x 量级）；tracing/Grafana/日志横扫已明确裁剪。
- **模糊性**：ops 端口免鉴权、低基数标签、request_id 透传/生成、观测 fail-open 均已明确取舍。

相关：[[feedback-consistency-over-simplicity]]、[[project-detailed-design-progress]]
