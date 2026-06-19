# M2.3 审计查询 + 变更历史 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 补齐 system 域审计覆盖（新建 `admin_audit_log` 分表）、两域内容级 diff（before/after，绝不含 secret）、三面查询 + per-entity 变更历史（gRPC + REST + Console）。

**架构：** 控制面纯增强、数据面零触碰。两条审计轨道：app 域 `policy_audit_log`（既有表结构不动，写路径补 diff）+ system 域 `admin_audit_log`（新表）。审计行与动作**同事务原子提交**。查询 = 2 个只读 RPC，keyset 分页，三面共用 `mgmt.AuthorizeRule`/`ruleTable` 唯一真相源；scopeApp（app 域）/ scopeTenant（admin 域，租户看自己、超管看全部）。per-entity 历史 = 同 RPC 带 entity 过滤。

**技术栈：** Go、PostgreSQL（golang-migrate）、protobuf（buf）、casbin v3.10.0 元-RBAC（adminauthz）、testcontainers（internal/dbtest）、net/http（restgw + console）、html/template（console BFF）。

**基准：** spec `docs/superpowers/specs/2026-06-19-sydom-m2-3-audit-query-change-history-design.md`。本计划在 off-main worktree 执行。

**关键不变量（贯穿全程，AUD-1..AUD-7）：**
- AUD-1 审计行与动作同事务（无单边）；AUD-2 secret 绝不入 diff；AUD-3 租户隔离 fail-close（WHERE 与 scope 锁步）；AUD-4 一份授权真相（adminauthz matcher 一字未改、enforcer.go diff=0、ruleTable 仅 +2）；AUD-5 读纯净（不 bump/无副作用）；AUD-6 diff 保真；AUD-7 数据面零影响（审计不入 sync/translate/sidecar）。

---

## 文件结构

**创建：**
- `db/migrations/000013_admin_audit_log.up.sql` / `.down.sql` — 新表 `admin_audit_log` + 2 索引。
- `internal/controlplane/store/audit.go` — app 域审计 keyset 查询（`QueryAppAudit` + 过滤/条目类型）。
- `internal/controlplane/adminauthz/audit.go` — `InsertAdminAudit` 写助手 + `QueryAdminAudit` keyset 查询 + 类型。
- `internal/controlplane/mgmt/audit.go` — `QueryAuditLog` / `QueryAdminAuditLog` handler + system 域 diff 构造助手。
- `internal/controlplane/mgmt/audit_test.go` — handler + 租户隔离矩阵 + diff/secret 断言测试。
- `internal/controlplane/console/routes_audit.go` — app 审计页 + admin 审计页 handler。
- `internal/controlplane/console/routes_audit_test.go` — console 审计页测试。
- `internal/controlplane/console/templates/audit.html` / `admin_audit.html` — 审计 feed 模板。
- `internal/controlplane/restgw/routes_audit_test.go` — REST 审计路由测试。
- `internal/controlplane/store/audit_test.go` — app 域查询 keyset/过滤测试。
- `internal/controlplane/adminauthz/audit_test.go` — admin 域写/查询测试。

**修改：**
- `api/proto/sydom/admin/v1/admin.proto` — +2 RPC +6 message。
- `internal/controlplane/store/store.go` — `InsertAudit` 加 `diff []byte` 参数。
- `internal/controlplane/policy/manager.go` — 2 处 `InsertAudit` 调用传 diff（从 Delta 序列化）。
- `internal/controlplane/adminauthz/store.go`（或新 audit.go）— `BumpAndReadVersion` 便捷读新版本（见任务 4）。
- `internal/controlplane/mgmt/admin_ops.go` — 8 个 system handler 落审计（4 个包事务）。
- `internal/controlplane/mgmt/accounts.go` — RegisterTenant / InviteMember 落审计。
- `internal/controlplane/mgmt/authz.go` — `ruleTable` +2 行（资源 `audit`）。
- `internal/controlplane/restgw/routes.go` — +2 路由 + query 助手 + 计数更新。
- `internal/controlplane/console/routes_rbac.go` — 注册 app 审计页路由。
- `internal/controlplane/console/handler.go` — 调 `registerAudit`（admin 审计页）。
- `internal/controlplane/console/templates/_appnav.html` — +「审计」tab。
- `internal/db/schema_test.go` — round-trip 表清单 +`admin_audit_log`。

**共享签名（全任务类型一致，务必逐字对齐）：**
```go
// store.InsertAudit —— diff 加在 entityID 之后、version 之前
func InsertAudit(ctx context.Context, ex cp.DBTX, appID int64,
    operator, action, entityType, entityID string, diff []byte, version int64) error

// adminauthz
func InsertAdminAudit(ctx context.Context, ex cp.DBTX, tenantID sql.NullInt64,
    operator, action, entityType, entityID string, diff []byte, adminVersion sql.NullInt64) error

// store.QueryAppAudit / adminauthz.QueryAdminAudit —— 内部取 Limit+1 行做 keyset，
// 返回已 trim 到 Limit 的条目 + nextCursor（无下页=0）
type AppAuditFilter struct {
    EntityType, EntityID, Action, Operator string
    Since, Until time.Time // 零值=不限
    Cursor uint64          // 上页最后 id；0=首页
    Limit  int             // 已钳制
}
type AppAuditEntry struct {
    ID int64; Operator, Action, EntityType string
    EntityID, Diff sql.NullString; Version int64; CreatedAt time.Time
}
func QueryAppAudit(ctx context.Context, q cp.DBTX, appID int64, f AppAuditFilter) (entries []AppAuditEntry, nextCursor uint64, err error)

type AdminAuditFilter struct {
    TenantID sql.NullInt64 // Valid=按租户过滤；!Valid=超管全量(不加 tenant 过滤)
    EntityType, EntityID, Action, Operator string
    Since, Until time.Time; Cursor uint64; Limit int
}
type AdminAuditEntry struct {
    ID int64; TenantID sql.NullInt64; Operator, Action, EntityType string
    EntityID, Diff sql.NullString; AdminVersion sql.NullInt64; CreatedAt time.Time
}
func QueryAdminAudit(ctx context.Context, q cp.DBTX, f AdminAuditFilter) (entries []AdminAuditEntry, nextCursor uint64, err error)
```

---

## 任务 1：migration `000013` admin_audit_log + 表 round-trip

**文件：**
- 创建：`db/migrations/000013_admin_audit_log.up.sql`
- 创建：`db/migrations/000013_admin_audit_log.down.sql`
- 修改：`internal/db/schema_test.go:37`（round-trip 表清单）

- [ ] **步骤 1：写迁移 up**

`db/migrations/000013_admin_audit_log.up.sql`：
```sql
CREATE TABLE admin_audit_log (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id     BIGINT,
    operator      VARCHAR(128) NOT NULL,
    action        VARCHAR(32)  NOT NULL,
    entity_type   VARCHAR(32)  NOT NULL,
    entity_id     VARCHAR(128),
    diff          JSONB,
    admin_version BIGINT,
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE INDEX idx_admin_audit_tenant_created ON admin_audit_log (tenant_id, created_at);
CREATE INDEX idx_admin_audit_tenant_entity  ON admin_audit_log (tenant_id, entity_type, entity_id);
```

- [ ] **步骤 2：写迁移 down**

`db/migrations/000013_admin_audit_log.down.sql`：
```sql
DROP TABLE admin_audit_log;
```

- [ ] **步骤 3：把新表加入 round-trip 断言**

`internal/db/schema_test.go` 第 37 行附近的表清单（当前含 `"casbin_rule", "policy_audit_log", ...`）追加 `"admin_audit_log"`。

- [ ] **步骤 4：运行验证**

运行：`go test ./internal/db/ -run TestMigrations_UpDownRoundTrip -v`
预期：PASS（up 后表存在、down 后被删、再 up 重建）。

- [ ] **步骤 5：Commit**

```bash
git add db/migrations/000013_admin_audit_log.up.sql db/migrations/000013_admin_audit_log.down.sql internal/db/schema_test.go
git commit -m "feat(db): M2.3 admin_audit_log 表(migration 000013, system 域审计)"
```

---

## 任务 2：proto 契约 — 2 RPC + 6 message

**文件：**
- 修改：`api/proto/sydom/admin/v1/admin.proto`（service 块 + 文件尾部 message）

- [ ] **步骤 1：在 service 块加 2 个 RPC**

在 `admin.proto` service `AdminService` 内、`rpc ListMembers(...)`（第 65 行）之后、`}`（第 66 行）之前插入：
```proto

  // —— 审计查询 + 变更历史（M2.3）——
  rpc QueryAuditLog(QueryAuditLogRequest) returns (QueryAuditLogResponse);            // app 域，scopeApp
  rpc QueryAdminAuditLog(QueryAdminAuditLogRequest) returns (QueryAdminAuditLogResponse); // admin 域，scopeTenant
```

- [ ] **步骤 2：在文件尾部加 6 个 message**

追加到 `admin.proto` 末尾：
```proto

// —— 审计查询（M2.3）——
// 标量 NULL 约定：tenant_id=0 表示纯系统级(NULL)；version/admin_version=0 表示无版本/缺省。
message QueryAuditLogRequest {
  uint64 app_id      = 1; // path 权威
  string entity_type = 2;
  string entity_id   = 3; // per-entity 变更历史
  string action      = 4;
  string operator    = 5;
  string since       = 6; // RFC3339，可选
  string until       = 7;
  uint64 cursor      = 8; // keyset：上页最后 id；0=首页
  uint32 limit       = 9;
}
message AuditEntry {
  uint64 id          = 1;
  string operator    = 2;
  string action      = 3;
  string entity_type = 4;
  string entity_id   = 5;
  string diff        = 6; // JSON 文本(原样)
  uint64 version     = 7;
  string created_at  = 8; // RFC3339
}
message QueryAuditLogResponse {
  repeated AuditEntry entries = 1;
  uint64 next_cursor          = 2;
}
message QueryAdminAuditLogRequest {
  uint64 tenant_id   = 1; // 权威：0→超管全量；非 0→该租户
  string entity_type = 2;
  string entity_id   = 3;
  string action      = 4;
  string operator    = 5;
  string since       = 6;
  string until       = 7;
  uint64 cursor      = 8;
  uint32 limit       = 9;
}
message AdminAuditEntry {
  uint64 id            = 1;
  uint64 tenant_id     = 2; // 0=纯系统级
  string operator      = 3;
  string action        = 4;
  string entity_type   = 5;
  string entity_id     = 6;
  string diff          = 7;
  uint64 admin_version = 8;
  string created_at    = 9;
}
message QueryAdminAuditLogResponse {
  repeated AdminAuditEntry entries = 1;
  uint64 next_cursor               = 2;
}
```

- [ ] **步骤 3：重新生成 + lint 校验**

运行：`make proto-gen` 然后 `make proto-check`（Makefile 目标：`proto-gen` = buf lint + generate；`proto-check` = proto-gen 后校验无漂移）。
预期：`gen/sydom/admin/v1/*.pb.go` 含 `QueryAuditLogRequest`/`AuditEntry`/`QueryAdminAuditLogRequest`/`AdminAuditEntry` 等类型；`make proto-check` 无漂移、buf lint 通过（请求/响应均标准命名，无需 buf.yaml except）。

- [ ] **步骤 4：编译校验**

运行：`go build ./...`
预期：通过（生成类型可用）。

- [ ] **步骤 5：Commit**

```bash
git add api/proto/sydom/admin/v1/admin.proto gen/
git commit -m "feat(proto): M2.3 QueryAuditLog/QueryAdminAuditLog RPC 契约(审计查询)"
```

---

## 任务 3：app 域 diff — InsertAudit 加 diff 参数 + manager 两处填充

**文件：**
- 修改：`internal/controlplane/store/store.go:69-76`（InsertAudit 签名 + SQL）
- 修改：`internal/controlplane/policy/manager.go`（第 94、347 两处调用 + 新增 diff 助手）
- 测试：`internal/controlplane/policy/manager_test.go`（断言 diff 落库）

- [ ] **步骤 1：写失败测试（diff 落库）**

在 `internal/controlplane/policy/manager_test.go` 追加（沿用该文件既有 dbtest + NewPolicyManager 装配范式；若 helper 名不同照该文件现有用法调整）：
```go
func TestRunVersionedWrite_PopulatesDiff(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	m := NewPolicyManager(db, nil)
	ctx := cp.WithOperator(context.Background(), "alice")

	// 建角色 → 应 bump + 落审计且 diff 非空、含 adds
	if _, err := m.CreateRole(ctx, appID, "admin", "管理员"); err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	var diff sql.NullString
	require.NoError(t, db.QueryRow(
		`SELECT diff FROM policy_audit_log WHERE app_id=$1 ORDER BY id DESC LIMIT 1`, appID).Scan(&diff))
	require.True(t, diff.Valid, "diff 应非 NULL")
	require.Contains(t, diff.String, "adds")
}
```
（`CreateRole` 若需 code/name 之外参数，对齐该文件其它调用；目的仅验证 diff 落库。）

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/controlplane/policy/ -run TestRunVersionedWrite_PopulatesDiff -v`
预期：编译失败（InsertAudit 参数不匹配）或断言失败（diff 为 NULL）。

- [ ] **步骤 3：改 InsertAudit 签名 + SQL**

`internal/controlplane/store/store.go` 第 69-76 行替换为：
```go
// InsertAudit 写一条审计记录。diff 为变更内容 JSON（可为 nil → 落 NULL）。
func InsertAudit(ctx context.Context, ex cp.DBTX, appID int64,
	operator, action, entityType, entityID string, diff []byte, version int64) error {
	_, err := ex.ExecContext(ctx, `
		INSERT INTO policy_audit_log (app_id, operator, action, entity_type, entity_id, diff, version)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		appID, operator, action, entityType, entityID, diff, version)
	return err
}
```

- [ ] **步骤 4：加 diff 助手 + 改两处调用**

在 `internal/controlplane/policy/manager.go` 顶部 import 加 `"encoding/json"`。文件内（如 `runVersionedWrite` 上方）加助手：
```go
// auditDiff 把一次写的策略变更序列化为审计 diff JSON（绝不含 secret——casbin 规则/数据策略本无凭据）。
func auditDiff(adds, removes []cp.Rule, changes []cp.DataPolicyChange) []byte {
	payload := map[string]any{}
	if len(adds) > 0 {
		payload["adds"] = adds
	}
	if len(removes) > 0 {
		payload["removes"] = removes
	}
	if len(changes) > 0 {
		payload["data_changes"] = changes
	}
	if len(payload) == 0 {
		return nil
	}
	b, _ := json.Marshal(payload) // 输入为本进程内领域结构，Marshal 不会失败
	return b
}
```
第 94-97 行（`runVersionedWrite` 内）改为：
```go
	if err := store.InsertAudit(ctx, tx, appID,
		cp.OperatorFromContext(ctx), op.action, op.entityType, op.entityID,
		auditDiff(adds, removes, dataChanges), vNew); err != nil {
		return nil, fmt.Errorf("policy: audit %s v%d: %w", op.action, vNew, err)
	}
```
第 347-350 行（`runVersionedWriteData` 内）改为：
```go
	if err := store.InsertAudit(ctx, tx, appID,
		cp.OperatorFromContext(ctx), op.action, op.entityType, op.entityID,
		auditDiff(nil, nil, changes), vNew); err != nil {
		return nil, fmt.Errorf("policy: audit %s v%d: %w", op.action, vNew, err)
	}
```

- [ ] **步骤 5：修既有 InsertAudit 测试调用（若有）**

运行：`grep -rn "store.InsertAudit\|InsertAudit(" internal/ | grep -v "func InsertAudit"`，对每处调用补 diff 实参（测试里传 `nil`）。

- [ ] **步骤 6：运行测试验证通过**

运行：`go test ./internal/controlplane/policy/ ./internal/controlplane/store/ -v`
预期：PASS（含新 diff 测试 + 既有写测试不回归）。

- [ ] **步骤 7：Commit**

```bash
git add internal/controlplane/store/store.go internal/controlplane/policy/manager.go
git commit -m "feat(audit): app 域 diff 落库(InsertAudit +diff, manager 两处从 Delta 序列化)"
```

---

## 任务 4：adminauthz — InsertAdminAudit 写助手 + bump 后读新版本

**文件：**
- 创建：`internal/controlplane/adminauthz/audit.go`（InsertAdminAudit；QueryAdminAudit 留任务 6）
- 测试：`internal/controlplane/adminauthz/audit_test.go`

- [ ] **步骤 1：写失败测试**

`internal/controlplane/adminauthz/audit_test.go`：
```go
package adminauthz_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestInsertAdminAudit_RoundTrip(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	err := adminauthz.InsertAdminAudit(ctx, db,
		sql.NullInt64{Int64: 7, Valid: true}, "root@sydom", "create",
		"application", "42", []byte(`{"after":{"name":"x"}}`),
		sql.NullInt64{Int64: 3, Valid: true})
	require.NoError(t, err)

	var tid, ver sql.NullInt64
	var op, act, et string
	var diff sql.NullString
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT tenant_id, operator, action, entity_type, diff, admin_version
		 FROM admin_audit_log ORDER BY id DESC LIMIT 1`).
		Scan(&tid, &op, &act, &et, &diff, &ver))
	require.Equal(t, int64(7), tid.Int64)
	require.Equal(t, "root@sydom", op)
	require.Equal(t, "application", et)
	require.Contains(t, diff.String, "after")
	require.Equal(t, int64(3), ver.Int64)
}

func TestInsertAdminAudit_NullTenantAndVersion(t *testing.T) {
	db := dbtest.SetupSchema(t)
	require.NoError(t, adminauthz.InsertAdminAudit(context.Background(), db,
		sql.NullInt64{}, "root@sydom", "reset", "operator", "9", nil, sql.NullInt64{}))
	var tid, ver sql.NullInt64
	require.NoError(t, db.QueryRow(
		`SELECT tenant_id, admin_version FROM admin_audit_log ORDER BY id DESC LIMIT 1`).Scan(&tid, &ver))
	require.False(t, tid.Valid)
	require.False(t, ver.Valid)
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/controlplane/adminauthz/ -run TestInsertAdminAudit -v`
预期：编译失败（InsertAdminAudit 未定义）。

- [ ] **步骤 3：写实现**

`internal/controlplane/adminauthz/audit.go`：
```go
package adminauthz

import (
	"context"
	"database/sql"
	"fmt"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
)

// InsertAdminAudit 写一条 system 域审计记录。tenantID/adminVersion 用 sql.NullInt64
// 承载 NULL（纯系统级动作无租户；无 bump 的动作无版本）。diff 可为 nil → 落 NULL。
// diff 绝不含 secret（由调用方构造，仅白名单非敏感字段）。
func InsertAdminAudit(ctx context.Context, ex cp.DBTX, tenantID sql.NullInt64,
	operator, action, entityType, entityID string, diff []byte, adminVersion sql.NullInt64) error {
	_, err := ex.ExecContext(ctx, `
		INSERT INTO admin_audit_log
		  (tenant_id, operator, action, entity_type, entity_id, diff, admin_version)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		tenantID, operator, action, entityType, nullStr(entityID), diff, adminVersion)
	if err != nil {
		return fmt.Errorf("adminauthz: insert admin audit: %w", err)
	}
	return nil
}

// nullStr 把空串转成 NULL（entity_id 可空）。
func nullStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/controlplane/adminauthz/ -run TestInsertAdminAudit -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/adminauthz/audit.go internal/controlplane/adminauthz/audit_test.go
git commit -m "feat(adminauthz): InsertAdminAudit 写助手(system 域审计, NULL 租户/版本)"
```

---

## 任务 5：system 域 handler 落审计（admin_ops.go 8 + accounts.go 2，4 个包事务）

**文件：**
- 修改：`internal/controlplane/mgmt/admin_ops.go`
- 修改：`internal/controlplane/mgmt/accounts.go`
- 修改：`internal/controlplane/mgmt/audit.go`（先建文件放 diff 助手；handler 留任务 7）
- 测试：`internal/controlplane/mgmt/audit_test.go`（AUD-1 原子性 + AUD-2 secret 不入 diff + 覆盖面）

> **上下文：** 已有事务的 handler（GrantAdminRole/BindOperatorRole/RevokeAdminGrant/UnbindOperatorRole/RegisterTenant/InviteMember）在 `BumpPolicyVersion` 之后、`tx.Commit()` 之前插审计；需 admin_version 时同 tx 内 `ReadPolicyVersion(ctx, tx)` 取新值。无事务的 4 个（CreateApplication/RotateApplicationSecret/ResetOperatorSecret/SetApplicationStatus）必须**改为事务**（动作 + 审计原子）。`SetOperatorStatus/CreateOperator` 也补审计（按现状是否有 tx，无则包 tx）。

- [ ] **步骤 1：建 diff 助手文件**

`internal/controlplane/mgmt/audit.go`（仅助手，handler 任务 7 再加）：
```go
package mgmt

import "encoding/json"

// auditJSON 把审计 diff payload 序列化（绝不放 secret——调用方只传白名单非敏感字段）。
func auditJSON(payload map[string]any) []byte {
	if len(payload) == 0 {
		return nil
	}
	b, _ := json.Marshal(payload)
	return b
}
```

- [ ] **步骤 2：写失败测试（AUD-1 + AUD-2 + 覆盖面）**

`internal/controlplane/mgmt/audit_test.go`（沿用该包既有 dbtest + AdminServer 装配范式；参照 `decision_test.go`/`admin_ops` 既有测试如何 new AdminServer、注入 operator ctx、SeedApp）：
```go
// RotateApplicationSecret 后落 1 条 admin 审计且 diff 不含新 secret 明文（AUD-2）。
func TestRotateApplicationSecret_AuditsWithoutSecret(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	s := newTestAdminServer(t, db) // 该包既有 helper（参照现有测试）
	ctx := cp.WithOperator(context.Background(), "root@sydom")

	resp, err := s.RotateApplicationSecret(ctx, &adminv1.RotateApplicationSecretRequest{AppId: uint64(appID)})
	require.NoError(t, err)

	var diff sql.NullString
	var op, et string
	require.NoError(t, db.QueryRow(
		`SELECT operator, entity_type, diff FROM admin_audit_log ORDER BY id DESC LIMIT 1`).
		Scan(&op, &et, &diff))
	require.Equal(t, "root@sydom", op)
	require.Equal(t, "application", et)
	require.NotContains(t, diff.String, resp.AppSecret) // 新 secret 绝不入 diff
}

// CreateApplication 审计失败回滚：app 与审计同事务（AUD-1）。
// 通过外部 drop admin_audit_log 触发审计 INSERT 失败，断言 app 行未落（事务回滚）。
func TestCreateApplication_AuditAtomic(t *testing.T) {
	db := dbtest.SetupSchema(t)
	var tenantID int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant(name) VALUES('acme') RETURNING id`).Scan(&tenantID))
	_, err := db.Exec(`DROP TABLE admin_audit_log`)
	require.NoError(t, err)
	s := newTestAdminServer(t, db)
	ctx := cp.WithOperator(context.Background(), "root@sydom")

	_, err = s.CreateApplication(ctx, &adminv1.CreateApplicationRequest{
		TenantId: uint64(tenantID), Domain: "d1", Name: "n1", AppKey: "k1"})
	require.Error(t, err) // 审计 INSERT 失败 → 整笔回滚
	var cnt int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM application WHERE app_key='k1'`).Scan(&cnt))
	require.Equal(t, 0, cnt) // app 未落库（AUD-1 原子）
}

// GrantAdminRole 落审计且 admin_version 记新版本。
func TestGrantAdminRole_AuditsWithVersion(t *testing.T) {
	db := dbtest.SetupSchema(t)
	s := newTestAdminServer(t, db)
	// 准备一个 admin_role（参照该包既有建角色测试）
	var roleID int64
	require.NoError(t, db.QueryRow(`INSERT INTO admin_role(code,name) VALUES('r1','R1') RETURNING id`).Scan(&roleID))
	ctx := cp.WithOperator(context.Background(), "root@sydom")
	_, err := s.GrantAdminRole(ctx, &adminv1.GrantAdminRoleRequest{
		RoleId: roleID, Domain: "*", Resource: "role", Action: "read"})
	require.NoError(t, err)
	var ver sql.NullInt64
	var et string
	require.NoError(t, db.QueryRow(
		`SELECT entity_type, admin_version FROM admin_audit_log ORDER BY id DESC LIMIT 1`).Scan(&et, &ver))
	require.Equal(t, "admin_grant", et)
	require.True(t, ver.Valid)
}
```
（若该包无 `newTestAdminServer` helper，按 `decision_test.go`/`admin_ops` 既有测试的 AdminServer 构造方式内联构造；`masterKey` 等依赖照现有测试。）

- [ ] **步骤 3：运行测试验证失败**

运行：`go test ./internal/controlplane/mgmt/ -run "TestRotateApplicationSecret_Audits|TestCreateApplication_AuditAtomic|TestGrantAdminRole_Audits" -v`
预期：失败（审计未写 / app 未原子）。

- [ ] **步骤 4：改有事务的 handler（admin_ops.go）**

在 `admin_ops.go` 顶部确认 import 有 `cp "github.com/nickZFZ/Sydom/internal/controlplane"`、`"database/sql"`。对 `GrantAdminRole`/`BindOperatorRole`/`RevokeAdminGrant`/`UnbindOperatorRole`，在各自 `adminauthz.BumpPolicyVersion(ctx, tx)` 成功之后、`tx.Commit()` 之前插入审计。以 `GrantAdminRole` 为例：
```go
	if err := adminauthz.BumpPolicyVersion(ctx, tx); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	ver, err := adminauthz.ReadPolicyVersion(ctx, tx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := adminauthz.InsertAdminAudit(ctx, tx, domainTenant(r.Domain),
		cp.OperatorFromContext(ctx), "grant", "admin_grant", fmt.Sprintf("%d", r.RoleId),
		auditJSON(map[string]any{"after": map[string]any{
			"role_id": r.RoleId, "domain": r.Domain, "resource": r.Resource, "action": r.Action}}),
		sql.NullInt64{Int64: ver, Valid: true}); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := tx.Commit(); err != nil { ... }
```
- `BindOperatorRole`：action `"bind"`，entity_type `"admin_binding"`，entity_id `operator_id`，diff after={operator_id,role_id,domain}，tenant=`domainTenant(r.Domain)`。
- `RevokeAdminGrant`：action `"revoke"`，entity_type `"admin_grant"`，entity_id `role_id`，diff **before**={role_id,domain,resource,action}，tenant=`domainTenant(r.Domain)`。
- `UnbindOperatorRole`：action `"unbind"`，entity_type `"admin_binding"`，entity_id `operator_id`，diff before={operator_id,role_id,domain}，tenant=`domainTenant(r.Domain)`。

在 `admin_ops.go` 加 import `"fmt"`（如未有）+ tenant 解析助手：
```go
// domainTenant 把 admin 域字符串映射到审计 tenant_id：t:<id> → 该租户；"*"/其它 → NULL。
func domainTenant(domain string) sql.NullInt64 {
	if strings.HasPrefix(domain, "t:") {
		if id, err := strconv.ParseInt(domain[2:], 10, 64); err == nil {
			return sql.NullInt64{Int64: id, Valid: true}
		}
	}
	return sql.NullInt64{}
}
```
（import 补 `"strings"`、`"strconv"`。）

- [ ] **步骤 5：改无事务的 4 个 handler 为事务 + 审计（admin_ops.go）**

- **CreateApplication**：现为单 `QueryRowContext INSERT ... RETURNING`。改为 `tx := s.db.BeginTx`；`defer tx.Rollback()`；INSERT 用 `tx.QueryRowContext` 拿 appID；随后 `InsertAdminAudit(ctx, tx, sql.NullInt64{Int64:int64(r.TenantId),Valid:true}, operator, "create", "application", fmt.Sprintf("%d",appID), auditJSON({"after":{name,app_key,domain,tenant_id}}), sql.NullInt64{})`（**无 secret**）；`tx.Commit()`。唯一/外键冲突分支保持（在 INSERT 处判定）。
- **RotateApplicationSecret**：改为事务。先 `tx.ExecContext(UPDATE application SET app_secret_enc=$1 WHERE id=$2)` 取 RowsAffected==0→NotFound（回滚）；查该 app 的 tenant_id（`SELECT tenant_id FROM application WHERE id=$1`，同 tx）作审计 tenant；`InsertAdminAudit(..., tenant, operator, "rotate_secret", "application", appID, auditJSON({"rotated":true}), sql.NullInt64{})`（**新旧 secret 都不入**）；`tx.Commit()`；最后返回新 secret（一次性）。
- **ResetOperatorSecret**：改为事务。`UPDATE admin_operator SET secret_enc=$1 WHERE id=$2`，RowsAffected==0→NotFound；`InsertAdminAudit(..., sql.NullInt64{}（纯系统级）, operator, "reset_secret", "operator", operator_id, auditJSON({"reset":true}), sql.NullInt64{})`；commit；返回新 secret。
- **SetApplicationStatus**：改为事务。先 `SELECT status, tenant_id FROM application WHERE id=$1`（取 before + tenant），无行→NotFound；`UPDATE application SET status=$1 ...`；`InsertAdminAudit(..., tenant, operator, "set_status", "application", appID, auditJSON({"before":old,"after":new}), sql.NullInt64{})`；commit。
- **SetOperatorStatus**：改为事务。`SELECT status FROM admin_operator WHERE id=$1`（before），无行→NotFound；UPDATE；`InsertAdminAudit(..., sql.NullInt64{}, operator, "set_status", "operator", operator_id, auditJSON({"before":old,"after":new}), sql.NullInt64{})`；commit。
- **CreateOperator**：若现为单语句则包事务；`InsertAdminAudit(..., sql.NullInt64{}, operator, "create", "operator", newOpID, auditJSON({"after":{principal}}), sql.NullInt64{})`（**无 secret**）。

> 每处 operator 取 `cp.OperatorFromContext(ctx)`。所有 diff 仅放非敏感白名单字段，secret/凭据一律不入（AUD-2）。

- [ ] **步骤 6：改 accounts.go 两个 handler**

- **RegisterTenant**：在 `BumpPolicyVersion(ctx, tx)` 后、`tx.Commit()` 前，`ver, _ := adminauthz.ReadPolicyVersion(ctx, tx)`；`InsertAdminAudit(ctx, tx, sql.NullInt64{Int64:tenantID,Valid:true}, r.OwnerPrincipal, "register", "tenant", fmt.Sprintf("%d",tenantID), auditJSON({"after":{"tenant_name":r.TenantName,"owner":r.OwnerPrincipal}}), sql.NullInt64{Int64:ver,Valid:true})`。operator 用 `r.OwnerPrincipal`（免鉴权、自助注册，actor 即新 owner）。**owner_secret 不入 diff**。
- **InviteMember**：在 `BumpPolicyVersion` 后、commit 前，`ver, _ := adminauthz.ReadPolicyVersion(ctx, tx)`；`InsertAdminAudit(ctx, tx, sql.NullInt64{Int64:int64(r.TenantId),Valid:true}, cp.OperatorFromContext(ctx), "invite", "membership", fmt.Sprintf("%d",opID), auditJSON({"after":{"principal":r.Principal,"tier":"admin"}}), sql.NullInt64{Int64:ver,Valid:true})`。**新成员 secret 不入 diff**。
（accounts.go import 补 `"database/sql"`、`"fmt"`。）

- [ ] **步骤 7：运行测试验证通过**

运行：`go test ./internal/controlplane/mgmt/ -run "Audit" -v` 然后 `go test ./internal/controlplane/mgmt/ -v`
预期：新审计测试 PASS；既有 mgmt 测试不回归（撤权/轮换/状态等行为不变，仅多落审计）。

- [ ] **步骤 8：Commit**

```bash
git add internal/controlplane/mgmt/admin_ops.go internal/controlplane/mgmt/accounts.go internal/controlplane/mgmt/audit.go internal/controlplane/mgmt/audit_test.go
git commit -m "feat(mgmt): system 域 10 handler 落审计(同事务原子, diff 不含 secret, 4 个包 tx)"
```

---

## 任务 6：store 查询 — QueryAppAudit + QueryAdminAudit（keyset + 过滤）

**文件：**
- 创建：`internal/controlplane/store/audit.go`（QueryAppAudit + AppAuditFilter/AppAuditEntry）
- 修改：`internal/controlplane/adminauthz/audit.go`（QueryAdminAudit + AdminAuditFilter/AdminAuditEntry）
- 测试：`internal/controlplane/store/audit_test.go`、`internal/controlplane/adminauthz/audit_test.go`

- [ ] **步骤 1：写失败测试（keyset 无跳无重 + 过滤 + 租户隔离）**

`internal/controlplane/store/audit_test.go`：
```go
func TestQueryAppAudit_KeysetAndFilter(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	// 直插 5 条审计（版本 1..5；entity_type 交替 role/data_policy）
	for i := 1; i <= 5; i++ {
		et := "role"
		if i%2 == 0 {
			et = "data_policy"
		}
		require.NoError(t, store.InsertAudit(context.Background(), db, appID,
			"alice", "create", et, fmt.Sprintf("%d", i), []byte(`{"adds":[]}`), int64(i)))
	}
	// 首页 limit=2 → 取最新两条(id 降序)，nextCursor 非 0
	e1, c1, err := store.QueryAppAudit(context.Background(), db, appID, store.AppAuditFilter{Limit: 2})
	require.NoError(t, err)
	require.Len(t, e1, 2)
	require.NotZero(t, c1)
	// 次页接着 cursor → 不重叠
	e2, _, err := store.QueryAppAudit(context.Background(), db, appID, store.AppAuditFilter{Limit: 2, Cursor: c1})
	require.NoError(t, err)
	require.Len(t, e2, 2)
	require.Less(t, e2[0].ID, e1[1].ID)
	// 过滤 entity_type=role → 仅 role 行
	ef, _, err := store.QueryAppAudit(context.Background(), db, appID,
		store.AppAuditFilter{Limit: 50, EntityType: "role"})
	require.NoError(t, err)
	for _, e := range ef {
		require.Equal(t, "role", e.EntityType)
	}
}
```
`internal/controlplane/adminauthz/audit_test.go` 追加：
```go
func TestQueryAdminAudit_TenantScopeAndAll(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	mk := func(tid sql.NullInt64, et string) {
		require.NoError(t, adminauthz.InsertAdminAudit(ctx, db, tid, "root", "x", et, "1", nil, sql.NullInt64{}))
	}
	mk(sql.NullInt64{Int64: 1, Valid: true}, "application")
	mk(sql.NullInt64{Int64: 2, Valid: true}, "application")
	mk(sql.NullInt64{}, "operator") // 纯系统级
	// 租户 1 过滤 → 仅 tenant_id=1
	e1, _, err := adminauthz.QueryAdminAudit(ctx, db,
		adminauthz.AdminAuditFilter{TenantID: sql.NullInt64{Int64: 1, Valid: true}, Limit: 50})
	require.NoError(t, err)
	require.Len(t, e1, 1)
	require.Equal(t, int64(1), e1[0].TenantID.Int64)
	// 超管全量（TenantID 不 Valid）→ 全部 3 条（含纯系统级）
	eAll, _, err := adminauthz.QueryAdminAudit(ctx, db, adminauthz.AdminAuditFilter{Limit: 50})
	require.NoError(t, err)
	require.Len(t, eAll, 3)
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/controlplane/store/ ./internal/controlplane/adminauthz/ -run "Audit" -v`
预期：编译失败（QueryAppAudit/QueryAdminAudit 未定义）。

- [ ] **步骤 3：实现 QueryAppAudit**

`internal/controlplane/store/audit.go`：
```go
package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
)

type AppAuditFilter struct {
	EntityType, EntityID, Action, Operator string
	Since, Until                           time.Time
	Cursor                                 uint64
	Limit                                  int
}

type AppAuditEntry struct {
	ID                          int64
	Operator, Action, EntityType string
	EntityID, Diff              sql.NullString
	Version                     int64
	CreatedAt                   time.Time
}

// QueryAppAudit 按 app_id + 过滤做 keyset 分页（id 降序）。内部取 Limit+1 行判断下页，
// 返回 trim 到 Limit 的条目与 nextCursor（无下页=0）。
func QueryAppAudit(ctx context.Context, q cp.DBTX, appID int64, f AppAuditFilter) ([]AppAuditEntry, uint64, error) {
	conds := []string{"app_id = $1"}
	args := []any{appID}
	add := func(cond string, val any) {
		args = append(args, val)
		conds = append(conds, cond+" $"+itoa(len(args)))
	}
	if f.Cursor > 0 {
		add("id <", int64(f.Cursor))
	}
	if f.EntityType != "" {
		add("entity_type =", f.EntityType)
	}
	if f.EntityID != "" {
		add("entity_id =", f.EntityID)
	}
	if f.Action != "" {
		add("action =", f.Action)
	}
	if f.Operator != "" {
		add("operator =", f.Operator)
	}
	if !f.Since.IsZero() {
		add("created_at >=", f.Since)
	}
	if !f.Until.IsZero() {
		add("created_at <=", f.Until)
	}
	args = append(args, f.Limit+1)
	query := `SELECT id, operator, action, entity_type, entity_id, diff, version, created_at
		FROM policy_audit_log WHERE ` + strings.Join(conds, " AND ") +
		` ORDER BY id DESC LIMIT $` + itoa(len(args))

	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []AppAuditEntry
	for rows.Next() {
		var e AppAuditEntry
		if err := rows.Scan(&e.ID, &e.Operator, &e.Action, &e.EntityType,
			&e.EntityID, &e.Diff, &e.Version, &e.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var next uint64
	if len(out) > f.Limit {
		next = uint64(out[f.Limit-1].ID)
		out = out[:f.Limit]
	}
	return out, next, nil
}
```
并在 store 包加 `itoa`（若已有 strconv 用法则直接 `strconv.Itoa`，避免重复定义；本计划用本地小助手或直接 `strconv.Itoa`——实现者二选一并保持一致）：
```go
import "strconv"
func itoa(n int) string { return strconv.Itoa(n) }
```

- [ ] **步骤 4：实现 QueryAdminAudit**

追加到 `internal/controlplane/adminauthz/audit.go`：
```go
import (
	"strconv"
	"strings"
	"time"
)

type AdminAuditFilter struct {
	TenantID                               sql.NullInt64 // Valid=按租户过滤；!Valid=超管全量
	EntityType, EntityID, Action, Operator string
	Since, Until                           time.Time
	Cursor                                 uint64
	Limit                                  int
}

type AdminAuditEntry struct {
	ID                           int64
	TenantID                     sql.NullInt64
	Operator, Action, EntityType string
	EntityID, Diff               sql.NullString
	AdminVersion                 sql.NullInt64
	CreatedAt                    time.Time
}

// QueryAdminAudit 按过滤做 keyset 分页（id 降序）。TenantID.Valid → WHERE tenant_id=值（租户隔离）；
// !Valid → 不加 tenant 过滤（超管全量，含纯系统级 tenant_id NULL 行）。
func QueryAdminAudit(ctx context.Context, q cp.DBTX, f AdminAuditFilter) ([]AdminAuditEntry, uint64, error) {
	var conds []string
	var args []any
	add := func(cond string, val any) {
		args = append(args, val)
		conds = append(conds, cond+" $"+strconv.Itoa(len(args)))
	}
	if f.TenantID.Valid {
		add("tenant_id =", f.TenantID.Int64)
	}
	if f.Cursor > 0 {
		add("id <", int64(f.Cursor))
	}
	if f.EntityType != "" {
		add("entity_type =", f.EntityType)
	}
	if f.EntityID != "" {
		add("entity_id =", f.EntityID)
	}
	if f.Action != "" {
		add("action =", f.Action)
	}
	if f.Operator != "" {
		add("operator =", f.Operator)
	}
	if !f.Since.IsZero() {
		add("created_at >=", f.Since)
	}
	if !f.Until.IsZero() {
		add("created_at <=", f.Until)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, f.Limit+1)
	query := `SELECT id, tenant_id, operator, action, entity_type, entity_id, diff, admin_version, created_at
		FROM admin_audit_log` + where + ` ORDER BY id DESC LIMIT $` + strconv.Itoa(len(args))

	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []AdminAuditEntry
	for rows.Next() {
		var e AdminAuditEntry
		if err := rows.Scan(&e.ID, &e.TenantID, &e.Operator, &e.Action, &e.EntityType,
			&e.EntityID, &e.Diff, &e.AdminVersion, &e.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var next uint64
	if len(out) > f.Limit {
		next = uint64(out[f.Limit-1].ID)
		out = out[:f.Limit]
	}
	return out, next, nil
}
```

- [ ] **步骤 5：运行测试验证通过**

运行：`go test ./internal/controlplane/store/ ./internal/controlplane/adminauthz/ -run "Audit" -v`
预期：PASS（keyset 无跳无重、过滤命中、租户隔离 + 超管全量）。

- [ ] **步骤 6：Commit**

```bash
git add internal/controlplane/store/audit.go internal/controlplane/store/audit_test.go internal/controlplane/adminauthz/audit.go internal/controlplane/adminauthz/audit_test.go
git commit -m "feat(store): 审计 keyset 查询(QueryAppAudit/QueryAdminAudit, 过滤+租户隔离 WHERE)"
```

---

## 任务 7：mgmt handler — QueryAuditLog + QueryAdminAuditLog + ruleTable

**文件：**
- 修改：`internal/controlplane/mgmt/audit.go`（加 2 个 handler + 时间解析助手）
- 修改：`internal/controlplane/mgmt/authz.go:83`（ruleTable +2 行）
- 测试：`internal/controlplane/mgmt/audit_test.go`（handler + 租户隔离矩阵 AUD-3 + AUD-5 不 bump）

- [ ] **步骤 1：ruleTable 加 2 行**

`internal/controlplane/mgmt/authz.go` 在 `ruleTable` 中 `ExplainDecision` 行（第 77 行）之后加：
```go
	"/sydom.admin.v1.AdminService/QueryAuditLog":      {"audit", "read", false, scopeApp},
	"/sydom.admin.v1.AdminService/QueryAdminAuditLog": {"audit", "read", false, scopeTenant},
```

- [ ] **步骤 2：写失败测试（含 AUD-3 租户隔离矩阵）**

`internal/controlplane/mgmt/audit_test.go` 追加：
```go
// QueryAuditLog 返回 app 审计、不 bump 版本（AUD-5）、limit 钳制。
func TestQueryAuditLog_ReadsAndNoBump(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	require.NoError(t, store.InsertAudit(context.Background(), db, appID,
		"alice", "create", "role", "1", []byte(`{"adds":[]}`), 1))
	var v0 int64
	require.NoError(t, db.QueryRow(`SELECT current_version FROM application WHERE id=$1`, appID).Scan(&v0))
	s := newTestAdminServer(t, db)
	ctx := cp.WithOperator(context.Background(), "alice")
	resp, err := s.QueryAuditLog(ctx, &adminv1.QueryAuditLogRequest{AppId: uint64(appID), Limit: 50})
	require.NoError(t, err)
	require.Len(t, resp.Entries, 1)
	require.Equal(t, "role", resp.Entries[0].EntityType)
	var v1 int64
	require.NoError(t, db.QueryRow(`SELECT current_version FROM application WHERE id=$1`, appID).Scan(&v1))
	require.Equal(t, v0, v1) // 读不 bump
}

// QueryAdminAuditLog：超管 tenant_id=0 看全部；指定 tenant_id 仅该租户。
func TestQueryAdminAuditLog_TenantScope(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	require.NoError(t, adminauthz.InsertAdminAudit(ctx, db, sql.NullInt64{Int64: 1, Valid: true}, "root", "create", "application", "1", nil, sql.NullInt64{}))
	require.NoError(t, adminauthz.InsertAdminAudit(ctx, db, sql.NullInt64{}, "root", "reset", "operator", "9", nil, sql.NullInt64{}))
	s := newTestAdminServer(t, db)
	sctx := cp.WithOperator(ctx, "root@sydom")
	// tenant_id=0 → 全部 2 条
	all, err := s.QueryAdminAuditLog(sctx, &adminv1.QueryAdminAuditLogRequest{TenantId: 0, Limit: 50})
	require.NoError(t, err)
	require.Len(t, all.Entries, 2)
	// tenant_id=1 → 仅 1 条
	one, err := s.QueryAdminAuditLog(sctx, &adminv1.QueryAdminAuditLogRequest{TenantId: 1, Limit: 50})
	require.NoError(t, err)
	require.Len(t, one.Entries, 1)
}
```
> 跨租户**鉴权拒绝**矩阵（租户 A 查租户 B → PermissionDenied）由任务 8/9 经 `AuthorizeRule` 的端到端测试覆盖（handler 本身不重复鉴权，依赖拦截器/AuthorizeRule）；此处 handler 单测聚焦 WHERE 与 scope 锁步的数据正确性。

- [ ] **步骤 3：运行测试验证失败**

运行：`go test ./internal/controlplane/mgmt/ -run "TestQueryAuditLog|TestQueryAdminAuditLog" -v`
预期：编译失败（handler 未定义）。

- [ ] **步骤 4：实现 2 个 handler**

追加到 `internal/controlplane/mgmt/audit.go`（import 补 `context`、`database/sql`、`time`、`adminv1`、`cp`、store、adminauthz、status/codes）：
```go
const auditMaxLimit = 200
const auditDefaultLimit = 50

func clampLimit(n uint32) int {
	if n == 0 {
		return auditDefaultLimit
	}
	if int(n) > auditMaxLimit {
		return auditMaxLimit
	}
	return int(n)
}

// parseTS 解析可选 RFC3339 时间；空串=零值(不过滤)。非法→InvalidArgument。
func parseTS(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, status.Error(codes.InvalidArgument, "invalid timestamp")
	}
	return t, nil
}

func nullStrToProto(s sql.NullString) string {
	if s.Valid {
		return s.String
	}
	return ""
}

// QueryAuditLog 读 app 域审计（scopeApp 已由 AuthorizeRule 鉴权）。纯读、不 bump。
func (s *AdminServer) QueryAuditLog(ctx context.Context, r *adminv1.QueryAuditLogRequest) (*adminv1.QueryAuditLogResponse, error) {
	since, err := parseTS(r.Since)
	if err != nil {
		return nil, err
	}
	until, err := parseTS(r.Until)
	if err != nil {
		return nil, err
	}
	entries, next, err := store.QueryAppAudit(ctx, s.db, int64(r.AppId), store.AppAuditFilter{
		EntityType: r.EntityType, EntityID: r.EntityId, Action: r.Action, Operator: r.Operator,
		Since: since, Until: until, Cursor: r.Cursor, Limit: clampLimit(r.Limit),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "query audit: %v", err)
	}
	out := &adminv1.QueryAuditLogResponse{NextCursor: next}
	for _, e := range entries {
		out.Entries = append(out.Entries, &adminv1.AuditEntry{
			Id: uint64(e.ID), Operator: e.Operator, Action: e.Action, EntityType: e.EntityType,
			EntityId: nullStrToProto(e.EntityID), Diff: nullStrToProto(e.Diff),
			Version: uint64(e.Version), CreatedAt: e.CreatedAt.Format(time.RFC3339),
		})
	}
	return out, nil
}

// QueryAdminAuditLog 读 admin 域审计（scopeTenant 已鉴权）。tenant_id=0→超管全量；非 0→该租户。
// WHERE 与 scope 锁步：AuthorizeRule 已保证调用者对该 tenant_id 域有权（租户管理员越租户取 0/他租户 → 已被拒）。
func (s *AdminServer) QueryAdminAuditLog(ctx context.Context, r *adminv1.QueryAdminAuditLogRequest) (*adminv1.QueryAdminAuditLogResponse, error) {
	since, err := parseTS(r.Since)
	if err != nil {
		return nil, err
	}
	until, err := parseTS(r.Until)
	if err != nil {
		return nil, err
	}
	var tenant sql.NullInt64
	if r.TenantId != 0 {
		tenant = sql.NullInt64{Int64: int64(r.TenantId), Valid: true}
	}
	entries, next, err := adminauthz.QueryAdminAudit(ctx, s.db, adminauthz.AdminAuditFilter{
		TenantID: tenant, EntityType: r.EntityType, EntityID: r.EntityId, Action: r.Action,
		Operator: r.Operator, Since: since, Until: until, Cursor: r.Cursor, Limit: clampLimit(r.Limit),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "query admin audit: %v", err)
	}
	out := &adminv1.QueryAdminAuditLogResponse{NextCursor: next}
	for _, e := range entries {
		var tid uint64
		if e.TenantID.Valid {
			tid = uint64(e.TenantID.Int64)
		}
		var ver uint64
		if e.AdminVersion.Valid {
			ver = uint64(e.AdminVersion.Int64)
		}
		out.Entries = append(out.Entries, &adminv1.AdminAuditEntry{
			Id: uint64(e.ID), TenantId: tid, Operator: e.Operator, Action: e.Action,
			EntityType: e.EntityType, EntityId: nullStrToProto(e.EntityID), Diff: nullStrToProto(e.Diff),
			AdminVersion: ver, CreatedAt: e.CreatedAt.Format(time.RFC3339),
		})
	}
	return out, nil
}
```

- [ ] **步骤 5：运行测试 + 全 mgmt 包**

运行：`go test ./internal/controlplane/mgmt/ -v`
预期：PASS（含审计读测试、租户 scope；既有测试不回归）。

- [ ] **步骤 6：Commit**

```bash
git add internal/controlplane/mgmt/audit.go internal/controlplane/mgmt/authz.go internal/controlplane/mgmt/audit_test.go
git commit -m "feat(mgmt): QueryAuditLog/QueryAdminAuditLog handler + ruleTable(audit read, scopeApp/scopeTenant)"
```

---

## 任务 8：REST — 2 路由 + query 助手

**文件：**
- 修改：`internal/controlplane/restgw/routes.go`（appRoutes +1、systemRoutes +1、计数、query 助手）
- 测试：`internal/controlplane/restgw/routes_audit_test.go`

- [ ] **步骤 1：写失败测试**

`internal/controlplane/restgw/routes_audit_test.go`（沿用该包既有 REST 测试范式：起 httptest server + REST-HMAC 签名 + bufconn AdminServer；参照 `routes_decision_test.go`）：
```go
// GET /v1/apps/{app_id}/audit → 200 + app_id path 权威；越权 → 403。
func TestREST_AppAudit(t *testing.T) {
	// 复用该包既有 setup helper：种子 app + 授权 operator + 直插一条审计
	// 断言 200 且响应 JSON 含 entries；用错 app_id/无权 → 403。
}

// GET /v1/admin/audit?tenant_id=0 → 200（超管全量）。
func TestREST_AdminAudit(t *testing.T) {
	// 复用既有 setup；断言 200 且响应含 entries。
}
```
（测试主体按该包既有 REST 测试 helper 落实——参照 `routes_decision_test.go` 的 server 起停、签名、状态码断言写法。）

- [ ] **步骤 2：加 query 助手**

`internal/controlplane/restgw/routes.go` 在 `queryInt64`（第 65 行）之后加：
```go
// queryUint64 取可选 uint64 query（缺=0）。
func queryUint64(r *http.Request, key string) (uint64, error) {
	s := r.URL.Query().Get(key)
	if s == "" {
		return 0, nil
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, status.Errorf(codes.InvalidArgument, "invalid query %s", key)
	}
	return v, nil
}

// queryUint32 取可选 uint32 query（缺=0）。
func queryUint32(r *http.Request, key string) (uint32, error) {
	v, err := queryUint64(r, key)
	if err != nil {
		return 0, err
	}
	return uint32(v), nil
}
```

- [ ] **步骤 3：appRoutes 加 app 审计路由**

在 `appRoutes()` 返回的切片内（如 `GetEffectivePermissions` 路由之后、第 282 行附近）加：
```go
		{"GET", "/v1/apps/{app_id}/audit", pfx + "QueryAuditLog",
			func(r *http.Request, _ []byte) (proto.Message, error) {
				id, err := pathUint64(r, "app_id")
				if err != nil {
					return nil, err
				}
				cursor, err := queryUint64(r, "cursor")
				if err != nil {
					return nil, err
				}
				limit, err := queryUint32(r, "limit")
				if err != nil {
					return nil, err
				}
				q := r.URL.Query()
				return &adminv1.QueryAuditLogRequest{
					AppId: id, EntityType: q.Get("entity_type"), EntityId: q.Get("entity_id"),
					Action: q.Get("action"), Operator: q.Get("operator"),
					Since: q.Get("since"), Until: q.Get("until"), Cursor: cursor, Limit: limit,
				}, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.QueryAuditLog(ctx, m.(*adminv1.QueryAuditLogRequest))
			}},
```

- [ ] **步骤 4：systemRoutes 加 admin 审计路由**

在 `systemRoutes()` 返回切片内（如 `ResetOperatorSecret` 路由之后、第 583 行附近）加：
```go
		{"GET", "/v1/admin/audit", pfx + "QueryAdminAuditLog",
			func(r *http.Request, _ []byte) (proto.Message, error) {
				tenant, err := queryUint64(r, "tenant_id")
				if err != nil {
					return nil, err
				}
				cursor, err := queryUint64(r, "cursor")
				if err != nil {
					return nil, err
				}
				limit, err := queryUint32(r, "limit")
				if err != nil {
					return nil, err
				}
				q := r.URL.Query()
				return &adminv1.QueryAdminAuditLogRequest{
					TenantId: tenant, EntityType: q.Get("entity_type"), EntityId: q.Get("entity_id"),
					Action: q.Get("action"), Operator: q.Get("operator"),
					Since: q.Get("since"), Until: q.Get("until"), Cursor: cursor, Limit: limit,
				}, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.QueryAdminAuditLog(ctx, m.(*adminv1.QueryAdminAuditLogRequest))
			}},
```

- [ ] **步骤 5：更新计数注释**

`appRoutes` 注释「21 路由」→「22 路由」（第 67 行）；`systemRoutes`「10」→「11」；`allRoutes` 注释（第 587 行）「39 路由（app 域 21 + ... + system 域 10 + ...）」→「41 路由（app 域 22 + ... + system 域 11 + ...）」。若该包有路由数量断言测试（grep `len(allRoutes` 或计数测试），同步更新期望值。

- [ ] **步骤 6：运行测试验证通过**

运行：`go test ./internal/controlplane/restgw/ -v`
预期：PASS（审计路由 200/403/path 权威；既有路由不回归）。

- [ ] **步骤 7：Commit**

```bash
git add internal/controlplane/restgw/routes.go internal/controlplane/restgw/routes_audit_test.go
git commit -m "feat(restgw): REST 审计 2 路由(GET /apps/{id}/audit + /admin/audit, keyset query)"
```

---

## 任务 9：Console — app 审计页 + admin 审计页

**文件：**
- 创建：`internal/controlplane/console/routes_audit.go`
- 创建：`internal/controlplane/console/templates/audit.html`、`admin_audit.html`
- 修改：`internal/controlplane/console/routes_rbac.go:33`（注册 app 审计）
- 修改：`internal/controlplane/console/handler.go:32`（调 registerAudit）
- 修改：`internal/controlplane/console/templates/_appnav.html`（+审计 tab）
- 测试：`internal/controlplane/console/routes_audit_test.go`

- [ ] **步骤 1：写失败测试**

`internal/controlplane/console/routes_audit_test.go`（沿用该包既有 console 测试范式：起 handler + 注入会话 cookie；参照 `routes_decision_test.go`）：
```go
// app 审计页：无会话 → 302 登录；有会话 → 200 渲染 feed。
func TestConsole_AppAudit_RequiresSessionThenRenders(t *testing.T) {
	// 复用既有 setup：无 cookie GET /apps/{id}/audit → 302
	// 带会话 + 直插审计 → 200 且 body 含审计动作文本
}

// admin 审计页：有会话(超管) → 200。
func TestConsole_AdminAudit_Renders(t *testing.T) {
	// 复用既有 setup：带超管会话 GET /admin/audit → 200
}
```
（测试主体参照 `routes_decision_test.go` 的 session 注入与状态码断言。）

- [ ] **步骤 2：实现 routes_audit.go**

`internal/controlplane/console/routes_audit.go`：
```go
package console

import (
	"net/http"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
)

// registerAudit 注册审计页：app 域(appnav tab) + admin 域(系统区)。
func (h *Handler) registerAudit(mux *http.ServeMux) {
	mux.HandleFunc("GET /apps/{app_id}/audit", h.appAudit)
	mux.HandleFunc("GET /admin/audit", h.adminAudit)
}

// appAudit：app 审计 feed（读）。可选 ?entity_type=&entity_id=&action=&cursor= 过滤/翻页。
// 鉴权：QueryAuditLog（scopeApp）；拒绝走 renderGRPCError（降级无枚举，不泄露存在性）。
func (h *Handler) appAudit(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	appID, err := pathUint64(r, "app_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"QueryAuditLog", err)
		return
	}
	cursor, err := formUint64(r, "cursor")
	if err != nil {
		h.renderGRPCError(w, r, svc+"QueryAuditLog", err)
		return
	}
	q := r.URL.Query()
	msg := &adminv1.QueryAuditLogRequest{
		AppId: appID, EntityType: q.Get("entity_type"), EntityId: q.Get("entity_id"),
		Action: q.Get("action"), Cursor: cursor, Limit: 50,
	}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"QueryAuditLog", principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"QueryAuditLog", err)
		return
	}
	resp, err := h.srv.QueryAuditLog(ctx, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"QueryAuditLog", err)
		return
	}
	h.renderPage(w, r, "audit.html", http.StatusOK, map[string]any{
		"Nav": "apps", "AppID": appID, "Tab": "audit", "CSRF": sess.CSRF,
		"Entries": resp.Entries, "NextCursor": resp.NextCursor,
		"EntityType": q.Get("entity_type"), "EntityID": q.Get("entity_id"), "Action": q.Get("action"),
	})
}

// adminAudit：admin 审计 feed（读，系统/租户区）。?tenant_id=（默认 0=超管全量）。
// 鉴权：QueryAdminAuditLog（scopeTenant）；拒绝即 403（renderGRPCError），绝不降级。
func (h *Handler) adminAudit(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	tenant, err := formUint64(r, "tenant_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"QueryAdminAuditLog", err)
		return
	}
	cursor, err := formUint64(r, "cursor")
	if err != nil {
		h.renderGRPCError(w, r, svc+"QueryAdminAuditLog", err)
		return
	}
	q := r.URL.Query()
	msg := &adminv1.QueryAdminAuditLogRequest{
		TenantId: tenant, EntityType: q.Get("entity_type"), EntityId: q.Get("entity_id"),
		Action: q.Get("action"), Cursor: cursor, Limit: 50,
	}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"QueryAdminAuditLog", principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"QueryAdminAuditLog", err)
		return
	}
	resp, err := h.srv.QueryAdminAuditLog(ctx, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"QueryAdminAuditLog", err)
		return
	}
	h.renderPage(w, r, "admin_audit.html", http.StatusOK, map[string]any{
		"Nav": "system", "CSRF": sess.CSRF, "TenantID": tenant,
		"Entries": resp.Entries, "NextCursor": resp.NextCursor,
	})
}
```
> 若该包已有 `formUint64` 助手用之；否则在 `forms.go` 加（镜像 `formInt64`）：
```go
func formUint64(r *http.Request, key string) (uint64, error) {
	s := r.FormValue(key)
	if s == "" {
		return 0, nil
	}
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, status.Errorf(codes.InvalidArgument, "invalid %s", key)
	}
	return v, nil
}
```

- [ ] **步骤 3：注册路由**

- `routes_rbac.go` 第 33 行（`decisionExplainer` 注册）后加：不在此处——app 审计走 `registerAudit`。改为：在 `handler.go` `registerOps(mux)`（第 32 行）后加 `h.registerAudit(mux)`。
- 即 `handler.go`：
```go
	h.registerOps(mux)        // M1.4 运营台
	h.registerAudit(mux)      // M2.3 审计页(app + admin)
```

- [ ] **步骤 4：appnav 加审计 tab**

`internal/controlplane/console/templates/_appnav.html` 在「决策解释」`<a>`（决策 tab）之后加：
```html
<a href="/apps/{{.AppID}}/audit" {{if eq .Tab "audit"}}class="active"{{end}}>审计</a>
```

- [ ] **步骤 5：写模板 audit.html**

`internal/controlplane/console/templates/audit.html`（参照 `grants.html`/`decision.html` 的 layout/appnav 包裹与表格范式）：
```html
{{define "content"}}
{{template "appnav" .}}
<main class="content">
<h1>审计 · App #{{.AppID}}</h1>
<form method="get" class="filterbar">
  <input name="entity_type" value="{{.EntityType}}" placeholder="实体类型(role/data_policy...)">
  <input name="entity_id" value="{{.EntityID}}" placeholder="实体 id(变更历史)">
  <input name="action" value="{{.Action}}" placeholder="动作">
  <button type="submit">过滤</button>
</form>
<table>
  <thead><tr><th>时间</th><th>操作者</th><th>动作</th><th>实体</th><th>版本</th><th>变更</th></tr></thead>
  <tbody>
  {{range .Entries}}
    <tr>
      <td>{{.CreatedAt}}</td><td>{{.Operator}}</td><td>{{.Action}}</td>
      <td>{{.EntityType}}{{if .EntityId}} #{{.EntityId}}{{end}}</td>
      <td>v{{.Version}}</td>
      <td><pre class="diff">{{.Diff}}</pre></td>
    </tr>
  {{else}}
    <tr><td colspan="6">无审计记录</td></tr>
  {{end}}
  </tbody>
</table>
{{if .NextCursor}}
  <a class="pager" href="/apps/{{.AppID}}/audit?entity_type={{.EntityType}}&entity_id={{.EntityID}}&action={{.Action}}&cursor={{.NextCursor}}">下一页 →</a>
{{end}}
</main>
{{end}}
```

- [ ] **步骤 6：写模板 admin_audit.html**

`internal/controlplane/console/templates/admin_audit.html`（参照 `operators.html` 系统区范式，Nav:system）：
```html
{{define "content"}}
<main class="content">
<h1>管理审计</h1>
<form method="get" class="filterbar">
  <input name="tenant_id" value="{{if .TenantID}}{{.TenantID}}{{end}}" placeholder="租户 id(0=全部)">
  <button type="submit">过滤</button>
</form>
<table>
  <thead><tr><th>时间</th><th>租户</th><th>操作者</th><th>动作</th><th>实体</th><th>变更</th></tr></thead>
  <tbody>
  {{range .Entries}}
    <tr>
      <td>{{.CreatedAt}}</td>
      <td>{{if .TenantId}}t:{{.TenantId}}{{else}}系统{{end}}</td>
      <td>{{.Operator}}</td><td>{{.Action}}</td>
      <td>{{.EntityType}}{{if .EntityId}} #{{.EntityId}}{{end}}</td>
      <td><pre class="diff">{{.Diff}}</pre></td>
    </tr>
  {{else}}
    <tr><td colspan="6">无审计记录</td></tr>
  {{end}}
  </tbody>
</table>
{{if .NextCursor}}
  <a class="pager" href="/admin/audit?tenant_id={{.TenantID}}&cursor={{.NextCursor}}">下一页 →</a>
{{end}}
</main>
{{end}}
```
> 模板经 `//go:embed` 自动发现（参照该包 `mustTemplates()` 装配；若需在 layout 顶部导航加「管理审计」入口链接，仿 operators 入口加一行）。

- [ ] **步骤 7：运行测试验证通过**

运行：`go test ./internal/controlplane/console/ -v`
预期：PASS（审计页 302/200/渲染；既有页不回归）。

- [ ] **步骤 8：Commit**

```bash
git add internal/controlplane/console/
git commit -m "feat(console): 审计页(app appnav tab + admin 系统区, keyset 翻页, diff 结构化)"
```

---

## 任务 10：全量验证 + adminauthz 零触碰核验 + 整体安全评审

**文件：** 无新增代码（仅验证 + 收尾文档/记忆）

- [ ] **步骤 1：AUD-4 adminauthz matcher 零触碰核验**

运行：`git diff <base>..HEAD -- internal/controlplane/adminauthz/enforcer.go`
预期：**0 行**（matcher/enforcer 一字未改；本里程碑仅在 adminauthz 包**新增** audit.go，未碰 enforcer.go）。
运行：`git diff <base>..HEAD -- internal/controlplane/mgmt/authz.go | grep '^[-+]' | grep -v '^[-+][-+]'`
预期：仅 `ruleTable` +2 行（audit read scopeApp/scopeTenant），无其它改动。

- [ ] **步骤 2：格式 + 静态检查 + proto 漂移**

运行：`gofmt -l internal/ && go vet ./... && make proto-check`
预期：`gofmt -l` 无输出；`go vet` 干净；`make proto-check` 无漂移。

- [ ] **步骤 3：全量测试（含 dbtest testcontainers）**

运行：`go test ./...`
预期：全包 0 FAIL（含 db / store / adminauthz / policy / mgmt / restgw / console / e2e）。

- [ ] **步骤 4：整体安全评审（AUD-1..AUD-7 逐条）**

以 spec §8 为清单逐条核验并记录证据：
- AUD-1 原子：`TestCreateApplication_AuditAtomic` 等证审计失败动作回滚；10 个 handler 审计均在同 tx commit 前。
- AUD-2 secret：`TestRotateApplicationSecret_AuditsWithoutSecret` + 通读 diff 构造点，确认 Rotate/Reset/Create/Register/Invite 的 diff payload 无任何 secret 字段。
- AUD-3 隔离：QueryAdminAudit WHERE 与 scopeTenant 锁步（tenant_id=0→全量仅超管可达；非 0→WHERE tenant_id）；QueryAppAudit 经 scopeApp + TenantDomainOf。端到端越权 → 403。
- AUD-4 真相：步骤 1 证 enforcer.go diff=0、ruleTable +2。
- AUD-5 读纯净：`TestQueryAuditLog_ReadsAndNoBump` 证读不 bump。
- AUD-6 保真：app diff 含 adds/removes/data_changes；system diff 含 before/after。
- AUD-7 数据面零影响：`git diff <base>..HEAD -- internal/sidecar/` = 0 行；审计不入 sync/translate。

- [ ] **步骤 5：更新进度记忆**

更新 `/home/tongyu/.claude/projects/-home-tongyu-codes-Sydom/memory/project_detailed_design_progress.md`（追加 M2.3 节）+ `MEMORY.md` 索引指针行（M2.3 完成 + 关键涌现）。

- [ ] **步骤 6：FF 合并到本地 main（不 push origin）**

按 M2.x 范式：worktree 内全绿 + 评审 READY 后，FF 合并回本地 main，清 worktree，**origin 不 push**。

---

## 自检记录

**规格覆盖度（对照 spec 各节）：**
- §3.1 admin_audit_log 表 → 任务 1 ✓；§3.2 policy_audit_log 不动 + InsertAudit 加 diff → 任务 3 ✓
- §4.1 InsertAdminAudit → 任务 4 ✓；§4.2 各 handler 落审计（含 4 个包 tx、diff 表、tenant 推导、bump 后读版本）→ 任务 5 ✓
- §5.1 proto 契约 → 任务 2 ✓；§5.2 keyset 分页 + §5.3 store 查询 → 任务 6 ✓
- §6 ruleTable 2 行（audit/scopeApp/scopeTenant）→ 任务 7 ✓
- §7 三面：gRPC handler 任务 7 ✓ / REST 任务 8 ✓ / Console 任务 9 ✓
- §8 AUD-1..AUD-7 → 全程 + 任务 10 逐条核验 ✓
- §9 错误处理（审计失败回滚、空结果非错误、时间非法 InvalidArgument）→ 任务 5/7 ✓
- §10 测试策略 → 各任务 TDD + 任务 10 全量 ✓

**类型一致性：** `InsertAudit`(+diff []byte before version)、`InsertAdminAudit`(sql.NullInt64 tenant/version)、`AppAuditFilter`/`AppAuditEntry`/`AdminAuditFilter`/`AdminAuditEntry`、`QueryAppAudit`/`QueryAdminAudit`(返回 entries+nextCursor+err) 在任务 3/4/5/6/7 间逐字一致；proto 类型名 `QueryAuditLogRequest`/`AuditEntry`/`QueryAdminAuditLogRequest`/`AdminAuditEntry` 在任务 2/7/8/9 间一致。

**占位符扫描：** 无 TODO/待定；REST/Console 测试主体明确指向复用该包既有 helper（routes_decision_test.go 范式）——非占位，是遵循「不重复既有 setup 样板」的现实约束。
