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

- **casbin 是内核，不是依赖**：Sydom 以 casbin v3.10.0 为鉴权引擎内核。**绝不修改 casbin 源码**。casbin 已有的能力通过复用或适配接口使用；只有 casbin 能力边界之外的功能才由 Sydom 自行实现（详见第 3 节 casbin 能力边界）。
- **控制面与数据面分离**：控制面负责授权管理，数据面负责鉴权生效
- **业务代码解耦**：数据面通过 Sidecar + 极薄 SDK 实现，业务逻辑层对权限无感知
- **多系统统一管理**：一套控制面管理多套业务系统的权限

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
                             │  策略下发 (gRPC stream / 长轮询)
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

---

## 4. 数据面 (Sidecar) 架构

每个业务服务旁边部署一个 Sidecar 进程，承载鉴权引擎的全部核心功能。

```
┌──────────────────────────────────────────┐
│              Sidecar 进程                 │
│                                          │
│  ┌────────────┐   ┌──────────────────┐   │
│  │  策略缓存   │◄──│   策略同步协程    │   │
│  │ (内存,全量) │   │ (gRPC stream 订阅)│   │
│  └─────┬──────┘   └──────────────────┘   │
│        │                                 │
│  ┌─────▼────────────────────────────┐    │
│  │         casbin Enforcer           │    │
│  │  SyncedEnforcer + 自定义 Adapter  │    │
│  │  (读内存缓存，零 DB 访问)           │    │
│  └─────┬────────────────────────────┘    │
│        │                                 │
│  ┌─────▼────────────────────────────┐    │
│  │       鉴权 API (HTTP/gRPC)        │    │
│  │  POST /check  → bool              │    │
│  │  POST /filter → 条件表达式         │    │  ← 数据权限下推
│  │  POST /batch-check → []bool        │    │
│  └──────────────────────────────────┘    │
└──────────────────────────────────────────┘
          ↕ localhost
┌──────────────────────────┐
│    业务进程 + SDK          │
│  middleware 拦截每个请求   │
│  → 调 Sidecar /check      │
│  → WHERE 条件注入查询      │
└──────────────────────────┘
```

**性能目标：**
- Sidecar 内存占用 < 50MB
- 本地回环鉴权延迟 < 1ms (P99)
- 策略变更同步延迟 < 5s

---

## 5. 权限模型设计

### 5.1 核心概念层次

```
租户 (Tenant)
  └── 应用 (Application)       ← 对应一套业务系统，即 casbin domain
        ├── 权限点 (Permission)  ← 功能权限：menu / button / api
        │     格式: {app}:{resource}:{action}
        │     示例: "order-system:order:create"
        │
        ├── 角色 (Role)          ← 角色继承树（RBAC）
        │     示例: admin > manager > viewer
        │
        └── 数据策略 (DataPolicy) ← 数据权限（ABAC 条件树）
              运行时下推为: SQL WHERE / ORM Filter / ES DSL
```

### 5.2 功能权限 — casbin 模型配置

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

### 5.3 数据权限 — 条件表达式下推

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

Sidecar `/filter` 接口接收 `{subject, resource, user_attrs}`，返回：
- `sql`：`department = 'HR' AND status IN ('pending','approved')`
- `orm`：结构化 Filter 对象（GORM / MyBatis 等 SDK 适配）
- `raw`：原始条件树（让 SDK 自行渲染）

`$user.department` 为运行时变量，Sidecar 在求值时从请求的 `user_attrs` 中替换。

---

## 6. 策略同步机制

```
控制面 Policy Manager
    │
    │  1. 策略变更事件（增量）
    ▼
gRPC 推送服务 (双向 stream)
    │
    │  2. 增量 patch 下发
    ▼
Sidecar 策略同步协程
    │
    │  3. 更新内存缓存
    ▼
casbin Enforcer.LoadIncrementalFilteredPolicy()
    │
    │  4. 重建角色图 BuildRoleLinks()
    ▼
鉴权 API（使用新策略）
```

**容灾策略：** Sidecar 启动时全量拉取，之后保持增量订阅。控制面不可达时，Sidecar 使用本地缓存继续工作（降级策略可配置：拒绝或放行）。

---

## 7. 权限点埋点上报机制

业务系统通过 SDK 自动上报权限点，免去手动配置：

```
业务代码注解 / 路由扫描
    │
    │  权限点元数据（路径、描述、资源类型）
    ▼
SDK 启动时上报
    │
    ▼
控制面 /api/permission-points (批量注册)
    │
    ▼
控制面 UI 自动展示可配置的权限点列表
```

SDK 支持：
- **注解扫描**：`@RequirePermission("order:create")` 自动注册
- **路由自动发现**：扫描框架路由表（Gin/Spring MVC/Express 等）
- **手动上报**：SDK 提供 `RegisterPermission(id, desc)` API

---

## 8. SDK 设计原则

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

## 9. 可追溯 & 可观测

| 能力 | 实现方式 |
|------|---------|
| 鉴权日志 | Sidecar 记录每次 check 结果（subject, resource, action, result, hit_rule） |
| 审计追踪 | 控制面记录所有策略变更操作（操作人、时间、diff） |
| 指标暴露 | Sidecar 暴露 Prometheus metrics（QPS、延迟、缓存命中率） |
| 决策解释 | `/check` 支持 `explain=true` 返回命中规则详情（casbin EnforceEx） |

---

## 10. 加分项规划

| 特性 | 优先级 | 说明 |
|------|--------|------|
| 权限点埋点自动上报 | P1 | 见第 7 节 |
| AI coding 友好 | P1 | 提供 MCP 工具，AI 开发者可直接通过工具调用配置权限 |
| 极致 UI 体验 | P2 | 可视化角色继承图、数据策略构建器（无需理解底层模型） |
| 聊天配置权限 | P2 | 控制面内嵌 AI 助手，自然语言描述权限需求后自动生成策略 |

---

## 11. 技术选型

| 组件 | 选型 | 理由 |
|------|------|------|
| 鉴权引擎 | casbin v3.10.0 | 多模型支持、Go 生态成熟 |
| Sidecar 语言 | Go | 内存占用低、编译为单二进制、与 casbin 同语言 |
| 控制面语言 | Go | 统一技术栈 |
| 控制面数据库 | PG / MySQL（可配置） | 企业级统一管理，标准 RDBMS |
| 策略下发协议 | gRPC stream | 低延迟增量推送，天然支持双向通信 |
| 控制面 API | REST + gRPC | REST 面向 UI，gRPC 面向 Sidecar 和外部集成 |

---

*下一步：详细设计各子系统（控制面 API、Sidecar 内部结构、SDK 接口规范、数据库 Schema）*
