# 司域控制面 ③-3 管理 API 详细设计

> 子项目：控制面第三子模块（③-3）。前置：①数据库 Schema、②gRPC 同步协议契约、③-1 策略核心引擎、③-2 同步下发服务 均已实现并入 main。
> 范围边界：**服务/库层**，不产出可运行二进制（cmd/main 的进程装配留作独立装配子项目）。
> 日期：2026-06-02

---

## 1. 目标与范围

控制面 ③-3 提供**管理写入面**：让管理员通过 gRPC 增删改业务权限策略（角色/权限/授权/继承/用户绑定/数据策略/应用），并把每次写入产生的 `Delta` 可靠地广播给数据面 Sidecar。它在 ③-1（策略核心引擎 `PolicyManager`）之上叠加四件事：

1. **管理 API 服务面**：gRPC `AdminService`，把 `PolicyManager` 现有写方法与必要的读/列表暴露为 RPC（gRPC 优先，REST/grpc-gateway 后补，不在本期）。
2. **管理鉴权（元-RBAC）**：用一个**独立于 ③-1 业务策略投影**的 casbin RBAC-with-domain enforcer 决定"谁能在哪个 app 域做什么管理动作"，实现管理层租户隔离。
3. **status 生命周期写拦截**：`application.status` 为停用态时拒绝对该 app 的策略写入。
4. **publish 侧写编排（事务性 outbox + relay）**：写事务内原子落 `Delta` 到 outbox，独立 relay 循环可靠投递到 `broadcast.Publisher`，接上 ③-2 的下发链路。

**非目标（本期不做）**：REST/grpc-gateway 网关；Web Console UI；可运行控制面二进制与配置/优雅关闭装配；外部 JWT/SSO 集成；admin 操作的细粒度字段级审计 diff（沿用 ③-1 既有审计行）。

---

## 2. 关键决策（已与用户逐条确认）

1. **管理鉴权模型 = 元-RBAC（casbin 自举）**。管理员是 casbin 的 subject，管理权是 casbin policy。控制面为此**额外运行一个 casbin enforcer**（注意：③-1 决策"控制面不跑 casbin"针对的是业务策略投影；管理鉴权是另一套、独立的 casbin 用途）。
2. **元-RBAC 用专用管理表 + 独立轻量 enforcer**，与业务表物理分离、与业务投影路径解耦。`app_id` 作 casbin domain，外加一个 `*` system 域给超级管理员。
3. **API gRPC 优先**，复用现有 buf/proto 工具链与认证拦截器模式；REST 后补。
4. **交付仅服务/库层**，全部用 bufconn + testcontainers(PG+Redis) 测，不建 cmd/main。
5. **publish 交付保证 = 事务性 outbox + relay**（高一致性）。写事务内把 `Delta` 与版本 bump 原子落库；独立 relay 循环 drain→翻译→`broadcast.Publisher.Publish`→标记已发。崩溃/Redis 抖动后续投，at-least-once，Sidecar 版本去重兜底。
6. **status 两态**：`1=active`、`2=disabled`（MVP）。
7. **bootstrap root operator** 初始凭据由配置/环境变量注入（fail-close：未配置则无 root，不创建默认弱凭据）。
8. **管理员自身 CRUD（operator/role/grant）也走 AdminService + 同一套元-RBAC 鉴权**：管理管理员的权限由 `*` system 域的元权限控制，形成自举闭环。

---

## 3. 组件结构（新增）

```
internal/controlplane/
  adminauthz/     管理鉴权：独立 casbin RBAC-with-domain enforcer + 自定义 Adapter（读 admin 表）+ 版本化重载
  outbox/         写事务内 DeltaSink 落库 + RunRelayLoop（drain→translate→Publisher→标记已发）
  mgmt/           AdminService gRPC 实现 + 三道 unary 拦截器（认证 / 鉴权 / status 写拦截）
api/proto/sydom/admin/v1/   AdminService + 管理 CRUD 消息（buf 生成到 gen/sydom/admin/v1/）
db/migrations/000013_*      admin schema（operator/role/grant/subject_role + 种子）
db/migrations/000014_*      policy_outbox 表
```

**职责边界（每单元一句话能说清）**：
- `mgmt`：只做协议适配与编排；业务写下沉 `policy.PolicyManager`（复用，不重写），管理元数据写下沉 admin store。
- `adminauthz`：只回答 `Enforce(principal, domain, resource, action) bool` 与认证 `ResolveOperator(principal, secret)`；不碰业务策略。
- `outbox`：只保证"写产生的 Delta 必被可靠投递"；不懂 RPC、不懂鉴权。
- 复用既有：`policy.PolicyManager`（写方法返回 `*cp.Delta`）、`translate.DeltaToProto`、`broadcast.Publisher`、`crypto`（AES-GCM）、`cp.WithOperator/OperatorFromContext`、`store`。

---

## 4. 数据模型（新 migration）

### 4.1 admin schema（`000013`）—— 元-RBAC，与业务表物理分离

```sql
-- 管理操作者及其凭据（凭据用 ②-crypto AES-256-GCM 加密，与 app secret 同机制；主密钥不入库）
CREATE TABLE admin_operator (
    id          BIGSERIAL PRIMARY KEY,
    principal   VARCHAR(128) NOT NULL UNIQUE,   -- 登录标识（ASCII 可打印非空格，挡同形字，复用 ② 的 validAppID 规则）
    secret_enc  BYTEA        NOT NULL,
    status      SMALLINT     NOT NULL DEFAULT 1, -- 1=active, 2=disabled
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- 管理角色
CREATE TABLE admin_role (
    id    BIGSERIAL PRIMARY KEY,
    code  VARCHAR(64) NOT NULL UNIQUE,
    name  VARCHAR(128) NOT NULL
);

-- 角色的管理权：casbin p 行 —— (role 主体, domain, resource, action)
-- domain = 目标 app_id 的字符串形式，或 '*' 表示 system 全域（超级管理员）
CREATE TABLE admin_role_grant (
    id        BIGSERIAL PRIMARY KEY,
    role_id   BIGINT      NOT NULL REFERENCES admin_role(id) ON DELETE CASCADE,
    domain    VARCHAR(64) NOT NULL,
    resource  VARCHAR(64) NOT NULL,   -- 见 §5.3 资源/动作词表
    action    VARCHAR(32) NOT NULL,
    UNIQUE (role_id, domain, resource, action)
);

-- 操作者-角色绑定：casbin g 行 —— g(operator_principal, role_code, domain)
CREATE TABLE admin_subject_role (
    id          BIGSERIAL PRIMARY KEY,
    operator_id BIGINT      NOT NULL REFERENCES admin_operator(id) ON DELETE CASCADE,
    role_id     BIGINT      NOT NULL REFERENCES admin_role(id) ON DELETE CASCADE,
    domain      VARCHAR(64) NOT NULL,
    UNIQUE (operator_id, role_id, domain)
);

-- 管理策略版本：用于 enforcer 重载判定（单调递增，任何 admin 写都 bump）
CREATE TABLE admin_policy_version (
    id      SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),  -- 单行
    version BIGINT   NOT NULL DEFAULT 0
);
INSERT INTO admin_policy_version (id, version) VALUES (1, 0);
```

**内置种子（同 migration 内）**：插入 `super-admin` 角色，并给它在 `*` 域授予全资源全动作（用通配 `resource='*', action='*'`，由 casbin matcher 处理，见 §6）。**不在 migration 内硬编码 root 凭据**——bootstrap root operator 由应用启动时按配置/环境注入（见 §7），fail-close。

### 4.2 outbox 表（`000014`）

```sql
CREATE TABLE policy_outbox (
    id           BIGSERIAL PRIMARY KEY,
    app_id       BIGINT      NOT NULL,
    version      BIGINT      NOT NULL,   -- 该 Delta 对应的目标版本（写事务 bump 后的 current_version）
    delta_proto  BYTEA       NOT NULL,   -- translate.DeltaToProto(delta) 序列化后的 syncv1.Delta 字节
    published_at TIMESTAMPTZ,            -- NULL=未发布
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_policy_outbox_unpublished ON policy_outbox (id) WHERE published_at IS NULL;
```

---

## 5. 写路径、publish 与服务面

### 5.1 DeltaSink：写事务内原子落 outbox（依赖倒置）

`policy.PolicyManager.runVersionedWrite` / `runVersionedWriteData` 当前在单事务内 bump 版本并产出 `cp.Delta`。本期**给 PolicyManager 注入一个可选的 `DeltaSink` 接口**，在**同一事务内、提交前**被调用：

```go
// policy 包新增（最小侵入）：
type DeltaSink interface {
    // Persist 在写事务内（同一 *sql.Tx）持久化 Delta；返回 error 即触发整个写事务回滚。
    Persist(ctx context.Context, tx cp.DBTX, appID int64, delta *cp.Delta) error
}
// NewPolicyManager 增加可选 sink（nil 时退化为 ③-1 当前行为，保持既有测试不破）。
```

`outbox` 包实现该 sink：`translate.DeltaToProto(delta)` → `proto.Marshal` → `INSERT INTO policy_outbox(...)`。**原子保证**：版本 bump 与 outbox 行同事务提交；sink 返回 error → 业务写整体回滚 → "版本变了但 Delta 没记"绝不发生（fail-close）。

> 修改 ③-1 的 `policy` 包属本期"接 publish 侧写编排"的指定集成点，非越界重构。改动限于：`PolicyManager` 增加 sink 字段与构造参数、在两个 `runVersionedWrite*` 的提交前调用 sink。既有方法签名与行为在 sink=nil 时不变。

### 5.2 relay 循环

`outbox.RunRelayLoop(ctx, db, pub broadcast.Publisher)`（类比 ③-2 的 `RunDispatchLoop`，阻塞至 ctx 取消，每副本启动一次）：
1. 取一批 `published_at IS NULL` 的行（按 id 升序）。
2. 对每行：`proto.Unmarshal(delta_proto)` → `pub.Publish(ctx, app_id, delta)` → 成功则 `UPDATE ... SET published_at=now()`。
3. 无新行时短暂 sleep / 可选 PG `LISTEN/NOTIFY` 唤醒（MVP 用轮询间隔，接口预留）。

语义：**at-least-once**。relay 或 Redis 故障 → 行保持未发布，重启续投；重复投递由 Sidecar 版本去重兜底（与 ③-2 一致）。publish 失败**不触碰 DB 业务数据**，只是不标记 published。

### 5.3 AdminService gRPC 面（`api/proto/sydom/admin/v1/`）

单一 `AdminService`，RPC 分三组（每个写 RPC 响应回带新的 `version`）：

- **业务策略写**（下沉 `PolicyManager`，每个对应一个已存在的写方法）：
  `CreateRole`/`DeleteRole`、`UpsertPermission`、`GrantPermission`/`RevokePermission`、`AddRoleInheritance`/`RemoveRoleInheritance`、`BindUserRole`/`UnbindUserRole`、`UpsertDataPolicy`/`DeleteDataPolicy`。
- **应用管理**：`CreateApplication`（生成 app_key/secret，secret 用 crypto 加密入库）、`SetApplicationStatus`（active/disabled）、`ListApplications`。
- **管理员自身管理**（自举闭环，受 `*` 域元权限约束）：`CreateOperator`/`SetOperatorStatus`、`CreateAdminRole`、`GrantAdminRole`(给角色加 grant)、`BindOperatorRole`(g 绑定) 及对应解绑/列表。

**资源/动作词表**（§4.1 grant 的 `resource`/`action` 取值，用于 §6 鉴权）：
- 资源：`role`、`permission`、`grant`、`inheritance`、`binding`、`data_policy`、`application`、`admin`（管理员自身管理），加通配 `*`。
- 动作：`create`、`update`、`delete`、`read`，加通配 `*`。

### 5.4 三道 unary 拦截器（按序）

1. **认证拦截器**：从 gRPC metadata 取 `operator` principal + `secret`；`adminauthz.ResolveOperator(principal, secret)` 校验（解密比对 + operator status=active）。成功 → `ctx = cp.WithOperator(ctx, principal)`（接上 ③-1 既有审计字段）。失败统一错误消息 → `Unauthenticated`（防枚举 Oracle，复用 ② 的安全教训）。
2. **鉴权拦截器**：从请求解析目标 `app_id`（业务策略类 RPC）或 `*`（应用创建/管理员管理等 system 级动作）作 domain，连同该 RPC 映射的 `(resource, action)`，调 `adminauthz.Enforce(principal, domain, resource, action)`。拒绝 → `PermissionDenied`（**fail-close**）。
3. **status 写拦截器**：仅对"业务策略写 + 针对具体 app"的 RPC，读 `application.status`，非 `active` → `FailedPrecondition`（"app disabled"）。读类与管理员管理类不受此限。

> RPC→(resource, action, 是否写, domain 取法) 的映射用一张静态表实现，集中维护、避免散落判断。

---

## 6. adminauthz enforcer 与一致性

- **模型**（casbin RBAC-with-domain，内嵌为常量字符串，不依赖外部文件）：
  ```
  [request_definition]  r = sub, dom, res, act
  [policy_definition]   p = sub, dom, res, act          # p.sub = 角色 code；p.dom = app_id 字符串或 "*"
  [role_definition]     g = _, _, _                     # g(operator_principal, role_code, domain)
  [policy_effect]       e = some(where (p.eft == allow))
  [matchers]            m = (g(r.sub, p.sub, r.dom) || g(r.sub, p.sub, "*")) \
                          && (p.dom == r.dom || p.dom == "*") \
                          && (p.res == r.res || p.res == "*") \
                          && (p.act == r.act || p.act == "*")
  ```
  其中 `r.sub`=operator principal，`r.dom`=目标 app_id 字符串（system 级动作用 `"*"`）。`admin_role_grant` 的每行 →一条 p；`admin_subject_role` 的每行 →一条 g。
  - **system 域超管的关键**：casbin 的 `g(user, role, domain)` 是按域隔离的，故超级管理员在 `*` 域的绑定**不会**自动匹配具体 app 域。matcher 用 `g(r.sub, p.sub, r.dom) || g(r.sub, p.sub, "*")` 兜住：在 `*` 域持有 `super-admin` 角色者，凭其 `(super-admin, "*", "*", "*")` 的 p 行对任意 app 域放行。普通 app 管理员则在具体 app 域绑定角色、grant 限定该 app 域。
- **Adapter**：自定义 casbin `persist.Adapter`，`LoadPolicy` 从 `admin_role_grant`（→ p 行）与 `admin_subject_role`（→ g 行，subject 用 operator principal、role 用 role code）组装。只读 adapter（写走 AdminService 的结构化方法，不用 casbin 的 AddPolicy）。
- **多副本一致性**：控制面无状态多副本。enforcer 进程内缓存，持有上次加载的 `admin_policy_version`；每次 `Enforce` 前（或按短间隔）比对 DB `admin_policy_version.version`，变化则 `LoadPolicy()` 重载。任何 admin 写在其事务内 bump `admin_policy_version`（与业务版本单调模式一致）。数据集小，重载廉价。**fail-close**：加载失败 / enforcer 未就绪 → 拒绝。

---

## 7. bootstrap、错误与容灾

- **bootstrap root**：应用启动时若配置/环境提供了 `root principal + 初始 secret`，则 ensure 一个绑定 `super-admin`@`*` 的 operator（幂等 upsert）。未配置 → 不创建任何 root（fail-close，杜绝默认弱凭据）。本期提供 `adminauthz.EnsureRootOperator(ctx, db, principal, secret)` 库函数（由未来的 cmd 装配调用，本期单测覆盖）。
- **fail-close 铁律全贯穿**（对齐项目一致性铁律）：未知/停用 operator、密文损坏、无匹配 grant、目标 app disabled、未知 app_id、admin 策略加载失败 —— 一律拒绝。
- **写原子性**：outbox sink 失败 → 业务写事务整体回滚。
- **relay 容灾**：publish 失败仅不标记 published，重试续投，不动 DB；至少一次投递，版本去重兜底。
- **审计**：业务写沿用 ③-1 的 `policy_audit_log`（operator 由认证拦截器注入），本期不新增审计语义。

---

## 8. 测试策略（TDD，bufconn + testcontainers PG+Redis）

- **adminauthz**：Enforce 矩阵（同域放行 / 跨 app 域隔离拒绝 / `*` 超级管理员全放行 / 资源动作通配 / 无 grant 拒绝）；ResolveOperator（正确凭据 / 错密钥 / 停用 operator / 未知 principal 均 fail-close）；admin-version 变化触发重载；自定义 Adapter LoadPolicy 正确组装 p/g。
- **outbox**：DeltaSink 原子性（业务写成功 ⇒ 恰有一条 outbox 行且 version 匹配；sink 失败 ⇒ 业务写回滚、无 outbox 行、版本不变）；relay drain（未发布行被 publish 并标记 published_at；publish 失败保持未发布、重启续投；按 id 顺序）。
- **mgmt**：三道拦截器（认证失败 Unauthenticated 统一消息 / 越权 PermissionDenied / 跨 app 隔离 / disabled app 写 FailedPrecondition / 读不被 status 拦截）；代表性写 RPC 端到端（调用 → PolicyManager 落库 + outbox 行 + 响应回带新 version）；管理员自身 CRUD 自举（super-admin 建 operator/role/grant，普通 operator 无 `admin` 资源权被拒）。
- **端到端（接 ③-2）**：AdminService 写 → outbox → `RunRelayLoop` → Redis → ③-2 `RunDispatchLoop`/Hub → `Subscribe` 流收到翻译后的 Delta version=N。
- **bootstrap**：`EnsureRootOperator` 幂等；未配置则无 root。
- 全量 `go test ./...` + 关键并发路径 `-race`。

---

## 9. 与既有子项目的衔接 / 移交项

- **接住 ③-2 遗留**：本期实现 ③-2 标注的"publish 侧写编排归 ③-3"——`translate.DeltaToProto` + `broadcast.Publisher` 在本期获得生产调用方（outbox sink + relay）。
- **沿用 ③-1**：`PolicyManager` 写方法、`cp.WithOperator`、`store`、审计行；唯一改动是注入 `DeltaSink`（§5.1）。
- **留给后续装配子项目**：cmd/控制面二进制（同时 serve AdminService + ③-2 PolicySync + 起 relay 循环 + 配置/主密钥/Redis 装配 + 优雅关闭）；REST/grpc-gateway；Web Console。
- **③-3 自身刻意保留**：`application.status` 仅在管理写层拦截；③-2 的 `PullSnapshot`/`Subscribe` 是否对 disabled app 改变下发行为，不在本期（保持 ③-2 现状）。
