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

## 3. 系统全局架构

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
