# M6.1d 成员配额（enforce + measure + UI）— 设计规格

**日期**：2026-07-13
**里程碑**：M6.1 计量+配额 · 第四增量（第二个配额维度：成员）
**前序**：M6.1a 应用配额强制、M6.1b 计量 RPC `GetTenantUsage`、M6.1c Console 用量页

## 目标

把数据驱动配额从"应用"扩到"成员"（per-tenant），**端到端一次交付一个完整维度**：强制（InviteMember 行锁门）+ 计量（`GetTenantUsage` 加 `members`）+ 可见（用量页加成员行）。避免"能挡但看不见"的半态。**决策无关、零触碰授权核心、proto additive 过 `make proto-breaking`。**

## 范围决策

**本片只做成员维度。** 成员是 per-tenant（`tenant_membership` count），与应用配额同构，可干净复用 M6.1a/b/c 三层范式。

**角色 / 数据策略配额延后**：二者是 per-app 资源（每 app 自己的角色/策略），per-app 限额语义与 per-tenant 的 `GetTenantUsage` 不对齐（需 per-app 用量 RPC 或跨 app 聚合），涉及产品决策，是独立后续增量。

## 非目标（YAGNI）

- 角色 / 数据策略配额（per-app 决策）
- 成员配额按 tier 细分（owner/admin 分别限额）
- 套餐升级 / 计费入口（供应商决策）
- 成员配额的用量事件计量（历史时间序列）

## 架构与数据流（复用 M6.1a/b/c 三层范式）

### 1. 迁移 `000022_plan_max_members`

```sql
-- up
ALTER TABLE plan ADD COLUMN max_members INTEGER NOT NULL DEFAULT 0;
UPDATE plan SET max_members = 3  WHERE name = 'free';
UPDATE plan SET max_members = 25 WHERE name = 'pro';
ALTER TABLE plan ALTER COLUMN max_members DROP DEFAULT;
-- down
ALTER TABLE plan DROP COLUMN max_members;
```

DEFAULT 0 使 NOT NULL 列可加到既有行 → UPDATE 设真值 → DROP DEFAULT 对齐 `max_applications`（无默认，新套餐须显式给值）。种子：free=3（owner + 2 邀请）、pro=25，数据驱动 `UPDATE plan` 即调。

### 2. store（`internal/controlplane/store/quota.go`）

- `PlanLimits` 加 `MaxMembers int`；`TenantPlanLimits` 一并 `SELECT p.max_applications, p.max_members`（同一 `FOR UPDATE OF t` 行锁，DRY——一次锁+一次查满足应用门与成员门）。
- 加 `CountMembers(ctx, ex, tenantID) (int, error)` → `SELECT count(*) FROM tenant_membership WHERE tenant_id=$1`。
- `TenantUsage` 加 `MaxMembers/UsedMembers int`；`TenantUsageOf` 加子查询 `(SELECT count(*) FROM tenant_membership tm WHERE tm.tenant_id = t.id)` 与 `p.max_members`（无锁读路径）。

### 3. mgmt `InviteMember`（`internal/controlplane/mgmt/accounts.go`）

在既有 tx 内插配额门。**关键顺序（Order B：insert-then-gate）**——先 `InsertMembership` 拿 `inserted` 布尔，`!inserted` 短路 `AlreadyExists`，再对**新**成员查门。这避免 Order A 的语义 bug：满员时"重复邀请已有成员"应返回 `AlreadyExists`（不增计数、不该被配额挡），而非 `ResourceExhausted`。

```
limits, err := store.TenantPlanLimits(ctx, tx, int64(r.TenantId))  // FOR UPDATE 锁租户行（早锁串行并发邀请）
  ErrNotFound → InvalidArgument "unknown tenant"
opID, created := EnsureOperator(...)                               // 幂等建/取全局 operator（无租户副作用）
count, err := store.CountMembers(ctx, tx, int64(r.TenantId))       // 锁内计数（pre-insert）
inserted := InsertMembership(...)
if !inserted → AlreadyExists                                       // 已是成员：不消耗配额，短路
if count >= limits.MaxMembers → ResourceExhausted "member quota reached (n/m); upgrade plan"  // 新成员超限：rollback 撤销 insert
BindTenantAdminTx / BumpPolicyVersion / 审计 / commit（原逻辑不变）
```

`count >= limits.MaxMembers` 与 `CreateApplication` 的 `count >= MaxApplications` 完全同构（pre-insert 计数）；`!inserted` 短路是成员维度独有（应用每次 create 皆新，无重复语义）。`TenantPlanLimits` 的 `FOR UPDATE OF t` 锁租户行、串行并发邀请杜绝绕过。ResourceExhausted 时 `defer tx.Rollback()` 撤销已发生的 InsertMembership。`InviteMember` 授权签名、成员⟺casbin 锁步（EnsureOperator/BindTenantAdminTx/BumpPolicyVersion/审计）逻辑**不变**。

### 4. proto（`api/proto/sydom/admin/v1/admin.proto`）

```proto
message GetTenantUsageResponse {
  string plan_name = 1;
  ResourceUsage applications = 2;
  ResourceUsage members = 3;   // additive 新字段号
}
```

`buf generate` 重生成；`ResourceUsage` 复用。additive 过 `make proto-breaking`。

### 5. handler `GetTenantUsage`（`internal/controlplane/mgmt/tenant_usage.go`）

填 `Members: &adminv1.ResourceUsage{Used: uint32(u.UsedMembers), Limit: uint32(u.MaxMembers)}`。

### 6. Console `errors.go`——修 latent bug

`httpStatusForCode` 现无 `codes.ResourceExhausted` 分支 → 落 `default:500`「internal error」，把配额文案脱敏吞掉。**这意味着当前经 Console「+新建应用」撞应用上限也显示 500 而非配额提示**（M6.1a 只修了 restgw，漏了 console 自有映射）。加：

```go
case codes.ResourceExhausted:
    return http.StatusTooManyRequests
```

一处修惠及应用 + 成员两维度；配额文案（429，非 500）如实展示给运营者。

### 7. Console 用量页重构为资源行列表（`routes_usage.go` + `usage.html`）

view model 从单一 Used/Limit 改为 `Rows []usageRow`：

```go
type usageRow struct {
    Name      string // "应用" / "成员"
    Used      int
    Limit     int
    AtLimit   bool
    ShowMeter bool
}
```

handler 组两行（应用 + 成员）；模板 `{{range .Rows}}` 渲染每行 `Name：Used / Limit` + `<meter>` + 至上限行内 `.alert-error`。**为"更多维度"可扩展**——将来角色/策略只是 append 一行，模板零改。`PlanLabel`/`TenantID` 保留。

## 测试（TDD）

- **store**（`quota_test.go` 或 mgmt_test）：`TenantPlanLimits` 返 `MaxMembers`（钉死 free=3）；`CountMembers` 计数；`TenantUsageOf` 返 `UsedMembers`（新租户 owner=1）。
- **mgmt**（`accounts_test.go`）：
  - `InviteMember` 建到 free 上限（owner=1，邀请至 3）后再邀请新 principal → `ResourceExhausted`；变异撤门 → 该断言应 FAIL（有齿）。
  - **Order B 正确性有齿**：满员时重复邀请**已有**成员 → `AlreadyExists`（非 `ResourceExhausted`）——钉死 `!inserted` 短路在配额门之前。
  - 并发邀请行锁串行恰到限（复用 M6.1a 并发范式，distinct principal）。
- **handler/REST**：`GetTenantUsage` 响应含 `members{used,limit}`；REST 侧 `ResourceExhausted→429`（已有映射，补断言）。
- **Console**：用量页渲染两行（应用 + 成员，钉死数字 + meter `value/max`）；成员至上限告警双向有齿；`errors.go` 表测加 `ResourceExhausted→429` 断言（挡 latent bug 回归）。

## 不变量

- **零触碰授权核心**：casbin / kernel / adminauthz 求值 / `mgmt/authz.go` ruleTable 零改（`InviteMember` 授权条目不变；配额门是只读 DB 查询，非授权判定）
- `InviteMember` 成员⟺casbin 同事务锁步逻辑不变
- proto additive 过 `make proto-breaking`；`make proto-check` 无 drift
- `go test ./...` EXIT 0

## 验收（M61D-1..8）

1. 零触碰授权核心（机器 diff：casbin/kernel/adminauthz 求值/authz.go 空）
2. 迁移 000022 up/down 往返；plan free/pro `max_members` 3/25
3. store：`TenantPlanLimits.MaxMembers`、`CountMembers`、`TenantUsageOf.UsedMembers` 有齿
4. `InviteMember` 成员配额门 fail-close ResourceExhausted；变异撤门 FAIL 证有齿
5. proto additive `members=3` 过 `make proto-breaking`；`GetTenantUsage` handler 填 members
6. Console `errors.go` `ResourceExhausted→429`（表测挡回归）
7. Console 用量页两行（应用 + 成员）+ 成员至上限告警双向有齿
8. `make proto-check` 无 drift；`go test ./...` EXIT 0
