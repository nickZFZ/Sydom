# M6.1d 成员配额 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 把数据驱动配额从"应用"扩到"成员"（per-tenant），端到端交付一个完整维度：强制（InviteMember 行锁门）+ 计量（GetTenantUsage 加 members）+ 可见（用量页加成员行），并顺带修 Console `ResourceExhausted→429` latent bug。

**架构：** 复用 M6.1a/b/c 三层范式。迁移加 `plan.max_members`；`store` 扩 `PlanLimits.MaxMembers`/`CountMembers`/`TenantUsage` 成员维；`InviteMember` 插 Order-B 配额门（insert-then-gate，`!inserted` 短路 AlreadyExists）；proto additive `members=3`；用量页重构为资源行列表。零触碰授权核心。

**技术栈：** Go、Postgres（`FOR UPDATE` 行锁）、golang-migrate、buf（proto）、html/template、testify、dbtest。

规格：`docs/superpowers/specs/2026-07-13-sydom-m6-1d-member-quota-design.md`

---

## 文件结构

- **创建** `db/migrations/000022_plan_max_members.up.sql` / `.down.sql` — plan 加 max_members 列 + 种子
- **修改** `internal/db/plan_migration_test.go` — 断言 max_members free=3/pro=25
- **修改** `internal/controlplane/store/quota.go` — `PlanLimits.MaxMembers`、`CountMembers`、`TenantUsage` 成员维
- **修改** `internal/controlplane/store/quota_test.go` — 成员维断言
- **修改** `internal/controlplane/mgmt/accounts.go` — `InviteMember` Order-B 配额门
- **创建** `internal/controlplane/mgmt/member_quota_test.go` — 成员门 fail-close + Order-B 正确性 + 并发
- **修改** `internal/controlplane/mgmt/account_pagination_test.go:229` — 抬高 free 成员限（解耦分页测试与配额）
- **修改** `api/proto/sydom/admin/v1/admin.proto` — `GetTenantUsageResponse` 加 `members=3`（+ `make proto-gen` 重生成 `gen/`）
- **修改** `internal/controlplane/mgmt/tenant_usage.go` — handler 填 `Members`
- **修改** `internal/controlplane/mgmt/tenant_usage_test.go` — 断言 members
- **修改** `internal/controlplane/console/errors.go` — `ResourceExhausted→429`
- **创建** `internal/controlplane/console/errors_test.go` — httpStatusForCode 表测
- **修改** `internal/controlplane/console/routes_usage.go` — view model 资源行列表
- **修改** `internal/controlplane/console/templates/usage.html` — `range .Rows`
- **修改** `internal/controlplane/console/routes_usage_test.go` — 两行断言 + 新告警文案

---

### 任务 1：迁移 000022 plan.max_members

**文件：**
- 创建：`db/migrations/000022_plan_max_members.up.sql`、`db/migrations/000022_plan_max_members.down.sql`
- 测试：`internal/db/plan_migration_test.go`

- [ ] **步骤 1：写失败的测试**

在 `plan_migration_test.go` 的 `TestMigration_PlanQuota` 里，`proMax` 断言之后（第 23 行 `require.Equal(t, 50, proMax)` 后）插入：

```go
	// M6.1d：max_members 列 + 种子 free=3/pro=25
	var freeMem, proMem int
	require.NoError(t, db.QueryRow(`SELECT max_members FROM plan WHERE name='free'`).Scan(&freeMem))
	require.NoError(t, db.QueryRow(`SELECT max_members FROM plan WHERE name='pro'`).Scan(&proMem))
	require.Equal(t, 3, freeMem)
	require.Equal(t, 25, proMem)
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/db/ -run TestMigration_PlanQuota -v`
预期：FAIL，`column "max_members" does not exist`

- [ ] **步骤 3：写迁移**

`db/migrations/000022_plan_max_members.up.sql`：

```sql
-- M6.1d 成员配额：plan 加 max_members（per-tenant 成员上限，数据驱动）。
-- DEFAULT 0 使 NOT NULL 列可加到既有行 → UPDATE 设真值 → DROP DEFAULT 对齐 max_applications（无默认）。
ALTER TABLE plan ADD COLUMN max_members INTEGER NOT NULL DEFAULT 0;
UPDATE plan SET max_members = 3  WHERE name = 'free';
UPDATE plan SET max_members = 25 WHERE name = 'pro';
ALTER TABLE plan ALTER COLUMN max_members DROP DEFAULT;
```

`db/migrations/000022_plan_max_members.down.sql`：

```sql
ALTER TABLE plan DROP COLUMN max_members;
```

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/db/ -run TestMigration_PlanQuota -v`
预期：PASS（up 种子正确 + 全链 down 回滚 plan 表消失）

- [ ] **步骤 5：Commit**

```bash
git add db/migrations/000022_plan_max_members.up.sql db/migrations/000022_plan_max_members.down.sql internal/db/plan_migration_test.go
git commit -m "feat(db): M6.1d 迁移 000022 plan.max_members(数据驱动成员上限 free3/pro25;DEFAULT 0 加列→UPDATE→DROP DEFAULT;up/down 往返测试)"
```

---

### 任务 2：store 成员维（PlanLimits.MaxMembers + CountMembers + TenantUsage）

**文件：**
- 修改：`internal/controlplane/store/quota.go`
- 测试：`internal/controlplane/store/quota_test.go`

- [ ] **步骤 1：写失败的测试**

在 `quota_test.go` 的 `TestTenantPlanLimits_ReadAndNotFound` 里，`require.Equal(t, 3, pl.MaxApplications, ...)` 之后加：

```go
	require.Equal(t, 3, pl.MaxMembers, "默认 free 成员限 3")
	m, err := store.CountMembers(context.Background(), tx, tenantID)
	require.NoError(t, err)
	require.Equal(t, 0, m, "SeedApp 租户无 membership")
```

在 `TestTenantUsageOf` 里，`require.Equal(t, 1, u.UsedApplications, ...)` 之后加：

```go
	require.Equal(t, 3, u.MaxMembers)
	require.Equal(t, 0, u.UsedMembers, "SeedApp 租户无 membership")
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/store/ -run 'TestTenantPlanLimits_ReadAndNotFound|TestTenantUsageOf' -v`
预期：编译失败（`pl.MaxMembers`/`store.CountMembers`/`u.MaxMembers`/`u.UsedMembers` 未定义）

- [ ] **步骤 3：实现**

`quota.go`：`PlanLimits` 加字段、`TenantPlanLimits` 一并查、加 `CountMembers`、`TenantUsage` 加字段、`TenantUsageOf` 加两列。改后关键片段：

```go
// PlanLimits 是租户套餐的资源上限。
type PlanLimits struct {
	MaxApplications int
	MaxMembers      int
}

func TenantPlanLimits(ctx context.Context, ex cp.DBTX, tenantID int64) (PlanLimits, error) {
	var pl PlanLimits
	err := ex.QueryRowContext(ctx,
		`SELECT p.max_applications, p.max_members
		   FROM tenant t JOIN plan p ON p.id = t.plan_id
		  WHERE t.id = $1 FOR UPDATE OF t`, tenantID).Scan(&pl.MaxApplications, &pl.MaxMembers)
	if errors.Is(err, sql.ErrNoRows) {
		return PlanLimits{}, ErrNotFound
	}
	return pl, err
}

// CountMembers 返回租户当前成员数（同 tx 内，配合 TenantPlanLimits 的锁）。
func CountMembers(ctx context.Context, ex cp.DBTX, tenantID int64) (int, error) {
	var n int
	err := ex.QueryRowContext(ctx, `SELECT count(*) FROM tenant_membership WHERE tenant_id=$1`, tenantID).Scan(&n)
	return n, err
}

// TenantUsage 是租户的套餐名 + 各资源用量/上限。
type TenantUsage struct {
	PlanName         string
	MaxApplications  int
	UsedApplications int
	MaxMembers       int
	UsedMembers      int
}

func TenantUsageOf(ctx context.Context, ex cp.DBTX, tenantID int64) (TenantUsage, error) {
	var u TenantUsage
	err := ex.QueryRowContext(ctx,
		`SELECT p.name, p.max_applications,
		        (SELECT count(*) FROM application a WHERE a.tenant_id = t.id),
		        p.max_members,
		        (SELECT count(*) FROM tenant_membership tm WHERE tm.tenant_id = t.id)
		   FROM tenant t JOIN plan p ON p.id = t.plan_id WHERE t.id = $1`,
		tenantID).Scan(&u.PlanName, &u.MaxApplications, &u.UsedApplications, &u.MaxMembers, &u.UsedMembers)
	if errors.Is(err, sql.ErrNoRows) {
		return TenantUsage{}, ErrNotFound
	}
	return u, err
}
```

（`CountApplications`、`PlanLimits` 注释、`import` 不变——`errors`/`sql`/`cp` 已在文件内。）

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/controlplane/store/ -run 'TestTenantPlanLimits_ReadAndNotFound|TestTenantUsageOf|TestTenantPlanLimits_LockSerializes' -v`
预期：全 PASS（既有锁串行测试不受影响）

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/store/quota.go internal/controlplane/store/quota_test.go
git commit -m "feat(cp): M6.1d store 成员维(PlanLimits.MaxMembers 同锁查+CountMembers+TenantUsage 成员用量;TenantUsageOf 加子查询 count tenant_membership)"
```

---

### 任务 3：InviteMember 成员配额门（Order-B）+ 修分页测试回归

**文件：**
- 修改：`internal/controlplane/mgmt/accounts.go`（InviteMember + imports）
- 创建：`internal/controlplane/mgmt/member_quota_test.go`
- 修改：`internal/controlplane/mgmt/account_pagination_test.go:229`（抬高成员限）

- [ ] **步骤 1：写失败的测试**

创建 `internal/controlplane/mgmt/member_quota_test.go`：

```go
package mgmt_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/controlplane/outbox"
	"github.com/nickZFZ/Sydom/internal/controlplane/policy"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// memberSrv 建 schema + 一个租户（owner 已是 1 个成员），返回 server / tenantID。
func memberSrv(t *testing.T) (*mgmt.AdminServer, uint64) {
	t.Helper()
	db := dbtest.SetupSchema(t)
	srv := mgmt.NewAdminServer(db, policy.NewPolicyManager(db, outbox.NewSink()), mk())
	reg, err := srv.RegisterTenant(context.Background(),
		&adminv1.RegisterTenantRequest{TenantName: "mq", OwnerPrincipal: "mqowner"})
	require.NoError(t, err)
	return srv, reg.TenantId
}

// free 成员限 3：owner(1) + 邀请 2 成功、第 3 个邀请 ResourceExhausted（fail-close）。
func TestInviteMember_QuotaFailClose(t *testing.T) {
	srv, tid := memberSrv(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ { // owner + 2 = 3 = free 限
		_, err := srv.InviteMember(ctx, &adminv1.InviteMemberRequest{
			TenantId: tid, Principal: fmt.Sprintf("m%d", i)})
		require.NoErrorf(t, err, "第 %d 个邀请应成功（free 成员限 3，含 owner）", i+1)
	}
	_, err := srv.InviteMember(ctx, &adminv1.InviteMemberRequest{TenantId: tid, Principal: "overflow"})
	require.Equal(t, codes.ResourceExhausted, status.Code(err), "满员后新邀请应 ResourceExhausted")
}

// Order-B 正确性：满员时重复邀请【已有】成员 → AlreadyExists（非 ResourceExhausted）。
func TestInviteMember_AtLimitReinviteExisting(t *testing.T) {
	srv, tid := memberSrv(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		_, err := srv.InviteMember(ctx, &adminv1.InviteMemberRequest{
			TenantId: tid, Principal: fmt.Sprintf("m%d", i)})
		require.NoError(t, err)
	}
	// 现已满员（owner + m0 + m1 = 3）。重复邀请已有成员 m0：
	_, err := srv.InviteMember(ctx, &adminv1.InviteMemberRequest{TenantId: tid, Principal: "m0"})
	require.Equal(t, codes.AlreadyExists, status.Code(err),
		"满员时重复邀请已有成员应 AlreadyExists（!inserted 短路先于配额门）")
}

// 8 并发邀请于 free(3) 租户（owner 已占 1）：行锁串行 → 恰 2 成功、其余 ResourceExhausted、DB 恰 3 成员。
func TestInviteMember_QuotaConcurrent(t *testing.T) {
	srv, tid := memberSrv(t)
	const N = 8
	var wg sync.WaitGroup
	codesCh := make(chan codes.Code, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := srv.InviteMember(context.Background(),
				&adminv1.InviteMemberRequest{TenantId: tid, Principal: fmt.Sprintf("c%d", i)})
			codesCh <- status.Code(err)
		}(i)
	}
	wg.Wait()
	close(codesCh)
	var ok, exhausted int
	for c := range codesCh {
		switch c {
		case codes.OK:
			ok++
		case codes.ResourceExhausted:
			exhausted++
		}
	}
	require.Equal(t, 2, ok, "owner 占 1，free(3) 下恰 2 邀请成功")
	require.Equal(t, N-2, exhausted, "其余超配额 ResourceExhausted")
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/mgmt/ -run TestInviteMember_Quota -v`
预期：FAIL（当前无配额门 → 全部邀请成功，`ResourceExhausted` 断言失败）

- [ ] **步骤 3：加配额门 + imports**

`accounts.go` import 块加 `"errors"` 与 `"github.com/nickZFZ/Sydom/internal/controlplane/store"`。

`InviteMember` 里 `defer tx.Rollback()` 之后、`opID, created, err := adminauthz.EnsureOperator(...)` 之前插入锁：

```go
	// 成员配额门（M6.1d）：早锁租户行取成员限额（FOR UPDATE 串行并发邀请）。unknown 租户→InvalidArgument。
	limits, err := store.TenantPlanLimits(ctx, tx, int64(r.TenantId))
	if errors.Is(err, store.ErrNotFound) {
		return nil, status.Error(codes.InvalidArgument, "unknown tenant")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "quota: %v", err)
	}
```

然后在 `inserted, err := adminauthz.InsertMembership(...)` **之前**取 pre-insert 计数：

```go
	memberCount, err := store.CountMembers(ctx, tx, int64(r.TenantId)) // 锁内 pre-insert 计数
	if err != nil {
		return nil, status.Errorf(codes.Internal, "quota: %v", err)
	}
```

并在既有 `if !inserted { return AlreadyExists }` 块**之后**加配额判定（Order-B：新成员才计费）：

```go
	if !inserted {
		return nil, status.Error(codes.AlreadyExists, "already a member")
	}
	if memberCount >= limits.MaxMembers { // 新成员超限 → ResourceExhausted（defer Rollback 撤销 insert）
		return nil, status.Errorf(codes.ResourceExhausted,
			"member quota reached (%d/%d); upgrade plan", memberCount, limits.MaxMembers)
	}
```

- [ ] **步骤 4：修分页测试回归**

`account_pagination_test.go` 的 `TestListMembers_PageTierFilterTenantScopeTotal` 需 4 成员/租户，超 free 限 3。在 `ctx := context.Background()`（第 221 行）之后加抬高成员限（该测试测分页非配额，解耦）：

```go
	// 该测试需每租户 4 成员，抬高 free 成员限（解耦分页测试与 M6.1d 成员配额）
	_, err = db.Exec(`UPDATE plan SET max_members=1000 WHERE name='free'`)
	require.NoError(t, err)
```

注：此处 `err` 变量在后续 `rA, err := ...` 首次 `:=` 声明；因本行在其之前，需改用 `var err error` 预声明或调整。**具体做法**：把该行放在两个 `RegisterTenant` 之后（第 227 行 `require.NoError(t, err)` 之后，第 229 行循环之前），此时 `err` 已声明，用 `_, err =`：

```go
	// 抬高 free 成员限（该测试需租户 A 达 4 成员，测分页非配额）
	_, err = db.Exec(`UPDATE plan SET max_members=1000 WHERE name='free'`)
	require.NoError(t, err)
```

- [ ] **步骤 5：运行验证通过**

运行：`go test ./internal/controlplane/mgmt/ -run 'TestInviteMember|TestListMembers_PageTierFilterTenantScopeTotal|TestListMyTenants_InMemoryPageQSort' -v`
预期：全 PASS（新成员门 3 测试 + 既有 InviteMember/分页测试不回归）

- [ ] **步骤 6：Commit**

```bash
git add internal/controlplane/mgmt/accounts.go internal/controlplane/mgmt/member_quota_test.go internal/controlplane/mgmt/account_pagination_test.go
git commit -m "feat(cp): M6.1d InviteMember 成员配额门(Order-B insert-then-gate,!inserted 短路 AlreadyExists 避满员重复邀请误判;FOR UPDATE 行锁串行并发;8 并发 owner+2 恰成;变异撤门证有齿;修分页测试抬限解耦)"
```

---

### 任务 4：proto members=3 + 重生成 + handler

**文件：**
- 修改：`api/proto/sydom/admin/v1/admin.proto:210-213`
- 重生成：`gen/`（`make proto-gen`）
- 修改：`internal/controlplane/mgmt/tenant_usage.go`
- 测试：`internal/controlplane/mgmt/tenant_usage_test.go`

- [ ] **步骤 1：写失败的测试**

在 `tenant_usage_test.go` 的 `TestGetTenantUsage` 里，`require.Equal(t, uint32(3), resp.Applications.Limit)`（第 30 行）之后加：

```go
	require.Equal(t, uint32(0), resp.Members.Used, "SeedApp 租户无 membership")
	require.Equal(t, uint32(3), resp.Members.Limit, "free 成员限 3")
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/mgmt/ -run TestGetTenantUsage -v`
预期：编译失败（`resp.Members` 未定义——proto 未加字段）

- [ ] **步骤 3：改 proto + 重生成**

`admin.proto` 的 `GetTenantUsageResponse` 加 `members = 3`：

```proto
message GetTenantUsageResponse {
  string plan_name = 1;
  ResourceUsage applications = 2;
  ResourceUsage members = 3;
}
```

运行：`make proto-gen`
预期：`gen/sydom/admin/v1/*.pb.go` 重生成，含 `Members` 字段。

- [ ] **步骤 4：改 handler**

`tenant_usage.go` 的 return 加 `Members`：

```go
	return &adminv1.GetTenantUsageResponse{
		PlanName:     u.PlanName,
		Applications: &adminv1.ResourceUsage{Used: uint32(u.UsedApplications), Limit: uint32(u.MaxApplications)},
		Members:      &adminv1.ResourceUsage{Used: uint32(u.UsedMembers), Limit: uint32(u.MaxMembers)},
	}, nil
```

- [ ] **步骤 5：验证通过 + 兼容门**

运行：`go test ./internal/controlplane/mgmt/ -run TestGetTenantUsage -v`
预期：PASS（members 0/3）

运行：`make proto-breaking`
预期：PASS（additive `members=3` 不破坏向后兼容）

运行：`make proto-check`
预期：PASS（gen/ 与 proto 同步无 drift）

- [ ] **步骤 6：Commit**

```bash
git add api/proto/sydom/admin/v1/admin.proto gen/ internal/controlplane/mgmt/tenant_usage.go internal/controlplane/mgmt/tenant_usage_test.go
git commit -m "feat(cp): M6.1d GetTenantUsage 加 additive members=3(handler 填成员用量/上限;buf generate 同步;additive 过 proto-breaking;handler 测断言 members 0/3)"
```

---

### 任务 5：Console errors.go ResourceExhausted→429（修 latent bug）

**文件：**
- 修改：`internal/controlplane/console/errors.go:14-33`
- 创建：`internal/controlplane/console/errors_test.go`

- [ ] **步骤 1：写失败的测试**

创建 `internal/controlplane/console/errors_test.go`：

```go
package console

import (
	"net/http"
	"testing"

	"google.golang.org/grpc/codes"
)

// httpStatusForCode 须把 ResourceExhausted 映射为 429（配额超限对外语义），
// 而非落 default 500 把配额文案吞成「internal error」。挡 M6.1a 遗漏的 latent bug 回归。
func TestHTTPStatusForCode_ResourceExhausted(t *testing.T) {
	cases := map[codes.Code]int{
		codes.OK:                http.StatusOK,
		codes.PermissionDenied:  http.StatusForbidden,
		codes.NotFound:          http.StatusNotFound,
		codes.ResourceExhausted: http.StatusTooManyRequests, // 429（M6.1d）
		codes.Internal:          http.StatusInternalServerError,
	}
	for c, want := range cases {
		if got := httpStatusForCode(c); got != want {
			t.Errorf("httpStatusForCode(%v)=%d，want %d", c, got, want)
		}
	}
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/console/ -run TestHTTPStatusForCode_ResourceExhausted -v`
预期：FAIL，`httpStatusForCode(ResourceExhausted)=500, want 429`

- [ ] **步骤 3：加分支**

`errors.go` 的 `httpStatusForCode` switch 里，`case codes.Unavailable:` 之前加：

```go
	case codes.ResourceExhausted:
		return http.StatusTooManyRequests
```

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/controlplane/console/ -run TestHTTPStatusForCode_ResourceExhausted -v`
预期：PASS

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/console/errors.go internal/controlplane/console/errors_test.go
git commit -m "fix(cp): M6.1d Console errors ResourceExhausted→429(修 M6.1a 遗漏:配额超限经 Console 曾落 default 500 吞掉配额文案;应用+成员两维同惠;表测挡回归)"
```

---

### 任务 6：Console 用量页重构为资源行列表（应用 + 成员）

**文件：**
- 修改：`internal/controlplane/console/routes_usage.go`
- 修改：`internal/controlplane/console/templates/usage.html`
- 测试：`internal/controlplane/console/routes_usage_test.go`

- [ ] **步骤 1：改测试（先改断言到多行 + 新告警文案）**

`routes_usage_test.go` 的 `TestConsole_UsagePage` 断言体替换为：

```go
	require.Equal(t, 1, strings.Count(body, "<h1>"), "须单 h1")
	require.Contains(t, body, "免费版")
	require.Contains(t, body, "应用：1 / 3") // 应用行（used=1 limit=3）
	require.Contains(t, body, "成员：0 / 3") // 成员行（SeedAppInTenant 无 membership）
	require.Contains(t, body, `value="1"`)   // 应用 meter
	require.Contains(t, body, `max="3"`)     // 上限
	require.NotContains(t, body, "应用已达上限") // 未达上限：不含告警（双向有齿）
```

`TestConsole_UsagePage_AtLimitWarning` 断言体替换为：

```go
	require.Contains(t, body, "应用：3 / 3")
	require.Contains(t, body, `value="3"`)
	require.Contains(t, body, "应用已达上限")     // 应用至上限告警
	require.NotContains(t, body, "成员已达上限")   // 成员 0/3 未达上限（跨维度双向有齿）
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/console/ -run TestConsole_UsagePage -v`
预期：FAIL（当前模板单一 Used/Limit，无「成员：」行、无「应用：」前缀、告警文案为旧「已达应用上限」）

- [ ] **步骤 3：改 handler view model**

`routes_usage.go`：在文件内（`registerUsage` 之前）加包级类型：

```go
// usageRow 是用量页一行资源（应用/成员/…），为多配额维度可扩展。
type usageRow struct {
	Name      string
	Used      int
	Limit     int
	AtLimit   bool
	ShowMeter bool
}

func makeUsageRow(name string, ru *adminv1.ResourceUsage) usageRow {
	used, limit := 0, 0
	if ru != nil {
		used = int(ru.Used)
		limit = int(ru.Limit)
	}
	return usageRow{Name: name, Used: used, Limit: limit, AtLimit: used >= limit, ShowMeter: limit > 0}
}
```

把 `usage` handler 里从 `used, limit := 0, 0` 到 `renderPage(...)` 的整段替换为：

```go
	rows := []usageRow{
		makeUsageRow("应用", resp.Applications),
		makeUsageRow("成员", resp.Members),
	}
	h.renderPage(w, r, "usage.html", http.StatusOK, map[string]any{
		"Nav":       "tenants",
		"TenantID":  tid,
		"PlanLabel": planLabel(resp.PlanName),
		"Rows":      rows,
	})
```

- [ ] **步骤 4：改模板**

`usage.html` 的 `<section>...</section>`（含 meter 与告警）整段替换为 `range .Rows`：

```html
{{range .Rows}}
<section aria-label="{{.Name}}配额">
  <p>{{.Name}}：{{.Used}} / {{.Limit}}</p>
  {{if .ShowMeter}}<meter class="usage-meter" min="0" max="{{.Limit}}" value="{{.Used}}">{{.Used}} / {{.Limit}}</meter>{{end}}
  {{if .AtLimit}}<div class="alert alert-error">{{.Name}}已达上限。升级套餐或删除后可再新增。</div>{{end}}
</section>
{{end}}
```

改后完整 `usage.html`：

```html
{{define "title"}}用量 · 司域 Console{{end}}
{{define "content"}}
<nav class="breadcrumb" aria-label="面包屑">租户 · 用量</nav>
<h1>用量</h1>
<p class="hint">当前租户 {{.TenantID}}</p>
<p>套餐：<span class="badge">{{.PlanLabel}}</span></p>
{{range .Rows}}
<section aria-label="{{.Name}}配额">
  <p>{{.Name}}：{{.Used}} / {{.Limit}}</p>
  {{if .ShowMeter}}<meter class="usage-meter" min="0" max="{{.Limit}}" value="{{.Used}}">{{.Used}} / {{.Limit}}</meter>{{end}}
  {{if .AtLimit}}<div class="alert alert-error">{{.Name}}已达上限。升级套餐或删除后可再新增。</div>{{end}}
</section>
{{end}}
{{end}}
```

- [ ] **步骤 5：运行验证通过**

运行：`go test ./internal/controlplane/console/ -run 'TestConsole_UsagePage|TestPageSweep|TestTemplates_NoInlineStyle' -v`
预期：全 PASS（两行渲染 + 至上限跨维度双向 + 横扫单 h1/breadcrumb + 无内联 style）

- [ ] **步骤 6：Commit**

```bash
git add internal/controlplane/console/routes_usage.go internal/controlplane/console/templates/usage.html internal/controlplane/console/routes_usage_test.go
git commit -m "feat(cp): M6.1d 用量页重构为资源行列表(应用+成员两行,range .Rows 为多维度可扩展;每行 meter+至上限行内告警;跨维度双向有齿)"
```

---

### 任务 7：全量验证 + 零触碰核验

**文件：** 无（仅验证）

- [ ] **步骤 1：proto 兼容 + 无 drift**

运行：`make proto-breaking && make proto-check`
预期：均 PASS。

- [ ] **步骤 2：全量测试**

运行：`go test ./...`
预期：EXIT 0（全绿）。

- [ ] **步骤 3：零触碰授权核心核验**

运行：
```bash
git diff --name-only 2a5a9c4..HEAD | grep -E 'casbin/|internal/kernel/|internal/sidecar/|adminauthz/(enforcer|role_manager|dataperm)|mgmt/authz\.go' && echo "!!! TOUCHED authz core" || echo "EMPTY ✓ 零触碰授权核心"
```
（`2a5a9c4` 为 M6.1b 起点前一提交基线；本片起点为 M6.1c 之后 `a192f63`——用 `a192f63..HEAD` 更精确。）
预期：EMPTY ✓（`adminauthz/membership.go`「InsertMembership」不属求值核心，且本片未改它；仅 `accounts.go` 加只读门）。

运行：
```bash
git diff --name-only a192f63..HEAD -- 'internal/controlplane/mgmt/authz.go' | cat
```
预期：空（ruleTable 未改——InviteMember 授权条目不变，配额门是只读 DB 查询非授权判定）。

- [ ] **步骤 4：无 commit（本任务仅验证）**

若前序任务已各自 commit，本任务无新增文件。若发现问题，回到对应任务修复。

---

## 验收对照（M61D-1..8）

| # | 验收项 | 覆盖任务 |
|---|---|---|
| 1 | 零触碰授权核心（机器 diff 空） | 任务 7 步骤 3 |
| 2 | 迁移 000022 up/down + free/pro max_members 3/25 | 任务 1 |
| 3 | store MaxMembers/CountMembers/UsedMembers 有齿 | 任务 2 |
| 4 | InviteMember 成员门 fail-close + Order-B 正确性 + 并发 | 任务 3（`member_quota_test.go` 3 测试） |
| 5 | proto additive members=3 过 proto-breaking + handler 填 | 任务 4 |
| 6 | Console errors ResourceExhausted→429（表测挡回归） | 任务 5 |
| 7 | 用量页两行（应用+成员）+ 跨维度至上限双向有齿 | 任务 6 |
| 8 | proto-check 无 drift + `go test ./...` EXIT 0 | 任务 4 步骤 5 + 任务 7 |

## 自检

**1. 规格覆盖度：** 规格全部章节均有任务——迁移(任务1)、store(任务2)、InviteMember 门 Order-B(任务3)、proto+handler(任务4)、Console 429 修(任务5)、用量页行列表(任务6)、验证(任务7)。回归风险（分页测试）已在任务3步骤4显式处理。无遗漏。

**2. 占位符扫描：** 无 TODO/待定；每个代码步骤含完整可编译代码。

**3. 类型一致性：** `PlanLimits.MaxMembers`/`CountMembers`/`TenantUsage.{MaxMembers,UsedMembers}`（任务2 定义）在任务3(门)、任务4(handler) 一致使用；proto `Members` 字段（任务4）在 handler + console(任务6 `resp.Members`) + 测试一致；`usageRow`/`makeUsageRow`（任务6）字段名与模板 `.Name/.Used/.Limit/.AtLimit/.ShowMeter` 一致；`TenantPlanLimits` 签名不变（仅 Scan 多一列），既有 `CreateApplication` 调用方零改（忽略新增 `.MaxMembers`）。
