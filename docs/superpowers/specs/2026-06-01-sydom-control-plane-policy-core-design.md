# 司域 (Sydom) 控制面 · 策略核心引擎 (Policy Core) 详细设计

> 厘定辖域，权归其位
>
> 版本：v0.1 | 日期：2026-06-01 | 状态：草稿
>
> 上游：[整体架构设计](2026-05-30-sydom-architecture-design.md) | 关联：[数据库 Schema](2026-05-31-sydom-database-schema-design.md)、[gRPC 同步协议](2026-05-31-sydom-grpc-sync-protocol-design.md)

---

## 1. 范围与定位

本文档是司域**控制面**的第一个子模块——**策略核心引擎（Policy Core）**的详细设计。控制面（详细设计第 3 个子项目）按依赖分为三块，本文档只覆盖第一块：

```
③-1 策略核心引擎（本文档）── 真相源写入引擎，纯 DB 层、零网络【底座】
③-2 同步下发服务 ── Redis 广播 + PolicySync gRPC 服务端（依赖 ③-1）
③-3 管理 API ── REST+gRPC 对外 CRUD + 管理鉴权（依赖 ③-1）
```

**③-1 是什么**：控制面的"真相源写入引擎"。对外暴露 Go service 方法（供 ③-3 调用），每个写方法是一次"版本号写事务"（数据库 spec §6 时序），把业务表变更投影成 `casbin_rule`，产出领域 `Delta`（供 ③-2 下发）。

**不在本文档范围内**（留给 ③-2 / ③-3）：
- Redis Pub/Sub 广播、PolicySync gRPC 服务端、`PullSnapshot`/心跳推送（→ ③-2）
- REST/gRPC 对外接口、管理鉴权（谁能改策略）（→ ③-3）
- Sidecar 如何 apply delta、数据权限引擎求值（→ Sidecar spec）

本文档只回答："控制面如何把业务实体的增删改，原子地落库、投影成 `casbin_rule`、生成 delta，并守住权限一致性。"

---

## 2. 设计决策（头脑风暴已逐条确认）

1. **控制面不跑 casbin（纯 SQL 投影）**：业务表 → `casbin_rule` 是司域自己的投影逻辑（SQL/Go），casbin 只在 Sidecar 跑。`PullSnapshot`=SELECT、delta=投影 diff，均为对 `casbin_rule` 的纯数据操作。契合"多副本无状态"（状态全在 DB，每次按需读），且投影是 casbin 的能力空白区（领域模型→p/g 行），用 SQL 投影**不算重复造轮子**。
2. **"BatchAdapter" 退化为 DAO**：决策 1 下控制面无 Enforcer 驱动 adapter，故 `casbin_rule` 仓储是普通 DAO（`ReadAppRules`/`ApplyDiff`），**不实现** casbin `persist.Adapter`。数据库 spec §8 留的悬念在此收口。
3. **delta = 全量重投影 + diff**：写事务里重新投影该 app 的完整期望 `casbin_rule` 集，与库中现有 diff 得 (adds, removes)。这个 diff 既是入库变更、也正是 delta——**单一正确性来源**，不依赖每条 CRUD 路径各自算对 delta。一致性优先于性能；重投影成本是 per-app、管理写低频，可接受（窄化范围属后续优化，YAGNI）。
4. **delta 不持久化**：只在写事务后现算一次，由 ③-2 推一次；丢包靠全量快照拉取兜底（架构 R10、数据库 spec §6）。
5. **角色继承环检测归 ③-1**：不跑 casbin 即用不了它的环检测，故用一个小 DFS 在写事务内校验 per-app 继承图（低频、图小，纯领域逻辑）。
6. **领域 Delta 不耦合 gRPC**：`Delta` 是领域结构体，`Version` 用 `int64`（与 DB `BIGINT` 一致）；②协议的 `uint64` 转换在 ③-2 边界做。③-1 可零网络、用 testcontainers PG 完整测试。

---

## 3. 包结构与依赖方向

```
internal/controlplane/
  store/        业务实体仓储（role / permission / role_permission /
                role_inheritance / user_role_binding / data_policy 的 CRUD）
                + casbin_rule 仓储（批量读、按 diff 增删行）
                + audit_log 写入。纯 SQL/DB 访问层。
  projection/   Projector.ProjectApp(tx, appID) → 期望 casbin_rule 集（落 §5 投影规则）
                + Diff(current, desired) → (adds[], removes[])
                + 角色继承环检测（DFS）。
  secret/       SecretResolver 实现：从 application.app_secret_enc 解密取原文
                （复用 internal/crypto AES-GCM），实现 ② 的 auth.SecretResolver 接口
                + 建应用时加密写入 app_secret_enc 的 helper。
  policy/       PolicyManager —— 编排 §6 写事务、对外写方法集，返回领域 Delta。
```

**依赖方向（无环）**：

```
policy ──→ projection, store, secret, audit(store 内)
projection, store ──→ internal/db
secret ──→ internal/crypto, store
（③-1 不依赖 gRPC、不依赖 Redis；Delta 由 ③-2 翻译成 syncv1.Delta）
```

**单一职责检验**：`store` 只做 SQL；`projection` 只做"业务表→规则集"的纯计算（输入 tx，输出规则切片）；`secret` 只做加解密+查表；`policy` 只做事务编排与对外契约。每个文件可独立放入上下文、独立测试。

---

## 4. 领域类型与组件接口

### 4.1 领域类型（`internal/controlplane`，不含 gRPC 依赖）

```go
// Rule 是一条 casbin_rule（ptype + v0..v5）。
type Rule struct {
    Ptype string
    V     [6]string // v0..v5，空位用空串（casbin 惯例）
}

// ChangeOp 是 data_policy 变更类型。
type ChangeOp int

const (
    ChangeAdd ChangeOp = iota
    ChangeUpdate
    ChangeRemove
)

// DataPolicy 是一条数据权限规则（条件树以 JSON 字符串承载，协议层不透明）。
type DataPolicy struct {
    ID          int64
    SubjectType string // "role" / "user"
    SubjectID   string
    Resource    string
    Condition   string // 条件树 JSON
}

type DataPolicyChange struct {
    Op     ChangeOp
    Policy DataPolicy
}

// Delta 是一次写事务的产物，供 ③-2 翻译并下发。
type Delta struct {
    AppID       int64
    Version     int64 // 与 DB BIGINT 一致；②协议 uint64 转换在 ③-2 边界
    RuleAdds    []Rule
    RuleRemoves []Rule
    DataChanges []DataPolicyChange
}
```

### 4.2 store 包接口

```go
// CasbinRuleRepo 读写物化投影表 casbin_rule（DAO，非 casbin persist.Adapter）。
type CasbinRuleRepo interface {
    // ReadAppRules 读取某 app 当前全部 casbin_rule 行（diff 基准 / 快照）。
    ReadAppRules(ctx context.Context, q Queryer, appID int64) ([]Rule, error)
    // ApplyDiff 按 diff 增删行并把新增/留存行的 version 标为 version。
    ApplyDiff(ctx context.Context, ex Execer, appID int64, adds, removes []Rule, version int64) error
}

// 业务实体仓储（节选；其余实体同构）。Queryer/Execer 是 *sql.DB 与 *sql.Tx 的公共子集接口，
// 使仓储方法既能在事务内、也能独立调用。
type RoleRepo interface {
    Create(ctx context.Context, ex Execer, appID int64, code, name string) (int64, error)
    Rename(ctx context.Context, ex Execer, roleID int64, name, desc string) error // 只改展示字段，不改 code
    Delete(ctx context.Context, ex Execer, roleID int64) error
    // ...
}
// PermissionRepo / RolePermissionRepo / RoleInheritanceRepo /
// UserRoleBindingRepo / DataPolicyRepo / AuditLogRepo 同构，略。
```

### 4.3 projection 包

```go
// ProjectApp 按 §5 投影规则，从业务表算出该 app 的"期望 casbin_rule 全集"。
func ProjectApp(ctx context.Context, q Queryer, appID int64) ([]Rule, error)

// Diff 计算集合差：desired - current = adds，current - desired = removes。
// 比较键为 (Ptype, V[0..5]) 全等。
func Diff(current, desired []Rule) (adds, removes []Rule)

// CheckNoCycle 校验把 (childID→parentID) 加入该 app 的角色继承图后不成环（DFS）。
func CheckNoCycle(ctx context.Context, q Queryer, appID, childID, parentID int64) error
```

### 4.4 policy 包：PolicyManager

```go
type PolicyManager struct {
    db   *sql.DB
    // store 仓储、projection 函数依赖通过构造注入
}

// 对外写方法（节选），全部返回领域 Delta，全部走 §6 统一事务模板。
func (m *PolicyManager) GrantPermission(ctx context.Context, appID, roleID, permID int64, eft string) (*Delta, error)
func (m *PolicyManager) RevokePermission(ctx context.Context, appID, roleID, permID int64) (*Delta, error)
func (m *PolicyManager) CreateRole(ctx context.Context, appID int64, code, name string) (roleID int64, d *Delta, err error)
func (m *PolicyManager) DeleteRole(ctx context.Context, appID, roleID int64) (*Delta, error)
// 注：角色/权限点的纯展示字段编辑（name/description）不影响投影、不 bump 版本，
// 不经 PolicyManager 版本化事务，由 ③-3 直接调 store 仓储（如 RoleRepo.Rename）。
func (m *PolicyManager) AddRoleInheritance(ctx context.Context, appID, childID, parentID int64) (*Delta, error)
func (m *PolicyManager) RemoveRoleInheritance(ctx context.Context, appID, childID, parentID int64) (*Delta, error)
func (m *PolicyManager) BindUserRole(ctx context.Context, appID int64, userID string, roleID int64) (*Delta, error)
func (m *PolicyManager) UnbindUserRole(ctx context.Context, appID int64, userID string, roleID int64) (*Delta, error)
func (m *PolicyManager) UpsertPermission(ctx context.Context, appID int64, code, resource, action string) (permID int64, d *Delta, err error)
func (m *PolicyManager) UpsertDataPolicy(ctx context.Context, appID int64, p DataPolicy) (*Delta, error)
func (m *PolicyManager) DeleteDataPolicy(ctx context.Context, appID, dataPolicyID int64) (*Delta, error)
```

> **无变更返回**：当某次写在 Diff 后无任何 casbin_rule 变更、且无 data_policy 变更时（幂等 upsert），方法**不 bump 版本、不写 audit、返回 `(nil, nil)`** —— 调用方据 `d == nil` 判断"无实际变更，无需下发"。

### 4.5 secret 包：SecretResolver

```go
type Resolver struct {
    db        *sql.DB
    masterKey []byte // 32 字节 AES-256 主密钥，由环境变量/KMS 注入，绝不入库
}

// ResolveSecret 实现 ② 的 auth.SecretResolver：按 app_key=appID 查 application →
// 取 app_secret_enc → crypto.Decrypt(masterKey, enc) 还原 AppSecret 原文。
func (r *Resolver) ResolveSecret(ctx context.Context, appID string) ([]byte, error)

// EncryptSecret 供建应用时加密 AppSecret 写入 app_secret_enc。
func (r *Resolver) EncryptSecret(plaintext []byte) ([]byte, error) // = crypto.Encrypt(masterKey, plaintext)
```

---

## 5. 投影规则（业务表 → casbin_rule）

落实数据库 spec §5（对应架构 6.2 model `p = sub, dom, obj, act, eft` / `g = _, _, _`）：

| 来源（均 JOIN application 取 domain） | ptype | v0 | v1 | v2 | v3 | v4 |
|---|---|---|---|---|---|---|
| `role_permission` ⋈ role ⋈ permission | `p` | `role.code` (sub) | `app.domain` (dom) | `perm.resource` (obj) | `perm.action` (act) | `rp.eft` |
| `user_role_binding` ⋈ role | `g` | `urb.user_id` (user) | `role.code` (role) | `app.domain` (dom) | `''` | `''` |
| `role_inheritance` ⋈ role(parent/child) | `g` | `child.code` (子) | `parent.code` (父) | `app.domain` (dom) | `''` | `''` |

- 两类 `g` 行同表共存，靠 v0/v1 语义区分（user→role vs child→parent），Sidecar 的 casbin `RoleManagerImpl` 天然支持混合。
- `data_policy` **不参与投影**，由 Sidecar 数据权限引擎独立加载；其变更只 bump `application.current_version` 与 `data_policy.version`，并进 `Delta.DataChanges`。
- 投影键 `role.code` / `permission.resource` / `permission.action` 创建后不可变（数据库 spec §223）——Rename 类只改 `name`/`description`，PolicyManager 拒绝改投影键。

---

## 6. 写事务时序（统一模板）

落实数据库 spec §6。所有 PolicyManager 写方法共用此模板（以功能权限类变更为例）：

```
BEGIN
  1. SELECT current_version FROM application WHERE id=:appID FOR UPDATE   -- 行锁，串行化本 app
  2. （仅 AddRoleInheritance）CheckNoCycle：加边不成环，否则 error 回滚
  3. 业务表 CUD（role / permission / role_permission /
                user_role_binding / role_inheritance / data_policy）
  4. desired := ProjectApp(tx, appID)
     current := CasbinRuleRepo.ReadAppRules(tx, appID)
     adds, removes := Diff(current, desired)
  5. 若 adds/removes 与 data 变更全空 → 无策略影响：COMMIT 已做的业务表变更
     （幂等写如重复授权通常本就无变更），不 bump 版本、不写 audit、返回 (nil, nil)
  6. v_new := current_version + 1
     CasbinRuleRepo.ApplyDiff(tx, appID, adds, removes, v_new)   -- 功能权限类
     （data_policy 类：UPDATE data_policy.version = v_new，不动 casbin_rule）
     UPDATE application SET current_version = v_new WHERE id=:appID
     INSERT policy_audit_log(version=v_new, operator, action, entity, diff)
COMMIT
返回 Delta{AppID, Version:v_new, RuleAdds:adds, RuleRemoves:removes, DataChanges:...}
（③-2 在 COMMIT 后翻译并下发；下发失败不回滚——DB 已是真相源）
```

**并发**：步骤 1 的 `FOR UPDATE` 串行化同 app 的版本号与变更；跨 app 无锁竞争，多副本可并发处理不同 app。

---

## 7. 一致性与错误处理铁律

守"权限不一致绝不可接受"（fail-close / DB 真相源）：

- **原子性 = 单事务全有或全无**：业务表 CUD + casbin_rule diff + version bump + audit 在同一 DB 事务。任一步失败 → 整体回滚 → 版本不变、`casbin_rule` 不变、无 Delta、调用方拿到 error。**失败的写改变不了任何东西**。
- **delta 与 DB 绝对同源**：delta 就是写入 `casbin_rule` 的同一份 diff（决策 3），不存在"库里写 X、推 Y"的偏差。下发是 COMMIT 之后（③-2）；下发失败靠版本号 + 心跳 + 全量快照兜底（架构 R3/R10）。
- **投影键不可变**：拒绝修改 `role.code` / `permission.resource` / `permission.action`。
- **无实际变更不推高版本**：Diff 为空且无 data 变更 → 不 bump、返回 nil Delta（幂等 upsert，对齐数据库 spec §356）。
- **环检测前置**：`AddRoleInheritance` 成环则 error，不进事务后续步骤。
- **主密钥缺失即 fail-close**：无主密钥时 `EncryptSecret`（建应用）与 `ResolveSecret` 均报错拒绝，不静默降级。

---

## 8. 测试策略

复用 ① 的 testcontainers 基建（`startPostgres` / `setupSchema` / `seedApp`，真实 PG）。TDD：先写失败测试 → 实现 → 绿。

**投影正确性（projection 包，最关键）**：
- seed `role_permission` / `role_inheritance` / `user_role_binding` → `ProjectApp` → 断言产出 `casbin_rule` 集**逐行精确**匹配 §5：`p` 行 `(role.code, domain, resource, action, eft)`、两类 `g` 行 `(user_id, role.code, domain)` 与 `(child.code, parent.code, domain)`。
- `Diff` 纯单元测试：current/desired 各种增删组合 → 精确 adds/removes。
- `CheckNoCycle`：构造 A→B→A、A→B→C→A → 报错；合法 DAG → 通过。

**写事务端到端（policy 包，testcontainers PG）**：
- `GrantPermission` → 断言 casbin_rule 多出对应 `p` 行、`current_version` +1、`policy_audit_log` 多一行、返回 `Delta` 与库变更一致。
- **幂等无 op**：重复 `GrantPermission` → 第二次 Diff 空 → 版本不变、返回 `(nil, nil)`。
- **原子回滚**：事务中途失败（如违反唯一约束）→ 断言版本未变、casbin_rule 未变、无 audit。
- **版本串行化**：N goroutine 并发对同 app 写 → 版本严格单调、无丢失更新（仿 ① 的 `TestApplication_VersionBumpSerialized`）。
- **环检测**：`AddRoleInheritance` 构造环 → 报错、无任何变更。
- **投影键不可变**：尝试改 `role.code` → 被拒。
- **data_policy 变更**：`UpsertDataPolicy` → 版本 +1、`casbin_rule` 不变、`data_policy.version` 更新、`Delta.DataChanges` 有值。

**SecretResolver（secret 包）**：
- 加密写 `app_secret_enc` → `ResolveSecret(app_key)` 解密得原文（往返）；错误/缺失主密钥 → 报错（fail-close）。

---

## 9. 边界与未决（留给后续子模块）

- **delta 翻译为 syncv1.Delta + 下发**（Redis PUBLISH / gRPC stream 推送 / int64→uint64 转换）→ ③-2。
- **PullSnapshot 读取面**（`SELECT casbin_rule + data_policy WHERE app_id`）→ ③-2 复用 ③-1 的 `store` 读方法实现。
- **REST/gRPC 对外接口、管理鉴权、operator 来源**（audit 的 operator 字段由 ③-3 的认证上下文提供）→ ③-3。
- **重投影范围窄化**（按受影响子图增量重投影而非全量）→ 性能优化，YAGNI，待量化后再做。
- **主键策略 / PG·MySQL 方言**：沿用数据库 spec（MVP 用 DB 自增 bigint，PG 优先）。

---

*下一步：本子模块（③-1）进入实现计划（writing-plans）；之后是 ③-2 同步下发服务、③-3 管理 API。*
