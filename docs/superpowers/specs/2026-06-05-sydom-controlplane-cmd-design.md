# 司域 · 控制面进程装配 (cmd/sydom-controlplane) 详细设计

> 版本：v0.1 | 日期：2026-06-05 | 状态：草稿

## 1. 范围与定位

控制面可执行二进制——把已实现并入 main 的控制面库层装配成一个长驻进程：加载配置、连 DB/Redis、幂等播种 root operator、起 **AdminService** 与 **PolicySync** 两个 gRPC 服务、跑 **relay**（outbox→Redis）与 **dispatch**（Redis→Hub→Sidecar 流）两个后台协程、响应信号优雅关闭。

这是 cmd 装配拆出的第一个子项目；`cmd/sydom-sidecar`（数据面二进制）是独立子项目，后做。本子项目**首次引入** `cmd/` 目录、配置加载、信号处理、结构化日志——此前各层刻意「仅库/组件层，不出 cmd」。

**上游依赖（均已实现并入 main）**：
- `internal/controlplane/secret`：`NewResolver(db, masterKey) (*Resolver, error)`（实现 `auth.SecretResolver`）。
- `internal/controlplane/policy`：`NewPolicyManager(db, sink DeltaSink) *PolicyManager`。
- `internal/controlplane/outbox`：`NewSink() *Sink`（实现 `policy.DeltaSink`）；`RunRelayLoop(ctx, db, pub broadcast.Publisher, poll time.Duration) error`。
- `internal/controlplane/adminauthz`：`NewEnforcer(db) (*Enforcer, error)`；`NewOperatorResolver(db, masterKey) (*OperatorResolver, error)`（实现 `auth.SecretResolver`，解 operator 凭据）；`EnsureRootOperator(ctx, db, masterKey, principal string, secret []byte) error`。
- `internal/controlplane/mgmt`：`NewAdminServer(db, mgr *policy.PolicyManager, masterKey []byte) *AdminServer`；`NewGRPCServer(srv *AdminServer, resolver auth.SecretResolver, enf *adminauthz.Enforcer, db *sql.DB) *grpc.Server`。
- `internal/controlplane/policysync`：`NewServer(db, Config{HeartbeatInterval, BufSize}) *Server`；`NewGRPCServer(srv *Server, res auth.SecretResolver) *grpc.Server`；`(*Server).RunDispatchLoop(ctx, sub broadcast.Subscriber) error`。
- `internal/controlplane/broadcast`：`NewRedisPublisher(*redis.Client) *RedisPublisher`；`NewRedisSubscriber(*redis.Client) *RedisSubscriber`。
- `internal/dbtest`（仅测试）：`StartPostgres`/`StartRedis`/`SetupSchema`/`SeedApp`。
- 驱动：`lib/pq`（`sql.Open("postgres", dsn)`）；`redis/go-redis/v9`；`gopkg.in/yaml.v3`（go.mod 已含前两者 + yaml.v3 indirect，零新模块，仅把 yaml.v3 提为直接依赖）。

## 2. 设计决策（头脑风暴逐条确认）

1. **先做控制面 cmd**：它是依赖根（Sidecar 要连它）；做完它服务端能跑，Sidecar cmd 才有真实端点连成端到端。
2. **配置 = YAML 文件 + env 覆盖密钥**：非敏感项（DSN、地址、间隔）走 YAML 文件；敏感项（主密钥、root 凭据）**只走 env、绝不落文件**。
3. **两个独立端口/监听器**：AdminService 与 PolicySync 各带不同拦截器（admin=operator 认证+元-RBAC；sync=app HMAC），各起一个 `grpc.Server`，直接复用现有两个 `NewGRPCServer`，零重构。
4. **薄 main + 可测 app 包**：逻辑全在 `internal/controlplane/app`；`Run` 接受**注入的监听器**，使集成测试用 `127.0.0.1:0` 拨真实端口、`main` 用配置地址——全装配可测。
5. **DB 假定预迁移**：cmd 不自动迁移（职责单一），运维先 `make migrate-up`。
6. **fail-close 启动**：主密钥非 32 字节/缺失、关键地址缺失、DB/Redis 连接失败、root 播种失败——任一即启动失败退出（非零码），绝不带半装配状态对外服务。

## 3. 组件分解

| 文件 | 职责 |
|---|---|
| `cmd/sydom-controlplane/main.go` | 极薄入口：`func main() { os.Exit(app.Main()) }` |
| `internal/controlplane/app/config.go` | `Config` 结构体 + `LoadConfig(path string, getenv func(string) string) (Config, error)`（YAML 解析 + env 覆盖密钥 + 校验） |
| `internal/controlplane/app/run.go` | `Run(ctx context.Context, cfg Config, adminLis, syncLis net.Listener, logger *slog.Logger) error`（装配 + 服务 + 后台协程 + 优雅关闭）；`Main() int`（解析 `-config`、装信号 ctx、建监听器、调 Run、记日志、返回退出码） |
| `internal/controlplane/app/config_test.go` | `LoadConfig` 纯单测 |
| `internal/controlplane/app/run_test.go` | `Run` 集成测试（testcontainers） |

`app` 包在 `internal/` 下，可被 `cmd/sydom-controlplane` 与测试导入。

## 4. 配置（config.go）

```go
type Config struct {
	DatabaseDSN       string        // postgres DSN
	RedisAddr         string        // Redis 地址
	AdminAddr         string        // AdminService 监听地址（如 ":8081"）
	SyncAddr          string        // PolicySync 监听地址（如 ":8082"）
	RootPrincipal     string        // root operator 标识
	HeartbeatInterval time.Duration // PolicySync 心跳间隔（零值用 30s）
	RelayPollInterval time.Duration // outbox relay 轮询间隔（零值用 1s）

	MasterKey  []byte // 仅 env：SYDOM_MASTER_KEY（base64 解码后须 32 字节）
	RootSecret []byte // 仅 env：SYDOM_ROOT_SECRET（root operator 初始 HMAC 密钥，原始字节）
}
```

YAML 文件（非敏感项）：
```yaml
database_dsn: "postgres://sydom:sydom@localhost:5432/sydom?sslmode=disable"
redis_addr: "localhost:6379"
admin_addr: ":8081"
sync_addr: ":8082"
root_principal: "root@sydom"
heartbeat_interval: "30s"
relay_poll_interval: "1s"
```

`LoadConfig(path, getenv)`：读文件 → `yaml.Unmarshal` 到中间结构（含 `database_dsn` 等可被 env 覆盖）→ env 覆盖：`SYDOM_DATABASE_DSN`/`SYDOM_REDIS_ADDR`（可选覆盖非敏感项）+ `SYDOM_MASTER_KEY`（base64 解码进 `MasterKey`）+ `SYDOM_ROOT_SECRET`（原始字节进 `RootSecret`）→ 校验：`len(MasterKey)==crypto.KeySize(32)`、`RootSecret` 非空、`DatabaseDSN`/`RedisAddr`/`AdminAddr`/`SyncAddr`/`RootPrincipal` 非空，任一不满足返 error。`getenv` 注入（`os.Getenv`/测试 fake）便于纯单测。

## 5. 装配数据流（run.go 的 Run 内部）

```
1.  db, _ = sql.Open("postgres", cfg.DatabaseDSN); db.PingContext(ctx)        // 失败→返错（fail-close）
2.  rdb = redis.NewClient(&redis.Options{Addr: cfg.RedisAddr}); rdb.Ping(ctx) // 失败→返错
3.  appResolver, _ = secret.NewResolver(db, cfg.MasterKey)                     // PolicySync 用：解 app HMAC 凭据
4.  EnsureRootOperator(ctx, db, cfg.MasterKey, cfg.RootPrincipal, cfg.RootSecret) // 幂等播种 + bump version
                                                                              // 必须在 NewEnforcer 之前（否则 enforcer 加载不到 root 的 super-admin 绑定→root 调 RPC 被拒）
5.  operatorResolver, _ = adminauthz.NewOperatorResolver(db, cfg.MasterKey)    // AdminService 用：解 operator HMAC 凭据
6.  enforcer, _ = adminauthz.NewEnforcer(db)                                   // 构造期加载策略（含上一步 root 绑定）
7.  mgr = policy.NewPolicyManager(db, outbox.NewSink())                        // 写事务内落 outbox
8.  adminSrv = mgmt.NewGRPCServer(mgmt.NewAdminServer(db, mgr, cfg.MasterKey), operatorResolver, enforcer, db)
9.  syncCore = policysync.NewServer(db, policysync.Config{HeartbeatInterval: cfg.HeartbeatInterval})
    syncSrv  = policysync.NewGRPCServer(syncCore, appResolver)
10. pub = broadcast.NewRedisPublisher(rdb); sub = broadcast.NewRedisSubscriber(rdb)
11. 后台协程（ctx 派生）：
      - adminSrv.Serve(adminLis)
      - syncSrv.Serve(syncLis)
      - outbox.RunRelayLoop(runCtx, db, pub, cfg.RelayPollInterval)   // outbox → Redis
      - syncCore.RunDispatchLoop(runCtx, sub)                          // Redis → Hub → Sidecar 流
12. 阻塞等 ctx.Done()，随后优雅关闭（§6）
```

**两个不同的 resolver**（关键，回源核实修正）：AdminService 走 `adminauthz.NewOperatorResolver`（解 **operator** 凭据），PolicySync 走 `secret.NewResolver`（解 **app** 凭据）——二者都实现 `auth.SecretResolver` 但解不同主体，绝不可复用同一个。`db`/`rdb` 由 Run 拥有并在关闭时 Close。

## 6. 优雅关闭

`Main` 用 `signal.NotifyContext(context.Background(), SIGINT, SIGTERM)` 建可取消 ctx 传给 `Run`。`Run` 内派生 `runCtx`（任一协程结束即 `cancel()`，实现「一个挂→全收敛」级联）。协程用 `sync.WaitGroup` 收敛（不引入 errgroup，零新依赖）。ctx（或 runCtx）取消后：
- `adminSrv.GracefulStop()` + `syncSrv.GracefulStop()`（停收新请求、放完在途流；在 `<-runCtx.Done()` 之后主流程调用）。`grpc.Server.Serve` 在 `GracefulStop` 后返回 `nil`。
- relay/dispatch 因 `runCtx` 取消返回 `context.Canceled`——**优雅关闭的预期值，需过滤**（`errors.Is(err, context.Canceled)` 不计入致命错）。
- `wg.Wait()` 收敛全部协程后 `db.Close()` + `rdb.Close()`。
- 返回首个非 `context.Canceled` 的协程错误（有则返，无则 nil）。

## 7. 日志（结构化，stdlib slog）

引入 `log/slog`（无新依赖）。`Run` 接收 `*slog.Logger`，`Main` 构造（`slog.NewTextHandler(os.Stderr, ...)`）。记录：启动（两监听地址、root 播种结果）、致命错（DB/Redis 连接失败、配置无效）、协程异常退出、优雅关闭进度。这是此前各层留给「接入层」的观测起点；业务库层不回填日志。

## 8. 测试策略

- **LoadConfig 纯单测**（`config_test.go`，注入 fake `getenv` + 临时 YAML 文件）：完整解析 + env 覆盖密钥；主密钥非 32 字节 / 缺失 → 错；`RootSecret` 缺失 → 错；关键地址缺失 → 错；间隔零值落默认。
- **Run 集成测试**（`run_test.go`，testcontainers PG+Redis）：需给 `dbtest` 加 `MigratedDSN(t) string`（起 PG 容器 + 跑迁移 + 返回 DSN，复用其内部 `db.RunMigrations`/`migrationsSource`）——现有 `SetupSchema` 只返回 `*sql.DB` 不暴露 DSN，而 Run 要按 DSN 自开连接池。
  - 用 `net.Listen("tcp", "127.0.0.1:0")` 建两监听器，后台 `go Run(ctx, cfg, adminLis, syncLis, logger)`；cfg.DatabaseDSN = `MigratedDSN(t)`、cfg.RedisAddr = `StartRedis(t)`，MasterKey 32 字节、RootPrincipal/RootSecret 设定。
  - 拨 `adminLis.Addr()`，以播种的 root operator 凭据（`auth.NewPerRPCCredentials(RootPrincipal, RootSecret, false)`）调一个 AdminService RPC（如 `ListApplications`），`require.NoError` —— **验证装配链贯通**：配置→DB/Redis→secret/enforcer/EnsureRootOperator→认证→元-RBAC→服务。
  - 断言 `cancel()` 后 `Run` 在超时（如 5s）内干净返回 nil（优雅关闭）。
  - 不重测 mgmt/policysync 业务逻辑（各层已有 bufconn+testcontainer 测试）；本测试只证「装配正确」。

## 9. 不在范围

- `cmd/sydom-sidecar`（数据面二进制，独立子项目）。
- DB 自动迁移（假定 `make migrate-up`）。
- 健康检查/readiness 端点、Prometheus metrics、TLS/mTLS、Redis Sentinel/Cluster、配置热重载、多副本协调（控制面本就无状态多副本，靠 DB+Redis，无需 cmd 额外协调）。YAGNI，留后续。

## 10. 自检结果

- **占位符扫描**：无 TODO/待定；Config 字段、装配步骤、关闭序列、测试断言均具体。
- **内部一致性**：决策 2（文件+env 密钥）贯穿 §4；决策 3（两端口）贯穿 §5 步骤 8-11；决策 4（注入监听器）贯穿 §3/§5/§8；决策 6（fail-close 启动）贯穿 §5 步骤 1-6 与 §8 LoadConfig 校验。**两个不同 resolver（operator vs app）+ EnsureRootOperator 先于 NewEnforcer**：回源核实修正，贯穿 §1 依赖/§5 步骤 3-9。
- **范围检查**：单一关注点（控制面装配），单计划可覆盖；sidecar/迁移/可观测扩展明确划出。
- **模糊性检查**：密钥编码（主密钥 base64、root secret 原始字节）、监听器注入点、GracefulStop 触发时机（ctx.Done 后独立 goroutine）、relay/dispatch 退出（随 ctx）均已明确。

相关：架构 `2026-05-30-sydom-architecture-design.md`（§4 控制面高可用 / §7 同步机制）；③-1 `2026-06-01-sydom-control-plane-policy-core-design.md`；③-2 `2026-06-01-sydom-control-plane-sync-service-design.md`（Hub/Dispatch/relay 语义）；③-3 `2026-06-02-sydom-control-plane-mgmt-api-design.md`（AdminService/认证/元-RBAC/outbox）；[[feedback-consistency-over-simplicity]]。
