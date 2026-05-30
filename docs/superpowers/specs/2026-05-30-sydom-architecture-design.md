# 司域 (Sydom) 整体架构设计

> 厘定辖域，权归其位
>
> 版本：v0.1 | 日期：2026-05-30 | 状态：草稿

---

## 1. 产品定位

司域是面向企业的权限管理平台，为多套业务系统提供**功能权限**和**数据权限**的统一解决方案。

核心问题：已知某个账号，业务系统如何判断其是否能访问某个菜单、按钮、接口或数据范围，并让该判断**可生效、可追溯、可观测**。

### 价值主张

| 维度 | 描述 |
|------|------|
| 权限模型强大 | 支持 ACL / RBAC / ABAC 及混合模型，覆盖绝大多数复杂场景 |
| 强产品力 | 不懂技术的业务用户也可轻松完成权限配置 |
| 数据面轻量 | Sidecar 内存占用 < 50MB，鉴权延迟 < 1ms |

---

## 2. 架构约束

### 2.1 工程约束

- **casbin 是内核，不是依赖**：Sydom 以 casbin v3.10.0 为鉴权引擎内核。**绝不修改 casbin 源码**。casbin 已有的能力通过复用或适配接口使用；只有 casbin 能力边界之外的功能才由 Sydom 自行实现（详见第 3 节 casbin 能力边界）。
- **控制面与数据面分离**：控制面负责授权管理，数据面负责鉴权生效
- **业务代码解耦**：数据面通过 Sidecar + 极薄 SDK 实现，业务逻辑层对权限无感知
- **多系统统一管理**：一套控制面管理多套业务系统的权限

### 2.2 关键架构原则

以下为全局纲领性原则，所有子系统详细设计必须遵循。每条原则后标注其在本文档的落地处。

- **数据一致性优先（最高优先级）**：**权限不一致绝不可接受**。凡遇到"简化/性能 vs 一致性"的取舍，一律倒向一致性。三道防线：
  - **fail-close**：异常路径默认拒绝（无可用策略快照即拒），对齐 casbin `enforce()` 出错返回 `(false, err)` 的内核语义。→ [7 节容灾](#7-策略同步机制)
  - **单一真相源**：DB（带单调版本号）是策略唯一真相源；Redis 广播仅提速，丢消息由版本号对账兜底，绝不拿缓存/广播当真相源。→ [4.1](#41-控制面高可用i4)、[7 节 C2](#7-策略同步机制)
  - **缓存安全失效**：决策缓存每应用一条 delta 必须全量 `InvalidateCache()`，不依赖按 key 删（RBAC 下不可靠），不依赖 TTL 做一致性。→ [5 节缓存铁律](#5-数据面-sidecar-架构)
- **高可用**：无单点。控制面从 MVP 起即多副本无状态 + 负载均衡；Sidecar 与业务进程同生命周期、持本地策略快照，控制面/广播层故障时鉴权不中断（降级继续）。→ [4.1](#41-控制面高可用i4)
- **高性能**：鉴权在数据面本地完成，不走网络到控制面。性能靠 casbin 官方路线达成——按 domain 分片加载（`LoadFilteredPolicy`）压低 n + 决策缓存，而非自建索引。本地回环延迟 < 1ms (P99)。→ [5 节性能目标](#5-数据面-sidecar-架构)
- **最终一致的时序容忍**：多 Sidecar 策略更新存在 < 5s 时序差，这是可接受的**新鲜度**滞后，与"数据一致性"不矛盾——前者是"何时收到最新策略"，后者是"基于已有策略的判定绝不出错"。两者分别由版本号对账与缓存失效保证。

> **原则间的优先级**：数据一致性 > 高可用 > 高性能。冲突时按此序裁决。例如缓存全量失效会短暂牺牲性能（回落 O(n) 扫描），但因一致性更高，无条件执行。

---

## 3. Casbin v3.10.0 能力边界

> 本节是 Sydom 详细设计的前提。所有设计决策必须先问：casbin 是否已覆盖？覆盖了就复用，没覆盖才自己做。

### 3.1 casbin 已覆盖（直接复用）

| 能力 | casbin 实现 | Sydom 使用方式 |
|------|------------|--------------|
| 多模型支持 | ACL / RBAC / ABAC / RESTful / 优先级，通过 `.conf` 配置切换 | 直接加载对应 model conf |
| 多租户域隔离 | `DomainManager`：per-domain `RoleManagerImpl`，`g(user,role,domain)` 三元组 | 以 Application 为 domain，天然隔离多业务系统 |
| 角色继承图 | `RoleManagerImpl`（内存邻接表），`BuildRoleLinks()` 从 `g` 段策略重建 | 直接使用默认实现，无需重造 |
| 条件角色 | `ConditionalRoleManager`：带参数条件函数的角色匹配 | 复杂角色场景按需启用 |
| 决策执行 | `SyncedEnforcer`（RWMutex 线程安全） + `EnforceContext`（多截面） | Sidecar 内核：`SyncedEnforcer.Enforce(ctx, sub, dom, obj, act)` |
| 批量鉴权 | `BatchEnforce` / `BatchEnforceWithMatcher` | 对应 Sidecar `/batch-check` 接口 |
| 决策解释 | `EnforceEx` 返回命中规则列表 | 对应 `/check?explain=true` |
| 决策缓存 | `CachedEnforcer`（LRU + 过期时间） | 热点权限点缓存优化（按需叠加） |
| 事务批量写 | `TransactionalEnforcer` | 控制面批量策略变更时使用 |
| 策略存储接口 | `persist.Adapter` / `BatchAdapter` / `FilteredAdapter` | **Sydom 实现内存 Adapter**（Sidecar 侧），控制面实现 DB Adapter |
| 策略变更通知 | `persist.Watcher` / `WatcherEx` / `UpdatableWatcher` 接口 | **Sydom 实现 gRPC Watcher**（控制面 → Sidecar 推送） |
| 循环继承检测 | `detector.DefaultDetector.Check()`，DFS 检测环 | 控制面在保存角色关系前调用 |
| 前端权限导出 | `CasbinJsGetPermissionForUser` | 如需前端鉴权直接复用 |
| 效果合并 | `DefaultEffector.MergeEffects`：4 种内置 effect 表达式，支持短路 | 直接使用内置效果，无需自定义 |

### 3.2 casbin 未覆盖（Sydom 需要自行实现）

| 能力 | 说明 | 对应 Sydom 模块 |
|------|------|--------------|
| **数据权限（行级过滤）** | casbin 只做 true/false 鉴权，不做"返回条件表达式"这类语义 | Sidecar 数据权限引擎：条件树求值 + SQL/ORM 方言渲染 |
| **策略持久化存储** | 内置只有 `fileadapter`（CSV），无 DB adapter | 控制面：实现 `persist.BatchAdapter` 对接 PG/MySQL |
| **策略下发传输层** | casbin Watcher 接口只定义回调契约，不提供传输实现 | 控制面 → Sidecar：实现 gRPC stream Watcher |
| **内存 Adapter（Sidecar）** | Sidecar 策略从内存缓存读，需实现读内存的 Adapter | `MemoryAdapter`：实现 `persist.BatchAdapter`，从本地缓存加载 |
| **权限点注册机制** | casbin 无"权限点"概念，只有 policy 规则 | 控制面：权限点注册表 + SDK 埋点上报 API |
| **控制面管理 API** | casbin 是纯库，无 HTTP/gRPC 管理接口 | 控制面：REST API + gRPC service（CRUD 策略、角色、应用） |
| **Sidecar 进程** | casbin 是 Go 库，不是独立进程 | 将 casbin 封装为独立 Sidecar 进程，暴露 HTTP/gRPC 鉴权 API |
| **多语言 SDK** | casbin 官方 SDK 各语言独立维护，不含框架 middleware | Sydom SDK：极薄框架胶水层（Go 优先，后续 Java/Node） |
| **审计日志** | casbin logger 接口只记录内部日志，无业务审计语义 | Sidecar 鉴权日志 + 控制面策略变更审计 |
| **可观测性** | casbin 无 Prometheus metrics | Sidecar 暴露 metrics（QPS、延迟、缓存命中率） |
| **UI 管理界面** | casbin 无 UI | 控制面 Web Console |
| **AI 配置助手** | casbin 无 | 控制面内嵌 AI 助手 / MCP 工具 |

### 3.3 casbin 的关键限制（设计时需规避）

| 限制 | 影响 | 规避方案 |
|------|------|---------|
| `govaluate` 每次请求重新编译 matcher | 高并发下 CPU 开销大 | 优先使用 `CachedEnforcer`；Sidecar 启用决策缓存 |
| 角色图是纯内存结构 | 重启后需从策略重建，大规模时 `BuildRoleLinks()` 耗时 | Sidecar 启动时全量加载，增量更新用 `LoadIncrementalFilteredPolicy` |
| Watcher 默认触发全量 `LoadPolicy()` | 高频策略变更时抖动大 | 实现 `WatcherEx` + 增量 `UpdatableWatcher`，只推变更 delta |
| 循环继承不自动检测 | 角色环导致 matcher 死循环 | 控制面保存角色关系前调用 `detector.Check()` |
| 无内置分布式一致性 | 多 Sidecar 策略更新有时序差 | 接受最终一致（< 5s），Sidecar 使用本地缓存降级 |

### 3.4 领域模型边界：casbin 是计算内核，不是领域模型

> 关键判断：casbin **不是"领域模型有缺陷"，而是它刻意不做完整的鉴权领域模型**。它要支持 ACL/RBAC/ABAC/ReBAC 任意范式，所以只能提供一套**无 schema 的元模型**（`Model = map[string]AssertionMap`，policy 是裸 `[]string`），把领域建模的责任留给集成方。司域的领域模型 = 复用 casbin 的授权计算 + 在其上下游补齐主体、资源、权限、治理四层。

#### 工程定位

casbin = 嵌在 Sidecar 进程里的**纯函数鉴权计算内核**：输入（model + policy + 请求）→ 输出（allow/deny）。它是无状态、无 I/O、无认证、无网络的计算单元，刻意把存储、传输、认证、租户隔离全部下推给集成方。

#### 完整鉴权服务的领域模型分层（业界收敛：Zanzibar / AWS IAM / OPA / Oso）

```
1. 主体层 (Subject)
   Principal ─┬─ User（用户）
              ├─ ServiceAccount（服务账号 / 机器身份）
              └─ Group（用户组，身份聚合，区别于 Role）
   要点：Group（你属于谁）≠ Role（你能做什么）

2. 资源层 (Resource)
   ResourceType（order / document）
     └─ Resource 实例（order#123）
          ├─ attributes（owner / department / status）← 数据权限基础
          └─ hierarchy（folder → file）              ← ReBAC 基础

3. 权限层 (Permission)
   Action（read / write / approve）
   Permission = ResourceType + Action  ← 即"权限点"，具象为 menu/button/api
   PermissionSet（权限集）

4. 授权层 (Authorization) ── casbin 的强项
   RoleBinding: (Subject, Role, Scope) 三元组
   Role = PermissionSet 载体 + 可继承
   Scope = Tenant / Application(Domain) / Org / ResourceInstance
   DataPolicy = (Subject/Role, ResourceType, ConditionExpr, Scope)  ← casbin 无

横切治理层 (Governance) ── casbin 完全空白
   AuthN（认证：谁在调用）       ← AppID/Secret 在此层
   Administration（管理鉴权：谁能改策略，元权限）
   Audit（审计：谁何时改了/查了什么）
   Multi-tenancy（租户物理/逻辑隔离）
```

#### casbin 覆盖对照

| 领域层 | casbin 提供 | 司域要补 |
|--------|------------|---------|
| 主体层 | 扁平字符串 + `g` 继承（User/Role 糊在一起） | User / Group / ServiceAccount 清晰建模 |
| 资源层 | 单个 `obj` 字符串 | ResourceType + 属性 + 层级 |
| 权限层 | 单个 `act` 字符串 | 权限点注册表 + 埋点上报 |
| 授权层 | **工业级强项**：RoleBinding 三元组 + domain Scope + 角色继承图 | Scope 扩展到 Tenant/Org；DataPolicy 条件树 |
| 治理层 | **完全空白** | 认证、管理鉴权、审计、租户隔离 |

#### 两层隔离叠加（重要）

casbin 的 `domain`（`DomainManager`）只提供**鉴权计算时的命名空间隔离**——A 域的 admin 越不到 B 域，发生在 Enforce 那一瞬间。它**不提供**工程意义的租户隔离：谁能读写某域的策略、策略数据在存储层是否隔离、认证凭据——全部为零。

因此司域的租户隔离是**两层叠加**：

- **鉴权层隔离**（运行时）：直接用 casbin domain，Application = domain，零自研。
- **管理层隔离**（控制面）：AppID/Secret 认证 + 服务端按凭据归属强制 app_id，casbin 完全空白，司域自建。

#### 该判断对其他决策的解释力

- **C1**（数据权限自建条件树）← casbin 资源层太薄，`obj` 只是字符串
- **I2**（AppID/Secret + 租户隔离）← casbin 治理层完全空白
- **权限点上报**（第 8 节）← casbin 权限层没有"权限点"实体

---

## 4. 系统全局架构

```
┌────────────────────────────────────────────────────────────────┐
│                      控制面 (Control Plane)                      │
│                                                                │
│   ┌──────────────┐   ┌──────────────┐   ┌──────────────────┐  │
│   │   管理 UI     │   │   管理 API    │   │   AI 配置助手    │  │
│   │ (Web Console) │   │  (REST/gRPC)  │   │  (Chat 配置方式)  │  │
│   └──────┬───────┘   └──────┬───────┘   └────────┬─────────┘  │
│          └─────────────────┼──────────────────────┘           │
│                       ┌────▼────┐                             │
│                       │ 权限服务  │                             │
│                       │(Policy   │                             │
│                       │Manager)  │                             │
│                       └────┬────┘                             │
│                       ┌────▼────┐                             │
│                       │PG/MySQL │  ← 策略持久化存储             │
│                       └─────────┘                             │
└────────────────────────────┬───────────────────────────────────┘
                             │  策略下发 (gRPC stream)
          ┌──────────────────┼──────────────────┐
          ▼                  ▼                  ▼
┌──────────────┐   ┌──────────────┐   ┌──────────────┐
│  数据面 A     │   │  数据面 B     │   │  数据面 C     │
│  (Sidecar)   │   │  (Sidecar)   │   │  (Sidecar)   │
│              │   │              │   │              │
│  业务系统 A   │   │  业务系统 B   │   │  业务系统 C   │
└──────┬───────┘   └──────────────┘   └──────────────┘
       │ localhost (Unix socket / 127.0.0.1)
  ┌────▼────┐
  │   SDK   │  ← 极薄，只做框架 middleware 适配
  │Go/Java/ │
  │Node/... │
  └─────────┘
```

### 4.1 控制面高可用（I4）

司域架构目标是**高可用、高性能**，控制面从 MVP 起即为**多副本无状态 + 负载均衡**，不存在单实例形态。

- **无状态副本**：控制面所有实例对等，状态全部落在 PG/MySQL，任一副本可随时增删；前置 LB（或 K8s Service）做请求分发与健康检查。
- **Redis Pub/Sub 作广播总线（MVP 即引入）**：数据面是"多 Sidecar 对一控制面"，策略变更需扇出到所有订阅的 Sidecar。可预见规模上来后必然要换专用广播组件，与其先上 DB 内建机制再迁移，不如 MVP 直接用 Redis：
  - 策略变更落 DB（事务）后，向 `policy-change:{app_id}` 频道 `PUBLISH` 一条 delta（含变更规则 + 单调版本号）。
  - 各控制面副本 `SUBSCRIBE` 对应频道，收到后向自己持有的 Sidecar stream 推 delta。Sidecar 只订阅本 app 分片，控制面按 app 过滤，避免全量广播放大。
- **Redis 是 at-most-once，不能作唯一通路**：Redis Pub/Sub 是 fire-and-forget、不持久化——某 Sidecar 正在重连时的 `PUBLISH` 会被静默丢失。因此 **DB 是唯一真相源，Redis 只是快速通知通道**：
  - **DB（兜底/对账）**：每个 app 的策略变更携带单调递增版本号，落 DB。这是权威、可重放的序列。
  - **Redis（快路径）**：低延迟把 delta 扇出，命中即增量 apply。
  - **对账**：Sidecar 记录 last-applied 版本号；发现版本不连续（丢包）或 stream 重连时，回退到一次 `LoadFilteredPolicy`（只拉本 app 分片）向控制面对齐——即 [第 7 节](#7-策略同步机制) C2 已定义的版本号兜底。Redis 丢一条消息，对账兜底会补上，最终一致不破。
- **Redis 高可用**：Redis 自身用 Sentinel 或 Cluster 部署，避免成为新的单点。Redis 完全不可用时，降级为"控制面副本轮询 DB 变更表"继续推送（牺牲实时性保可用），鉴权侧因 Sidecar 持有本地快照不受影响。
- **后续可换 NATS / Kafka**：若需要消息持久化、回放、更高吞吐，把广播总线从 Redis 换成 NATS JetStream 或 Kafka，控制面与 Sidecar 的 stream 协议不变。

> **与 casbin 分布式机制的关系（已回源核实）：** casbin 提供两套机制，都面向"多个**对等 enforcer 实例**之间同步策略"：
> - `Watcher`/`WatcherEx`（`persist/watcher*.go`）：通知/广播 delta，接收端自行 reload 或 apply，**最终一致**。
> - `Dispatcher`（`persist/dispatcher.go` + `DistributedEnforcer`）：写入短路进 dispatcher，对等实例各自落地，**共识式强一致**（典型挂 Raft）。
>
> 司域拓扑与两者都不同：**控制面单写、Sidecar 只读订阅**——写入只发生在控制面（落 DB），Sidecar 不回写。因此不需要 Dispatcher 的对等共识/Raft，最终一致（< 5s）即可。控制面副本之间也无需互相同步策略状态（状态在 DB），只需共享一条广播通道把变更扩散给 Sidecar。司域复用的是 WatcherEx 的"广播 delta"语义，接收端 apply 逻辑自实现（casbin 对 WatcherEx 本就不提供默认接收实现）。

---

## 5. 数据面 (Sidecar) 架构

每个业务服务旁边部署一个 Sidecar 进程，承载鉴权引擎的全部核心功能。

```
┌──────────────────────────────────────────┐
│              Sidecar 进程                 │
│                                          │
│  ┌────────────┐   ┌──────────────────┐   │
│  │  内存 model │◄──│   策略同步协程    │   │
│  │ (本应用分片) │   │ (gRPC stream 订阅)│   │
│  └─────┬──────┘   └──────────────────┘   │
│        │  推 delta 直接改内存             │
│        │  AddPolicies/RemovePolicies      │
│        │  + BuildIncrementalRoleLinks     │
│        │  + InvalidateCache (全量清缓存)   │
│  ┌─────▼────────────────────────────┐    │
│  │   casbin SyncedEnforcer           │    │
│  │   + MemoryAdapter(仅启动加载快照) │    │
│  │   + CachedEnforcer(决策缓存)      │    │
│  └─────┬────────────────────────────┘    │
│        │                                 │
│  ┌─────▼────────────────────────────┐    │
│  │  数据权限引擎(条件树求值/渲染)     │    │
│  │  复用 GetImplicitRolesForUser     │    │  ← 主体角色展开
│  └─────┬────────────────────────────┘    │
│        │                                 │
│  ┌─────▼────────────────────────────┐    │
│  │       鉴权 API (HTTP/gRPC)        │    │
│  │  POST /check  → bool              │    │
│  │  POST /filter → 参数化条件 +args  │    │  ← 数据权限下推
│  │  POST /batch-check → []bool        │    │
│  └──────────────────────────────────┘    │
└──────────────────────────────────────────┘
          ↕ localhost
┌──────────────────────────┐
│    业务进程 + SDK          │
│  middleware 拦截每个请求   │
│  → 调 Sidecar /check      │
│  → 参数化 WHERE 注入查询   │
└──────────────────────────┘
```

**性能目标（前提假设）：**

> 以下指标在**中型基线规模**下成立：单应用 ~10 万条 policy 规则、~1 千角色、~1 万用户；且 Sidecar **按 domain 分片加载**（`LoadFilteredPolicy` 只加载本应用策略）+ 启用 `CachedEnforcer`。大型规模（~100 万规则）为已知边界，需压测验证。

- Sidecar 内存占用 < 50MB
- 本地回环鉴权延迟 < 1ms (P99)
- 策略变更同步延迟 < 5s

> 说明：casbin 的 Enforce 是 O(n) 线性扫描（`PolicyMap` 只用于增删改去重，不参与匹配），因此 < 1ms 依赖"分片把 n 压到单应用量级 + 决策缓存"两个前提。这是 casbin 官方性能路线（分片加载 + 缓存 + 增量角色图），而非自建索引——详见 [3.3](#33-casbin-的关键限制设计时需规避)。

> **决策缓存一致性铁律（已回源核实 `enforcer_cached.go`）：权限不一致绝不可接受，故缓存失效只能全量清。**
>
> casbin `CachedEnforcer` 只在 `LoadPolicy` / `InvalidateCache` / `ClearPolicy`（全量清）和 `RemovePolicy` / `RemovePolicies`（按 key 精确删）时动缓存。它**未重写** `AddPolicy` / `AddPolicies` / `UpdatePolicy` / `UpdatePolicies` / `RemoveFilteredPolicy`，`BuildIncrementalRoleLinks` 更完全不碰缓存。直接用它的默认失效逻辑会产生两类不一致：
> 1. **授权/更新不生效**：`AddPolicy`、`UpdatePolicy` 不清缓存，此前缓存的 deny/旧结果残留。
> 2. **RBAC 撤权不生效（结构性）**：缓存 key 由**请求**主体构成（如 `alice$$...`），而 `RemovePolicy` 只按**规则**主体删 key（如 `manager$$...`）。alice 经 `manager` 角色获得的 `true` 缓存对不上、不会被删——撤权后 alice 仍能访问。按 key 失效在角色间接性下**无法正确**。
>
> **因此司域的硬性规则：Sidecar 同步协程每应用一条 delta（`AddPolicies`/`RemovePolicies`/`UpdatePolicy` + `BuildIncrementalRoleLinks`）后，必须调用 `InvalidateCache()` 全量清空决策缓存。** 不依赖按 key 删（RBAC 下不可靠），不依赖 TTL 做一致性（TTL=N 秒 ≡ 允许 N 秒权限不一致，违反底线；TTL 仅作内存上界）。代价：策略变更后缓存短暂回冷、回落 O(n) 扫描，但策略变更频率远低于鉴权请求，很快重新焐热，是正确性优先的合理取舍。

---

## 6. 权限模型设计

### 6.1 核心概念层次

```
租户 (Tenant)
  └── 应用 (Application)       ← 对应一套业务系统，即 casbin domain
        ├── 权限点 (Permission)  ← 功能权限：menu / button / api
        │     格式: {app}:{resource}:{action}
        │     示例: "order-system:order:create"
        │
        ├── 角色 (Role)          ← 角色继承树（RBAC），casbin g 段
        │     示例: admin > manager > viewer
        │
        └── 数据策略 (DataPolicy) ← 数据权限（ABAC 条件树），独立存储
              运行时下推为: SQL WHERE / ORM Filter
```

> **DataPolicy 与角色图的关系（C1）：** 数据策略以独立条件树存储，不进 casbin 的 `p`/`g` 段。但它的 `subject` 复用 casbin 角色图——求值时先用 `GetImplicitRolesForUser` 把用户展开成隐式角色集，再匹配挂在这些角色上的数据策略。即"功能权限判定"与"数据权限主体解析"共享同一份角色继承关系，避免两套主体模型。

### 6.2 功能权限 — casbin 模型配置

```ini
[request_definition]
r = sub, dom, obj, act

[policy_definition]
p = sub, dom, obj, act, eft

[role_definition]
g = _, _, _    # user, role, domain (三元组支持多租户)

[policy_effect]
e = some(where (p.eft == allow)) && !some(where (p.eft == deny))

[matchers]
m = g(r.sub, p.sub, r.dom) && r.dom == p.dom && r.obj == p.obj && r.act == p.act
```

> **关于 `p.sub`（M3）：** policy 的 `p.sub` 存的是**角色**（如 `role:manager`），不是用户。`g(r.sub, p.sub, r.dom)` 负责把请求主体 `r.sub`（用户）通过角色图解析到 `p.sub`（角色）。即权限只挂在角色上，用户通过 `g` 关系获得角色 → 间接获得权限。直接给用户授权（ACL）是 `p.sub` 存用户的特例，同一 matcher 兼容。

### 6.3 数据权限 — 条件表达式下推

数据策略以结构化条件树存储，运行时由 Sidecar 求值并渲染为目标方言：

```json
{
  "policy_id": "dp_001",
  "subject": "role:manager",
  "resource": "order",
  "condition": {
    "op": "AND",
    "children": [
      { "field": "department", "op": "EQ",  "value": "$user.department" },
      { "field": "status",     "op": "IN",  "value": ["pending", "approved"] }
    ]
  }
}
```

**主体解析（C1）：** Sidecar 收到 `{subject, resource, user_attrs}` 后，先用 casbin `GetImplicitRolesForUser(user, dom)` 把用户展开为隐式角色集，再取出挂在这些角色上的全部 DataPolicy 求值合并。数据权限主体模型与功能权限共用同一份角色继承图。

**参数化渲染（I3）：** `/filter` 接口返回**参数化模板 + 参数数组**，绝不拼接字面量值，从根上杜绝 SQL 注入：

- `sql`：
  ```json
  { "sql": "department = ? AND status IN (?, ?)", "args": ["HR", "pending", "approved"] }
  ```
- `orm`：结构化 Filter 对象（GORM / MyBatis 等 SDK 适配，由 SDK 绑定参数）
- `raw`：原始条件树（让 SDK 自行渲染为参数化语句）

`$user.department` 为运行时变量，Sidecar 在求值时从请求的 `user_attrs` 中取值，**作为参数填入 `args`**，不进入 SQL 文本。

---

## 7. 策略同步机制

```
控制面 Policy Manager
    │
    │  1. 策略变更落库（事务）+ 生成变更 delta + 单调递增版本号
    ▼
gRPC 推送服务 (双向 stream)
    │
    │  2. 推送 delta（变更类型 + 规则 + 版本号）
    ▼
Sidecar 策略同步协程
    │
    │  3. 按 delta 直接改内存策略（不回源全量 LoadPolicy）
    ▼
casbin 内存操作：
    AddPolicies / RemovePolicies / UpdatePolicy
    + BuildIncrementalRoleLinks（仅 g 段，增量重建受影响角色链）
    + InvalidateCache（全量清决策缓存，见 5 节铁律）
    │
    │  4. 比对版本号，缺口则触发一次全量 LoadFilteredPolicy 兜底
    ▼
鉴权 API（使用新策略）
```

> **为什么不用默认 `Watcher`（C2）：** 已回源核实 casbin 三个相关接口（`persist/watcher.go`、`watcher_ex.go`、`enforcer.go:244` `SetWatcher`）：
> - **`Watcher`（基础）**：`SetWatcher` 默认把回调设为 `func(string){ _ = e.LoadPolicy() }`——收到通知即**全量重载**，高频变更下抖动大、且与"分片内存"模型冲突。**不用。**
> - **`WatcherEx`（带 delta）**：`UpdateForAddPolicies` 等方法携带具体变更规则；但 `SetWatcher` 对 WatcherEx **故意不设默认回调**（源码注释："has no generic implementation"）——即 casbin 只负责把 delta 广播出去，**接收端如何 apply 完全交给实现方**。Sydom 正是落在这个空位上：Sidecar 收到 delta 后直接调内存增删改 API（`AddPolicies`/`RemovePolicies`/`UpdatePolicy`）并 `BuildIncrementalRoleLinks`，只重建受影响的角色链，不回源、不全量重建。
>
> 版本号用于检测丢包：Sidecar 发现版本不连续时，回退到一次 `LoadFilteredPolicy`（只拉本 domain 分片）做兜底对齐。
>
> **为什么不是 `Dispatcher`：** 已核实 `Dispatcher`（`persist/dispatcher.go`）+ `DistributedEnforcer`（`enforcer_distributed.go`）：策略写入会在 `internal_api.go:46` 处**短路**进 `dispatcher`，由各对等实例通过 `*Self` 方法（`AddPoliciesSelf` 等）各自落地——这是**多个对等"读写" enforcer 之间的共识式复制**（典型实现挂 Raft）。司域拓扑根本不同：**控制面单写、Sidecar 只读**，Sidecar 永不回写策略，因此不需要 Dispatcher 的对等共识，更不引入 Raft。司域用的是 WatcherEx 的"广播 delta"语义 + 自实现的接收端 apply，**不是** Dispatcher 路线。

**容灾策略（C3，fail-close 默认）：** Sidecar 启动时全量拉取本应用分片，之后保持增量订阅。控制面不可达时，Sidecar 使用本地缓存的策略**继续鉴权**（策略快照仍然有效，只是不再更新）。

默认安全姿态为 **fail-close**：当且仅当本地无任何可用策略快照（如冷启动就连不上控制面）时，鉴权一律拒绝。这与 casbin `enforce()` 出错即返回 `(false, err)` 的内核语义一致——异常路径默认不放行。`fail-open`（异常放行）不作为默认值提供，仅在明确评估风险的特定 domain 上作为显式配置项开放。

---

## 8. 权限点埋点上报机制

业务系统通过 SDK 自动上报权限点，免去手动配置：

```
业务路由表扫描 / 显式注册调用
    │
    │  权限点元数据（路径、描述、资源类型、source=auto）
    ▼
SDK 启动时上报
    │
    │  携带 AppID + 签名（基于 AppSecret）
    ▼
控制面 /api/permission-points (批量幂等 upsert)
    │
    │  服务端按凭据归属强制 app_id，忽略请求体里的 app_id
    ▼
控制面 UI 自动展示可配置的权限点列表
```

**首期（Go SDK）上报方式（M1）：**
- **路由自动发现**：扫描框架路由表（Gin / Echo / net/http 等），自动提取 path + method 生成权限点
- **显式注册 API**：`sydom.RegisterPermission(id, desc, resourceType)`，覆盖路由扫描覆盖不到的细粒度权限点（如按钮级）
- **注解扫描**：`@RequirePermission(...)` 这类注解依赖编译期元数据，**留给后续 Java SDK**（Spring AOP 场景），Go 不走注解路线

**租户隔离与防伪（I2）：**
- 每个 Application 在控制面分配 `AppID` + `AppSecret`，SDK 上报与 Sidecar 订阅均需携带 AppID 并用 AppSecret 签名
- 服务端**按凭据归属强制 `app_id`**：写入的权限点、订阅的策略分片，其 `app_id` 一律取自认证后的凭据，而非请求体——业务系统无法伪造或越权写入其他 app 的权限点
- 上报为**幂等 upsert**（按 `app_id + permission_id` 去重），SDK 重启重复上报不产生脏数据
- 自动上报的权限点标记 `source=auto`，与人工在 UI 创建的 `source=manual` 区分，避免自动扫描覆盖人工配置

---

## 9. SDK 设计原则

SDK 定位为**极薄的框架胶水层**，不包含任何鉴权逻辑：

| 职责 | 说明 |
|------|------|
| Middleware/Interceptor 注入 | 自动拦截 HTTP 请求，调用 Sidecar `/check` |
| 数据权限注入 | ORM Hook 层自动注入 WHERE 条件 |
| 权限点上报 | 启动时扫描并上报权限点到控制面 |
| 连接管理 | 管理与 Sidecar 的 localhost 连接（连接复用、重试） |

SDK **不负责**：策略存储、策略同步、鉴权决策逻辑（全部在 Sidecar）。

首期支持语言：**Go**（Sidecar 本身是 Go 写的，Go SDK 最先完善）。

---

## 10. 可追溯 & 可观测

| 能力 | 实现方式 |
|------|---------|
| 鉴权日志 | Sidecar 记录每次 check 结果（subject, resource, action, result, hit_rule） |
| 审计追踪 | 控制面记录所有策略变更操作（操作人、时间、diff） |
| 指标暴露 | Sidecar 暴露 Prometheus metrics（QPS、延迟、缓存命中率） |
| 决策解释 | `/check` 支持 `explain=true` 返回命中规则详情（casbin EnforceEx） |

---

## 11. 加分项规划

| 特性 | 优先级 | 说明 |
|------|--------|------|
| 权限点埋点自动上报 | P1 | 见第 8 节 |
| AI coding 友好 | P1 | 提供 MCP 工具，AI 开发者可直接通过工具调用配置权限 |
| 极致 UI 体验 | P2 | 可视化角色继承图、数据策略构建器（无需理解底层模型） |
| 聊天配置权限 | P2 | 控制面内嵌 AI 助手，自然语言描述权限需求后自动生成策略 |

---

## 12. 技术选型

| 组件 | 选型 | 理由 |
|------|------|------|
| 鉴权引擎 | casbin v3.10.0 | 多模型支持、Go 生态成熟 |
| Sidecar 语言 | Go | 内存占用低、编译为单二进制、与 casbin 同语言 |
| 控制面语言 | Go | 统一技术栈 |
| 控制面数据库 | PG / MySQL（可配置） | 企业级统一管理，标准 RDBMS，策略唯一真相源 |
| 广播总线 | Redis Pub/Sub（Sentinel/Cluster 高可用） | 策略变更扇出到多副本→Sidecar；at-most-once，由 DB 版本号对账兜底 |
| 策略下发协议 | gRPC stream | 低延迟增量推送，天然支持双向通信 |
| 控制面 API | REST + gRPC | REST 面向 UI，gRPC 面向 Sidecar 和外部集成 |

---

*下一步：详细设计各子系统（控制面 API、Sidecar 内部结构、SDK 接口规范、数据库 Schema）*
