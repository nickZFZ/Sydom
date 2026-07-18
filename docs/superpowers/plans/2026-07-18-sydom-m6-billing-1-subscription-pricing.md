# M6-billing-1 订阅+定价模型主干 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 给司域补齐供应商无关的计费地基第一片——`plan` 定价 + 每租户一条 `subscription` 生命周期实体 + 平台超管 `ChangeTenantPlan` + `GetTenantUsage` 回显定价。

**架构：** additive 模型——`subscription` 只存生命周期（status/period，**不含 plan_id**），套餐真相源仍是 `tenant.plan_id`（配额读点零改）。ChangeTenantPlan 改 `tenant.plan_id` + 重置 subscription 周期。零触碰授权决策核心。

**技术栈：** Go 1.26、PostgreSQL（lib/pq）、golang-migrate iofs、protobuf/buf、gRPC、testcontainers。

规格：`docs/superpowers/specs/2026-07-18-sydom-m6-billing-1-subscription-pricing-design.md`

---

## 文件结构

| 文件 | 职责 | 动作 |
|---|---|---|
| `db/migrations/000023_subscription.up.sql` | plan 定价列 + subscription 表 + 回填 | 创建 |
| `db/migrations/000023_subscription.down.sql` | 对称回滚 | 创建 |
| `internal/db/subscription_migration_test.go` | 迁移幂等 + 回填 + 定价列断言 | 创建 |
| `internal/dbtest/dbtest.go` | SeedApp/SeedAppInTenant 插 subscription 行 | 修改 |
| `internal/controlplane/mgmt/accounts.go` | RegisterTenant 事务内插 subscription | 修改 |
| `internal/controlplane/store/subscription.go` | Subscription 类型 + SubscriptionOf + ChangeTenantPlanTx | 创建 |
| `internal/controlplane/store/quota.go` | TenantUsage 加定价/订阅字段 + TenantUsageOf LEFT JOIN | 修改 |
| `internal/controlplane/store/subscription_test.go` | store 层 TDD | 创建 |
| `api/proto/sydom/admin/v1/admin.proto` | ChangeTenantPlan + GetTenantUsageResponse additive | 修改 |
| `gen/sydom/admin/v1/*.pb.go` | buf generate 产物 | 生成 |
| `internal/controlplane/mgmt/authz.go` | ruleTable 加 ChangeTenantPlan 条目 | 修改 |
| `internal/controlplane/mgmt/billing.go` | ChangeTenantPlan handler | 创建 |
| `internal/controlplane/mgmt/tenant_usage.go` | GetTenantUsage 回显定价字段 | 修改 |
| `internal/controlplane/mgmt/billing_test.go` | handler TDD（超管/非超管/plan 不存在/并发） | 创建 |
| `internal/controlplane/restgw/routes_accounts.go` | ChangeTenantPlan REST 路由 | 修改 |
| `internal/controlplane/mgmt/apidoc_test.go` | RPC 计数断言 +1 | 修改 |

---

## 任务 1：迁移 000023（plan 定价 + subscription 表 + 回填）

**文件：**
- 创建：`db/migrations/000023_subscription.up.sql`、`db/migrations/000023_subscription.down.sql`
- 测试：`internal/db/subscription_migration_test.go`

- [ ] **步骤 1：编写失败的测试**

`internal/db/subscription_migration_test.go`（模型参照既有 `internal/db/plan_migration_test.go`；`setupDB` 见 `internal/db/helpers_test.go`）：
```go
package db_test

import (
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestMigration000023_PlanPricingAndSubscription(t *testing.T) {
	db := dbtest.SetupSchema(t) // 跑全部迁移含 000023

	// plan 有定价列且 free/pro 保持 0（AD-4 不 seed 价格）
	var price int64
	var period, currency string
	require.NoError(t, db.QueryRow(
		`SELECT price_cents, billing_period, currency FROM plan WHERE name='free'`).
		Scan(&price, &period, &currency))
	require.Equal(t, int64(0), price)
	require.Equal(t, "month", period)
	require.Equal(t, "CNY", currency)

	// billing_period CHECK 拒非法值
	_, err := db.Exec(`INSERT INTO plan (name, max_applications, max_members, billing_period)
		VALUES ('bad', 1, 1, 'weekly')`)
	require.Error(t, err, "billing_period CHECK 应拒 'weekly'")

	// 回填：插一个 tenant（迁移后）不自动得订阅——回填只覆盖迁移时存在的行。
	// 故验证 subscription 表存在 + 唯一约束：同租户插两次订阅→冲突。
	var tid int64
	require.NoError(t, db.QueryRow(
		`INSERT INTO tenant (name) VALUES ('mig-t') RETURNING id`).Scan(&tid))
	_, err = db.Exec(`INSERT INTO subscription (tenant_id) VALUES ($1)`, tid)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO subscription (tenant_id) VALUES ($1)`, tid)
	require.Error(t, err, "uq_subscription_tenant 应拒同租户第二条订阅")

	// status CHECK 拒非法值
	_, err = db.Exec(`UPDATE subscription SET status='bogus' WHERE tenant_id=$1`, tid)
	require.Error(t, err, "ck_subscription_status 应拒非法 status")
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/db/ -run TestMigration000023 -count=1`
预期：FAIL——`price_cents` 列不存在 / `subscription` 表不存在。

- [ ] **步骤 3：编写迁移**

`db/migrations/000023_subscription.up.sql`：
```sql
-- plan 定价列（M6-billing-1）。DEFAULT 使既有 free/pro 行平滑加列；不 seed 具体价格（运营后置）。
ALTER TABLE plan ADD COLUMN price_cents    BIGINT      NOT NULL DEFAULT 0;
ALTER TABLE plan ADD COLUMN billing_period VARCHAR(16) NOT NULL DEFAULT 'month'
    CONSTRAINT ck_plan_billing_period CHECK (billing_period IN ('month','year'));
ALTER TABLE plan ADD COLUMN currency       CHAR(3)     NOT NULL DEFAULT 'CNY';

-- subscription 生命周期实体（不含 plan_id：套餐真相源保持 tenant.plan_id）。
CREATE TABLE subscription (
    id                    BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id             BIGINT      NOT NULL REFERENCES tenant(id),
    status                VARCHAR(16) NOT NULL DEFAULT 'active',
    current_period_start  TIMESTAMPTZ NOT NULL DEFAULT now(),
    current_period_end    TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_subscription_tenant UNIQUE (tenant_id),
    CONSTRAINT ck_subscription_status CHECK (status IN ('active','trialing','past_due','canceled'))
);

-- 回填：迁移时存在的每个 tenant 建一条 active 订阅。
INSERT INTO subscription (tenant_id) SELECT id FROM tenant;
```

`db/migrations/000023_subscription.down.sql`：
```sql
DROP TABLE IF EXISTS subscription;
ALTER TABLE plan DROP COLUMN IF EXISTS currency;
ALTER TABLE plan DROP COLUMN IF EXISTS billing_period;
ALTER TABLE plan DROP COLUMN IF EXISTS price_cents;
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/db/ -run TestMigration000023 -count=1`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add db/migrations/000023_subscription.up.sql db/migrations/000023_subscription.down.sql internal/db/subscription_migration_test.go
git commit -m "feat(db): 迁移 000023 plan 定价列 + subscription 生命周期表（M6-billing-1）"
```

---

## 任务 2：dbtest 助手 + RegisterTenant 插 subscription

保证「新建租户 + 测试夹具租户」都有订阅行（配额读点零改故不受影响；仅 GetTenantUsage/订阅相关路径需要）。

**文件：**
- 修改：`internal/dbtest/dbtest.go`（SeedApp、SeedAppInTenant）
- 修改：`internal/controlplane/mgmt/accounts.go`（RegisterTenant）
- 测试：复用现有 `internal/controlplane/mgmt/accounts_test.go`（新增用例）

- [ ] **步骤 1：编写失败的测试**

`internal/controlplane/mgmt/accounts_test.go` 追加：
```go
func TestRegisterTenant_CreatesSubscription(t *testing.T) {
	db := dbtest.SetupSchema(t)
	srv := accountsSrv(db)
	resp, err := srv.RegisterTenant(context.Background(), &adminv1.RegisterTenantRequest{
		TenantName: "sub-tenant", OwnerPrincipal: "owner@sub",
	})
	require.NoError(t, err)

	var status string
	require.NoError(t, db.QueryRow(
		`SELECT status FROM subscription WHERE tenant_id=$1`, int64(resp.TenantId)).Scan(&status))
	require.Equal(t, "active", status)
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/controlplane/mgmt/ -run TestRegisterTenant_CreatesSubscription -count=1`
预期：FAIL——subscription 查询无行（RegisterTenant 尚未建订阅）。

- [ ] **步骤 3：实现**

`internal/controlplane/mgmt/accounts.go`：在 `RegisterTenant` 的 `BindTenantAdminTx` 之后、`BumpPolicyVersion` 之前插入订阅（同事务）：
```go
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO subscription (tenant_id) VALUES ($1)`, tenantID); err != nil {
		return nil, status.Errorf(codes.Internal, "create subscription: %v", err)
	}
```

`internal/dbtest/dbtest.go`：`SeedApp` 与 `SeedAppInTenant` 在 tenant INSERT 之后、application INSERT 之前，各加一行订阅插入。SeedApp：
```go
	require.NoError(t, conn.QueryRow(
		`INSERT INTO tenant (name) VALUES ('acme') RETURNING id`).Scan(&tenantID))
	_, err := conn.Exec(`INSERT INTO subscription (tenant_id) VALUES ($1)`, tenantID)
	require.NoError(t, err)
```
SeedAppInTenant 同理（tenantID 变量已在），在其 tenant INSERT 后加：
```go
	_, err := conn.Exec(`INSERT INTO subscription (tenant_id) VALUES ($1)`, tenantID)
	require.NoError(t, err)
```
（注意：SeedAppInTenant 现有代码用 `:=` 声明 appID，加 `err` 前用 `var err error` 或调整赋值避免重复声明——实现时按编译器提示处理。）

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/controlplane/mgmt/ -run TestRegisterTenant_CreatesSubscription -count=1`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/dbtest/dbtest.go internal/controlplane/mgmt/accounts.go internal/controlplane/mgmt/accounts_test.go
git commit -m "feat(mgmt): RegisterTenant + dbtest 助手建 subscription 行（M6-billing-1）"
```

---

## 任务 3：store 层 —— Subscription + ChangeTenantPlanTx + TenantUsageOf 扩展

**文件：**
- 创建：`internal/controlplane/store/subscription.go`
- 修改：`internal/controlplane/store/quota.go`（TenantUsage 结构 + TenantUsageOf 查询）
- 测试：`internal/controlplane/store/subscription_test.go`

- [ ] **步骤 1：编写失败的测试**

`internal/controlplane/store/subscription_test.go`（store 外部测试包；参照 `store` 现有测试用 `dbtest`）：
```go
package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestChangeTenantPlanTx_UpdatesPlanAndPeriod(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db) // 建 tenant(含订阅)+app
	_ = appID
	var tid int64
	require.NoError(t, db.QueryRow(`SELECT id FROM tenant WHERE name='acme'`).Scan(&tid))
	// 设 pro 有价格与年周期，验证 period_end 计算
	_, err := db.Exec(`UPDATE plan SET price_cents=9900, billing_period='year' WHERE name='pro'`)
	require.NoError(t, err)
	var proID int64
	require.NoError(t, db.QueryRow(`SELECT id FROM plan WHERE name='pro'`).Scan(&proID))

	now := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	require.NoError(t, store.ChangeTenantPlanTx(context.Background(), tx, tid, proID, now))
	require.NoError(t, tx.Commit())

	// tenant.plan_id 变为 pro
	var planID int64
	require.NoError(t, db.QueryRow(`SELECT plan_id FROM tenant WHERE id=$1`, tid).Scan(&planID))
	require.Equal(t, proID, planID)

	// subscription 周期重置：pro 年付 → period_end = now + 1 年
	sub, err := store.SubscriptionOf(context.Background(), db, tid)
	require.NoError(t, err)
	require.Equal(t, "active", sub.Status)
	require.True(t, sub.CurrentPeriodEnd.Valid)
	require.Equal(t, now.AddDate(1, 0, 0), sub.CurrentPeriodEnd.Time.UTC())
}

func TestTenantUsageOf_EchoesPricing(t *testing.T) {
	db := dbtest.SetupSchema(t)
	dbtest.SeedApp(t, db)
	var tid int64
	require.NoError(t, db.QueryRow(`SELECT id FROM tenant WHERE name='acme'`).Scan(&tid))
	_, err := db.Exec(`UPDATE plan SET price_cents=0, billing_period='month', currency='CNY' WHERE name='free'`)
	require.NoError(t, err)

	u, err := store.TenantUsageOf(context.Background(), db, tid)
	require.NoError(t, err)
	require.Equal(t, "free", u.PlanName)
	require.Equal(t, int64(0), u.PriceCents)
	require.Equal(t, "CNY", u.Currency)
	require.Equal(t, "month", u.BillingPeriod)
	require.Equal(t, "active", u.SubStatus) // SeedApp 建了订阅
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/controlplane/store/ -run 'TestChangeTenantPlanTx|TestTenantUsageOf_EchoesPricing' -count=1`
预期：FAIL——`store.ChangeTenantPlanTx` / `store.SubscriptionOf` 未定义、`TenantUsage` 无 `PriceCents` 字段。

- [ ] **步骤 3：实现**

`internal/controlplane/store/subscription.go`：
```go
package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
)

// Subscription 是租户的订阅生命周期状态（不含 plan_id：套餐真相源在 tenant.plan_id）。
type Subscription struct {
	TenantID           int64
	Status             string
	CurrentPeriodStart time.Time
	CurrentPeriodEnd   sql.NullTime
}

// SubscriptionOf 读租户订阅（无锁读路径）；无行→ErrNotFound。
func SubscriptionOf(ctx context.Context, ex cp.DBTX, tenantID int64) (Subscription, error) {
	var s Subscription
	err := ex.QueryRowContext(ctx,
		`SELECT tenant_id, status, current_period_start, current_period_end
		   FROM subscription WHERE tenant_id=$1`, tenantID).
		Scan(&s.TenantID, &s.Status, &s.CurrentPeriodStart, &s.CurrentPeriodEnd)
	if errors.Is(err, sql.ErrNoRows) {
		return Subscription{}, ErrNotFound
	}
	return s, err
}

// ChangeTenantPlanTx 在调用方事务内改租户套餐指针 + 重置订阅周期。
// tenant 不存在→ErrNotFound；plan 不存在→FK 违约(pq 23503)由调用方映射。
// period_end：目标 plan price_cents=0→NULL（无到期）；否则 month→+1 月、year→+1 年。
func ChangeTenantPlanTx(ctx context.Context, tx cp.DBTX, tenantID, planID int64, now time.Time) error {
	res, err := tx.ExecContext(ctx,
		`UPDATE tenant SET plan_id=$1, updated_at=now() WHERE id=$2`, planID, tenantID)
	if err != nil {
		return err // plan 不存在→FK 23503
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	var period string
	var price int64
	if err := tx.QueryRowContext(ctx,
		`SELECT billing_period, price_cents FROM plan WHERE id=$1`, planID).Scan(&period, &price); err != nil {
		return err
	}
	var end sql.NullTime
	if price > 0 {
		if period == "year" {
			end = sql.NullTime{Time: now.AddDate(1, 0, 0), Valid: true}
		} else {
			end = sql.NullTime{Time: now.AddDate(0, 1, 0), Valid: true}
		}
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE subscription SET current_period_start=$1, current_period_end=$2, updated_at=now()
		  WHERE tenant_id=$3`, now, end, tenantID)
	return err
}
```

`internal/controlplane/store/quota.go`：`TenantUsage` 结构加字段并改 `TenantUsageOf` 查询：
```go
type TenantUsage struct {
	PlanName         string
	MaxApplications  int
	UsedApplications int
	MaxMembers       int
	UsedMembers      int
	PriceCents       int64        // M6-billing-1
	Currency         string       // M6-billing-1
	BillingPeriod    string       // M6-billing-1
	SubStatus        string       // 订阅状态（无订阅行→""）
	CurrentPeriodEnd sql.NullTime // 无到期/无订阅→NULL
}
```
`TenantUsageOf` 查询改为（LEFT JOIN subscription，定价来自 plan）：
```go
	err := ex.QueryRowContext(ctx,
		`SELECT p.name, p.max_applications,
		        (SELECT count(*) FROM application a WHERE a.tenant_id = t.id),
		        p.max_members,
		        (SELECT count(*) FROM tenant_membership tm WHERE tm.tenant_id = t.id),
		        p.price_cents, p.currency, p.billing_period,
		        COALESCE(s.status, ''), s.current_period_end
		   FROM tenant t
		   JOIN plan p ON p.id = t.plan_id
		   LEFT JOIN subscription s ON s.tenant_id = t.id
		  WHERE t.id = $1`,
		tenantID).Scan(&u.PlanName, &u.MaxApplications, &u.UsedApplications,
		&u.MaxMembers, &u.UsedMembers,
		&u.PriceCents, &u.Currency, &u.BillingPeriod, &u.SubStatus, &u.CurrentPeriodEnd)
```
（`quota.go` 顶部 import 加 `"time"` 若 `sql.NullTime` 需要——`database/sql` 已在 import，`sql.NullTime` 无需 time import。确认编译。）

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/controlplane/store/ -run 'TestChangeTenantPlanTx|TestTenantUsageOf_EchoesPricing' -count=1`
预期：PASS。再跑全 store 包确认无回归：`go test ./internal/controlplane/store/ -count=1`。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/store/subscription.go internal/controlplane/store/quota.go internal/controlplane/store/subscription_test.go
git commit -m "feat(store): Subscription + ChangeTenantPlanTx + TenantUsageOf 回显定价（M6-billing-1）"
```

---

## 任务 4：proto —— ChangeTenantPlan + GetTenantUsageResponse additive

**文件：**
- 修改：`api/proto/sydom/admin/v1/admin.proto`
- 生成：`gen/sydom/admin/v1/*.pb.go`（`make proto-gen`）

- [ ] **步骤 1：改 proto**

`api/proto/sydom/admin/v1/admin.proto`：
1. `GetTenantUsageResponse`（现 tag 1-3）追加 tag 4-8：
```proto
message GetTenantUsageResponse {
  string plan_name = 1;
  ResourceUsage applications = 2;
  ResourceUsage members = 3;
  int64  price_cents         = 4;
  string currency            = 5;
  string billing_period      = 6;
  string subscription_status = 7;
  string current_period_end  = 8; // RFC3339；空=无到期或无订阅
}
```
2. `AdminService` 加 RPC（与 GetTenantUsage 相邻）：
```proto
  rpc ChangeTenantPlan(ChangeTenantPlanRequest) returns (ChangeTenantPlanResponse);
```
3. 加消息（与 GetTenantUsage 消息相邻）：
```proto
message ChangeTenantPlanRequest  { uint64 tenant_id = 1; uint64 plan_id = 2; }
message ChangeTenantPlanResponse {
  uint64 tenant_id = 1;
  uint64 plan_id = 2;
  string status = 3;
  string current_period_end = 4; // RFC3339；空=无到期
}
```

- [ ] **步骤 2：生成 + 校验兼容**

运行：`make proto-gen && make proto-check`
预期：`buf generate` 无错、`git diff --exit-code`（proto-check 内）无 drift（生成物已提交）。
运行：`make proto-breaking`
预期：PASS（纯 additive，无破坏性变更）。

- [ ] **步骤 3：编译确认生成物可用**

运行：`go build ./gen/... ./internal/...`
预期：EXIT 0（`adminv1.ChangeTenantPlanRequest` 等类型可用）。

- [ ] **步骤 4：Commit**

```bash
git add api/proto/sydom/admin/v1/admin.proto gen/sydom/admin/v1/
git commit -m "feat(proto): ChangeTenantPlan RPC + GetTenantUsageResponse 定价字段 additive（M6-billing-1）"
```

---

## 任务 5：handler —— ChangeTenantPlan + ruleTable + GetTenantUsage 回显 + REST + apidoc

**文件：**
- 修改：`internal/controlplane/mgmt/authz.go`（ruleTable）
- 创建：`internal/controlplane/mgmt/billing.go`（ChangeTenantPlan handler）
- 修改：`internal/controlplane/mgmt/tenant_usage.go`（GetTenantUsage 回显）
- 修改：`internal/controlplane/restgw/routes_accounts.go`（REST 路由）
- 测试：`internal/controlplane/mgmt/billing_test.go`
- 修改：`internal/controlplane/mgmt/apidoc_test.go`（RPC 计数 +1）

- [ ] **步骤 1：编写失败的测试**

`internal/controlplane/mgmt/billing_test.go`（内部包 `mgmt`，直调 handler；参照 `business_role_test.go` 的 `accountsSrv` 与租户/超管播种）：
```go
package mgmt_test

import (
	"bytes"
	"context"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/crypto"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestChangeTenantPlan_SuperAdminChanges(t *testing.T) {
	db := dbtest.SetupSchema(t)
	tid := dbtest.SeedApp(t, db) // 复用：SeedApp 返回 appID，这里另取 tenant id
	_ = tid
	var tenantID, proID int64
	require.NoError(t, db.QueryRow(`SELECT id FROM tenant WHERE name='acme'`).Scan(&tenantID))
	require.NoError(t, db.QueryRow(`SELECT id FROM plan WHERE name='pro'`).Scan(&proID))

	srv := accountsSrv(db)
	// 直调 handler：授权由拦截器完成，单测 handler 用超管 ctx（OperatorFromContext）。
	ctx := cp.WithOperator(context.Background(), "root")
	resp, err := srv.ChangeTenantPlan(ctx, &adminv1.ChangeTenantPlanRequest{
		TenantId: uint64(tenantID), PlanId: uint64(proID),
	})
	require.NoError(t, err)
	require.Equal(t, uint64(proID), resp.PlanId)

	var got int64
	require.NoError(t, db.QueryRow(`SELECT plan_id FROM tenant WHERE id=$1`, tenantID).Scan(&got))
	require.Equal(t, proID, got)
}

func TestChangeTenantPlan_UnknownPlan_FailedPrecondition(t *testing.T) {
	db := dbtest.SetupSchema(t)
	dbtest.SeedApp(t, db)
	var tenantID int64
	require.NoError(t, db.QueryRow(`SELECT id FROM tenant WHERE name='acme'`).Scan(&tenantID))
	srv := accountsSrv(db)
	ctx := cp.WithOperator(context.Background(), "root")
	_, err := srv.ChangeTenantPlan(ctx, &adminv1.ChangeTenantPlanRequest{
		TenantId: uint64(tenantID), PlanId: 999999,
	})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}

func TestChangeTenantPlan_UnknownTenant_NotFound(t *testing.T) {
	db := dbtest.SetupSchema(t)
	srv := accountsSrv(db)
	var proID int64
	require.NoError(t, db.QueryRow(`SELECT id FROM plan WHERE name='pro'`).Scan(&proID))
	ctx := cp.WithOperator(context.Background(), "root")
	_, err := srv.ChangeTenantPlan(ctx, &adminv1.ChangeTenantPlanRequest{
		TenantId: 999999, PlanId: uint64(proID),
	})
	require.Equal(t, codes.NotFound, status.Code(err))
}

// 授权门：非超管经 AuthorizeRule 应 PermissionDenied（scopeSystem）。
func TestChangeTenantPlan_NonSuperAdmin_Denied(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	mk := bytes.Repeat([]byte{0x11}, crypto.KeySize)
	tA, _ := dbtest.SeedAppInTenant(t, db, "tenant-a", "domain-a", "AK_a")
	require.NoError(t, adminauthz.EnsureTenantAdmin(ctx, db, mk, tA, "alice", []byte("sa")))
	enf, err := adminauthz.NewEnforcer(db)
	require.NoError(t, err)
	const method = "/sydom.admin.v1.AdminService/ChangeTenantPlan"
	req := &adminv1.ChangeTenantPlanRequest{TenantId: uint64(tA), PlanId: 2}
	_, err = mgmt.AuthorizeRule(ctx, enf, method, "alice", req)
	require.Equal(t, codes.PermissionDenied, status.Code(err), "租户管理员非超管，改套餐须拒")
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/controlplane/mgmt/ -run TestChangeTenantPlan -count=1`
预期：FAIL——`srv.ChangeTenantPlan` 未定义、ruleTable 无该 method（AuthorizeRule 找不到规则）。

- [ ] **步骤 3：实现**

`internal/controlplane/mgmt/authz.go`：ruleTable 加条目（与 GetTenantUsage 相邻）：
```go
	"/sydom.admin.v1.AdminService/ChangeTenantPlan":          {"billing", "update", false, scopeSystem},
```

`internal/controlplane/mgmt/billing.go`（新建；模型参照 `SetApplicationStatus`）：
```go
package mgmt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ChangeTenantPlan 变更租户套餐（仅平台超管，ruleTable scopeSystem）。
// 改 tenant.plan_id（套餐真相源）+ 重置 subscription 周期，单事务锁租户行防并发撕裂。
func (s *AdminServer) ChangeTenantPlan(ctx context.Context, r *adminv1.ChangeTenantPlanRequest) (*adminv1.ChangeTenantPlanResponse, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin: %v", err)
	}
	defer tx.Rollback()

	var before int64
	if err := tx.QueryRowContext(ctx,
		`SELECT plan_id FROM tenant WHERE id=$1 FOR UPDATE`, int64(r.TenantId)).Scan(&before); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "unknown tenant")
		}
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := store.ChangeTenantPlanTx(ctx, tx, int64(r.TenantId), int64(r.PlanId), time.Now()); err != nil {
		if isForeignKeyViolation(err) {
			return nil, status.Error(codes.FailedPrecondition, "unknown plan")
		}
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := adminauthz.InsertAdminAudit(ctx, tx,
		sql.NullInt64{Int64: int64(r.TenantId), Valid: true}, cp.OperatorFromContext(ctx),
		"change_plan", "tenant", fmt.Sprintf("%d", r.TenantId),
		auditJSON(map[string]any{"before": before, "after": r.PlanId}),
		sql.NullInt64{}); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "commit: %v", err)
	}
	sub, err := store.SubscriptionOf(ctx, s.db, int64(r.TenantId))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read subscription: %v", err)
	}
	resp := &adminv1.ChangeTenantPlanResponse{
		TenantId: r.TenantId, PlanId: r.PlanId, Status: sub.Status,
	}
	if sub.CurrentPeriodEnd.Valid {
		resp.CurrentPeriodEnd = sub.CurrentPeriodEnd.Time.Format(time.RFC3339)
	}
	return resp, nil
}
```
（`isForeignKeyViolation` 已存在于 `admin_ops.go`，同包可用。）

`internal/controlplane/mgmt/tenant_usage.go`：`GetTenantUsage` 返回值加定价字段：
```go
	resp := &adminv1.GetTenantUsageResponse{
		PlanName:           u.PlanName,
		Applications:       &adminv1.ResourceUsage{Used: uint32(u.UsedApplications), Limit: uint32(u.MaxApplications)},
		Members:            &adminv1.ResourceUsage{Used: uint32(u.UsedMembers), Limit: uint32(u.MaxMembers)},
		PriceCents:         u.PriceCents,
		Currency:           u.Currency,
		BillingPeriod:      u.BillingPeriod,
		SubscriptionStatus: u.SubStatus,
	}
	if u.CurrentPeriodEnd.Valid {
		resp.CurrentPeriodEnd = u.CurrentPeriodEnd.Time.Format(time.RFC3339)
	}
	return resp, nil
```
（`tenant_usage.go` import 加 `"time"`。）

`internal/controlplane/restgw/routes_accounts.go`：`accountRoutes()` 返回的 `[]route` 加一条（REST parity）：
```go
		{"POST", "/v1/tenants/{tenant_id}/plan", pfx + "ChangeTenantPlan",
			func(r *http.Request, body []byte) (proto.Message, error) {
				m := &adminv1.ChangeTenantPlanRequest{}
				if err := decodeBody(body, m); err != nil {
					return nil, err
				}
				id, err := pathUint64(r, "tenant_id")
				if err != nil {
					return nil, err
				}
				m.TenantId = id
				return m, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.ChangeTenantPlan(ctx, m.(*adminv1.ChangeTenantPlanRequest))
			}},
```

`internal/controlplane/mgmt/apidoc_test.go`：把 RPC 总数断言 +1（找断言 `len(docs)` / 期望计数的行，加 1）。实现时运行测试看实际期望值报错再改准确数字。

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/controlplane/mgmt/ -run TestChangeTenantPlan -count=1`
预期：PASS（超管改成功、非超管 PermissionDenied、plan 不存在 FailedPrecondition、租户不存在 NotFound）。
运行：`go test ./internal/controlplane/mgmt/ ./internal/controlplane/restgw/ -count=1`
预期：PASS（apidoc 计数已更新、REST 路由派生正确）。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/mgmt/authz.go internal/controlplane/mgmt/billing.go internal/controlplane/mgmt/tenant_usage.go internal/controlplane/mgmt/billing_test.go internal/controlplane/mgmt/apidoc_test.go internal/controlplane/restgw/routes_accounts.go
git commit -m "feat(mgmt): ChangeTenantPlan RPC（超管）+ GetTenantUsage 回显定价 + REST 路由（M6-billing-1）"
```

---

## 任务 6：全局验证 + 变异 + 零触碰核验

- [ ] **步骤 1：全仓测试 + 兼容门**

运行：
```bash
go build ./... && go vet ./...
go test ./... -count=1
make proto-breaking
```
预期：build/vet EXIT 0；`go test ./...` 全绿（尤其既有 M6.1a/d 配额测试——读点零改的行为不变回归网）；proto-breaking PASS。

- [ ] **步骤 2：零触碰授权核心核验**

运行：
```bash
git diff --name-only main -- ':!docs' | grep -iE 'casbin/|adminauthz/enforcer|/kernel/|dataperm|/authz.go$' || echo "零触碰授权核心"
```
预期：输出「零触碰授权核心」（ruleTable 在 authz.go 但仅加一行配置条目——若 authz.go 出现在列表，人工核 diff 确认仅新增 ruleTable 条目、无求值逻辑改动）。

- [ ] **步骤 3：变异实验证有齿（两处）**

变异 A（撤超管门）：临时把 ruleTable 的 ChangeTenantPlan 条目 `scopeSystem` 改 `scopeTenant`：
运行：`go test ./internal/controlplane/mgmt/ -run TestChangeTenantPlan_NonSuperAdmin_Denied -count=1`
预期：FAIL（租户管理员在自己租户域通过 → 不再 PermissionDenied）。**还原**。

变异 B（撤周期重置）：临时注释 `ChangeTenantPlanTx` 末尾的 subscription UPDATE：
运行：`go test ./internal/controlplane/store/ -run TestChangeTenantPlanTx -count=1`
预期：FAIL（period_end 不再等于 now+1 年）。**还原**。

- [ ] **步骤 4：最终 Commit（如变异还原产生改动，确认工作树干净）**

```bash
git status --short   # 应干净（变异均已还原）
```

---

## 自检记录

- **规格覆盖**：§4 数据模型→任务 1；§5 store→任务 3；§6 RPC→任务 4+5；§7 RegisterTenant→任务 2；§8 不变量→各任务测试 + 任务 6；§9 测试计划→逐任务 TDD + 任务 6 变异；§10 顺序→任务 1-6。
- **占位符**：无 TODO/待定；apidoc 计数在任务 5 步骤 3 明确「运行看报错再填准确数字」（计数依当前 RPC 总数，实现期确定）。
- **类型一致性**：`TenantUsage` 新字段（PriceCents/Currency/BillingPeriod/SubStatus/CurrentPeriodEnd）在任务 3 定义、任务 5 handler 消费，命名一致；`Subscription`/`ChangeTenantPlanTx`/`SubscriptionOf` 签名跨任务一致。
