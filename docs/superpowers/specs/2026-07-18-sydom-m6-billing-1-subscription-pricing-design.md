# M6-billing-1：订阅 + 定价模型主干（供应商无关计费地基第一片）

**日期**：2026-07-18
**里程碑**：M6 商业化+合规+GA → 计费（供应商无关地基）
**范围**：单一实现计划可覆盖的第一片。后续片（发票/生命周期/计量/支付/Console 计费页）各走独立 spec→plan→实现周期。

> **修订（规划期）**：初版 AD-2 让 `subscription.plan_id` 当套餐真相源；规划时量出这会使「每租户必有订阅行」成为配额/用量查询硬依赖，测试夹具（~20 处直接插租户）耦合脆弱。经用户确认改为 **additive 模型**：subscription 只存生命周期、不含 plan_id，`tenant.plan_id` 仍是套餐真相源，配额查询零改。

## 1. 背景与定位

用户选定「计费」方向后，进一步选定 **供应商无关的计费地基**（不锁死支付供应商，决策无关、可自主、GA 前置），并将第一片范围锁定为 **订阅 + 定价模型主干**（后端，不含 UI/发票/dunning/自助 checkout）。

现有地基：`plan` 表（free/pro + `max_applications`/`max_members`）+ `tenant.plan_id` + `GetTenantUsage` RPC + M6.1a/d 配额强制。**无价格、无订阅状态、无计量事件、无支付**。

本片补齐 **订阅生命周期实体 + 套餐定价**，为任何后续供应商与计费能力铺路。

## 2. 目标 / 非目标

**目标**
- `plan` 具备定价（价格/计费周期/币种）。
- 每租户有一条 `subscription`（附加生命周期实体），承载订阅状态与当前计费周期。
- 平台超管可变更租户套餐（升/降级）。
- `GetTenantUsage` 回显定价与订阅状态。

**非目标（YAGNI，明确延后到后续片）**
- 发票 / 收据 / 税。
- dunning（失败重试）。
- 自助 checkout / 自助升级。
- 自动续期 / 周期自动推进（无 cron、无支付 webhook）。
- 多币种定价、价格历史 / grandfathering。
- Console 计费页。
- 支付网关集成。
- subscription 拥有 plan_id（套餐真相源保持在 tenant.plan_id）。

## 3. 架构决策

**AD-1 订阅建模 = 独立 `subscription` 表**
理由：计费地基注定生长，未来 invoice/生命周期/计量周期都需 FK 一个「订阅」实体；现在立一等实体比日后从 `tenant` 列抽表更省事、边界更清（tenant 身份表不该累积计费生命周期）。

**AD-2 `subscription` 为附加生命周期实体（additive，不含 plan_id）**
`subscription` 只承载订阅生命周期（status + 计费周期）；**套餐真相源仍是 `tenant.plan_id`**（不变、配额读点零改）。理由：让 subscription.plan_id 当真相源会使「每租户必有订阅行」成为配额/用量查询硬依赖，测试夹具（~20 处直接插租户）耦合脆弱；YAGNI 下 subscription 不重复 plan_id，待未来发票片真正需要「订阅↔套餐↔价格」快照时再提升。仍单真相源（plan_id 仅在 tenant）。

**AD-3 `ChangeTenantPlan` 仅平台超管**
无支付网关下，自助升级 = 白嫖高配额；故套餐变更是 sales/ops 辅助动作，授权 scope = `scopeSystem`（"*" 域运营平面）；超管在 "*" 域持通配 grant（`p.res="*" p.act="*"`，见 `adminauthz/enforcer.go:31`），故新 resource `"billing"` 无需 seed。绝不开放租户自助。

**AD-4 不 seed 具体价格**
定价是产品决策。迁移只加列（DEFAULT 0），free/pro 价保持 0，真值由运营后置设定。避免代码擅自编价。

**AD-5 金额 = 整数最小币种单位**
`price_cents BIGINT`，避浮点误差；币种 ISO4217 `CHAR(3)`，per-plan。

## 4. 数据模型（迁移 `000023`，expand-only）

### 4.1 `plan` 加定价列
```sql
ALTER TABLE plan ADD COLUMN price_cents    BIGINT      NOT NULL DEFAULT 0;
ALTER TABLE plan ADD COLUMN billing_period VARCHAR(16) NOT NULL DEFAULT 'month'
    CONSTRAINT ck_plan_billing_period CHECK (billing_period IN ('month','year'));
ALTER TABLE plan ADD COLUMN currency       CHAR(3)     NOT NULL DEFAULT 'CNY';
-- 不 seed 具体价格（AD-4）：free/pro 价保持 0，运营后置设真值。
```
（对齐 M6.1d `max_members` 的加列范式：DEFAULT 使既有行平滑加列。定价列保留 DEFAULT 供未来新 plan 行安全插入。）

### 4.2 新表 `subscription`（不含 plan_id，AD-2）
```sql
CREATE TABLE subscription (
    id                    BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id             BIGINT      NOT NULL REFERENCES tenant(id),
    status                VARCHAR(16) NOT NULL DEFAULT 'active',
    current_period_start  TIMESTAMPTZ NOT NULL DEFAULT now(),
    current_period_end    TIMESTAMPTZ,          -- NULL = 无到期（free/永久）
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_subscription_tenant UNIQUE (tenant_id),
    CONSTRAINT ck_subscription_status CHECK (status IN ('active','trialing','past_due','canceled'))
);
```

### 4.3 回填
```sql
INSERT INTO subscription (tenant_id)
SELECT id FROM tenant;   -- status 默认 active、周期默认 now/NULL
```
回填后每既有 tenant 恰一条订阅（status=active）。套餐仍读 `tenant.plan_id`。

### 4.4 down 迁移
`DROP TABLE subscription;` + `ALTER TABLE plan DROP COLUMN price_cents/billing_period/currency;`（对称回滚；`tenant.plan_id` 未动）。

## 5. store 层

### 5.1 配额读点：**零改**
`store/quota.go`（`TenantPlanLimits`、`TenantUsageOf`）仍 `JOIN plan p ON p.id = t.plan_id`——套餐真相源不动，配额判定逐字不变。既有 M6.1a/d 配额测试为回归网。

### 5.2 新增 / 扩展
- 新增 `Subscription` 类型 + `SubscriptionOf(ctx, ex, tenantID) (Subscription, error)`：读 status/period（无锁读路径；无行→ErrNotFound）。
- 新增 `ChangeTenantPlanTx(ctx, tx, tenantID, planID, now) error`：一事务内
  ①`UPDATE tenant SET plan_id=$planID WHERE id=$tenantID`（改套餐指针；plan 不存在→FK 23503）；
  ②`UPDATE subscription SET current_period_start=$now, current_period_end=<按目标 plan.billing_period 算：price_cents=0→NULL；month→$now+1 月；year→$now+1 年>, updated_at=now() WHERE tenant_id=$tenantID`（重置周期）。
- 扩展 `TenantUsageOf`：**LEFT JOIN subscription** 取 status/period（既有直插租户无订阅行→NULL，COALESCE 兜默认），并从 `plan` 取 price_cents/currency/billing_period。`TenantUsage` 结构加对应字段。

## 6. RPC（proto additive）

### 6.1 `ChangeTenantPlan`
```proto
rpc ChangeTenantPlan(ChangeTenantPlanRequest) returns (ChangeTenantPlanResponse);
message ChangeTenantPlanRequest  { uint64 tenant_id = 1; uint64 plan_id = 2; }
message ChangeTenantPlanResponse { uint64 tenant_id = 1; uint64 plan_id = 2;
    string status = 3; string current_period_end = 4; }
```
- ruleTable 条目：`"…/ChangeTenantPlan": {"billing", "update", false, scopeSystem}`（AD-3；超管通配 grant 覆盖，无需 seed）。
- handler：写事务 `SELECT id FROM tenant WHERE id=$1 FOR UPDATE`（锁租户行，与 M6.1a CreateApplication 锁范式一致，防并发改套餐撕裂；无行→NotFound）→ `ChangeTenantPlanTx` → `adminauthz.InsertAdminAudit`（"change_plan"，diff 记 before/after plan_id，绝不含 secret）→ commit → 读回 subscription 组响应。
- 错误经 **M6-errsem `mapWriteErr`** 归一（plan 不存在→FailedPrecondition）。

### 6.2 `GetTenantUsageResponse` additive 字段
现有 `GetTenantUsageResponse` 占 tag 1-3（plan_name/applications/members），additive 字段续 tag 4 起：
```proto
int64  price_cents          = 4;
string currency             = 5;
string billing_period       = 6;
string subscription_status  = 7;
string current_period_end   = 8;   // RFC3339；空=无到期或无订阅行
```
`buf generate` 重生成；过 `make proto-breaking`（against main）+ proto-check 无 drift。handler 从扩展后的 `TenantUsageOf` 填这些字段。

## 7. 租户创建接线
`RegisterTenant` 事务内、tenant/owner/membership 之后加：
```sql
INSERT INTO subscription (tenant_id) VALUES ($tenantID);
```
与 tenant 同事务锁步——呼应 M1「membership⟺casbin 同事务」范式，杜绝无订阅的孤儿租户（新建租户恒有订阅；既有租户由 4.3 回填覆盖；测试直插租户无订阅→GetTenantUsage 经 LEFT JOIN 兜默认，不破）。

## 8. 不变量

- **INV-1 一租户一订阅**：`uq_subscription_tenant` 保证；RegisterTenant 建之、回填补既有、无删除路径。
- **INV-2 套餐真相源单一**：套餐指针仅在 `tenant.plan_id`（不变）；subscription 不重复 plan_id，无双真相源。
- **INV-3 配额行为不变**：读点零改（既有配额测试为回归网）。
- **INV-4 fail-close**：ChangeTenantPlan 非超管拒（PermissionDenied）、plan 不存在拒（FailedPrecondition）、租户不存在拒（NotFound），绝不静默改。
- **INV-5 零触碰授权核心**：casbin/adminauthz 求值/kernel/dataperm/authz.go 求值逻辑不动（ruleTable 加条目是接入面配置非求值核心；机器 diff 核）。
- **INV-6 additive 兼容**：proto 仅追加，过 proto-breaking 门。

## 9. 测试计划（TDD，测试须有齿）

1. **迁移**：`RunMigrationsFS` 幂等；回填后每 tenant 一订阅；plan 有定价列；down 对称。
2. **配额行为不变**：既有 M6.1a/d 全套配额测试仍绿（读点零改，天然回归网）。
3. **SubscriptionOf / ChangeTenantPlanTx**（store 层，dbtest）：改套餐后 tenant.plan_id 变、subscription 周期重置（month→+1 月 / free→NULL）；plan 不存在→错误（FK）。
4. **ChangeTenantPlan RPC**：超管改成功（回响应 plan/status/period）；非超管→PermissionDenied；不存在 plan→FailedPrecondition；不存在租户→NotFound；并发两次改同租户→FOR UPDATE 串行不撕裂。
5. **GetTenantUsage**：回显 price/currency/billing_period/status/period_end；改套餐后回显随之变；无订阅行的租户→status 空/默认不 panic。
6. **RegisterTenant**：新租户建订阅（status=active）；无订阅孤儿。
7. **零触碰 + 兼容**：机器 diff 核授权核心空；proto additive 过 proto-breaking + proto-check。
8. **变异证有齿**：撤 ChangeTenantPlan 超管门（ruleTable scope 改 scopeTenant）→ 非超管测试红；撤 ChangeTenantPlanTx 的周期重置 → period 测试红。

## 10. 落地顺序（供 writing-plans 细化）

1. 迁移 000023（plan 定价列 + subscription 表〔无 plan_id〕 + 回填 + down）。
2. dbtest 助手 + RegisterTenant：插 subscription 行（保证新/测试租户有订阅；配额读点零改故仅 GetTenantUsage/subscription 相关需要）。
3. store：`Subscription` 类型 + `SubscriptionOf` + `ChangeTenantPlanTx` + `TenantUsageOf` LEFT JOIN 扩字段。
4. proto：ChangeTenantPlan + GetTenantUsageResponse additive → `buf generate`。
5. handler：ChangeTenantPlan（scopeSystem + FOR UPDATE + audit + mapWriteErr）+ GetTenantUsage 回显 + ruleTable + apidoc 断言 + restgw 路由 + apidoc 派生。
6. 验证：go test ./...、proto-breaking、机器 diff 零触碰、变异实验。

## 11. 风险 / 权衡

- **R-1 subscription 不拥有 plan_id**（AD-2 折中）：未来发票需「订阅↔套餐↔价格」快照时，届时提升（加 plan_id/price 快照列或订阅明细表）。当前 YAGNI，配额/测试零 churn 换来低风险。
- **R-2 直插租户的测试无订阅行**：GetTenantUsage 用 LEFT JOIN + COALESCE 兜默认（status 空、period NULL），不破；配额路径不碰 subscription 故不受影响。
- **R-3 周期无自动推进**：本片 period 仅在改套餐时设定；自动续期属生命周期片。GetTenantUsage 回显的 period_end 可能已过期而未推进——已知边界（无支付/续期引擎），文档注明。
