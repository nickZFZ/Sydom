# 司域 · Sidecar 鉴权 API (④-4) 详细设计

> 版本：v0.1 | 日期：2026-06-05 | 状态：草稿

## 1. 范围与定位

Sidecar 内部结构（④）的第 4 个子项目，也是数据面对外的**鉴权出口**。组合 ④-1 内核 `kernel.Engine`（功能鉴权）+ ④-2 `dataperm.Filter`（数据权限下推）+ ④-3 `syncclient.SyncClient` 暴露的陈旧信号，对外提供 check / batch-check / filter，并落地**陈旧度守卫**（fail-close 底线 + 可配陈旧上限）。

**上游依赖（均已实现并入 main）**：
- ④-1 `internal/sidecar/kernel`：`Engine.Enforce(sub,dom,obj,act)` / `BatchEnforce([][]string)` / `Ready()` / `Version()` / 哨兵 `ErrNotReady`/`ErrForeignDomain`。
- ④-2 `internal/sidecar/dataperm`：`Filter.FilterSQL(user,dom,resource,attrs) (SQLResult,error)` / `FilterRaw(...) (RawResult,error)`；哨兵 `ErrMissingVar`/`ErrInvalidPolicy`。
- ④-3 `internal/sidecar/syncclient`：`SyncClient.Ready()` / `LastSyncAt() time.Time` / `Connected()`。

**交付边界**：与 ③/④ 一贯——**仅库/组件层，不出 cmd/binary**。进程装配（构造 Engine+Table+Filter+SyncClient+Authorizer、起 `SyncClient.Run` goroutine、注册 gRPC、监听本地端点、传输安全、配置 MaxStaleness）留后续 cmd 子项目。

**不在范围**：
- explain（`EnforceEx` 返回命中规则）——内核 `Engine` 当前无 `EnforceEx`，需回头动 ④-1，YAGNI 推迟。
- roles 自省、gRPC `FilterRaw`（仅 Go 门面，见决策 4）。
- 业务→Sidecar 本地回环以外的认证——HMAC 是 Sidecar→控制面（② `internal/auth`）的事；业务进程与 Sidecar 同机回环，v1 不加 HMAC，传输安全（unix socket 权限 / mTLS）留 cmd。

## 2. 设计决策（头脑风暴逐条确认）

1. **交付形态 = Authorizer 门面 + gRPC AuthService**。先做 Go `Authorizer` 门面（组合 Engine+Filter+陈旧守卫，真逻辑所在），再包一层 gRPC `AuthService`（proto+handler，bufconn 可测）。与架构 spec §5 的 HTTP/gRPC API、本仓 gRPC-first 惯例（③-3 mgmt、③-2 sync）一致；仍不出 cmd。
2. **陈旧度策略 = 可配陈旧上限，超阈转拒**。`!Ready()` 一律 fail-close 是底线（对齐架构 §2.2「异常路径默认拒绝」）；额外配 `MaxStaleness`：`now - LastSyncAt() > MaxStaleness` 视为太陈旧 → fail-close。`MaxStaleness=0` 关闭该守卫（Ready 即持续服务陈旧快照，「陈旧可用」）。阈值与 fail-open/close 的最终取舍归调用方（④-3 决策 2 把阈值留给 ④-4，④-4 再把"无法判定"信号上抛给 SDK）。
3. **v1 暴露面 = Check / BatchCheck / FilterSQL / FilterRaw**（门面）。基线三件 + FilterRaw（dataperm 已现成，供 ORM/多语言 SDK 自渲染条件树）。explain/roles 推迟。
4. **gRPC FilterRaw 推迟**。gRPC `AuthService` v1 只出 Check/BatchCheck/FilterSQL；FilterRaw 仅 Go 门面（供进程内 Go 调用方）。理由：FilterRaw 返回「变量已解析的递归条件树」（`*dataperm.Condition`，叶子值 any），忠实跨 proto 要建递归 `Condition` 消息 + 译码器，自成一档子设计，留作快跟。
5. **fail-close 贯穿 + 区分"无法判定"与"判定为拒"**。所有公开方法先过陈旧守卫；not-ready/too-stale 以独立错误（→ gRPC `Unavailable`）上抛，**绝不**伪装成 `allowed=false`，否则调用方无法对低风险点自定 fail-open。

## 3. 组件分解

新包 `internal/sidecar/authz`（与控制面 `adminauthz` 不同路径，无冲突）。

| 文件 | 职责 | 依赖 |
|---|---|---|
| `authorizer.go` | `Authorizer` 门面 + `Config`(MaxStaleness) + 窄接口 `Freshness` + 构造 | kernel、dataperm |
| `errors.go` | 哨兵 `ErrTooStale`（`ErrNotReady` 复用 kernel 的） | — |
| `server.go` | gRPC `AuthService` handler 包装 Authorizer + `NewGRPCServer` + 错误码映射 + WKT 译码 | gen authv1、dataperm、structpb |

外加：`api/proto/sydom/auth/v1/auth.proto`（`go_package = ".../gen/sydom/auth/v1;authv1"`）+ 生成代码 `gen/sydom/auth/v1/`。

**对 ④-1 的唯一改动**：`internal/sidecar/kernel/engine.go` 给 `Engine` 加 `Domain() string { return e.domain }` getter。让 Authorizer 的 pin 域来自内核**单一真相源**，而非平行配置（平行配置一旦与内核 pin 域不一致，`Enforce` 恒 `ErrForeignDomain` → deny-all，是隐蔽的一致性事故）。改动极小、纯增、不碰既有行为，与 ④-2 给 `types.go` 加 `Effect` 字段同性质。

## 4. Authorizer 门面（核心逻辑所在，authorizer.go）

```go
// Freshness 暴露同步新鲜度信号；*syncclient.SyncClient 满足之。窄接口便于测试注入。
type Freshness interface {
    Ready() bool
    LastSyncAt() time.Time
}

// Config 是 Authorizer 的策略参数。
type Config struct {
    MaxStaleness time.Duration // 0=关闭陈旧守卫（Ready 即服务）；>0 时超阈 fail-close
}

type Authorizer struct {
    engine *kernel.Engine
    filter *dataperm.Filter
    fresh  Freshness
    domain string        // = engine.Domain()，构造时取，单一真相源
    cfg    Config
    now    func() time.Time // 注入便于测试（默认 time.Now）
}

func New(engine *kernel.Engine, filter *dataperm.Filter, fresh Freshness, cfg Config) *Authorizer
```

- **陈旧守卫** `checkFresh() error`：`!fresh.Ready()` → `ErrNotReady`；`cfg.MaxStaleness>0 && (LastSyncAt 为零 || now-LastSyncAt > MaxStaleness)` → `ErrTooStale`；否则 nil。每个公开方法首行过守卫。
- `Check(sub,obj,act string) (bool,error)`：守卫 → `engine.Enforce(sub, a.domain, obj, act)`。
- `BatchCheck(reqs []CheckReq) ([]bool,error)`（`CheckReq{Subject,Object,Action string}`）：守卫 → 用 pin 域组 `[][]string{{sub,domain,obj,act},...}` → `engine.BatchEnforce`。
- `FilterSQL(sub,resource string, attrs map[string]any) (dataperm.SQLResult,error)`：守卫 → `filter.FilterSQL(sub, a.domain, resource, attrs)`。
- `FilterRaw(sub,resource string, attrs map[string]any) (dataperm.RawResult,error)`：守卫 → `filter.FilterRaw(...)`。

> 注：`engine.Enforce` 自身也先判 `Ready`（fail-close）；陈旧守卫在其上叠加 `MaxStaleness` 上限——防御纵深，与一致性优先一致。

## 5. gRPC AuthService 契约（v1，auth.proto）

```proto
service AuthService {
  rpc Check(CheckRequest) returns (CheckResponse);
  rpc BatchCheck(BatchCheckRequest) returns (BatchCheckResponse);
  rpc FilterSQL(FilterRequest) returns (FilterSQLResponse);
}

message CheckRequest  { string subject = 1; string object = 2; string action = 3; }
message CheckResponse { bool allowed = 1; }

message BatchCheckRequest  { repeated CheckRequest requests = 1; }
message BatchCheckResponse { repeated bool allowed = 1; } // 与 requests 等长同序

message FilterRequest { string subject = 1; string resource = 2; google.protobuf.Struct attrs = 3; }
message FilterSQLResponse {
  string sql = 1;                              // 无过滤=空串；deny-all="1=0"；否则参数化片段
  repeated google.protobuf.Value args = 2;     // 占位符实参（JSON 标量，值绝不进 SQL 文本）
}
```

- 请求体**不带 domain**：域由 Sidecar pin（强隔离，对齐 ③ 的 app_id 不入请求体）。
- `attrs`/`args` 用 WKT `google.protobuf.Struct`/`Value` 承载 JSON 标量（`import "google/protobuf/struct.proto"`，buf 自带 protocompile 支持 WKT）。handler 经 `structpb` 互转：`req.GetAttrs().AsMap()` → `map[string]any`；`SQLResult.Args []any` 逐个 `structpb.NewValue`（含 nil→NullValue）。
- buf lint：`CheckRequest` 作 `Check` 请求且被 `BatchCheckRequest` 以**字段**嵌入（非另一 RPC 的请求/响应角色），不触 `RPC_REQUEST_RESPONSE_UNIQUE`；若 lint 仍报，按 ③-3 既有先例加豁免、不改契约。

## 6. 错误映射 / fail-close 语义（关键）

鉴权 API **把"无法判定"与"判定为拒"分开**，让调用方自定 fail-open/close（决策 5）：

| 来源 | Authorizer 返回 | gRPC code | 调用方语义 |
|---|---|---|---|
| 未就绪 | `ErrNotReady` | `Unavailable` | 无可用决策 → 按自定 fail-open/close 策略处置 |
| 太陈旧 | `ErrTooStale` | `Unavailable` | 同上（与传输断线 Unavailable 统一：SDK 不关心"为何无决策"，只据此走自定策略） |
| 正常判定 | `(allowed, nil)` | OK | `allowed` 是确定结论（true/false 都算判定） |
| attrs 不足 | `dataperm.ErrMissingVar` | `InvalidArgument` | 调用方入参问题，补全 attrs 重试 |
| 命中中毒策略 | `dataperm.ErrInvalidPolicy` | `FailedPrecondition` | 服务端数据损坏（非瞬时）→ fail-close，拿不到条件即不放数据 |
| 越域（pin 域下不应发生） | `kernel.ErrForeignDomain` | `Internal` | 配置错（Authorizer 域 ≠ 内核 pin 域） |

要点：not-ready/too-stale **不返回 `allowed=false`**——否则调用方无法区分"被策略拒"与"系统没法判"，也就无法对白名单低风险点自定 fail-open。

## 7. 测试策略（纯内存，无 Docker）

- **Authorizer 单测**（真实 Engine apply 快照 + 真实 Filter + fake `Freshness` + 注入 `now`）：
  - 未就绪 → `ErrNotReady`；`MaxStaleness>0` 且超阈 → `ErrTooStale`；`MaxStaleness=0` 关闭 → Ready 即放行。
  - 正常 allow/deny（含经角色继承的 Check）；BatchCheck 多请求等长同序。
  - FilterSQL 反映 deny override（`(... AND NOT (...))`）；FilterRaw 返回合并树；`ErrMissingVar` 透传。
- **AuthService 单测**（bufconn + 真实 Authorizer）：Check/BatchCheck/FilterSQL RPC 往返 + 错误码映射（`Unavailable`/`InvalidArgument`/`FailedPrecondition`）；`attrs`/`args` 经 WKT 往返保真。
- **端到端**：快照带功能策略（g+p）+ allow/deny 数据策略 → `Check` 反映角色继承、`FilterSQL` 反映 deny override（贯通 ④-1/④-2/④-4）。
- **陈旧守卫边界**：注入 `now` 与 fake `LastSyncAt`，断言恰好等于阈值放行、超 1ns 即拒。
- 全程 `-race`（陈旧守卫读 vs 同步写并发）。

## 8. 移交 cmd 装配

构造 Engine+Table+Filter+SyncClient+Authorizer、起 `SyncClient.Run` goroutine、`authv1.RegisterAuthServiceServer`、监听本地端点（unix socket / loopback）、传输安全、按部署配置 `MaxStaleness`、可观测性（metrics/审计日志）。

## 9. 自检结果

- **占位符扫描**：无 TODO/待定；组件/门面方法/错误矩阵/proto 契约均具体。
- **内部一致性**：决策 1（门面+gRPC）贯穿 §3/§4/§5；决策 2（陈旧上限）贯穿 §4 守卫/§6 映射；决策 4（gRPC 无 FilterRaw）贯穿 §1/§5；决策 5（区分无法判定/判定为拒）贯穿 §6。`Engine.Domain()` 的 ④-1 改动在 §3 声明、§4 消费。
- **范围检查**：单一关注点（鉴权出口），单计划可覆盖；explain/roles/gRPC-FilterRaw/cmd 明确划出。
- **模糊性检查**：域 pin（不入请求体）、陈旧守卫与 engine 内置 Ready 的叠加关系、not-ready 用 Unavailable 而非 allowed=false、WKT 译码点均已明确。

相关：架构 `2026-05-30-sydom-architecture-design.md`（§3 casbin 能力边界 / §5 数据面 API）；④-1 内核 `2026-06-03-sydom-sidecar-kernel-design.md`（Enforce/Ready 语义）；④-2 dataperm `2026-06-04-sydom-sidecar-data-policy-engine-design.md`（FilterSQL/FilterRaw/中毒 fail-close）；④-3 同步客户端 `2026-06-04-sydom-sidecar-sync-client-design.md`（陈旧信号来源、阈值留 ④-4）；[[feedback-consistency-over-simplicity]]。
