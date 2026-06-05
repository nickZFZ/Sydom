# 司域 · Sidecar 进程装配 (cmd/sydom-sidecar) 详细设计

> 版本：v0.1 | 日期：2026-06-05 | 状态：草稿

## 1. 范围与定位

数据面可执行二进制——把已实现并入 main 的 sidecar 库层（④-1~④-4）装配成一个长驻进程：加载配置、构造 `Table→Engine→Filter→SyncClient→Authorizer`、起 **SyncClient** 对账协程、监听**本地** AuthService（loopback TCP）、响应信号优雅关闭。

这是 cmd 装配拆出的第 2 个（也是最后一个）二进制；`cmd/sydom-controlplane`（控制面二进制）已先行并入 main，本子项目连的就是它暴露的 PolicySync 端点。与控制面 cmd 的关键差异：**Sidecar 不连 DB/Redis**——它唯一的外部连接是经 gRPC 拨控制面 PolicySync 订阅策略，所有权限状态都在进程内存（内核 + 数据权限表），靠对账保持新鲜。

**上游依赖（均已实现并入 main）**：
- ④-1 `internal/sidecar/kernel`：`New(domain string, c cache.Cache, applier DataPolicyApplier) (*Engine, error)`（`c=nil` 内建 1024 LRU；`applier` 接收数据策略）；`Engine.Domain() string`；`Enforce`/`BatchEnforce`/`Ready`/`Version`。
- ④-2 `internal/sidecar/dataperm`：`NewTable() *Table`（实现 `kernel.DataPolicyApplier`）；`NewFilter(roles RoleResolver, table *Table) *Filter`（`*kernel.Engine` 满足 `RoleResolver`）。
- ④-3 `internal/sidecar/syncclient`：`New(cfg Config, engine *kernel.Engine) (*SyncClient, error)`；`Config{Endpoint, AppID, Secret []byte, Secure bool, DialOptions, BackoffInitial, BackoffMax}`；`(*SyncClient).Run(ctx) error`（ctx 取消返回 nil）；`Close() error`；`Ready()`/`LastSyncAt()`（满足 `authz.Freshness`）。
- ④-4 `internal/sidecar/authz`：`New(engine *kernel.Engine, filter *dataperm.Filter, fresh Freshness, cfg Config) *Authorizer`（pin 域取自 `engine.Domain()`）；`Config{MaxStaleness}`；`NewGRPCServer(a *Authorizer) *grpc.Server`。
- ② `internal/auth`：`NewPerRPCCredentials(appID string, secret []byte, secure bool)`（syncclient 内部已用）；`Sign(secret []byte, ...)` 直接以 secret 原始字节作 HMAC key。
- 驱动/库：`google.golang.org/grpc`（含 `credentials/insecure`，syncclient 已用）；`gopkg.in/yaml.v3`（控制面 cmd 已提为直接依赖）；`log/slog`（stdlib）。**零新模块**。

## 2. 设计决策（头脑风暴逐条确认）

1. **薄 main + 可测 `internal/sidecar/app` 包**（镜像 `internal/controlplane/app`）：逻辑全在 `app`；`Run(ctx, cfg, authLis net.Listener, logger *slog.Logger) error` 接受**注入的监听器**，使集成测试用 `127.0.0.1:0`、`main` 用配置地址——全装配可测。
2. **配置 = YAML 文件 + env 覆盖密钥**：非敏感项（`control_plane_addr`/`app_key`/`domain`/`auth_addr`/`max_staleness`/退避）走 YAML；敏感项 HMAC `secret` **只走 env**（`SYDOM_APP_SECRET`，原始字节，镜像控制面 `SYDOM_ROOT_SECRET`），绝不落文件。
3. **`domain` 与 `app_key` 是两个独立字段**（回源核实）：`application` 表里 `domain VARCHAR(64)`（租户内唯一）与 `app_key VARCHAR(64)`（全局唯一）并存；投影 `ProjectApp` 用 `app.domain` 作 casbin 域，PolicySync 用 `app_key` 做 HMAC 认证 + 流路由（`ResolveAppIDByKey`）。故 Sidecar 配置须**同时**有 `domain`（→`kernel.New` 域）与 `app_key`（→`syncclient.AppID`）。二者错配会使内核恒 `ErrForeignDomain` → deny-all（隐蔽一致性事故），故均必填、启动校验。
4. **`max_staleness` 默认 0（关闭，由运维配）**：`!Ready()` fail-close 是底线（恒开，与本项无关）；`max_staleness` 是「已就绪但久未同步」时的额外上限（`now-LastSyncAt>阈值`→拒，返 `Unavailable`）。④-4 spec 把具体阈值「留给部署配置」、内核默认 0=关闭该守卫——cmd 沿用该默认；`config.example.yaml` 给非零示例值 + 注释引导运维显式开启。
5. **本地端点 = loopback TCP**（v1）：`net.Listen("tcp", cfg.AuthAddr)`（如 `127.0.0.1:8090`），镜像控制面监听方式。unix socket / mTLS / 业务→Sidecar 回环认证明确 YAGNI 留后续（如同控制面 cmd 把 TLS 划出范围）。
6. **fail-close 启动**：HMAC `secret` 缺失、`control_plane_addr`/`app_key`/`domain`/`auth_addr` 任一空 → 启动失败退出（非零码），绝不带半装配状态对外服务。

## 3. 组件分解

| 文件 | 职责 |
|---|---|
| `cmd/sydom-sidecar/main.go` | 极薄入口：`func main() { os.Exit(app.Main()) }` |
| `internal/sidecar/app/config.go` | `Config` 结构体 + `LoadConfig(path string, getenv func(string) string) (Config, error)`（YAML 解析 + env 覆盖密钥 + 校验） |
| `internal/sidecar/app/run.go` | `Run(ctx, cfg, authLis, logger) error`（装配 + 服务 + 对账协程 + 优雅关闭）；`Main() int`（解析 `-config`、装信号 ctx、建监听器、调 Run、返回退出码） |
| `internal/sidecar/app/config_test.go` | `LoadConfig` 纯单测 |
| `internal/sidecar/app/run_test.go` | `Run` 集成测试（真实 TCP fake PolicySync，无 Docker） |
| `cmd/sydom-sidecar/config.example.yaml` | 运维参考配置示例 |

`app` 包在 `internal/sidecar/` 下（与控制面 `internal/controlplane/app` 路径不同、无冲突），可被 `cmd/sydom-sidecar` 与测试导入。

## 4. 配置（config.go）

```go
type Config struct {
    ControlPlaneAddr string        // 控制面 PolicySync 地址
    AppKey           string        // app_key：HMAC 认证标识 + 流路由（→ syncclient.AppID）
    Domain           string        // casbin 域（= application.domain，→ kernel.New 域）
    AuthAddr         string        // 本地 AuthService 监听地址（如 "127.0.0.1:8090"）
    MaxStaleness     time.Duration // 陈旧守卫上限（零值=关闭，默认）
    BackoffInitial   time.Duration // syncclient 退避初值（零值用 500ms）
    BackoffMax       time.Duration // syncclient 退避上限（零值用 30s）

    Secret []byte // 仅 env：SYDOM_APP_SECRET（HMAC 密钥，原始字节）
}
```

YAML 文件（非敏感项）：
```yaml
control_plane_addr: "localhost:8082"
app_key: "app-prod-01"
domain: "shop"
auth_addr: "127.0.0.1:8090"
max_staleness: "0s"        # 0=关闭陈旧守卫（!Ready 仍 fail-close）；生产建议显式设非零，如 "90s"
backoff_initial: "500ms"
backoff_max: "30s"
```

`LoadConfig(path, getenv)`：读文件 → `yaml.Unmarshal` 到中间结构 → env 覆盖：`SYDOM_CONTROL_PLANE_ADDR`（可选覆盖非敏感项）+ `SYDOM_APP_SECRET`（原始字节进 `Secret`）→ 解析三个 duration（`max_staleness` 缺省 0、`backoff_initial` 缺省 500ms、`backoff_max` 缺省 30s）→ 校验：`Secret` 非空、`ControlPlaneAddr`/`AppKey`/`Domain`/`AuthAddr` 非空，任一不满足返 error。`getenv` 注入（`os.Getenv`/测试 fake）便于纯单测。

> `max_staleness: "0s"` 与缺省均解析为 0（关闭守卫）；`time.ParseDuration("0s")==0`，无歧义。

## 5. 装配数据流（run.go 的 Run 内部）

```
1. table  := dataperm.NewTable()                                  // 实现 kernel.DataPolicyApplier
2. engine, _ := kernel.New(cfg.Domain, nil, table)               // pin 域；table 接收数据策略；nil→内建 1024 LRU
3. filter := dataperm.NewFilter(engine, table)                   // engine 作 RoleResolver
4. sync, _ := syncclient.New(syncclient.Config{
       Endpoint: cfg.ControlPlaneAddr, AppID: cfg.AppKey, Secret: cfg.Secret,
       Secure: false, BackoffInitial: cfg.BackoffInitial, BackoffMax: cfg.BackoffMax,
   }, engine)                                                     // 拨号（不启动对账）
5. authzr := authz.New(engine, filter, sync, authz.Config{MaxStaleness: cfg.MaxStaleness})
                                                                  // pin 域取自 engine.Domain()，单一真相源
6. authSrv := authz.NewGRPCServer(authzr)                         // 注册 AuthService
7. 后台协程（runCtx 派生；cascade cancel + WaitGroup）：
     - sync.Run(runCtx)         // bootstrap→订阅消费→断连退避重连；ctx 取消干净返回 nil
     - authSrv.Serve(authLis)   // 本地 AuthService；GracefulStop 后返回 nil
8. <-runCtx.Done() → authSrv.GracefulStop() → wg.Wait() → sync.Close()
```

**就绪前行为**（关键，符合 ④-4 语义）：AuthService 启动即监听，但 bootstrap 完成前 `engine.Ready()==false`，所有 `Check`/`FilterSQL` 经陈旧守卫返 `ErrNotReady`→gRPC `Unavailable`（fail-close）；bootstrap 成功后转正常判定。Sidecar 不阻塞等待就绪才监听——让业务侧能立即连上、并据 `Unavailable` 自定 fail-open/close。

`sync`/`authSrv` 由 Run 拥有并在关闭时收敛（`sync.Close()` 关底层 gRPC 连接）。Sidecar 无 DB/Redis 句柄，无额外 `Close`。

## 6. 优雅关闭

镜像控制面：`Main` 用 `signal.NotifyContext(context.Background(), SIGINT, SIGTERM)` 建可取消 ctx 传给 `Run`。`Run` 内派生 `runCtx`（任一协程结束即 `cancel()`，实现「一个挂→全收敛」级联）。协程用 `sync.WaitGroup` 收敛（不引入 errgroup，零新依赖）。ctx（或 runCtx）取消后：
- `authSrv.GracefulStop()`（停收新请求、放完在途调用；`grpc.Server.Serve` 在 `GracefulStop` 后返回 `nil`）。
- `sync.Run` 因 `runCtx` 取消返回 `nil`（已在 ④-3 实现：ctx 取消是干净退出，非错误）。
- `wg.Wait()` 收敛全部协程后 `sync.Close()`。
- 返回首个非 `context.Canceled` 的协程错误（有则返，无则 nil）。

> 与控制面差异：`syncclient.Run` ctx 取消返回 `nil`（非 `context.Canceled`），故 `launch` 的 `errors.Is(err, context.Canceled)` 过滤对它恒不命中，但保留过滤保持与控制面 `launch` 同构、且防御 `authSrv.Serve` 等未来可能返回 `context.Canceled` 的协程。

## 7. 日志（结构化，stdlib slog）

`Run` 接收 `*slog.Logger`，`Main` 构造（`slog.NewTextHandler(os.Stderr, nil)`）。记录：启动（`control_plane_addr`/`auth_addr`/`domain`/`app_key`）、致命错（配置无效、syncclient 拨号失败）、协程异常退出、优雅关闭进度。`secret` 绝不入日志。

## 8. 测试策略

- **LoadConfig 纯单测**（`config_test.go`，注入 fake `getenv` + 临时 YAML 文件）：完整解析 + env secret 覆盖；`secret` 缺失 → 错；`control_plane_addr`/`app_key`/`domain`/`auth_addr` 任一缺失 → 错；退避零值落默认（500ms/30s）；`max_staleness` 缺省 = 0；`max_staleness: "90s"` 正确解析。
- **Run 集成测试**（`run_test.go`，**真实 TCP** fake PolicySync，无 Docker）：
  - 起一个真实 TCP（`net.Listen("tcp","127.0.0.1:0")`）的 fake `PolicySyncServer`（镜像 syncclient `client_test.go` 的 fakeServer：`PullSnapshot` 返回带 `g`(alice→manager) + `p`(manager 可 read order, allow) 规则 + allow/deny 两条数据策略的快照，域 `dom1`；`Subscribe` 发完保持长连）。fake 不装 HMAC 拦截器，故任意 secret 均通过——无需 DB/Redis。
  - cfg：`ControlPlaneAddr` = fake 的 `lis.Addr()`、`AppKey`="app-1"、`Domain`="dom1"、`Secret`=[]byte("secret")、`MaxStaleness`=0、退避设极小值（毫秒级）。
  - 注入 `authLis = net.Listen("tcp","127.0.0.1:0")`，后台 `go Run(ctx, cfg, authLis, logger)`。
  - 拨 `authLis.Addr()` 建 `authv1.NewAuthServiceClient`；`require.Eventually` 断言 bootstrap 后 `Check{Subject:"alice",Object:"order",Action:"read"}.Allowed==true`（经 manager 角色继承，贯通 syncclient→engine→authorizer→gRPC）。
  - `FilterSQL{Subject:"alice",Resource:"order",Attrs:{department:"HR"}}` 返回 `sql=="(dept = ? AND NOT (status IN (?, ?)))"`、`args==["HR","locked","void"]`（贯通 deny override：syncclient→table→filter→gRPC）。
  - 断言 `cancel()` 后 `Run` 在超时（如 5s）内干净返回 nil（优雅关闭：sync.Run 退出 + authSrv.GracefulStop + sync.Close）。
  - 全程 `-race`（对账写 vs 鉴权读、关闭协调并发）。
- 不重测 syncclient/authz/dataperm 业务逻辑（各层已有单测）；本测试只证「装配正确 + 端到端贯通」。

## 9. 不在范围

- unix socket 监听 / mTLS / 业务进程→Sidecar 本地回环的认证（v1 同机回环不加 HMAC）。
- 健康检查 / readiness 探针 / Prometheus metrics / 审计日志。
- 配置热重载、单进程复用多 app/多域（架构上一进程一 app/一域）。
- DB/Redis 直连（Sidecar 不需要——状态全来自对账）。

YAGNI，留后续。

## 10. 自检结果

- **占位符扫描**：无 TODO/待定；Config 字段、装配步骤、关闭序列、测试断言均具体。
- **内部一致性**：决策 1（薄 main + 注入监听器）贯穿 §3/§5/§8；决策 2（YAML+env 密钥）贯穿 §4；决策 3（domain≠app_key 双字段）贯穿 §1 依赖/§4 Config/§5 步骤 2&4；决策 4（max_staleness 默认 0）贯穿 §4 解析/§5 步骤 5/§8；决策 5（loopback TCP）贯穿 §5/§6/§8；决策 6（fail-close 启动）贯穿 §4 校验/§5。
- **范围检查**：单一关注点（Sidecar 装配），单计划可覆盖；unix socket/mTLS/可观测/多 app 复用明确划出。
- **模糊性检查**：secret 形态（原始字节，HMAC key）、domain/app_key 来源与去向、监听器注入点、就绪前 fail-close 行为、`syncclient.Run` ctx 取消返回 nil（≠ control plane 的 context.Canceled）、`max_staleness:"0s"`==缺省==0 均已明确。

相关：架构 `2026-05-30-sydom-architecture-design.md`（§2.2 异常默认拒绝 / §5 数据面 API / §7 同步机制）；④-1 内核 `2026-06-03-sydom-sidecar-kernel-design.md`（域 pin / Ready）；④-2 dataperm `2026-06-04-sydom-sidecar-data-policy-engine-design.md`（FilterSQL/deny override）；④-3 同步客户端 `2026-06-04-sydom-sidecar-sync-client-design.md`（对账/陈旧信号/ctx 取消语义）；④-4 鉴权 API `2026-06-05-sydom-sidecar-auth-api-design.md`（§8 移交 cmd 装配清单 / 陈旧守卫 / 错误映射）；控制面 cmd `2026-06-05-sydom-controlplane-cmd-design.md`（装配/优雅关闭范本）；[[feedback-consistency-over-simplicity]]。
