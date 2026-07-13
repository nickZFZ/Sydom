# M6.1a 租户套餐 + 应用配额 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 立套餐+配额基座（plan 表数据驱动限额 + tenant.plan_id），在 `CreateApplication` 以 per-tenant 行锁并发安全强制应用配额（fail-close `ResourceExhausted`），REST 映 429。

**架构：** 迁移建 plan 表（seed free/pro）+ tenant.plan_id；store `quota.go` 锁租户行取限额+计数；`CreateApplication` tx 内配额门；restgw 补 ResourceExhausted→429。零触碰授权求值核心。

**技术栈：** golang-migrate、`database/sql` FOR UPDATE 行锁、testcontainers PG、gRPC `codes.ResourceExhausted`。

**BASE：** `feat/m6-1a-tenant-plans-app-quota` @ 含设计规格提交；规格 `docs/superpowers/specs/2026-07-13-sydom-m6-1a-tenant-plans-app-quota-design.md`。

**零触碰铁律：** `git diff 81a3d7f..HEAD -- casbin/ adminauthz/ internal/sidecar/kernel internal/sidecar/dataperm internal/sidecar/authz internal/auth` 必须为空。

---

## 任务 1：迁移 000021（plan 表 + seed + tenant.plan_id）

**文件：**
- 创建：`db/migrations/000021_plan_quota.up.sql`
- 创建：`db/migrations/000021_plan_quota.down.sql`

- [ ] **步骤 1：写 up**

`db/migrations/000021_plan_quota.up.sql`：
```sql
CREATE TABLE plan (
    id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name             VARCHAR(64) NOT NULL UNIQUE,
    max_applications INTEGER     NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO plan (name, max_applications) VALUES ('free', 3), ('pro', 50);
ALTER TABLE tenant ADD COLUMN plan_id BIGINT NOT NULL DEFAULT 1 REFERENCES plan(id);
```

- [ ] **步骤 2：写 down**

`db/migrations/000021_plan_quota.down.sql`：
```sql
ALTER TABLE tenant DROP COLUMN plan_id;
DROP TABLE plan;
```

- [ ] **步骤 3：迁移生效 + 往返验证（新增测试）**

新增 `internal/db/plan_migration_test.go`：
```go
package db

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration_PlanQuota(t *testing.T) {
	dsn := startPostgres(t)
	require.NoError(t, RunMigrations(dsn, migrationsSource))
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	// plan 表存在 + seed free/pro
	require.True(t, tableExists(t, db, "plan"))
	var freeMax, proMax int
	require.NoError(t, db.QueryRow(`SELECT max_applications FROM plan WHERE name='free'`).Scan(&freeMax))
	require.NoError(t, db.QueryRow(`SELECT max_applications FROM plan WHERE name='pro'`).Scan(&proMax))
	require.Equal(t, 3, freeMax)
	require.Equal(t, 50, proMax)

	// tenant.plan_id 存在且默认 free(id=1)
	var tid, planID int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant (name) VALUES ('t') RETURNING id, plan_id`).Scan(&tid, &planID))
	require.Equal(t, int64(1), planID, "新租户应默认 free(id=1)")

	// down 干净回滚
	require.NoError(t, MigrateDown(dsn, migrationsSource))
	require.False(t, tableExists(t, db, "plan"))
}
```

运行：`go test ./internal/db/ -run TestMigration_PlanQuota -v`
预期：PASS（plan+seed+tenant.plan_id 默认 free；down 删表）。

- [ ] **步骤 4：Commit**

```bash
git add db/migrations/000021_plan_quota.up.sql db/migrations/000021_plan_quota.down.sql internal/db/plan_migration_test.go
git commit -m "feat(db): M6.1a 迁移 000021 plan 表(数据驱动限额 max_applications)+seed free(3)/pro(50)+tenant.plan_id FK 默认 free 既有租户回填(up/down 往返测试)"
```

---

## 任务 2：store 配额助手（行锁取限额 + 计数）

**文件：**
- 创建：`internal/controlplane/store/quota.go`
- 创建：`internal/controlplane/store/quota_test.go`

- [ ] **步骤 1：写 quota.go**

`internal/controlplane/store/quota.go`：
```go
package store

import (
	"context"
	"database/sql"
	"errors"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
)

// PlanLimits 是租户套餐的资源上限（本增量仅 applications 维）。
type PlanLimits struct {
	MaxApplications int
}

// TenantPlanLimits 锁租户行（FOR UPDATE，序列化本租户资源创建）并返回其套餐限额。
// 须在调用方事务内调用（锁随 tx 生命周期）；租户不存在 → ErrNotFound。
func TenantPlanLimits(ctx context.Context, ex cp.DBTX, tenantID int64) (PlanLimits, error) {
	var pl PlanLimits
	err := ex.QueryRowContext(ctx,
		`SELECT p.max_applications
		   FROM tenant t JOIN plan p ON p.id = t.plan_id
		  WHERE t.id = $1 FOR UPDATE OF t`, tenantID).Scan(&pl.MaxApplications)
	if errors.Is(err, sql.ErrNoRows) {
		return PlanLimits{}, ErrNotFound
	}
	return pl, err
}

// CountApplications 返回租户当前应用数（同 tx 内，配合 TenantPlanLimits 的锁）。
func CountApplications(ctx context.Context, ex cp.DBTX, tenantID int64) (int, error) {
	var n int
	err := ex.QueryRowContext(ctx, `SELECT count(*) FROM application WHERE tenant_id=$1`, tenantID).Scan(&n)
	return n, err
}
```

- [ ] **步骤 2：写 quota_test.go（限额读 + 计数 + 并发行锁串行）**

`internal/controlplane/store/quota_test.go`：
```go
package store_test

import (
	"context"
	"database/sql"
	"sync"
	"testing"

	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestTenantPlanLimits_ReadAndNotFound(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db) // 建 acme 租户 + 一应用
	var tenantID int64
	require.NoError(t, db.QueryRow(`SELECT tenant_id FROM application WHERE id=$1`, appID).Scan(&tenantID))

	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	defer tx.Rollback()
	pl, err := store.TenantPlanLimits(context.Background(), tx, tenantID)
	require.NoError(t, err)
	require.Equal(t, 3, pl.MaxApplications, "默认 free 限 3")
	n, err := store.CountApplications(context.Background(), tx, tenantID)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	_, err = store.TenantPlanLimits(context.Background(), tx, 999999)
	require.ErrorIs(t, err, store.ErrNotFound)
}

// 并发 8 个 tx 各 TenantPlanLimits(FOR UPDATE)→+1 应用→commit：行锁串行，无交错超计。
func TestTenantPlanLimits_LockSerializes(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	var tenantID int64
	require.NoError(t, db.QueryRow(`SELECT tenant_id FROM application WHERE id=$1`, appID).Scan(&tenantID))
	// 抬高限额只测锁串行性（不测拒绝）
	_, err := db.Exec(`UPDATE plan SET max_applications=1000 WHERE id=(SELECT plan_id FROM tenant WHERE id=$1)`, tenantID)
	require.NoError(t, err)

	const N = 8
	var wg sync.WaitGroup
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tx, e := db.BeginTx(context.Background(), nil)
			if e != nil { errs <- e; return }
			if _, e = store.TenantPlanLimits(context.Background(), tx, tenantID); e != nil { tx.Rollback(); errs <- e; return }
			cnt, e := store.CountApplications(context.Background(), tx, tenantID)
			if e != nil { tx.Rollback(); errs <- e; return }
			_ = cnt
			if _, e = tx.Exec(
				`INSERT INTO application (tenant_id, domain, name, app_key, app_secret_enc)
				 VALUES ($1,$2,$3,$4,'\xab'::bytea)`,
				tenantID, "d", "n", fmtKey(i)); e != nil { tx.Rollback(); errs <- e; return }
			errs <- tx.Commit()
		}(i)
	}
	wg.Wait(); close(errs)
	for e := range errs { require.NoError(t, e) }
	var total int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM application WHERE tenant_id=$1`, tenantID).Scan(&total))
	require.Equal(t, 1+N, total, "1 seed + 8 并发插入，行锁串行无丢")
}

func fmtKey(i int) string { return "ak_" + string(rune('a'+i)) }
```

运行：`go test ./internal/controlplane/store/ -run 'TestTenantPlanLimits' -v`
预期：两测试 PASS（需 Docker）。

- [ ] **步骤 3：Commit**

```bash
git add internal/controlplane/store/quota.go internal/controlplane/store/quota_test.go
git commit -m "feat(cp): M6.1a store 配额助手 TenantPlanLimits(FOR UPDATE 锁租户行取套餐限额,unknown→ErrNotFound)+CountApplications;并发 8 tx 行锁串行测试证无交错"
```

---

## 任务 3：CreateApplication 配额门（fail-close + 有齿变异 + 并发）

**文件：**
- 修改：`internal/controlplane/mgmt/admin_ops.go`
- 创建：`internal/controlplane/mgmt/quota_test.go`

- [ ] **步骤 1：加配额门**

在 `internal/controlplane/mgmt/admin_ops.go` 的 `CreateApplication` 里，`defer tx.Rollback()` 之后、`INSERT INTO application` 之前插入：
```go
	limits, err := store.TenantPlanLimits(ctx, tx, int64(r.TenantId))
	if errors.Is(err, store.ErrNotFound) {
		return nil, status.Error(codes.InvalidArgument, "unknown tenant")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "quota: %v", err)
	}
	count, err := store.CountApplications(ctx, tx, int64(r.TenantId))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "quota: %v", err)
	}
	if count >= limits.MaxApplications {
		return nil, status.Errorf(codes.ResourceExhausted,
			"application quota reached (%d/%d); upgrade plan", count, limits.MaxApplications)
	}
```
（确认 `store`、`errors`、`status`、`codes` 已 import；`store` 应已在本文件用。若 `errors` 未 import 则加。）

- [ ] **步骤 2：写 quota_test.go**

`internal/controlplane/mgmt/quota_test.go`（复用本包既有测试脚手架建 AdminServer；参照本包其它 `*_test.go` 如何构造 `newAdminServer`/seed 租户）：
```go
package mgmt

import (
	"context"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// free 套餐限 3：建到第 3 个成功、第 4 个 ResourceExhausted（fail-close）。
func TestCreateApplication_QuotaFailClose(t *testing.T) {
	s, ctx, tenantID := newQuotaFixture(t) // 见下 helper
	for i := 0; i < 3; i++ {
		_, err := s.CreateApplication(ctx, &adminv1.CreateApplicationRequest{
			TenantId: uint64(tenantID), Domain: "d", Name: "n", AppKey: "ak_" + string(rune('a'+i))})
		require.NoErrorf(t, err, "第 %d 个应用应成功（free 限 3）", i+1)
	}
	_, err := s.CreateApplication(ctx, &adminv1.CreateApplicationRequest{
		TenantId: uint64(tenantID), Domain: "d", Name: "n", AppKey: "ak_over"})
	require.Equal(t, codes.ResourceExhausted, status.Code(err), "第 4 个应超配额 fail-close")
}

// 数据驱动：把 plan 限额 UPDATE 到 5，则第 4/5 个也成功。
func TestCreateApplication_QuotaDataDriven(t *testing.T) {
	s, ctx, tenantID := newQuotaFixture(t)
	_, err := s.db.Exec(`UPDATE plan SET max_applications=5 WHERE id=(SELECT plan_id FROM tenant WHERE id=$1)`, tenantID)
	require.NoError(t, err)
	for i := 0; i < 5; i++ {
		_, err := s.CreateApplication(ctx, &adminv1.CreateApplicationRequest{
			TenantId: uint64(tenantID), Domain: "d", Name: "n", AppKey: "ak_" + string(rune('a'+i))})
		require.NoErrorf(t, err, "限额提到 5 后第 %d 个应成功", i+1)
	}
}
```
`newQuotaFixture(t)` helper：用 `dbtest.SetupSchema` + 本包既有构造 `AdminServer` 的方式（查本包 `admin_ops_test.go` 的既有 setup 复用），建一个空租户（无应用），返回 `(*AdminServer, ctx, tenantID)`。ctx 须带 operator（`cp.OperatorFromContext` 审计用）——参照既有测试如何注入 operator 到 ctx。**实现者：先读本包既有 `*_test.go` 的 setup helper，复用之，勿另造。**

- [ ] **步骤 3：运行 + 有齿变异实验**

运行：`go test ./internal/controlplane/mgmt/ -run 'TestCreateApplication_Quota' -v` → 预期 PASS。
**变异**：临时注释掉步骤 1 的 `if count >= limits.MaxApplications { ... }` 块 → 跑 `TestCreateApplication_QuotaFailClose` → 预期 **FAIL**（第 4 个不再被拒）→ 还原 → PASS。证配额门有齿。

- [ ] **步骤 4：并发安全测试（8 并发 free(3) → 恰 3 成功）**

在 `quota_test.go` 加：
```go
func TestCreateApplication_QuotaConcurrent(t *testing.T) {
	s, ctx, tenantID := newQuotaFixture(t)
	const N = 8
	var wg sync.WaitGroup
	codesCh := make(chan codes.Code, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.CreateApplication(ctx, &adminv1.CreateApplicationRequest{
				TenantId: uint64(tenantID), Domain: "d", Name: "n", AppKey: "ak_" + string(rune('a'+i))})
			codesCh <- status.Code(err)
		}(i)
	}
	wg.Wait(); close(codesCh)
	var ok, exhausted int
	for c := range codesCh {
		switch c {
		case codes.OK: ok++
		case codes.ResourceExhausted: exhausted++
		}
	}
	require.Equal(t, 3, ok, "free(3) 下恰 3 成功")
	require.Equal(t, N-3, exhausted, "其余超配额")
	var total int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM application WHERE tenant_id=$1`, tenantID).Scan(&total))
	require.Equal(t, 3, total, "DB 应用数恰 3（行锁串行无超限）")
}
```
（import `sync`。）运行：`go test ./internal/controlplane/mgmt/ -run TestCreateApplication_QuotaConcurrent -race -v` → 预期 PASS（恰 3 成功、DB 恰 3）。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/mgmt/admin_ops.go internal/controlplane/mgmt/quota_test.go
git commit -m "feat(cp): M6.1a CreateApplication 应用配额门(tx 内 TenantPlanLimits 行锁+计数,达 max_applications fail-close ResourceExhausted;变异撤门证有齿;8 并发 free(3)→恰 3 成功 DB 恰 3 行锁串行无超限;数据驱动 UPDATE plan 生效)"
```

---

## 任务 4：REST ResourceExhausted → 429 映射 + 验收

**文件：**
- 修改：`internal/controlplane/restgw/errors.go`

- [ ] **步骤 1：加映射**

在 `internal/controlplane/restgw/errors.go` 的 `httpStatusForCode` switch 里，`case codes.Unavailable:` 之前加：
```go
	case codes.ResourceExhausted:
		return http.StatusTooManyRequests
```
并在同文件 `codeName`（snake_case 映射）里补 `ResourceExhausted → "resource_exhausted"`（若该函数用 switch/map，加一条；确保对外错误体 code 字段正确）。

- [ ] **步骤 2：验证映射（单测）**

若本包已有 `errors_test.go` 测 `httpStatusForCode`，加一条 `ResourceExhausted→429`；否则新增最小测试：
```go
func TestHTTPStatusForCode_ResourceExhausted(t *testing.T) {
	if got := httpStatusForCode(codes.ResourceExhausted); got != http.StatusTooManyRequests {
		t.Fatalf("ResourceExhausted 应映 429，实测 %d", got)
	}
}
```
（`package restgw`，import codes/http/testing。）运行：`go test ./internal/controlplane/restgw/ -run ResourceExhausted -v` → PASS。

- [ ] **步骤 3：最终验收**

运行：
```bash
go build ./... && go test ./... 2>&1 | grep -E "FAIL|panic" | head; echo "GO-EXIT=${PIPESTATUS[1]}"
git diff 81a3d7f..HEAD -- casbin/ adminauthz/ internal/sidecar/kernel internal/sidecar/dataperm internal/sidecar/authz internal/auth | head; echo "ZERO-TOUCH-DONE(空)"
```
预期：`go test ./...` 无 FAIL（GO-EXIT=0）；零触碰 diff 空。

- [ ] **步骤 4：Commit**

```bash
git add internal/controlplane/restgw/errors.go internal/controlplane/restgw/errors_test.go 2>/dev/null || git add internal/controlplane/restgw/errors.go
git commit -m "feat(cp): M6.1a REST 网关 ResourceExhausted→429(配额超限对外 429 Too Many Requests,三面 parity;+codeName resource_exhausted)"
```

---

## 自检

**1. 规格覆盖度：** §4.1 文件→任务1(迁移)+任务2(store)+任务3(handler)+任务4(restgw)；§4.2 迁移→任务1；§4.3 store→任务2；§4.4 配额门→任务3；§5 验证→各任务测试（含并发/变异/往返/零触碰）；§6 M61A-1..7→M61A-1 任务4步3、M61A-2 任务1、M61A-3 任务3步1/3、M61A-4 任务3步4、M61A-5 任务2、M61A-6 任务3步2(DataDriven)、M61A-7 任务4步3。全覆盖。

**2. 占位符扫描：** 迁移/store/handler/测试为实代码+命令+预期。任务3 步2 的 `newQuotaFixture` 明确要求实现者**复用本包既有测试 setup**（读 `admin_ops_test.go`）——非占位而是必要的复用指令。变异实验给确切改法+还原。

**3. 类型一致性：** `store.PlanLimits{MaxApplications int}`、`store.TenantPlanLimits(ctx, cp.DBTX, int64)(PlanLimits,error)`、`store.CountApplications(ctx, cp.DBTX, int64)(int,error)`、`store.ErrNotFound`（既有）任务2 定义、任务3 一致调用；`adminv1.CreateApplicationRequest{TenantId uint64,Domain,Name,AppKey}`、`AdminServer.CreateApplication`、`AdminServer.db` 与实查一致；`codes.ResourceExhausted`/`status.Code` 标准；`httpStatusForCode`（restgw 既有）任务4 改。
