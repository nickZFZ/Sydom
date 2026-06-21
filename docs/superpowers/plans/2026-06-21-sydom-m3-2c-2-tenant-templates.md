# M3.2c-2 租户自有模板 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 让租户把一个已配好的 app 的整套授权模型（权限点 + 业务角色 + 授权 + 角色数据范围）一键存为租户私有可复用模板，再 apply/克隆到本租户其他 app。

**架构：** 新表 `tenant_template`（tenant 私有、单 JSONB blob 镜像 preset 形状）；`tenanttemplate` 包从 app 捕获 bundle（读 perms/roles/grants/role data_scopes，排除 user 绑定/凭据）；apply 复用同一 `policy.ApplyTemplate` 引擎（确定性 code `tpl:tt-<id>:<key>`）；5 个 AdminService RPC + REST + Console「我的模板」区，全 tenant-scoped、跨租户 fail-close。纯控制面写 + 数据面经既有引擎同步。

**技术栈：** Go、PostgreSQL（JSONB）、protobuf（buf）、testcontainers PG、`html/template`、既有 `policy.ApplyTemplate` / `store` / `mgmt.AuthorizeRule` / M3.2c-1 `conditionPredicate`。

**基准：** spec `docs/superpowers/specs/2026-06-21-sydom-m3-2c-2-tenant-templates-design.md`。本计划在 off-main worktree 执行。

**关键不变量（TT-1..8，贯穿全程）：** TT-1 一份授权真相（复用 AuthorizeRule+唯一 ruleTable，adminauthz/M1.1 matcher diff=0，仅 ruleTable 追加）/ TT-2 租户隔离（tenant_id 闸，跨租户 fail-close 不泄露存在性）/ TT-3 apply 一致（复用 policy.ApplyTemplate 无第二套）/ TT-4 捕获保真（排除 user 绑定/凭据）/ TT-5 符号口径（预览谓词不枚举）/ TT-6 secret 不泄露 / TT-7 sidecar 零漂移 / TT-8 fail-close（未知 template→NotFound、bundle 解析失败拒绝、任一步失败整事务回滚）。

---

## 文件结构

- 创建 `db/migrations/000018_tenant_template.up.sql` / `.down.sql` — 新表。
- 创建 `internal/controlplane/store/tenant_template.go` — tenant_template CRUD（Insert/Get/List/Delete，tenant-scoped）。
- 创建 `internal/controlplane/store/tenant_template_test.go`。
- 创建 `internal/controlplane/tenanttemplate/bundle.go` — Bundle 类型 + `Capture(db, appID)` 全模型捕获 + 安全 key 派生。
- 创建 `internal/controlplane/tenanttemplate/bundle_test.go`。
- 修改 `api/proto/sydom/admin/v1/admin.proto` + regen `gen/` — 5 RPC + message。
- 创建 `internal/controlplane/mgmt/tenant_templates.go` — 5 handler + bundle↔proto/apply 转换。
- 创建 `internal/controlplane/mgmt/tenant_templates_test.go`。
- 修改 `internal/controlplane/mgmt/authz.go` — ruleTable +5 条。
- 创建 `internal/controlplane/restgw/routes_tenant_templates.go`（或并入既有 routes）+ test — 5 REST 路由。
- 创建 `internal/controlplane/console/routes_tenant_templates.go` + test — 「我的模板」区 + 「存为模板」入口。
- 修改 `internal/controlplane/console/templates/ops_templates.html` + 新模板 — 渲染。
- 修改 `internal/controlplane/console/handler.go`（或路由注册处）— 注册新路由。

---

## 任务 1：migration + store CRUD（tenant_template，tenant-scoped）

**文件：**
- 创建：`db/migrations/000018_tenant_template.up.sql`、`db/migrations/000018_tenant_template.down.sql`
- 创建：`internal/controlplane/store/tenant_template.go`、`internal/controlplane/store/tenant_template_test.go`

- [ ] **步骤 1：写迁移**

`000018_tenant_template.up.sql`：
```sql
CREATE TABLE tenant_template (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id     BIGINT       NOT NULL,
    name          VARCHAR(128) NOT NULL,
    description   VARCHAR(512),
    bundle        JSONB        NOT NULL,
    source_app_id BIGINT,
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT fk_tenant_template_tenant FOREIGN KEY (tenant_id) REFERENCES tenant(id),
    CONSTRAINT uq_tenant_template_name   UNIQUE (tenant_id, name)
);
CREATE INDEX idx_tenant_template_tenant ON tenant_template (tenant_id);
```
`000018_tenant_template.down.sql`：
```sql
DROP TABLE IF EXISTS tenant_template;
```

- [ ] **步骤 2：写失败测试 `tenant_template_test.go`**

```go
package store_test

import (
	"context"
	"testing"

	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestTenantTemplateCRUD(t *testing.T) {
	db := dbtest.SetupSchema(t)
	tID, appID := dbtest.SeedAppInTenant(t, db, "t-a", "dom-a", "AK_a")
	ctx := context.Background()
	bundle := []byte(`{"permissions":[],"roles":[]}`)

	id, err := store.InsertTenantTemplate(ctx, db, tID, "标准后台", "通用", bundle, appID)
	require.NoError(t, err)
	require.NotZero(t, id)

	// 同租户重名→ErrConflict。
	_, err = store.InsertTenantTemplate(ctx, db, tID, "标准后台", "x", bundle, appID)
	require.ErrorIs(t, err, store.ErrConflict)

	// Get（tenant-scoped）。
	got, err := store.GetTenantTemplate(ctx, db, tID, id)
	require.NoError(t, err)
	require.Equal(t, "标准后台", got.Name)
	require.JSONEq(t, `{"permissions":[],"roles":[]}`, string(got.Bundle))

	// 跨租户 Get→ErrNotFound。
	tB, _ := dbtest.SeedAppInTenant(t, db, "t-b", "dom-b", "AK_b")
	_, err = store.GetTenantTemplate(ctx, db, tB, id)
	require.ErrorIs(t, err, store.ErrNotFound)

	// List（tenant-scoped）。
	rows, total, err := store.ListTenantTemplates(ctx, db, tID, 50, 0, "id", "ASC", "")
	require.NoError(t, err)
	require.Equal(t, uint32(1), total)
	require.Len(t, rows, 1)

	// Delete（tenant-scoped；跨租户→ErrNotFound）。
	require.ErrorIs(t, store.DeleteTenantTemplate(ctx, db, tB, id), store.ErrNotFound)
	require.NoError(t, store.DeleteTenantTemplate(ctx, db, tID, id))
	_, err = store.GetTenantTemplate(ctx, db, tID, id)
	require.ErrorIs(t, err, store.ErrNotFound)
}
```

> 先 Read 既有 `store` 包：确认 `ErrNotFound` 哨兵是否已存在（M2.1 引入 `ErrNotFound`）。`ErrConflict` 若无则在本任务新增（`var ErrConflict = errors.New("store: conflict")`）。`dbtest.SeedAppInTenant(t, db, tenant, domain, appKey) (tenantID, appID int64)` 是既有夹具（见 routes_accounts_test.go 用法）。

- [ ] **步骤 3：运行验证失败**

`go test ./internal/controlplane/store/ -run TestTenantTemplateCRUD -count=1` → FAIL（函数未定义）。

- [ ] **步骤 4：实现 `tenant_template.go`**

```go
package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/lib/pq"
)

// TenantTemplate 是一条租户自有模板（bundle 为 JSON blob，协议层不透明）。
type TenantTemplate struct {
	ID          int64
	TenantID    int64
	Name        string
	Description string
	Bundle      []byte
	SourceAppID int64
}

// InsertTenantTemplate 写入一条租户模板；同租户重名→ErrConflict。
func InsertTenantTemplate(ctx context.Context, ex cp.DBTX, tenantID int64, name, desc string, bundle []byte, sourceAppID int64) (int64, error) {
	var id int64
	err := ex.QueryRowContext(ctx,
		`INSERT INTO tenant_template (tenant_id, name, description, bundle, source_app_id)
		 VALUES ($1,$2,$3,$4,$5) RETURNING id`,
		tenantID, name, desc, bundle, sourceAppID).Scan(&id)
	var pqErr *pq.Error
	if errors.As(err, &pqErr) && pqErr.Code == "23505" { // unique_violation
		return 0, ErrConflict
	}
	if err != nil {
		return 0, err
	}
	return id, nil
}

// GetTenantTemplate 按 (tenant_id, id) 取模板；不存在/跨租户→ErrNotFound。
func GetTenantTemplate(ctx context.Context, ex cp.DBTX, tenantID, id int64) (TenantTemplate, error) {
	var t TenantTemplate
	var srcApp sql.NullInt64
	err := ex.QueryRowContext(ctx,
		`SELECT id, tenant_id, name, COALESCE(description,''), bundle, source_app_id
		 FROM tenant_template WHERE tenant_id=$1 AND id=$2`, tenantID, id).
		Scan(&t.ID, &t.TenantID, &t.Name, &t.Description, &t.Bundle, &srcApp)
	if errors.Is(err, sql.ErrNoRows) {
		return TenantTemplate{}, ErrNotFound
	}
	if err != nil {
		return TenantTemplate{}, err
	}
	t.SourceAppID = srcApp.Int64
	return t, nil
}

// ListTenantTemplates 列出某租户模板（分页/搜索/排序），返回行与 total。
func ListTenantTemplates(ctx context.Context, ex cp.DBTX, tenantID int64, limit, offset int, orderCol, orderDir, q string) ([]TenantTemplate, uint32, error) {
	conds := []string{"tenant_id = $1"}
	args := []any{tenantID}
	if q != "" {
		args = append(args, "%"+q+"%")
		conds = append(conds, "(name ILIKE $"+strconv.Itoa(len(args))+" OR description ILIKE $"+strconv.Itoa(len(args))+")")
	}
	where := strings.Join(conds, " AND ")
	var total uint32
	if err := ex.QueryRowContext(ctx, `SELECT count(*) FROM tenant_template WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	rows, err := ex.QueryContext(ctx,
		`SELECT id, tenant_id, name, COALESCE(description,''), source_app_id FROM tenant_template WHERE `+where+
			` ORDER BY `+orderCol+` `+orderDir+` LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []TenantTemplate
	for rows.Next() {
		var t TenantTemplate
		var srcApp sql.NullInt64
		if err := rows.Scan(&t.ID, &t.TenantID, &t.Name, &t.Description, &srcApp); err != nil {
			return nil, 0, err
		}
		t.SourceAppID = srcApp.Int64
		out = append(out, t)
	}
	return out, total, rows.Err()
}

// DeleteTenantTemplate 删某租户模板；不存在/跨租户→ErrNotFound。
func DeleteTenantTemplate(ctx context.Context, ex cp.DBTX, tenantID, id int64) error {
	res, err := ex.ExecContext(ctx, `DELETE FROM tenant_template WHERE tenant_id=$1 AND id=$2`, tenantID, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
```

> 注：`orderCol`/`orderDir` 由调用方（mgmt handler）经白名单 `resolveOrder` 产出受控标识符，**不可**直接拼用户输入（LS-2 注入防护）。本 store 函数假定上游已白名单化。若 `store` 包未引入 `github.com/lib/pq`，确认 go.mod 已有（既有 pq 坑见 M2.3）。`ErrConflict` 若新增放本文件顶部 `var ErrConflict = errors.New("store: conflict")`。

- [ ] **步骤 5：运行测试 + 构建**

`go test ./internal/controlplane/store/ -run TestTenantTemplateCRUD -count=1 2>&1 | tail -8`（PASS）；`go build ./...`、`gofmt -l internal/controlplane/store/`（空）。

- [ ] **步骤 6：Commit**

```bash
git add db/migrations/000018_tenant_template.* internal/controlplane/store/tenant_template.go internal/controlplane/store/tenant_template_test.go
git commit -m "feat(store): tenant_template 表(000018)+CRUD(tenant-scoped/重名 ErrConflict/跨租户 ErrNotFound)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 2：bundle 捕获（tenanttemplate 包，全模型 + 安全 key）

**文件：**
- 创建：`internal/controlplane/tenanttemplate/bundle.go`、`internal/controlplane/tenanttemplate/bundle_test.go`

- [ ] **步骤 1：写失败测试 `bundle_test.go`**

```go
package tenanttemplate_test

import (
	"context"
	"testing"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/controlplane/tenanttemplate"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestCapture_FullModel(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	ctx := context.Background()

	// 配好一个 app：权限点 + 角色 + 授权 + 角色数据范围（用既有 store 写入）。
	// （实现者：用既有 store.UpsertAutoPermission / UpsertTemplateRole / InsertRolePermission /
	//  UpsertDataPolicy 在版本 1 下种入一条 order.read 权限、一个 cs 角色授权该权限、一条 role 数据范围；
	//  并写一条 user 主体 data_policy + 一条 user_role_binding 作为「应被排除」的对照。
	//  照搬 policy/manager_apply_template_test.go 与 dataperm 测试的既有写法。）
	seedConfiguredApp(t, db, appID)

	b, err := tenanttemplate.Capture(ctx, db, appID)
	require.NoError(t, err)
	require.Len(t, b.Permissions, 1)
	require.Equal(t, "order.read", b.Permissions[0].Code)
	require.Len(t, b.Roles, 1)
	r := b.Roles[0]
	require.NotContains(t, r.Key, ":") // 安全 key：去 ':'（ApplyTemplate 拒 key 含 ':'）
	require.Contains(t, r.PermissionCodes, "order.read")
	require.Len(t, r.DataScopes, 1)
	require.Equal(t, "order", r.DataScopes[0].Resource)
	require.JSONEq(t, `{"field":"department","op":"EQ","value":"$user.department"}`, string(r.DataScopes[0].Condition))
	// TT-4：排除 user 主体数据策略（bundle 只含 role 数据范围）。
	for _, dr := range b.Roles {
		_ = dr
	}
}

func TestSafeKey_DropsColonAndDedups(t *testing.T) {
	seen := map[string]bool{}
	require.Equal(t, "tpl_x_editor", tenanttemplate.SafeKey("tpl:x:editor", seen))
	require.Equal(t, "tpl_x_editor_2", tenanttemplate.SafeKey("tpl:x:editor", seen)) // 同名去重
}
```

> `seedConfiguredApp` 是本测试文件内的小助手，照搬既有 store 写法种入对照数据（含一条 user 主体 data_policy + 一条 user_role_binding 作排除对照）。实现者按既有 `store` 函数签名编写。

- [ ] **步骤 2：运行验证失败**

`go test ./internal/controlplane/tenanttemplate/ -count=1` → FAIL（包/函数未定义）。

- [ ] **步骤 3：实现 `bundle.go`**

```go
// Package tenanttemplate 把一个 app 的整套授权模型捕获为可复用 bundle（租户自有模板内容）。
// 捕获=纯读：权限点(auto+manual) + 业务角色 + 角色授权 + 角色数据范围；排除 user 绑定/凭据。
package tenanttemplate

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
)

// maxBundleRoleKey 限制 bundle role key 长度，使 apply 期确定性 code `tpl:tt-<id>:<key>` 不超 role.code 列宽(64)。
const maxBundleRoleKey = 40

type Bundle struct {
	Permissions []BundlePermission `json:"permissions"`
	Roles       []BundleRole       `json:"roles"`
}

type BundlePermission struct {
	Code        string `json:"code"`
	Resource    string `json:"resource"`
	Action      string `json:"action"`
	Type        string `json:"type"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type BundleRole struct {
	Key             string            `json:"key"`
	Name            string            `json:"name"`
	Description     string            `json:"description"`
	PermissionCodes []string          `json:"permission_codes"`
	DataScopes      []BundleDataScope `json:"data_scopes"`
}

type BundleDataScope struct {
	Resource  string          `json:"resource"`
	Effect    string          `json:"effect"`
	Condition json.RawMessage `json:"condition"`
}

// SafeKey 把角色 code 派生为合法 bundle key：去 ':'（ApplyTemplate 拒含 ':'），限长，去重。
func SafeKey(code string, seen map[string]bool) string {
	k := strings.ReplaceAll(code, ":", "_")
	if k == "" {
		k = "role"
	}
	if len(k) > maxBundleRoleKey {
		k = k[:maxBundleRoleKey]
	}
	base := k
	for n := 2; seen[k]; n++ {
		k = base + "_" + strconv.Itoa(n)
	}
	seen[k] = true
	return k
}

// Capture 读取 appID 全部授权模型，组装为 Bundle（不改 app，不下发）。
func Capture(ctx context.Context, db cp.DBTX, appID int64) (Bundle, error) {
	var b Bundle

	// 1. 权限点（auto+manual）。
	prows, err := db.QueryContext(ctx,
		`SELECT code, resource, action, type, name, COALESCE(description,'') FROM permission WHERE app_id=$1 ORDER BY code`, appID)
	if err != nil {
		return Bundle{}, err
	}
	for prows.Next() {
		var p BundlePermission
		if err := prows.Scan(&p.Code, &p.Resource, &p.Action, &p.Type, &p.Name, &p.Description); err != nil {
			prows.Close()
			return Bundle{}, err
		}
		b.Permissions = append(b.Permissions, p)
	}
	prows.Close()
	if err := prows.Err(); err != nil {
		return Bundle{}, err
	}

	// 2. 角色数据范围：subject_type='role' → 按 role code 分组。
	dps, err := store.ReadAppDataPolicies(ctx, db, appID)
	if err != nil {
		return Bundle{}, err
	}
	scopesByRole := map[string][]BundleDataScope{}
	for _, dp := range dps {
		if dp.SubjectType != "role" {
			continue // 排除 user 主体数据策略（TT-4）
		}
		scopesByRole[dp.SubjectID] = append(scopesByRole[dp.SubjectID], BundleDataScope{
			Resource: dp.Resource, Effect: dp.Effect, Condition: json.RawMessage(dp.Condition),
		})
	}

	// 3. 角色 + 授权。
	rrows, err := db.QueryContext(ctx,
		`SELECT id, code, name, COALESCE(description,'') FROM role WHERE app_id=$1 ORDER BY id`, appID)
	if err != nil {
		return Bundle{}, err
	}
	type roleRow struct {
		id          int64
		code, name  string
		description string
	}
	var roles []roleRow
	for rrows.Next() {
		var r roleRow
		if err := rrows.Scan(&r.id, &r.code, &r.name, &r.description); err != nil {
			rrows.Close()
			return Bundle{}, err
		}
		roles = append(roles, r)
	}
	rrows.Close()
	if err := rrows.Err(); err != nil {
		return Bundle{}, err
	}

	seen := map[string]bool{}
	for _, r := range roles {
		grows, err := db.QueryContext(ctx,
			`SELECT p.code FROM role_permission rp JOIN permission p ON p.id=rp.permission_id
			 WHERE rp.app_id=$1 AND rp.role_id=$2 ORDER BY p.code`, appID, r.id)
		if err != nil {
			return Bundle{}, err
		}
		var codes []string
		for grows.Next() {
			var c string
			if err := grows.Scan(&c); err != nil {
				grows.Close()
				return Bundle{}, err
			}
			codes = append(codes, c)
		}
		grows.Close()
		if err := grows.Err(); err != nil {
			return Bundle{}, err
		}
		b.Roles = append(b.Roles, BundleRole{
			Key: SafeKey(r.code, seen), Name: r.name, Description: r.description,
			PermissionCodes: codes, DataScopes: scopesByRole[r.code],
		})
	}
	return b, nil
}
```

> 注：`cp.DataPolicy.Condition` 是 `string`（JSONB 读出的 JSON 串）；`json.RawMessage(dp.Condition)` 原样透传（DSC-3）。`SafeKey` 限长 40 + `tpl:tt-<id>:`（≤约 13）保证最终 code ≤64。捕获不读 `user_role_binding`、不读任何 secret 列（TT-4/TT-6）。

- [ ] **步骤 4：运行测试 + 构建**

`go test ./internal/controlplane/tenanttemplate/ -count=1 2>&1 | tail -12`（PASS）；`go build ./...`、`gofmt -l internal/controlplane/tenanttemplate/`（空）、`go vet ./internal/controlplane/tenanttemplate/`。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/tenanttemplate/
git commit -m "feat(tenanttemplate): 从 app 捕获全授权模型 bundle(perms+roles+grants+role data_scopes/排除 user 绑定/安全 key 去冒号限长)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 3：proto — 5 RPC + message + 生成代码

**文件：**
- 修改：`api/proto/sydom/admin/v1/admin.proto`
- 生成：`gen/`

- [ ] **步骤 1：在 `admin.proto` 加 RPC（AdminService service 块内）**

```proto
  rpc SaveAppAsTemplate(SaveAppAsTemplateRequest) returns (TenantTemplateRef);
  rpc ListTenantTemplates(ListTenantTemplatesRequest) returns (ListTenantTemplatesResponse);
  rpc GetTenantTemplate(GetTenantTemplateRequest) returns (TenantTemplate);
  rpc ApplyTenantTemplate(ApplyTenantTemplateRequest) returns (ApplyTemplateResponse);
  rpc DeleteTenantTemplate(DeleteTenantTemplateRequest) returns (WriteResponse);
```

- [ ] **步骤 2：加 message（放在 Template/ApplyTemplate message 附近）**

```proto
message SaveAppAsTemplateRequest {
  uint64 app_id = 1;
  string name = 2;
  string description = 3;
}
message TenantTemplateRef {
  uint64 id = 1;
  string name = 2;
}
message ListTenantTemplatesRequest {
  uint64 tenant_id = 1;
  ListPage page = 2;
}
message TenantTemplateSummary {
  uint64 id = 1;
  string name = 2;
  string description = 3;
  uint64 source_app_id = 4;
}
message ListTenantTemplatesResponse {
  repeated TenantTemplateSummary templates = 1;
  uint32 total = 2;
}
message GetTenantTemplateRequest {
  uint64 tenant_id = 1;
  uint64 template_id = 2;
}
message TenantTemplate {
  uint64 id = 1;
  string name = 2;
  string description = 3;
  uint64 source_app_id = 4;
  repeated TemplatePermission permissions = 5;
  repeated TemplateRole roles = 6;
}
message ApplyTenantTemplateRequest {
  uint64 app_id = 1;
  uint64 template_id = 2;
}
message DeleteTenantTemplateRequest {
  uint64 tenant_id = 1;
  uint64 template_id = 2;
}
```

> 复用既有 `TemplatePermission` / `TemplateRole`（含 `data_scopes`，M3.2c-1）/ `TemplateDataScope` / `ListPage`（M2.4）/ `WriteResponse` / `ApplyTemplateResponse`。`ApplyTenantTemplate` 复用 `ApplyTemplateResponse` 不违 lint（buf.yaml 已 except `RPC_REQUEST_RESPONSE_UNIQUE`）。

- [ ] **步骤 3：生成 + 校验**

`make proto-gen` → `make proto-check`（无漂移）→ `go build ./...`（AdminServer 经 Unimplemented embedding 编译；handler 下一任务加）。
> 若 lint 报新错，停下汇报，勿擅改 buf.yaml。

- [ ] **步骤 4：Commit**

```bash
git add api/proto/sydom/admin/v1/admin.proto gen/
git commit -m "feat(proto): 租户自有模板 5 RPC(Save/List/Get/Apply/Delete)+message(复用 ListPage/Template*/ApplyTemplateResponse/WriteResponse)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 4：mgmt SaveAppAsTemplate + DeleteTenantTemplate + ruleTable

**文件：**
- 创建：`internal/controlplane/mgmt/tenant_templates.go`、`internal/controlplane/mgmt/tenant_templates_test.go`
- 修改：`internal/controlplane/mgmt/authz.go`

- [ ] **步骤 1：ruleTable +5 条（`authz.go` 的 ruleTable map 内）**

```go
	"/sydom.admin.v1.AdminService/SaveAppAsTemplate":    {"template", "create", false, scopeApp},
	"/sydom.admin.v1.AdminService/ListTenantTemplates":  {"template", "read", false, scopeTenant},
	"/sydom.admin.v1.AdminService/GetTenantTemplate":    {"template", "read", false, scopeTenant},
	"/sydom.admin.v1.AdminService/ApplyTenantTemplate":  {"template", "apply", true, scopeApp},
	"/sydom.admin.v1.AdminService/DeleteTenantTemplate": {"template", "delete", false, scopeTenant},
```
> `SaveAppAsTemplate` isWrite=false（快照只读 app、不改其状态，停用 app 亦可快照）。`ApplyTenantTemplate` isWrite=true（写目标 app、受 status 闸）。Delete/List/Get scopeTenant 用请求 `tenant_id` 域。

- [ ] **步骤 2：写失败测试 `tenant_templates_test.go`**

```go
func TestSaveAppAsTemplate_CapturesAndDelete(t *testing.T) {
	db := dbtest.SetupSchema(t)
	tID, appID := dbtest.SeedAppInTenant(t, db, "t-a", "dom-a", "AK_a")
	srv := accountsSrv(db)
	ctx := context.Background()
	seedConfiguredApp(t, db, appID) // 同任务2：种 1 权限+1 角色授权+1 role 数据范围

	ref, err := srv.SaveAppAsTemplate(ctx, &adminv1.SaveAppAsTemplateRequest{
		AppId: uint64(appID), Name: "标准后台", Description: "通用"})
	require.NoError(t, err)
	require.NotZero(t, ref.Id)

	// 重名→AlreadyExists。
	_, err = srv.SaveAppAsTemplate(ctx, &adminv1.SaveAppAsTemplateRequest{AppId: uint64(appID), Name: "标准后台"})
	require.Equal(t, codes.AlreadyExists, status.Code(err))

	// Delete（tenant-scoped）。
	_, err = srv.DeleteTenantTemplate(ctx, &adminv1.DeleteTenantTemplateRequest{TenantId: uint64(tID), TemplateId: ref.Id})
	require.NoError(t, err)
	// 再删→NotFound。
	_, err = srv.DeleteTenantTemplate(ctx, &adminv1.DeleteTenantTemplateRequest{TenantId: uint64(tID), TemplateId: ref.Id})
	require.Equal(t, codes.NotFound, status.Code(err))
}
```
> `accountsSrv(db)` / `seedConfiguredApp` 照搬既有；`SeedAppInTenant` 返回 (tenantID, appID)。

- [ ] **步骤 3：运行验证失败** → `go test ./internal/controlplane/mgmt/ -run TestSaveAppAsTemplate -count=1`（FAIL）。

- [ ] **步骤 4：实现 handler（`tenant_templates.go`）**

```go
package mgmt

import (
	"context"
	"encoding/json"
	"errors"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/controlplane/tenanttemplate"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SaveAppAsTemplate 捕获源 app 全模型存为本租户模板。
func (s *AdminServer) SaveAppAsTemplate(ctx context.Context, r *adminv1.SaveAppAsTemplateRequest) (*adminv1.TenantTemplateRef, error) {
	if r.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name required")
	}
	var tenantID int64
	if err := s.db.QueryRowContext(ctx, `SELECT tenant_id FROM application WHERE id=$1`, int64(r.AppId)).Scan(&tenantID); err != nil {
		return nil, status.Errorf(codes.Internal, "resolve tenant: %v", err) // 注：scopeApp 已校验 app 存在且有权
	}
	b, err := tenanttemplate.Capture(ctx, s.db, int64(r.AppId))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "capture: %v", err)
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "marshal bundle: %v", err)
	}
	id, err := store.InsertTenantTemplate(ctx, s.db, tenantID, r.Name, r.Description, raw, int64(r.AppId))
	if errors.Is(err, store.ErrConflict) {
		return nil, status.Errorf(codes.AlreadyExists, "template name %q already exists", r.Name)
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "save template: %v", err)
	}
	return &adminv1.TenantTemplateRef{Id: uint64(id), Name: r.Name}, nil
}

// DeleteTenantTemplate 删本租户模板（tenant-scoped）。
func (s *AdminServer) DeleteTenantTemplate(ctx context.Context, r *adminv1.DeleteTenantTemplateRequest) (*adminv1.WriteResponse, error) {
	err := store.DeleteTenantTemplate(ctx, s.db, int64(r.TenantId), int64(r.TemplateId))
	if errors.Is(err, store.ErrNotFound) {
		return nil, status.Error(codes.NotFound, "template not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "delete template: %v", err)
	}
	return &adminv1.WriteResponse{}, nil
}
```
> 先 Read `mgmt/server.go` 确认 `AdminServer.db` 字段名与 `WriteResponse` 构造范式（镜像既有 DeleteRole handler）。

- [ ] **步骤 5：测试 + 构建** → `go test ./internal/controlplane/mgmt/ -run TestSaveAppAsTemplate -count=1 2>&1 | tail -10`（PASS）；`go build ./...`、`gofmt -l internal/controlplane/mgmt/`（空）。

- [ ] **步骤 6：Commit**

```bash
git add internal/controlplane/mgmt/tenant_templates.go internal/controlplane/mgmt/tenant_templates_test.go internal/controlplane/mgmt/authz.go
git commit -m "feat(mgmt): SaveAppAsTemplate 捕获+DeleteTenantTemplate+ruleTable 5 条(Save scopeApp/List·Get·Delete scopeTenant/Apply scopeApp)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 5：mgmt ListTenantTemplates + GetTenantTemplate（预览）

**文件：**
- 修改：`internal/controlplane/mgmt/tenant_templates.go`、`internal/controlplane/mgmt/tenant_templates_test.go`

- [ ] **步骤 1：写失败测试（追加）**

```go
func TestListAndGetTenantTemplate(t *testing.T) {
	db := dbtest.SetupSchema(t)
	tID, appID := dbtest.SeedAppInTenant(t, db, "t-a", "dom-a", "AK_a")
	srv := accountsSrv(db)
	ctx := context.Background()
	seedConfiguredApp(t, db, appID)
	ref, _ := srv.SaveAppAsTemplate(ctx, &adminv1.SaveAppAsTemplateRequest{AppId: uint64(appID), Name: "标准后台"})

	lst, err := srv.ListTenantTemplates(ctx, &adminv1.ListTenantTemplatesRequest{TenantId: uint64(tID)})
	require.NoError(t, err)
	require.Equal(t, uint32(1), lst.Total)
	require.Equal(t, "标准后台", lst.Templates[0].Name)

	got, err := srv.GetTenantTemplate(ctx, &adminv1.GetTenantTemplateRequest{TenantId: uint64(tID), TemplateId: ref.Id})
	require.NoError(t, err)
	require.NotEmpty(t, got.Permissions)
	require.NotEmpty(t, got.Roles)
	require.NotEmpty(t, got.Roles[0].DataScopes)
	require.Equal(t, "order", got.Roles[0].DataScopes[0].Resource)

	// 跨租户 Get→NotFound（fail-close，不泄露存在性）。
	tB, _ := dbtest.SeedAppInTenant(t, db, "t-b", "dom-b", "AK_b")
	_, err = srv.GetTenantTemplate(ctx, &adminv1.GetTenantTemplateRequest{TenantId: uint64(tB), TemplateId: ref.Id})
	require.Equal(t, codes.NotFound, status.Code(err))
}
```

- [ ] **步骤 2：运行验证失败** → FAIL。

- [ ] **步骤 3：实现（追加到 `tenant_templates.go`）**

```go
// ListTenantTemplates 列出本租户模板（分页/搜索/排序）。
func (s *AdminServer) ListTenantTemplates(ctx context.Context, r *adminv1.ListTenantTemplatesRequest) (*adminv1.ListTenantTemplatesResponse, error) {
	order := resolveOrder(r.Page.GetSort(), r.Page.GetOrder(),
		map[string]string{"id": "id", "name": "name"}, "id")
	// resolveOrder 返回 "<col> <DIR>"；ListTenantTemplates 需拆列与方向——这里改用既有 pageOf + 显式列。
	limit, offset := pageOf(r.Page)
	col, dir := splitOrder(order) // 见下方助手；或直接传 order 整串给 store（调整 store 签名为单一 orderExpr 受控串）
	rows, total, err := store.ListTenantTemplates(ctx, s.db, int64(r.TenantId), limit, offset, col, dir, r.Page.GetQ())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list templates: %v", err)
	}
	out := &adminv1.ListTenantTemplatesResponse{Total: total}
	for _, t := range rows {
		out.Templates = append(out.Templates, &adminv1.TenantTemplateSummary{
			Id: uint64(t.ID), Name: t.Name, Description: t.Description, SourceAppId: uint64(t.SourceAppID),
		})
	}
	return out, nil
}

// GetTenantTemplate 取模板并把 bundle 渲染为预览（含 data_scopes，符号谓词在表现层渲染）。
func (s *AdminServer) GetTenantTemplate(ctx context.Context, r *adminv1.GetTenantTemplateRequest) (*adminv1.TenantTemplate, error) {
	t, err := store.GetTenantTemplate(ctx, s.db, int64(r.TenantId), int64(r.TemplateId))
	if errors.Is(err, store.ErrNotFound) {
		return nil, status.Error(codes.NotFound, "template not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get template: %v", err)
	}
	var b tenanttemplate.Bundle
	if err := json.Unmarshal(t.Bundle, &b); err != nil {
		return nil, status.Errorf(codes.Internal, "bundle parse") // TT-8 fail-close，不回显原文
	}
	out := &adminv1.TenantTemplate{Id: uint64(t.ID), Name: t.Name, Description: t.Description, SourceAppId: uint64(t.SourceAppID)}
	for _, p := range b.Permissions {
		out.Permissions = append(out.Permissions, &adminv1.TemplatePermission{
			Code: p.Code, Resource: p.Resource, Action: p.Action, Type: p.Type, Name: p.Name, Description: p.Description,
		})
	}
	for _, role := range b.Roles {
		tr := &adminv1.TemplateRole{Key: role.Key, Name: role.Name, Description: role.Description, PermissionCodes: role.PermissionCodes}
		for _, ds := range role.DataScopes {
			tr.DataScopes = append(tr.DataScopes, &adminv1.TemplateDataScope{
				Resource: ds.Resource, Effect: ds.Effect, Condition: string(ds.Condition),
			})
		}
		out.Roles = append(out.Roles, tr)
	}
	return out, nil
}
```

> **实现期决议**：`store.ListTenantTemplates` 接收 `(orderCol, orderDir)` 两参。若复用 mgmt `resolveOrder`（返回 `"col DIR"` 整串），改 store 签名为接收单一**受控** `orderExpr string`（由 `resolveOrder` 白名单产出，LS-2 安全），删去本处 `splitOrder`。二选一保持一致即可——优先「store 收单一受控 orderExpr」最简。先 Read `mgmt/listpage.go` 确认 `resolveOrder`/`pageOf`/`clampLimit` 签名后定稿。

- [ ] **步骤 4：测试 + 构建** → PASS；`go build ./...`、`gofmt -l`（空）、`go vet ./internal/controlplane/mgmt/`。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/mgmt/tenant_templates.go internal/controlplane/mgmt/tenant_templates_test.go
git commit -m "feat(mgmt): ListTenantTemplates 分页+GetTenantTemplate 预览(bundle→Template proto 含 data_scopes/跨租户 NotFound)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 6：mgmt ApplyTenantTemplate（复用 ApplyTemplate 引擎）

**文件：**
- 修改：`internal/controlplane/mgmt/tenant_templates.go`、`internal/controlplane/mgmt/tenant_templates_test.go`

- [ ] **步骤 1：写失败测试（追加）**

```go
func TestApplyTenantTemplate_ReusesEngine(t *testing.T) {
	db := dbtest.SetupSchema(t)
	tID, srcApp := dbtest.SeedAppInTenant(t, db, "t-a", "dom-a", "AK_a")
	_, dstApp := dbtest.SeedAppInTenant(t, db, "t-a2", "dom-a2", "AK_a2") // 同租户另一 app（用同 tenant 的 SeedApp 方式）
	srv := accountsSrv(db)
	ctx := context.Background()
	seedConfiguredApp(t, db, srcApp)
	ref, _ := srv.SaveAppAsTemplate(ctx, &adminv1.SaveAppAsTemplateRequest{AppId: uint64(srcApp), Name: "标准后台"})

	resp, err := srv.ApplyTenantTemplate(ctx, &adminv1.ApplyTenantTemplateRequest{AppId: uint64(dstApp), TemplateId: ref.Id})
	require.NoError(t, err)
	require.GreaterOrEqual(t, resp.RolesCreated, uint32(1))
	require.GreaterOrEqual(t, resp.DataScopesCreated, uint32(1)) // 数据范围随模板种入（复用 ApplyTemplate）

	// re-apply 幂等：角色已存在→跳过。
	resp2, err := srv.ApplyTenantTemplate(ctx, &adminv1.ApplyTenantTemplateRequest{AppId: uint64(dstApp), TemplateId: ref.Id})
	require.NoError(t, err)
	require.Equal(t, uint32(0), resp2.RolesCreated)
	require.Equal(t, uint32(0), resp2.DataScopesCreated)

	// 跨租户 apply→fail-close（模板属 t-a，目标 app 属别的租户）。
	tB, appB := dbtest.SeedAppInTenant(t, db, "t-b", "dom-b", "AK_b")
	_ = tID
	_, err = srv.ApplyTenantTemplate(ctx, &adminv1.ApplyTenantTemplateRequest{AppId: uint64(appB), TemplateId: ref.Id})
	require.Equal(t, codes.NotFound, status.Code(err))
	_ = tB
}
```
> **实现者注**：`dbtest.SeedAppInTenant` 每调用建新租户。同租户内建第二个 app 的方式：先 Read `internal/dbtest/` 助手，若无「同租户再建 app」助手，则在测试内用 `dbtest.SeedApp` 变体或直接 INSERT application（同 tenant_id）。目标=src/dst 两 app 同租户、第三 app 异租户。

- [ ] **步骤 2：运行验证失败** → FAIL。

- [ ] **步骤 3：实现（追加到 `tenant_templates.go`）**

```go
// ApplyTenantTemplate 把本租户模板 apply 到本租户目标 app（复用同一 ApplyTemplate 引擎）。
func (s *AdminServer) ApplyTenantTemplate(ctx context.Context, r *adminv1.ApplyTenantTemplateRequest) (*adminv1.ApplyTemplateResponse, error) {
	// 目标 app 的租户。
	var tenantID int64
	if err := s.db.QueryRowContext(ctx, `SELECT tenant_id FROM application WHERE id=$1`, int64(r.AppId)).Scan(&tenantID); err != nil {
		return nil, status.Errorf(codes.Internal, "resolve tenant: %v", err)
	}
	// 取模板（WHERE tenant_id=目标 app 租户 → 跨租户自然 NotFound，TT-2/TT-8）。
	t, err := store.GetTenantTemplate(ctx, s.db, tenantID, int64(r.TemplateId))
	if errors.Is(err, store.ErrNotFound) {
		return nil, status.Error(codes.NotFound, "template not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get template: %v", err)
	}
	var b tenanttemplate.Bundle
	if err := json.Unmarshal(t.Bundle, &b); err != nil {
		return nil, status.Error(codes.Internal, "bundle parse")
	}
	perms, roles := bundleToApplyInputs(b)
	res, _, err := s.mgr.ApplyTemplate(ctx, int64(r.AppId), "tt-"+strconv.FormatInt(t.ID, 10), perms, roles)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "apply tenant template: %v", err)
	}
	return &adminv1.ApplyTemplateResponse{
		PermissionsUpserted: uint32(res.PermsUpserted), PermissionsSkipped: uint32(res.PermsSkipped),
		RolesCreated: uint32(res.RolesCreated), RolesSkipped: uint32(res.RolesSkipped),
		DataScopesCreated: uint32(res.DataScopesCreated),
	}, nil
}

func bundleToApplyInputs(b tenanttemplate.Bundle) ([]cp.PermissionPoint, []policy.TemplateRole) {
	perms := make([]cp.PermissionPoint, 0, len(b.Permissions))
	for _, p := range b.Permissions {
		perms = append(perms, cp.PermissionPoint{
			Code: p.Code, Resource: p.Resource, Action: p.Action, Type: p.Type, Name: p.Name, Description: p.Description,
		})
	}
	roles := make([]policy.TemplateRole, 0, len(b.Roles))
	for _, r := range b.Roles {
		tr := policy.TemplateRole{Key: r.Key, Name: r.Name, PermissionCodes: r.PermissionCodes}
		for _, ds := range r.DataScopes {
			tr.DataScopes = append(tr.DataScopes, policy.TemplateDataScope{
				Resource: ds.Resource, Effect: ds.Effect, Condition: string(ds.Condition),
			})
		}
		roles = append(roles, tr)
	}
	return perms, roles
}
```
> 加 import `cp "github.com/nickZFZ/Sydom/internal/controlplane"`、`"github.com/nickZFZ/Sydom/internal/controlplane/policy"`、`"strconv"`。确认 `AdminServer.mgr` 字段（既有 ApplyTemplate handler 用 `s.mgr.ApplyTemplate`）。

- [ ] **步骤 4：测试 + 构建** → PASS；`go build ./...`、`gofmt -l`（空）、`go vet`。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/mgmt/tenant_templates.go internal/controlplane/mgmt/tenant_templates_test.go
git commit -m "feat(mgmt): ApplyTenantTemplate 复用 policy.ApplyTemplate 引擎(tt-<id> 命名空间/幂等/跨租户 fail-close)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 7：REST 5 路由 + Console「我的模板」区 + 「存为模板」入口

**文件：**
- 创建/修改：`internal/controlplane/restgw/routes_tenant_templates.go`（或并入既有 routes 文件）+ test
- 创建：`internal/controlplane/console/routes_tenant_templates.go` + test
- 修改：`internal/controlplane/console/templates/ops_templates.html`、新模板（`ops_template_saved.html` 等）
- 修改：路由注册处（restgw routes 表 + console mux）

- [ ] **步骤 1：REST 路由（镜像既有 ApplyTemplate/path 权威范式）**

先 Read `internal/controlplane/restgw/` 既有 `routes_templates.go` + routes 注册，照搬范式加 5 路由（path 权威覆写 app_id/template_id/tenant_id）：
- `POST /v1/apps/{app_id}/template-captures`（SaveAppAsTemplate，name/description 走 body 或 query，被 REST-HMAC 签名覆盖）
- `GET /v1/tenants/{tenant_id}/templates`（List，ListPage query）
- `GET /v1/tenants/{tenant_id}/templates/{template_id}`（Get）
- `POST /v1/apps/{app_id}/tenant-templates/{template_id}/apply`（Apply）
- `DELETE /v1/tenants/{tenant_id}/templates/{template_id}`（Delete）

- [ ] **步骤 2：REST 测试**

`restgw` test 照搬 `newTestGW`/`rootClient`/`protoUnmarshal`：`TestREST_TenantTemplate_SaveListApply` —— save（捕获已配 app）→ list（total≥1）→ apply 到同租户 app（`protoUnmarshal` ApplyTemplateResponse，`RolesCreated≥1`）。

- [ ] **步骤 3：Console「我的模板」区 + 入口**

先 Read `console/routes_templates.go`（M3.2c-1）照搬范式：
- 模板库页加「我的模板（租户自有）」区：ListTenantTemplates 渲染列表 + GetTenantTemplate 预览（roles/perms + 数据范围**符号谓词**复用 `conditionPredicate`）+「应用到本应用」（doWrite→ApplyTenantTemplate，非 PRG 摘要）+「删除」（doWrite→DeleteTenantTemplate，PRG）。
- app/建模台或模板库页加「存为模板」表单（填 name+description → SaveAppAsTemplate，doWrite）。
- 复用 M3.1 设计系统、**无新 JS**；安全管线镜像 `doWrite`（会话→CSRF→AuthorizeRule→[Apply 走 status 闸]→调用）。
- tenant_id 来源：Console 会话 principal 的租户（先 Read 既有 console 如何取当前租户，如 M1.2 租户页）。

- [ ] **步骤 4：Console 测试**

`console` test 照搬 `newConsole`/`loginAndCSRF`/`readBody`：`TestConsole_TenantTemplate_SaveAndPreview` —— 存为模板→列表见模板名→预览 body 含 `$user.`（符号谓词，DSC-2）、NotContains 真实枚举值。

- [ ] **步骤 5：构建 + 测试**

`go build ./...`；`go test ./internal/controlplane/restgw/ ./internal/controlplane/console/ -run 'TenantTemplate' -count=1 2>&1 | tail -15`（PASS）；`gofmt -l internal/controlplane/restgw/ internal/controlplane/console/`（空）。

- [ ] **步骤 6：Commit**

```bash
git add internal/controlplane/restgw/ internal/controlplane/console/
git commit -m "feat(rest+console): 租户自有模板 5 REST 路由(path 权威)+运营台「我的模板」区(存/预览符号谓词/应用/删除,无新 JS)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 8：整体验证 + TT-1..8 + FF 合并

- [ ] **步骤 1：TT 不变量逐条核验**

```bash
BASE=<worktree base sha>
# TT-1 一份授权真相：adminauthz 零触碰、authz.go 仅 +5 条 ruleTable（M1.1 matcher 一字未改）
git diff $BASE..HEAD -- internal/controlplane/adminauthz/ | wc -l        # 0
git diff $BASE..HEAD -- internal/controlplane/mgmt/authz.go | grep -E '^\+' | grep -c TenantTemplate  # 5（仅新增条目）
# TT-7 sidecar 零漂移
git diff $BASE..HEAD -- internal/sidecar/ | wc -l                        # 0
# TT-6 secret：捕获/bundle 链路不碰 secret 列
grep -rn 'secret\|app_secret' internal/controlplane/tenanttemplate/ || echo "TT-6 OK: 捕获无 secret"
# 无新 JS
ls internal/controlplane/console/static/*.js   # 仅 datapolicy.js
```
TT-2 租户隔离 / TT-3 apply 一致 / TT-4 捕获保真 / TT-5 符号口径 / TT-8 fail-close：由任务 2/4/5/6/7 单测覆盖（复跑确认）。

- [ ] **步骤 2：格式/静态/proto/全量测试**

```bash
gofmt -l internal/ api/   # 空
go vet ./...              # 净
make proto-check          # 无漂移
go test ./... 2>&1 | tail -40   # 0 FAIL（含 store/tenanttemplate/mgmt/console/restgw/sidecar/e2e）
```

- [ ] **步骤 3：更新进度记忆**

`project_detailed_design_progress.md` 加 M3.2c-2 节（租户自有模板：tenant_template 表 + 捕获全模型 + apply 复用引擎 + 5 RPC 三面 + Console 我的模板；TT-1..8；M3.2c 完结）；`MEMORY.md` 索引钩子追加 M3.2c-2 完成 + 下一步 M3.3。

- [ ] **步骤 4：FF 合并本地 main（不 push origin）**

worktree 全绿 + opus 整体评审 READY 后 FF 并入本地 main，清 worktree（ExitWorktree keep → 主仓 `git merge --ff-only` →ExitWorktree(remove) 或 worktree remove）。

---

## 自检记录

**规格覆盖度（对照 spec）：** §3 数据模型→任务1 ✓；§4 捕获→任务2 ✓；§5 apply→任务6 ✓；§6 proto→任务3 + 三面（mgmt 任务4/5/6、REST+Console 任务7）✓；§7 TT-1..8→任务2/4/5/6/7/8 ✓；§8 测试策略→各任务 TDD + 任务8 全量 ✓；§9 YAGNI（版本化/diff/dry-run/覆盖/克隆官方/构建器/跨租户共享/选择性捕获均未触）✓。

**占位符扫描：** 各任务给出实际代码（migration/store CRUD/Capture/handler/bundleToApplyInputs/ruleTable/proto 均完整）；测试助手（accountsSrv/newConsole/loginAndCSRF/newTestGW/seedConfiguredApp/SeedAppInTenant）注明「照搬既有」——非占位，是适配既有夹具的明确指令。任务5 的 `resolveOrder`↔store orderCol/Dir 对接是实现期明确二选一（优先 store 收单一受控 orderExpr），非未完成需求。任务7 REST/Console 形态注明「照搬既有 routes_templates 范式」，路由清单已列全。

**类型一致性：** `store.TenantTemplate{Bundle []byte}` ↔ `tenanttemplate.Bundle`（json.Marshal/Unmarshal）↔ proto `TenantTemplate{permissions[],roles[]}`（任务5 渲染）一致；`tenanttemplate.BundleDataScope{Condition json.RawMessage}` ↔ `policy.TemplateDataScope{Condition string}`（任务6 `string(ds.Condition)`）↔ `adminv1.TemplateDataScope{condition string}`（任务5）一致；`policy.ApplyTemplate(appID, "tt-<id>", perms, roles)`（任务6）复用任务 M3.2c-1 既有签名、`ApplyTemplateResult.DataScopesCreated` 透出一致；`store.ErrConflict`（任务1 新增）↔ mgmt `codes.AlreadyExists`（任务4）、`store.ErrNotFound`（既有）↔ `codes.NotFound`（任务4/5/6）一致。

**关键口径固化：** 捕获=纯读全模型排除 user 绑定/凭据（TT-4/TT-6）；apply 复用同一 `policy.ApplyTemplate` 无第二套（TT-3，含 data_scope 种入/幂等/原子/广播）；租户隔离=tenant_id 闸 + 跨租户 NotFound 不泄露存在性（TT-2/TT-8，Get/Apply 均 WHERE tenant_id）；命名空间 `tpl:tt-<id>:<key>` 不撞官方 preset；安全 key 去 `:` 限长保证 code ≤64；ruleTable 仅 +5 条、M1.1 matcher 一字未改（TT-1）；预览符号谓词复用 `conditionPredicate` 不枚举（TT-5）。
