# M5.1 可观测性基座 — 整体核验 / 走查记录

> 日期：2026-07-07　BASE=main `e31efff`（M4 全部完结）　分支 `worktree-feat+m5-1-observability-foundation`
> 范式：子代理驱动 + 两阶段审查（规格→质量）；TDD；每任务独立 commit；**禁 `--amend`**。
> 规格 `docs/superpowers/specs/2026-07-07-sydom-m5-1-observability-foundation-design.md`（含 OB-1..7）；计划 `docs/superpowers/plans/2026-07-07-sydom-m5-1-observability-foundation.md`（6 任务）。

## 做了什么（一句话）

新增 `internal/obs` 包（**自持** Prometheus registry + 低基数指标 + nil-safe 类型化助手 + gRPC 拦截器 + net/http 中间件 + ops handler + logctx + 决策缓存装饰器），接线到控制面（mgmt/relay gRPC 拦截器**最外层** + REST/Console HTTP 中间件 + ops 端口并 `/metrics`）与 sidecar 数据面（判定拦截器**只读响应** + 指标 cache 经 `kernel.New` 注入缝 + connected gauge + ops 并 `/metrics`）——**零触碰判定/求值算法核心**。

## 提交序列（6 实现/测试 commit，clean，无 --amend）

| SHA | 任务 | 内容 |
|---|---|---|
| `474e7fd` | 1 | obs 核心：自持 registry + 10 指标向量 + nil-safe 助手 + statusClass + OpsHandler（并 /metrics+health）|
| `1ba6763` | 2 | gRPC 拦截器（最外层记 RED + 访问日志 + request_id 透传/生成 + app 经 GetAppId 鸭子类型）+ logctx nil-safe |
| `5f989da` | 3 | net/http 中间件（`r.Pattern` 路由模板标签防基数爆炸 + status 捕获 + 访问日志 + X-Request-Id 回写）|
| `c2e474c` | 4 | 决策缓存命中率指标（metricsCache 装饰器；kernel **仅加** `NewBoundedCache` 导出，LRU 逻辑零改）|
| `ae113b1` | 5 | 接线（mgmt/relay 拦截器最外层 + REST/Console 中间件 + ops 并 /metrics + sidecar 判定拦截器读响应 + 指标 cache 注入）|
| `5e4c47e` | 6 | OB-7 端到端 /metrics 抓取核验 + decisionInterceptor 只读响应逐条计数回归 |

## 指标契约（严守低基数）

| 指标 | 类型 | 标签（低基数） |
|---|---|---|
| `sydom_grpc_requests_total` | Counter | service, method, code |
| `sydom_grpc_request_duration_seconds` | Histogram | service, method |
| `sydom_http_requests_total` | Counter | handler(**路由模板**), method, code(2xx/3xx/4xx/5xx) |
| `sydom_http_request_duration_seconds` | Histogram | handler, method |
| `sydom_authz_decisions_total` | Counter | decision(allow/deny) |
| `sydom_authz_check_duration_seconds` | Histogram | （无标签）|
| `sydom_cache_hits_total` / `sydom_cache_misses_total` | Counter | （无标签）|
| `sydom_sidecar_snapshot_applied_total` | Counter | （无标签；见技术债①，M5.1 恒 0）|
| `sydom_sidecar_connected` | Gauge | （无标签，0/1）|
| `go_*` / `process_*` | 标准采集器 | — |

**铁律**：tenant/app/user/resource/action/具体 path 一律进**日志**、绝不进指标标签。gRPC 的 `app_id` 经 `GetAppId()` 只进日志 `app` 字段。

## OB-1..7 逐条核验（最终评审 READY）

- **OB-1 零触碰授权/求值** — PASS（机器验证）：`git diff e31efff..HEAD -- internal/sidecar/kernel internal/sidecar/dataperm internal/sidecar/authz internal/controlplane/adminauthz casbin` **只有** `kernel/cache.go` 追加 `NewBoundedCache`（薄包 `newBoundedCache`）+4 行，别无判定/求值逻辑改动。`authz/server.go`（Check/BatchCheck 判定分发）、`authorizer.go`、`dataperm/`、`adminauthz/`、`casbin/` 内容 diff=0。判定计数在 app 层 `decisionInterceptor` **读响应**得到。
- **OB-2 观测性 fail-open** — PASS：全部助手 nil-safe（`if m==nil {return}`）且**不返回 error、不 panic**；拦截器/中间件 `resp, err := handler(...)` 后仅计时/记日志，`return resp, err` 原样透传，指标失败绝不改变主路径。
- **OB-3 低基数** — PASS：标签仅有界枚举；`app_id` 只进日志。三处测试断言不同 app_id 命中同一模板标签、series 不增长（`TestMetrics_HTTPLowCardinality`、`TestHTTPMiddleware_RouteTemplateLabel`、`TestScrape_EndToEnd` 两 app_id → httpReqs 恰 2 series）。restgw/Console 均标准 Go 1.22 ServeMux，`r.Pattern` 落真实模板（实证 `handler="GET /v1/apps/{app_id}/roles"`）。
- **OB-4 无 secret 泄露** — PASS：`/metrics` 仅计数/延迟/枚举；访问日志字段白名单化（无 secret/session/cookie）。`TestScrape_EndToEnd` 断言 /metrics 文本 `NotContains` {secret,password,app_secret,session}。
- **OB-5 ops 端口隔离免鉴权** — PASS：`OpsHandler` 挂 /metrics+/healthz+/readyz、无鉴权中间件，接线于既有 `HealthAddr` 独立端口（控制面 + sidecar 各一）；主服务端口（admin/sync/REST/Console/sidecar auth）均无 /metrics 路由。
- **OB-6 request_id 关联** — PASS：gRPC 入站 `x-request-id` 透传/无则生成；HTTP `X-Request-Id` 透传/生成并**回写响应 header**；每请求恰一条访问日志带 request_id。
- **OB-7 可演示** — PASS：见下方端到端抓取。

## OB-7 端到端 /metrics 抓取证据（可复现测试胜过一次性 curl）

`internal/obs/scrape_test.go`（`TestScrape_EndToEnd`）驱动真实 gRPC 拦截器（OK + **PermissionDenied**）+ HTTP 中间件（两 app_id 同模板 + 一个 5xx）+ 判定计数 + 经真实 `kernel.NewBoundedCache` 的缓存装饰器（miss→set→hit），再从真正的 `OpsHandler` 抓 `/metrics`，断言以下关键 series **逐字**出现：

```
sydom_grpc_requests_total{code="OK",method="ListRoles",service="AdminService"} 1
sydom_grpc_requests_total{code="PermissionDenied",method="ListRoles",service="AdminService"} 1   # 最外层计入被拒
sydom_http_requests_total{code="2xx",handler="GET /v1/apps/{app_id}/roles",method="GET"} 2       # 两 app_id 同模板→1 series 值 2
sydom_http_requests_total{code="5xx",handler="GET /v1/boom",method="GET"} 1
sydom_authz_decisions_total{decision="allow"} 1
sydom_authz_decisions_total{decision="deny"} 1
sydom_cache_hits_total 1
sydom_cache_misses_total 1
# + go_goroutines / process_* / *_duration_seconds_bucket 直方图族
```

真实结构化访问日志（同测试运行捕获，佐证 OB-6 request_id 关联 + OB-3 模板标签 + 计入被拒）：

```
INFO grpc_request request_id=75734f76… service=AdminService method=ListRoles code=OK              principal=system app=0
INFO grpc_request request_id=b7357298… service=AdminService method=ListRoles code=PermissionDenied principal=system app=0
INFO http_request request_id=3c7c635d… handler="GET /v1/apps/{app_id}/roles" method=GET status=200 principal=system
INFO http_request request_id=8db1da19… handler="GET /v1/apps/{app_id}/roles" method=GET status=200 principal=system
INFO http_request request_id=0d3b20c0… handler="GET /v1/boom"                 method=GET status=500 principal=system
```

`internal/sidecar/app/metrics_interceptor_test.go`（`TestDecisionInterceptor_CountsFromResponse`）另证 sidecar 判定拦截器**只读响应**逐条计数：Check(allow) + BatchCheck([true,false,true]) → `decision="allow" 3` / `decision="deny" 1` / `check_duration_count 2`（每 RPC 一次耗时）。

## 全量验证

```
gofmt -l internal/     → 空
go vet ./...           → EXIT 0
go build ./...         → EXIT 0
go test ./...          → EXIT 0（全绿；NewGRPCServer 加参的 4 处测试调用点已同步补 nil）
```

## 非阻断观察 / 技术债（不影响 READY，留后续 M5.x）

1. **`sydom_sidecar_snapshot_applied_total` 计划性 defer**：助手 `SnapshotApplied()` 与指标已定义但未 Inc（快照 apply 在 `syncclient.Run` 内部，无干净 app 层 hook；**不改 syncclient 内部**）。sidecar `run.go` 留 `TODO(M5.x)`，指标恒 0，待 syncclient 暴露快照事件 hook 后接入。
2. **流式 RPC 未纳入 RED**：obs 仅 `UnaryServerInterceptor`；`PolicySync.Subscribe`（stream）不计 grpc 指标（sidecar 连接态由 `sydom_sidecar_connected` gauge 侧面覆盖）。基座范围可接受。
3. **gRPC 未把 request_id 回写客户端**：仅生成/透传进日志与 ctx（HTTP 则回写 header）。OB-6 对 gRPC 只要求「有则透传/无则生成」，已满足；HTTP/gRPC 对称性小差。
4. **obs→controlplane 依赖方向**：`obs/grpc.go`、`obs/http.go` 导入 `internal/controlplane` 取 `OperatorFromContext`，使 sidecar 数据面经 obs 传递依赖控制面包（编译无碍、helper 极小）。可日后把 principal helper 下沉共享包解耦。
5. **connected gauge / 缓存注入缝无接线级端到端断言**：connected-gauge 为直白的 5s 轮询 `syncCli.Connected()`；缓存装饰器有 `TestMetricsCache_HitMiss` 单测，但 `kernel.New` 注入缝本身无端到端断言。

## 范式复盘

- **子代理驱动 + 两阶段审查**：每任务全新实现子代理（TDD）→ opus 独立规格合规审查（读实际代码，不信报告）→ 代码质量核验；Task 5（集成/触及零触碰边界）+ 最终 OB-1..7 评审用 opus 深审。
- **零触碰双证**：每次接触 sidecar/kernel 的任务都跑 `git diff` 机器核验；发现计划里的零触碰 grep 命令在本机 ugrep 下对 `^\+\+\+` 报错短路（给出误导性 0），改用 `git show <sha> --numstat -- <核心路径>` 权威核验（本 commit 对核心 0 行）。
- **计划的两处「停下问控制者」在实现期被落实**：restgw 路由标签——确认 restgw/Console 都用标准 http.ServeMux，`r.Pattern` 就地填充可用，无需特殊处理；sidecar 快照/连接事件点——syncclient 无干净 hook，connected 用公开 `Connected()` 轮询接、snapshot 按计划 defer。

**结论：M5.1 可观测性基座 OB-1..7 全部满足，构建/测试全绿，零触碰授权求值核心 diff=0，最终评审 READY。**
