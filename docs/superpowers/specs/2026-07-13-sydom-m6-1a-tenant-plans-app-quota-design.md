# M6.1a 租户套餐 + 应用配额（fail-close 强制）— 设计规格

> M6「商业化」子项目 M6.1（用量计量 + 配额）的第一增量。BASE=main `81a3d7f`。用户选定方向=用量计量+配额（套餐/额度数据驱动可配、无需先定 billing 供应商）。本增量立**套餐+配额基座**并在**应用创建**维度强制，为后续计量可见性 + 更多维度铺路。

## 1. 背景与目标

司域是多租户 SaaS，租户可自助建应用/角色/策略（M1）。GA 商业化需**套餐分层 + 资源配额**：不同套餐限不同资源量，超额 fail-close 拒绝（呼应「一致性/fail-close 优先」文化）。当前**无套餐、无配额**——任何租户可无限建应用。

**目标（本增量 M6.1a）**：
1. **套餐（plan）数据模型**：套餐定义各资源上限；租户绑定套餐（默认 free）。**限额数据驱动**（DB 表，可改，不硬编码产品决策）。
2. **应用配额强制**：`CreateApplication` 前查该租户应用数 < 套餐 `max_applications`，**并发安全**（per-tenant 行锁串行化，杜绝并发绕过），达上限 fail-close `ResourceExhausted`。
3. **零触碰授权求值核心**：配额是管理写路径的新门（mgmt handler + store + migration），**不碰** casbin/adminauthz/kernel/dataperm/authz 决策逻辑。

**非目标（本增量外，明确留后续 M6.1b/c）**：
- **计量可见性 RPC**（`GetTenantUsage` 返套餐+用量，三面 parity）：紧接的下一增量（proto regen 已验证可行）。
- **角色/数据策略/成员维度配额**：plan 表可预留列，但本增量只强制 applications 一维（机制立住，逐维扩展）。
- **用量事件计量**（Check/API 请求量的高频聚合计费）：需计量管道，远期增量。
- **套餐管理 RPC / Console 套餐 UI / 升级流程 / 计费集成**：待用户产品方向（billing 供应商等）。
- 免费额度具体数值是**可配置默认**（我定合理起点，用户后调），非产品硬决策。

## 2. 现状（实查）

- `tenant`（`000001`）：id/name/status/created_at/updated_at，无 plan。下一 migration = `000021`。
- `application`：`tenant_id` FK，`CreateApplication`（`mgmt/admin_ops.go:37`）在 tx 内 `INSERT INTO application(tenant_id,...)` + admin 审计 + commit；`r.TenantId` 来自请求。
- `AdminServer{db *sql.DB, mgr *policy.PolicyManager, masterKey []byte}`。
- store 用 `cp.DBTX`（`*sql.Tx` 满足）；M5.4a 已确立 `SELECT ... FOR UPDATE` 行锁串行化并发写模式（`LockAppVersion`）——配额并发安全复用同思路（锁 tenant 行）。
- gRPC `codes.ResourceExhausted` → REST 网关映射（restgw 有 grpc code→HTTP 映射，ResourceExhausted→429）；三面写路径共用 `CreateApplication` 单一 handler。

## 3. 方案

**A（选定）plan 表 + tenant.plan_id + CreateApplication 内 per-tenant 行锁 + 计数 + fail-close。**
- `plan` 表（数据驱动限额，seed free/pro）；`tenant.plan_id` FK 默认 free。
- `CreateApplication` 事务内：`SELECT plan_id ... FROM tenant WHERE id=$1 FOR UPDATE`（锁租户行，序列化本租户建应用）→ 查 plan `max_applications` → `COUNT(*) application WHERE tenant_id=$1` → 若 `count >= max` 回滚返 `ResourceExhausted`；否则原有 INSERT。锁 + 计数 + 插入同一 tx → 并发建应用严格串行，无绕过。
- **优点**：机制最小、并发安全（复用 M5.4a 行锁范式）、数据驱动限额、零触碰授权核心、三面自动覆盖（单 handler）。

**B 计数用独立查询（无锁）。** 并发两个 create 可同时通过计数检查、双超限 → 违 fail-close。否决。

**C 限额存 config 非 DB。** 不便按租户/套餐差异化、不可运行时调。否决（DB 真相源 + 数据驱动）。

## 4. 设计

### 4.1 文件

| 文件 | 职责 |
|---|---|
| `db/migrations/000021_plan_quota.up.sql` / `.down.sql`（新） | `plan` 表（限额列）+ seed free/pro + `tenant.plan_id` FK 默认 free |
| `internal/controlplane/store/quota.go`（新） | `TenantPlanLimits(ctx,ex,tenantID)`（FOR UPDATE 锁租户+取套餐限额）+ `CountApplications(ctx,ex,tenantID)` |
| `internal/controlplane/store/quota_test.go`（新） | 限额读 + 计数 + 并发行锁串行 |
| `internal/controlplane/mgmt/admin_ops.go`（改） | `CreateApplication` tx 内插入前加配额门 |
| `internal/controlplane/mgmt/quota_test.go`（新） | 配额达上限 fail-close ResourceExhausted / 未达允许 / 并发不超限 |

### 4.2 迁移 `000021_plan_quota`

```sql
-- up
CREATE TABLE plan (
    id                     BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name                   VARCHAR(64) NOT NULL UNIQUE,
    max_applications       INTEGER     NOT NULL,   -- 每租户应用数上限
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- 数据驱动默认套餐（数值为可配起点，运维可 UPDATE 调整）
INSERT INTO plan (name, max_applications) VALUES ('free', 3), ('pro', 50);
ALTER TABLE tenant ADD COLUMN plan_id BIGINT NOT NULL DEFAULT 1 REFERENCES plan(id);
-- 说明：default 1 = 首条 seed 的 free（GENERATED IDENTITY 从 1 起）；既有租户回填 free。
```
```sql
-- down
ALTER TABLE tenant DROP COLUMN plan_id;
DROP TABLE plan;
```
> `max_applications` 起点 free=3/pro=50（可配）。仅此一维（本增量）；后续维度加列。

### 4.3 store `quota.go`

```go
// TenantPlanLimits 锁租户行（FOR UPDATE，序列化本租户资源创建）并返回其套餐限额。
// 必须在调用方事务内调用（锁随 tx 生命周期）；租户不存在 → ErrNotFound。
func TenantPlanLimits(ctx context.Context, ex cp.DBTX, tenantID int64) (PlanLimits, error) {
	var pl PlanLimits
	err := ex.QueryRowContext(ctx,
		`SELECT p.max_applications
		   FROM tenant t JOIN plan p ON p.id = t.plan_id
		  WHERE t.id = $1 FOR UPDATE OF t`, tenantID).Scan(&pl.MaxApplications)
	if errors.Is(err, sql.ErrNoRows) { return PlanLimits{}, ErrNotFound }
	return pl, err
}

// CountApplications 返回租户当前应用数（同 tx 内，配合 TenantPlanLimits 的锁）。
func CountApplications(ctx context.Context, ex cp.DBTX, tenantID int64) (int, error) {
	var n int
	err := ex.QueryRowContext(ctx, `SELECT count(*) FROM application WHERE tenant_id=$1`, tenantID).Scan(&n)
	return n, err
}
```
`PlanLimits{MaxApplications int}`。`FOR UPDATE OF t` 只锁 tenant 行（不锁 join 的 plan）。

### 4.4 `CreateApplication` 配额门

在 `CreateApplication` 的 tx 内、INSERT application 之前插入：
```go
limits, err := store.TenantPlanLimits(ctx, tx, int64(r.TenantId))
if errors.Is(err, store.ErrNotFound) {
	return nil, status.Error(codes.InvalidArgument, "unknown tenant")
}
if err != nil { return nil, status.Errorf(codes.Internal, "quota: %v", err) }
count, err := store.CountApplications(ctx, tx, int64(r.TenantId))
if err != nil { return nil, status.Errorf(codes.Internal, "quota: %v", err) }
if count >= limits.MaxApplications {
	return nil, status.Errorf(codes.ResourceExhausted,
		"application quota reached (%d/%d) for tenant; upgrade plan", count, limits.MaxApplications)
}
```
锁在 `TenantPlanLimits` 的 `FOR UPDATE` → 本租户并发建应用串行；计数+插入同 tx → 严格不超限。`unknown tenant` 提前于原 FK 冲突分支给出（语义等价，且不泄露）。

### 4.5 数据流 / 一致性

CreateApplication → BeginTx → `TenantPlanLimits`（锁 tenant 行 + 取限额）→ `CountApplications` → 达限 rollback+ResourceExhausted / 未达 → 原 INSERT+审计+commit（释放锁）。并发第二个 create 阻塞在 FOR UPDATE 直到第一个 commit，再看到更新后的计数 → fail-close 正确。REST/Console 经同一 handler → 三面一致；REST 网关 ResourceExhausted→429。

## 5. 验证

- **配额强制（有齿）**：free（max 3）租户建 3 个应用成功、第 4 个 `ResourceExhausted`；变异实验（临时移除配额门）→ 第 4 个成功 → 测试 FAIL，证门有齿。
- **并发安全**：N=8 并发 CreateApplication 于 free(max 3) 租户 → 恰 3 成功、5 ResourceExhausted、DB 应用数恰 3（行锁串行，无超限）。
- **store**：`TenantPlanLimits` 取正确限额 + 租户不存在 ErrNotFound；`CountApplications` 准。
- **迁移**：`000021` up 建 plan+seed+tenant.plan_id、既有租户回填 free；down 干净回滚（up/down 往返）。
- **零触碰**：`git diff 81a3d7f..HEAD -- casbin/ adminauthz/ internal/sidecar/{kernel,dataperm,authz}/ internal/auth/`=空。
- `go test ./...` EXIT 0。

## 6. 验收标准（M61A-1..7）

- **M61A-1** 零触碰授权核心：上述 diff = 空（配额在 mgmt handler + store + migration）。
- **M61A-2** 迁移：`000021` plan 表+seed free/pro+tenant.plan_id 默认 free、既有租户回填；up/down 往返绿。
- **M61A-3** 应用配额 fail-close：达 `max_applications` → `ResourceExhausted`；变异移除门证有齿。
- **M61A-4** 并发安全：8 并发于 free(3) → 恰 3 成功、DB 恰 3（行锁串行）。
- **M61A-5** store 助手：限额读（含 unknown tenant ErrNotFound）+ 计数正确。
- **M61A-6** 数据驱动限额：限额来自 plan 表（非硬编码）；改 plan 行即改限额（测试可 UPDATE plan 验证生效）。
- **M61A-7** `go test ./...` EXIT 0；三面写经单 handler 覆盖（REST ResourceExhausted→429 由网关既有映射派生）。

## 7. 风险

- **既有租户回填**：`plan_id NOT NULL DEFAULT 1` + free 为首条 seed（id=1）→ 既有租户自动 free；迁移顺序（先建 plan+seed 再 ALTER tenant）保 DEFAULT 1 有效。
- **锁粒度**：`FOR UPDATE OF t` 锁单 tenant 行，仅序列化同租户建应用（跨租户不阻塞）；粒度合适。
- **计量维度单一**：本增量只 applications；plan 表可加列扩维度，机制已立。透明标注为增量 a。
- **REST 429 映射依赖网关既有 code map**：验证时确认 restgw 已含 ResourceExhausted→429（若无则本增量补一行映射，仍非授权核心）。
