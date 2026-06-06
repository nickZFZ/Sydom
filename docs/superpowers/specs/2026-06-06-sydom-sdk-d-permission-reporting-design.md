# 司域 SDK ⑤-D：权限点上报 设计

> 日期：2026-06-06　状态：设计已批准，待实现
> 关联：⑤ SDK A+B（`83dd09a`）、C（`aa7be1d`）已并入 main。本文是 SDK 最后一片 **D——权限点埋点上报**（架构 §8）。

## 1. 背景与范围

架构 §8 设想业务系统经 SDK 自动上报权限点，免去手动配置：app 凭据认证、服务端按凭据强制 `app_id`、幂等 upsert、自动上报标 `source=auto` 且不覆盖人工 `source=manual`。

A+B 落地时确认：现有 `AdminService.UpsertPermission` 是 **operator 凭据 + 元-RBAC**，不是 app 能调的端点。D 因此要新建一条 **app 凭据**的上报链路。已回源核实的现状约束：

- `permission` 表（migration 000004）**已有** `source VARCHAR(8) NOT NULL DEFAULT 'manual'` 列与唯一键 `uq_permission_app_code (app_id, code)`——**D 无需新 migration**。
- `PolicySync` 服务（`sync.v1`）已是 **app 凭据 HMAC** 认证、`app_id` 由凭据强制（`auth.AppIDFromContext`→`store.ResolveAppIDByKey`）——正是 §8 模型。
- `policy.runVersionedWrite`（manager.go:76-82）：**投影无 diff 且无 dataChanges 时只 COMMIT 业务态、不 bump、不广播**。权限点只在被授权（role_permission）后才进 casbin 投影，故纯目录上报天然不惊动 Sidecar。
- `cp.OperatorFromContext` 未设时默认 `"system"`——app 上报无 operator，可设 `"auto-report"`。

**范围**：SDK→Sidecar→控制面 的整条上报链路。**跨 ②proto / ③控制面 / ④Sidecar / ⑤SDK 四层**（D 是跨切面切片，必然改动多个已交付包，§8 改动面详列）。

**不在范围**：net/http 路由自动发现（ServeMux 不暴露已注册路由，无自省→仅显式注册）；Gin/Echo；删除已下线权限点（只 upsert 不删，留后续）；上报重试/后台周期上报（启动一次性，失败交业务）；注解扫描（Java SDK 后续）。

## 2. 设计决策

- **D1 传输 = SDK→Sidecar→控制面 中继**：SDK 调本地 Sidecar（loopback 无 HMAC），Sidecar 用其已认证的 `PolicySync` 连接转发到控制面。→ **AppSecret 只在 Sidecar、SDK 零加密零 internal 依赖**（保持极薄胶水），复用 Sidecar 既有 CP 连接。
- **D2 采集 = 仅显式注册 API**：`sydom.Registry.Register(Permission)` 收集，启动时一次性 `Report`。net/http 无路由自省，自动发现不做。
- **D3 本地入口 = 加到 AuthService**：`sydom.auth.v1 AuthService` 加 `ReportPermissions` RPC（SDK 已连此 loopback 服务，复用同一连接/客户端）。
- **D4 auto 绝不覆盖 manual**：app 上报写 `source='auto'`；`ON CONFLICT (app_id,code) DO UPDATE SET ... WHERE permission.source='auto'`——manual 行原样保留，按 RowsAffected 计数 upserted/skipped。
- **D5 版本经 runVersionedWrite**：批量上报走统一写事务模板，复用锁/重投影/diff/仅 diff 才 bump 的全部一致性机制。纯目录上报无 diff→不 bump；命中"已授权权限点改了 resource/action"→bump+广播（改了即应下发，一致性要求）。audit actor = `"auto-report"`。
- **D6 上报 fail-soft（与鉴权 fail-close 区分）**：权限点上报是**目录元数据、非鉴权决策**。上报失败只返回 error 交业务记日志/忽略，**绝不阻塞业务启动、不影响既有策略 enforce**。fail-close 铁律只管鉴权决策，不管元数据上报。
- **D7 租户隔离**：`app_id` 全程由凭据强制（SDK 与 Sidecar 都不在请求体传 app_id），业务无法越权写他 app 的权限点。

## 3. 数据流

```
业务 app：reg.Register(Permission{...}) × N  收集
      │ 启动一次性
      ▼
sydom.Registry.Report(ctx, client)
      ▼
sydom.Client.ReportPermissions(ctx, perms)          调本地 Sidecar（loopback 无 HMAC）
      │  auth.v1 AuthService.ReportPermissions（无 app_id，Sidecar pin 域）
      ▼
Sidecar authz.Server.ReportPermissions              本地入口，译 authv1→relay 点
      │  委托 syncCli.ReportPermissions（复用已认证 PolicySync 连接 + HMAC）
      ▼
控制面 PolicySync.ReportPermissions                 sync.v1 新增；app_key 由凭据强制
      │  ResolveAppIDByKey → PolicyManager.ReportPermissions(批量)
      ▼
runVersionedWrite（批量 mutate：逐条 conflict-aware auto upsert，计数）
      │  catalog-only→无 diff→COMMIT 不 bump；granted-change→bump+广播
      ▼
permission 表：source='auto' 幂等 upsert，manual 行不动
      ▲ 返回 {upserted, skipped} 计数沿原路回传
```

## 4. 组件分解

### 4.1 proto（regen）
- `api/proto/sydom/sync/v1/policy_sync.proto`：`PolicySync` 加
  ```
  rpc ReportPermissions(ReportPermissionsRequest) returns (ReportPermissionsResponse);
  message ReportPermissionsRequest { repeated PermissionPoint permissions = 1; } // app_id 凭据强制
  message PermissionPoint { string code=1; string resource=2; string action=3; string type=4; string name=5; string description=6; }
  message ReportPermissionsResponse { uint32 upserted = 1; uint32 skipped = 2; }
  ```
- `api/proto/sydom/auth/v1/auth.proto`：`AuthService` 加同形 `ReportPermissions`（本地，无 app_id；message 同名同形，不同 package 不冲突）。

### 4.2 控制面
- `store.UpsertAutoPermission(ctx, ex, appID int64, code, resource, action, permType, name, description string) (applied bool, err error)`：
  ```sql
  INSERT INTO permission (app_id, code, resource, action, type, name, description, source)
  VALUES ($1..$7, 'auto')
  ON CONFLICT (app_id, code) DO UPDATE SET
    resource=EXCLUDED.resource, action=EXCLUDED.action, type=EXCLUDED.type,
    name=EXCLUDED.name, description=EXCLUDED.description, updated_at=now()
  WHERE permission.source='auto'
  RETURNING id
  ```
  新增或命中 auto 行 → 返回 id（applied=true）；命中 manual 行 → DO UPDATE 的 WHERE 为假、零行返回 `sql.ErrNoRows`（applied=false，**非错误**）；其它 err 透传。
- `cp.PermissionPoint{Code,Resource,Action,Type,Name,Description string}`（types.go）+ `cp.ReportResult{Upserted, Skipped int}`。
- `policy.PolicyManager.ReportPermissions(ctx, appID int64, points []cp.PermissionPoint) (cp.ReportResult, error)`：`ctx=cp.WithOperator(ctx,"auto-report")` 后单批走 `runVersionedWrite`，mutate 闭包逐条 `UpsertAutoPermission` 累计 upserted/skipped、返回 nil dataChanges；模板负责重投影/diff/仅 diff 才 bump。
- `policysync.Server.ReportPermissions(ctx, req)`：`auth.AppIDFromContext`→`ResolveAppIDByKey`→校验 points（code/resource/action 非空，否则 InvalidArgument）→委托注入的窄接口 `reporter`→返回计数。`Server` 加 `reporter` 字段（窄接口 `permissionReporter{ ReportPermissions(ctx, appID, points) (ReportResult, error) }`，`*policy.PolicyManager` 满足）。
- `internal/controlplane/app/run.go`：把已构造的 `PolicyManager` 注入 `policysync.NewServer`。

### 4.3 Sidecar
- `syncclient.SyncClient.ReportPermissions(ctx, points []PermissionPoint) (ReportResult, error)`：经已认证连接调 CP `PolicySync.ReportPermissions`（HMAC 凭据已在连接上），译域类型→`syncv1.PermissionPoint`，回传计数。新增 `syncclient.PermissionPoint`/`ReportResult` 域类型。
- `authz.Server.ReportPermissions(ctx, req *authv1.ReportPermissionsRequest)`：本地处理器，译 `authv1`→relay 点，委托注入的窄接口 `relay`（`PermissionRelay{ ReportPermissions(ctx, points) (ReportResult, error) }`，`*syncclient.SyncClient` 满足），回 `authv1.ReportPermissionsResponse`。**不过陈旧守卫**（上报非决策）。`NewServer`/`NewGRPCServer` 签名加 `relay`。
- `internal/sidecar/app/run.go`：把 `syncCli` 注入 authz server。

### 4.4 SDK（`sdk/go/sydom`）
- `Permission{Code, Resource, Action, Type, Name, Description string}` + `ReportResult{Upserted, Skipped int}`。
- `Client.ReportPermissions(ctx, perms []Permission) (ReportResult, error)`：构 `authv1.ReportPermissionsRequest`→调本地 `AuthService.ReportPermissions`→回计数；错误经 `mapErr`（`codes.Unavailable`→`ErrUnavailable`，与既有方法一致）。
- `PermissionReporter` 窄接口（`ReportPermissions(ctx, []Permission)(ReportResult,error)`，`*Client` 满足）+ `Registry`（`Register(Permission)` 线程安全收集 + `Report(ctx, PermissionReporter)` 启动时一次性 flush）。

## 5. 错误处理

| 情形 | 行为 |
|---|---|
| 上报点 code/resource/action 为空 | 控制面返 `InvalidArgument`（fail-soft：错误回传业务，不写脏数据） |
| 命中 manual 行 | 保留 manual、计入 skipped（非错误） |
| CP/Sidecar 不可达、传输错误 | 返回 error（`ErrUnavailable` 哨兵），业务记日志/忽略——**不阻塞启动、不影响 enforce** |
| 上报改了已授权权限点的 resource/action | `runVersionedWrite` bump+广播（一致性，下发新 p 行） |
| app_id | 全程凭据强制，请求体不含，无法越权 |

## 6. 测试策略

- **store**（testcontainers PG）：`UpsertAutoPermission` 新增=applied、命中 auto=刷新 applied、命中 manual=skipped 且 manual 字段不变；计数正确。
- **policy**（testcontainers PG）：`ReportPermissions` 批量——纯目录无 bump（version 不变）；命中已授权权限点改 resource/action→bump+delta；auto/manual 混批计数。
- **policysync + authz + syncclient 中继**（bufconn 真链路）：SDK 点→authz→syncclient→CP，端到端贯通一次，断言落库 source='auto' + 计数回传 + app_id 凭据强制（伪造体内 app_id 无效）。
- **SDK**（bufconn fake AuthService）：`Client.ReportPermissions` 计数/错误透传；`Registry` Register 收集 + Report 一次性 flush + 线程安全（-race）。

## 7. 对既有代码的改动面（D 是跨切面，必然改已交付包）

| 包 | 改动 |
|---|---|
| `api/proto/.../sync/v1`、`auth/v1`（+gen） | 各加 ReportPermissions RPC + 3 message，regen |
| `internal/controlplane/store` | +UpsertAutoPermission |
| `internal/controlplane`（types） | +PermissionPoint、ReportResult |
| `internal/controlplane/policy` | +PolicyManager.ReportPermissions |
| `internal/controlplane/policysync` | +Server.ReportPermissions + reporter 依赖；NewServer 签名 |
| `internal/controlplane/app` | run.go 注入 reporter |
| `internal/sidecar/syncclient` | +SyncClient.ReportPermissions + 域类型 |
| `internal/sidecar/authz` | +Server.ReportPermissions + relay 依赖；NewServer/NewGRPCServer 签名 |
| `internal/sidecar/app` | run.go 注入 relay |
| `sdk/go/sydom` | +permission.go（Permission/ReportResult/Client.ReportPermissions/Registry/PermissionReporter） |

既有 RPC（Subscribe/PullSnapshot/Check/BatchCheck/FilterSQL）与既有写路径（runVersionedWrite 等）**不改语义**，仅追加。

## 8. YAGNI / 范围外

- 路由自动发现 / Gin / Echo / 注解扫描。
- 权限点删除（下线检测）——只 upsert。
- 上报重试 / 周期上报——启动一次性。
- 上报点 type 取值不在协议层枚举约束（与既有 permission.type 自由串一致）。

## 9. 自检

- **占位符扫描**：无 TODO/待定；message、函数签名、SQL、计数语义均写实。
- **内部一致性**：D1 中继 / D3 AuthService 入口 / §3 数据流 / §4 组件 三处一致；D4 conflict 语义与 §4.2 SQL、§5 错误表一致；D6 fail-soft 与 §5、§4.3「不过陈旧守卫」一致。
- **范围检查**：单一计划可覆盖（一条上报链路），但跨 4 层、改动面较大（§7），plan 需拆 8-10 个有序 TDD 任务（proto→store→policy→policysync→控制面 wiring→syncclient→authz→sidecar wiring→SDK→E2E）。
- **模糊性检查**：「不惊动 Sidecar」明确为「投影无 diff 才不 bump，granted-change 仍 bump」；「fail-soft」明确为「上报失败返 error 交业务，不阻塞、不碰 enforce」；app_id 凭据强制（不在请求体）三处重申。
