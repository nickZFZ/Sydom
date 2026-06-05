# 司域 · SDK 接口规范（⑤，A+B 切片：核心客户端 + net/http 中间件）详细设计

> 司域详细设计的第 ⑤（最后）个子项目。本切片只覆盖 **A 核心 SDK 客户端** + **B net/http 鉴权中间件**；ORM 数据权限注入、权限点上报、其它语言 SDK 留后续独立子项目。

## 1. 范围与定位

SDK 是架构 §9 定位的**极薄框架胶水层**：不含任何鉴权逻辑（策略存储/同步/决策全在 Sidecar），只做「调用同机 Sidecar 本地 `AuthService` + 框架适配」。

**本轮覆盖（A+B）：**
- **A 核心客户端**（`sdk/go/sydom`）：封装 Sidecar `AuthService`（Check / BatchCheck / FilterSQL，已建于 ④-4），含连接管理与公共 Go API 面、错误语义。
- **B net/http 中间件**（`sdk/go/sydomhttp`）：拦截 HTTP 请求调 `Check`，默认 fail-close，fail-open 可显式 opt-in。

**硬约束：** SDK 被外部业务进程 import，**不能落在 `internal/`**（Go 禁止外部 import `internal/`），故走公共路径 `sdk/go/`，并为后续 Java/Node SDK 留位（`sdk/java`…）。

**不在本轮（见 §9）：** Gin/Echo 适配、GORM/ORM 数据权限自动注入、权限点上报（需新增控制面 app-HMAC 上报 RPC，跨控制面）、其它语言 SDK、mTLS/unix socket 本地传输、决策缓存、metrics/tracing。

## 2. 设计决策（头脑风暴逐条确认）

| # | 决策 | 理由 |
|---|------|------|
| D1 | 公共路径 `sdk/go/`，**不进 `internal/`** | SDK 须可被外部业务 import；`sdk/go/` 留多语言位 |
| D2 | **双包**：`sydom`（核心，零 HTTP 依赖）+ `sydomhttp`（中间件，依赖窄接口 `Checker`） | 核心保持极薄、不用 HTTP 的业务零负担引核心；中间件靠接口解耦、可注入 mock 独立测试 |
| D3 | B 仅覆盖 **net/http** 标准库 | `func(http.Handler) http.Handler` 是所有 Go web 框架通用底座；Gin/Echo 薄适配留后续（YAGNI） |
| D4 | 请求→(subject,object,action) 走**调用方注入的 `Resolver` 函数** | subject 是 app 私有身份、obj/act 映射也 app 私有，SDK 不臆测；另附 `PathMethodResolver` 便利约定 |
| D5 | 默认 **fail-close**；fail-open 作为**每中间件实例显式 opt-in** | 贴合架构 §7「仅在明确评估风险的特定点显式开放」；不同路由组挂不同实例即「每路由 opt-in」 |
| D6 | **区分「无法判定」(`ErrUnavailable`) 与「判定为拒」(allowed=false)** | 项目一致性铁律：not-ready/too-stale/断线绝不伪装成 deny，调用方才能对低风险点自定 fail-open |

## 3. 组件分解

| 包 / 文件 | 职责 |
|---|---|
| `sdk/go/sydom/client.go` | `Client`：拨号 + Check / BatchCheck / FilterSQL / Close；gRPC↔域类型译码 |
| `sdk/go/sydom/options.go` | `Option`：`WithDialOptions` / `WithConn`（注入既有连接，测试/复用） |
| `sdk/go/sydom/errors.go` | 哨兵 `ErrUnavailable`；gRPC code → 错误映射 |
| `sdk/go/sydomhttp/middleware.go` | `New(checker, resolver, opts...)` 中间件 + 每请求终态分流 + context 注入 |
| `sdk/go/sydomhttp/resolver.go` | `Resolver` 类型 + `PathMethodResolver` 便利 helper + `ErrSkipAuth` 哨兵 |
| `sdk/go/sydomhttp/options.go` | `Option`：`WithFailOpen` / `WithDenyHandler` / `WithUnavailableHandler` / `WithErrorHandler` / `WithErrorLog` |
| `sdk/go/sydomhttp/context.go` | 放行后注入 `(sub,obj,act)` 到 `r.Context()`；`FromContext(ctx)` 取出 |

**依赖方向：** `sydomhttp` → `sydom`（仅经窄接口 `Checker`，不依赖 `*Client` 具体类型）。`sydom` 不依赖 `sydomhttp`、不依赖 `net/http`。

## 4. 核心包 `sdk/go/sydom`

### 4.1 公共 API

```go
type Client struct { /* 持有 *grpc.ClientConn + authv1.AuthServiceClient；ownsConn bool */ }

// New 拨 loopback gRPC（insecure 传输）；target 形如 "127.0.0.1:8090"，对齐 ④-4 sidecar auth_addr。
func New(target string, opts ...Option) (*Client, error)

// Close 关闭自持连接；若经 WithConn 注入既有连接，则 Close 不关该连接（归注入方管）。
func (c *Client) Close() error

func (c *Client) Check(ctx context.Context, subject, object, action string) (bool, error)
func (c *Client) BatchCheck(ctx context.Context, reqs []CheckReq) ([]bool, error)
func (c *Client) FilterSQL(ctx context.Context, subject, resource string, attrs map[string]any) (FilterResult, error)

type CheckReq struct{ Subject, Object, Action string }
type FilterResult struct {
    SQL  string // 无过滤=空串；deny-all="1=0"；否则参数化片段（值在 Args，绝不进 SQL 文本）
    Args []any  // 占位符实参（JSON 标量）
}
```

### 4.2 Option

```go
func WithDialOptions(opts ...grpc.DialOption) Option // 进阶：自定义拨号参数
func WithConn(conn *grpc.ClientConn) Option          // 注入既有连接（测试 bufconn / 连接复用）；此时 New 不自拨、Close 不关注入连接
```

### 4.3 连接与传输

- 拨 **loopback TCP**（匹配 ④-4 sidecar `auth_addr`），默认 `insecure.NewCredentials()`——业务→Sidecar 本地回环 v1 不加 HMAC（HMAC 是 Sidecar→控制面的事，与 ④-4 一致）。
- 用 `grpc.NewClient`（非 deprecated 的 `DialContext`）。gRPC 原生重连，SDK **不自建**重试循环。
- `attrs map[string]any` → `structpb.NewStruct`；`FilterSQLResponse.args`（`[]*structpb.Value`）→ `[]any`（`v.AsInterface()`），与 ④-4 server 译码对称。

### 4.4 错误语义（fail-close 内核）

| gRPC 来源 | 核心包返回 |
|---|---|
| `codes.Unavailable`（sidecar not-ready/too-stale **或** 传输不可达） | `(false, ErrUnavailable)` |
| `codes.InvalidArgument`（如 `ErrMissingVar`）/ `FailedPrecondition`（中毒策略）/ `Internal`（如 `ErrForeignDomain`） | `(false, err)`，err 保留 gRPC `status`（调用方可 `status.FromError`） |
| 成功 | `(allowed, nil)` |

- **铁律：** 任何底层出错一律返回 `(false, err)`，**绝不** `(true, err)`——异常路径默认不放行；调用方以 `allowed && err==nil` 判定放行。
- `BatchCheck` 出错返回 `(nil, err)`（整批失败=全部视为不可放行）；成功返回与请求等长同序的 `[]bool`。
- `ErrUnavailable` 把「sidecar 自报无法判定」与「传输断线」统一为同一哨兵——二者对调用方语义一致（此刻拿不到可信决策），由调用方据风险自定 fail-open/close。

```go
var ErrUnavailable = errors.New("sydom: authorization decision unavailable")
```

## 5. 中间件包 `sdk/go/sydomhttp`

### 5.1 公共 API

```go
// Checker 是中间件对核心的窄依赖；*sydom.Client 自动满足。
type Checker interface {
    Check(ctx context.Context, subject, object, action string) (bool, error)
}

// Resolver 从请求解析鉴权三元组；返回 ErrSkipAuth 表示该请求为公开路由、直接放行。
type Resolver func(r *http.Request) (subject, object, action string, err error)

// New 返回标准 net/http 中间件。
func New(checker Checker, resolver Resolver, opts ...Option) func(http.Handler) http.Handler

// PathMethodResolver 便利约定：object=请求 path、action=HTTP method；业务只提供 subject 提取。
// subjectFn 返回 error 时该请求按 fail-close 拒绝（视作无法识别身份）。
func PathMethodResolver(subjectFn func(r *http.Request) (string, error)) Resolver

var ErrSkipAuth = errors.New("sydomhttp: skip authorization")
```

### 5.2 Option

```go
func WithFailOpen() Option                      // Unavailable 时放行（默认 fail-close）；作用域=该中间件实例
func WithDenyHandler(h http.Handler) Option      // 判定为拒 或 resolver 非 skip 错误 的响应（默认 403 Forbidden）
func WithUnavailableHandler(h http.Handler) Option // 无法判定且 fail-close 的响应（默认 503 Service Unavailable）
func WithErrorHandler(h http.Handler) Option     // Check 硬错误的响应（默认 500 Internal Server Error）
func WithErrorLog(fn func(r *http.Request, err error)) Option // 错误日志钩子（SDK 不绑定具体日志库）
```

### 5.3 每请求流程（终态分流）

1. `sub, obj, act, err := resolver(r)`
   - `errors.Is(err, ErrSkipAuth)` → **next**（公开路由）。
   - 其它 `err != nil` → ErrorLog + **DenyHandler（403）**（fail-close；无法识别请求/身份，与判定为拒同归一类）。
2. `allowed, err := checker.Check(r.Context(), sub, obj, act)`
3. 终态：
   - `err == nil && allowed` → 注入 `(sub,obj,act)` 到 context → **next**。
   - `err == nil && !allowed` → **DenyHandler（403）**。
   - `errors.Is(err, sydom.ErrUnavailable)` → `WithFailOpen` 则 **next**；否则 ErrorLog + **UnavailableHandler（503）**。
   - 其它 err（硬错误：Internal/InvalidArgument/FailedPrecondition…）→ ErrorLog + **ErrorHandler（500）**，**fail-open 不豁免**（系统/配置异常不该被 fail-open 掩盖成放行）。

> **`ErrUnavailable`（可 fail-open）vs 硬错误（fail-open 不豁免）的分界**与 ④-4 错误码映射对齐：`Unavailable` 表「此刻无可用决策」（可用性，瞬时），是架构 §7 允许 fail-open 的那一类；`Internal/InvalidArgument/FailedPrecondition` 表「确定性错误」（如越域 deny-all、缺变量、策略中毒），属系统/请求异常，恒 fail-close。

### 5.4 context 注入

放行后把 `(subject,object,action)` 存入 `r.Context()`，业务 handler 经 `sydomhttp.FromContext(ctx) (Decision, bool)` 取用（审计/二次用途）。轻量，不含决策缓存。

## 6. 数据流

```
业务进程
  HTTP req
    └─ sydomhttp.Middleware
         ├─ resolver(r) ──► (sub,obj,act)  | ErrSkipAuth→next | err→403
         ├─ sydom.Client.Check ──gRPC(loopback, insecure)──► Sidecar AuthService.Check
         │                                                      └ kernel.Enforce + 陈旧守卫
         │   ◄── allowed=T/F | Unavailable(not-ready|too-stale) | 硬错误 ──
         ├─ allowed       → 注入 context → next handler
         ├─ deny          → 403
         ├─ unavailable   → 503（默认 fail-close） / next（WithFailOpen）
         └─ 硬错误         → 500（fail-open 不豁免）

数据权限（handler 内直接用，非中间件）：
  client.FilterSQL(ctx, sub, res, attrs) ──► (sql, args) ──► 拼入查询 WHERE
  （ORM 自动注入 = C 层，留后续子项目）
```

## 7. 测试策略（纯 Go，无 Docker）

- **`sydom` 核心包**：用 `bufconn` 起 fake `AuthService`（可编程返回 allow / deny / `codes.Unavailable` / 硬错误码），经 `WithConn` 注入连接，断言：三方法的请求/响应译码（含 `attrs`/`args` 的 structpb 往返）、`Unavailable`→`ErrUnavailable` 哨兵、硬错误码透传保留 `status`、出错恒 `(false,err)`。
- **`sydomhttp` 中间件包**：注入实现 `Checker` 的 mock（可编程 allow/deny/`ErrUnavailable`/硬错误）+ `net/http/httptest`，断言：5 类终态状态码、`WithFailOpen` 对 `ErrUnavailable`/硬错误的差异（前者放行、后者仍拒）、`ErrSkipAuth` 放行、resolver 错误 403、context 注入可被下游取出、各 `WithXxxHandler` 覆盖默认。
- **可选端到端 1 条**：拨真实 ④-4 `authz.NewGRPCServer`（真 `Engine`+`Table`+`Filter`，喂固定快照），经 `sydom.Client` 验证 Check/FilterSQL 真链路贯通——复用 `internal/sidecar/app` 集成测试的 fake `PolicySync` 模式。

## 8. 与既有代码的关系

- **零改动既有包**：核心包只消费已发布的 `gen/sydom/auth/v1`（④-4 契约），中间件只依赖自身窄接口。无需动 `internal/sidecar/*` 或 proto。
- **go.mod**：`google.golang.org/grpc`、`google.golang.org/protobuf` 已是直接依赖（④-4/② 引入），无新模块。

## 9. 不在范围（YAGNI / 留后续子项目）

| 项 | 去向 |
|---|---|
| Gin / Echo 中间件适配 | 后续 B' 薄适配（net/http 是通用底座） |
| GORM / ORM 数据权限自动注入 WHERE | 后续 **C 层**子项目 |
| 权限点上报（启动扫描路由 + 上报控制面） | 后续 **D 层**——**须先在控制面新增 app-HMAC 鉴权的上报 RPC**（当前仅有 operator 凭据的 `AdminService.UpsertPermission`，无 app 凭据上报口） |
| 其它语言 SDK（Java/Node） | `sdk/java`…，公共路径已留位 |
| mTLS / unix socket 本地传输、决策缓存、metrics/tracing | 显式 YAGNI |

## 10. 自检结果（对齐项目铁律）

- **fail-close 默认贯穿**：核心 `Check` 出错恒 `(false,err)`；中间件默认拒绝，fail-open 须显式 opt-in。
- **区分无法判定 vs 判定为拒**：`ErrUnavailable` 哨兵把 not-ready/too-stale/断线与确定性 deny 分开（一致性铁律，呼应 ④-4）。
- **硬错误 fail-open 不豁免**：系统/配置异常（越域 deny-all 等）即便配了 fail-open 也不放行。
- **极薄定位**：SDK 不含任何策略存储/同步/决策逻辑；subject/obj/act 映射全交调用方 resolver，不臆测。
- **边界清晰**：双包单一职责，核心零 HTTP 依赖，中间件靠窄接口可独立测试。
