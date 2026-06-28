# 司域 M4.1 · 策略即代码（Policy-as-Code）+ 导入导出 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 让技术向用户把一个 app 的授权模型在「声明式 YAML/JSON 文件 ⇄ 系统状态」间双向同步：export 复用 `Capture` 导出全模型；import 真 IaC 收敛（建/改/删/采纳），删除治理域限 `source='iac'` 子集，dry-run diff 预览 + 显式确认后原子 apply。三面 parity（gRPC+REST+Console），不碰授权决策核心。

**架构：** 迁移给 `role`/`data_policy` 加 `source` 列（对齐 `permission` 既有 `source`）。新 `internal/controlplane/iac` 包做纯函数解析/序列化/校验/diff（无 DB、可隔离测试）。写经 `PolicyManager.runVersionedWrite`（原子+bump+广播）。export 复用 `tenanttemplate.Capture`。新 2 RPC 经唯一 `AuthorizeRule`+`ruleTable`。Console 加「策略即代码」页（服务端渲染 diff，无新 JS）。

**技术栈：** Go、PostgreSQL（golang-migrate）、`gopkg.in/yaml.v3`（唯一新依赖）、protobuf/buf、html/template、testcontainers（PG/Redis）、testify。

**规格：** `docs/superpowers/specs/2026-06-28-sydom-m4-1-policy-as-code-design.md`（`a7a6494`）。BASE = 本地/远端 main `85354f0`。

---

## 关键既有 API（实现者可信赖，已核实签名）

```go
// internal/controlplane/store/store.go
func InsertRole(ctx, ex cp.DBTX, appID int64, code, name string) (int64, error)
func DeleteRole(ctx, ex cp.DBTX, appID, roleID int64) error  // 仅删 role 行；调用方须先卸 role_permission/role_inheritance
func UpsertPermission(ctx, ex cp.DBTX, appID int64, code, resource, action, permType, name string) (int64, error)
func InsertRolePermission(ctx, ex cp.DBTX, appID, roleID, permID int64, eft string) error
func DeleteRolePermission(ctx, ex cp.DBTX, appID, roleID, permID int64) error
func UpsertDataPolicy(ctx, ex cp.DBTX, appID int64, p cp.DataPolicy, version int64) (id int64, created bool, err error)
func DeleteDataPolicy(ctx, ex cp.DBTX, appID, id int64) error
func PermissionIDsByCode(ctx, ex cp.DBTX, appID int64, codes []string) (map[string]int64, error)
// internal/controlplane/store/read.go
func ReadAppDataPolicies(ctx, q cp.DBTX, appID int64) ([]cp.DataPolicy, error)
// internal/controlplane/tenanttemplate/bundle.go
func Capture(ctx, db cp.DBTX, appID int64) (Bundle, error) // Bundle{Permissions[], Roles[]}; 排除 user 绑定/凭据
// internal/controlplane/policy/manager.go
func (m *PolicyManager) runVersionedWrite(ctx, appID int64, op writeOp) (*cp.Delta, error)
type writeOp struct { action, entityType, entityID string; mutate func(ctx, *sql.Tx)([]cp.DataPolicyChange,error); preCheck func(ctx,*sql.Tx)error }
```
```go
// internal/controlplane/types.go
type DataPolicy struct { ID int64; SubjectType, SubjectID, Resource, Condition, Effect, Description string }
type PermissionPoint struct { Code, Resource, Action, Type, Name, Description string }
type DBTX interface { ExecContext; QueryContext; QueryRowContext }  // *sql.DB 与 *sql.Tx 公共子集
```
`ruleTable`（`internal/controlplane/mgmt/authz.go:45`）条目格式：`"/sydom.admin.v1.AdminService/<RPC>": {"<resource>", "<action>", <isWrite bool>, scopeApp}`。

---

## 文件结构

- 创建：`db/migrations/000020_entity_source.up.sql` / `.down.sql` — role/data_policy 加 source 列。
- 创建：`internal/controlplane/iac/doc.go` — 文件信封模型 + 内部期望态模型类型。
- 创建：`internal/controlplane/iac/parse.go` / `parse_test.go` — YAML/JSON 自动识别解析 + 序列化 + 校验（纯函数）。
- 创建：`internal/controlplane/iac/diff.go` / `diff_test.go` — 收敛 diff 计算（纯函数：desired+current→Plan）。
- 创建：`internal/controlplane/store/source.go` / `source_test.go` — 来源感知 store 助手（建/改/查带 source）。
- 创建：`internal/controlplane/policy/policy_as_code.go` / `policy_as_code_test.go` — `ExportAppPolicy` 读 + `ImportAppPolicy`（dry_run/apply）经 runVersionedWrite。
- 修改：`proto/sydom/admin/v1/admin.proto` — 2 RPC + message。
- 修改：`internal/controlplane/mgmt/server.go`、`authz.go` — 2 handler + ruleTable +2。
- 创建：`internal/controlplane/mgmt/policy_as_code_test.go` — handler + 跨租户矩阵测试。
- 修改：`internal/controlplane/restgw/routes.go`（或同级路由文件） — 2 REST 路由。
- 创建：`internal/controlplane/console/routes_policy_code.go`、`templates/policy_code.html`、`templates/policy_code_diff.html` — Console 页。
- 修改：`internal/controlplane/console/handler.go`（路由注册） + 建模台导航。

每个任务独立、可单独审查；任务 3/4（解析 + diff）是纯函数核心，任务 5（apply）最关键。

---

## 任务 1：迁移 000020 — role/data_policy 加 source 列

**文件：**
- 创建：`db/migrations/000020_entity_source.up.sql` / `.down.sql`
- 创建：`internal/controlplane/store/source_migration_test.go`

- [ ] **步骤 1：先写失败测试**

`internal/controlplane/store/source_migration_test.go`：
```go
package store_test

import (
	"database/sql"
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// role/data_policy 新增 source 列，既有插入默认 'manual'（向后兼容）。
func TestEntitySource_DefaultsManual(t *testing.T) {
	dsn := dbtest.MigratedDSN(t)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, app := dbtest.SeedAppInTenant(t, db, "src", "src", "src-key")
	var roleSrc string
	require.NoError(t, db.QueryRow(
		`INSERT INTO role (app_id, code, name) VALUES ($1,'r','R') RETURNING source`, app).Scan(&roleSrc))
	require.Equal(t, "manual", roleSrc)

	var dpSrc string
	require.NoError(t, db.QueryRow(
		`INSERT INTO data_policy (app_id, subject_type, subject_id, resource, condition, effect, version)
		 VALUES ($1,'role','r','order','{}'::jsonb,'allow',1) RETURNING source`, app).Scan(&dpSrc))
	require.Equal(t, "manual", dpSrc)
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/store/ -run TestEntitySource -count=1`
预期：FAIL（source 列不存在，`RETURNING source` 报 column 不存在）。

- [ ] **步骤 3：编写 up 迁移**

`db/migrations/000020_entity_source.up.sql`：
```sql
-- 为 IaC 治理域引入来源维度（对齐 permission 既有 source）。既有行默认 manual，向后兼容。
ALTER TABLE role        ADD COLUMN source VARCHAR(8) NOT NULL DEFAULT 'manual';
ALTER TABLE data_policy ADD COLUMN source VARCHAR(8) NOT NULL DEFAULT 'manual';
```

- [ ] **步骤 4：编写 down 迁移**

`db/migrations/000020_entity_source.down.sql`：
```sql
ALTER TABLE data_policy DROP COLUMN source;
ALTER TABLE role        DROP COLUMN source;
```

- [ ] **步骤 5：运行验证通过 + 回归**

运行：`go test ./internal/controlplane/store/ -run TestEntitySource -count=1`（PASS）。
运行：`go test ./internal/controlplane/store/ ./internal/controlplane/mgmt/ -count=1`（既有写路径不受影响）。

- [ ] **步骤 6：Commit**

```bash
git add db/migrations/000020_entity_source.up.sql db/migrations/000020_entity_source.down.sql internal/controlplane/store/source_migration_test.go
git commit -m "feat(db): 000020 role/data_policy 加 source 列(对齐 permission,默认 manual 向后兼容,为 M4.1 IaC 治理域引入来源维度)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 2：proto — ExportAppPolicy / ImportAppPolicy RPC

**文件：**
- 修改：`proto/sydom/admin/v1/admin.proto`

- [ ] **步骤 1：加 RPC + message**

在 `service AdminService` 内加 2 RPC，并在文件内加 message（字段号接现有最大值续，参考相邻 RPC 的 message 风格）：
```proto
  rpc ExportAppPolicy(ExportAppPolicyRequest) returns (ExportAppPolicyResponse);
  rpc ImportAppPolicy(ImportAppPolicyRequest) returns (ImportAppPolicyResponse);
```
```proto
message ExportAppPolicyRequest {
  uint64 app_id = 1;
  string format = 2; // "yaml" | "json"
}
message ExportAppPolicyResponse {
  string content = 1;
}
message ImportAppPolicyRequest {
  uint64 app_id = 1;
  string content = 2;
  bool dry_run = 3;
}
message PolicyDiffEntry {
  string kind = 1;        // "create" | "adopt" | "update" | "delete" | "conflict"
  string entity_type = 2; // "permission" | "role" | "data_policy"
  string identity = 3;    // code / key / subject:resource
  string detail = 4;      // 人读差异摘要（绝不含 secret）
}
message ImportAppPolicyResponse {
  repeated PolicyDiffEntry diff = 1;
  bool applied = 2;        // dry_run=true 恒 false
  int64 version = 3;       // apply 后的新版本；dry_run 回当前版本
  int32 creates = 4;
  int32 adopts = 5;
  int32 updates = 6;
  int32 deletes = 7;
  int32 conflicts = 8;
}
```

- [ ] **步骤 2：生成 + 校验**

运行：`make proto`（buf generate）。
运行：`make proto-check`（buf lint + 零漂移核对；若 lint 报 message 命名等，按既有 `buf.yaml` except 既定范式处理）。
运行：`go build ./gen/...`
预期：`gen/` 出现新类型 `ExportAppPolicyRequest` 等，编译通过。

- [ ] **步骤 3：Commit**

```bash
git add proto/sydom/admin/v1/admin.proto gen/
git commit -m "feat(proto): M4.1 ExportAppPolicy/ImportAppPolicy RPC + PolicyDiffEntry message(策略即代码导入导出契约)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 3：iac 包 — 文件模型 + 解析/序列化/校验（纯函数 TDD）

**文件：**
- 创建：`internal/controlplane/iac/doc.go`
- 创建：`internal/controlplane/iac/parse.go`、`parse_test.go`

- [ ] **步骤 1：定义模型（doc.go）**

`internal/controlplane/iac/doc.go`：
```go
// Package iac 是 M4.1 策略即代码的纯函数核心：文件信封模型 + YAML/JSON 解析/序列化/校验 + 收敛 diff。
// 无 DB、无 I/O，可隔离单测。写入由 policy 包在 runVersionedWrite 事务内执行。
package iac

import "encoding/json"

const APIVersion = "sydom.policy/v1"

// Document 是策略即代码文件的信封 + 期望态模型。
type Document struct {
	APIVersion   string       `json:"apiVersion" yaml:"apiVersion"`
	App          *AppRef      `json:"app,omitempty" yaml:"app,omitempty"`
	Permissions  []Permission `json:"permissions" yaml:"permissions"`
	Roles        []Role       `json:"roles" yaml:"roles"`
	DataPolicies []DataPolicy `json:"data_policies,omitempty" yaml:"data_policies,omitempty"`
}

type AppRef struct {
	Key string `json:"key,omitempty" yaml:"key,omitempty"`
}

type Permission struct {
	Code        string `json:"code" yaml:"code"`
	Resource    string `json:"resource" yaml:"resource"`
	Action      string `json:"action" yaml:"action"`
	Type        string `json:"type" yaml:"type"`
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

type Role struct {
	Key             string      `json:"key" yaml:"key"`
	Name            string      `json:"name" yaml:"name"`
	Description     string      `json:"description,omitempty" yaml:"description,omitempty"`
	PermissionCodes []string    `json:"permission_codes" yaml:"permission_codes"`
	DataScopes      []DataScope `json:"data_scopes,omitempty" yaml:"data_scopes,omitempty"`
}

type DataScope struct {
	Resource  string          `json:"resource" yaml:"resource"`
	Effect    string          `json:"effect" yaml:"effect"`
	Condition json.RawMessage `json:"condition" yaml:"condition"`
}

type DataPolicy struct {
	SubjectType string          `json:"subject_type" yaml:"subject_type"`
	SubjectID   string          `json:"subject_id" yaml:"subject_id"`
	Resource    string          `json:"resource" yaml:"resource"`
	Effect      string          `json:"effect" yaml:"effect"`
	Condition   json.RawMessage `json:"condition" yaml:"condition"`
}
```

- [ ] **步骤 2：先写失败测试（parse_test.go）**

```go
package iac

import "testing"

func TestParse_AutoDetectsJSONAndYAML_SameModel(t *testing.T) {
	js := `{"apiVersion":"sydom.policy/v1","permissions":[{"code":"order:read","resource":"order","action":"read","type":"app","name":"查看订单"}],"roles":[{"key":"viewer","name":"查看员","permission_codes":["order:read"]}]}`
	ya := "apiVersion: sydom.policy/v1\npermissions:\n  - code: order:read\n    resource: order\n    action: read\n    type: app\n    name: 查看订单\nroles:\n  - key: viewer\n    name: 查看员\n    permission_codes: [order:read]\n"
	dj, err := Parse([]byte(js))
	if err != nil { t.Fatalf("json parse: %v", err) }
	dy, err := Parse([]byte(ya))
	if err != nil { t.Fatalf("yaml parse: %v", err) }
	if len(dj.Permissions) != 1 || dj.Permissions[0].Code != "order:read" { t.Fatalf("json model: %+v", dj) }
	if len(dy.Roles) != 1 || dy.Roles[0].PermissionCodes[0] != "order:read" { t.Fatalf("yaml model: %+v", dy) }
}

func TestValidate_RejectsUndeclaredPermissionCode(t *testing.T) {
	d := &Document{APIVersion: APIVersion,
		Permissions: []Permission{{Code: "a:read", Resource: "a", Action: "read", Type: "app", Name: "A"}},
		Roles:       []Role{{Key: "r", Name: "R", PermissionCodes: []string{"b:write"}}}, // b:write 未声明
	}
	if err := Validate(d); err == nil {
		t.Fatal("expected validation error for undeclared permission code")
	}
}

func TestValidate_RejectsColonInRoleKey(t *testing.T) {
	d := &Document{APIVersion: APIVersion, Roles: []Role{{Key: "a:b", Name: "X"}}}
	if err := Validate(d); err == nil { t.Fatal("expected error for ':' in role key") }
}
```

- [ ] **步骤 3：运行验证失败**

运行：`go test ./internal/controlplane/iac/ -count=1`
预期：FAIL（`Parse`/`Validate` 未定义，编译错误）。

- [ ] **步骤 4：实现 parse.go**

`internal/controlplane/iac/parse.go`：
```go
package iac

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Parse 自动识别 JSON / YAML（首个非空白字符 '{' 或 '[' → JSON，否则 YAML）→ Document。
func Parse(content []byte) (*Document, error) {
	trimmed := bytes.TrimSpace(content)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("iac: empty document")
	}
	var d Document
	if trimmed[0] == '{' || trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &d); err != nil {
			return nil, fmt.Errorf("iac: json parse: %w", err)
		}
	} else {
		if err := yaml.Unmarshal(trimmed, &d); err != nil {
			return nil, fmt.Errorf("iac: yaml parse: %w", err)
		}
	}
	return &d, nil
}

// Serialize 把 Document 序列化为 yaml 或 json。
func Serialize(d *Document, format string) ([]byte, error) {
	switch format {
	case "json":
		return json.MarshalIndent(d, "", "  ")
	case "yaml", "":
		return yaml.Marshal(d)
	default:
		return nil, fmt.Errorf("iac: unknown format %q", format)
	}
}

// Validate 做引用完整性 + 唯一性 + 合法性校验（fail-close）。
func Validate(d *Document) error {
	if d.APIVersion != "" && d.APIVersion != APIVersion {
		return fmt.Errorf("iac: unsupported apiVersion %q", d.APIVersion)
	}
	permCodes := map[string]bool{}
	for _, p := range d.Permissions {
		if p.Code == "" || strings.ContainsRune(p.Code, ':') == false && strings.TrimSpace(p.Code) == "" {
			return fmt.Errorf("iac: permission code empty")
		}
		if permCodes[p.Code] {
			return fmt.Errorf("iac: duplicate permission code %q", p.Code)
		}
		permCodes[p.Code] = true
	}
	roleKeys := map[string]bool{}
	for _, r := range d.Roles {
		if r.Key == "" {
			return fmt.Errorf("iac: role key empty")
		}
		if strings.ContainsRune(r.Key, ':') {
			return fmt.Errorf("iac: role key %q must not contain ':'", r.Key)
		}
		if roleKeys[r.Key] {
			return fmt.Errorf("iac: duplicate role key %q", r.Key)
		}
		roleKeys[r.Key] = true
		for _, pc := range r.PermissionCodes {
			if !permCodes[pc] {
				return fmt.Errorf("iac: role %q references undeclared permission code %q", r.Key, pc)
			}
		}
		for _, ds := range r.DataScopes {
			if err := validCondition(ds.Condition); err != nil {
				return fmt.Errorf("iac: role %q data_scope: %w", r.Key, err)
			}
			if err := validEffect(ds.Effect); err != nil {
				return err
			}
		}
	}
	for _, dp := range d.DataPolicies {
		if err := validCondition(dp.Condition); err != nil {
			return fmt.Errorf("iac: data_policy %s/%s: %w", dp.SubjectID, dp.Resource, err)
		}
		if err := validEffect(dp.Effect); err != nil {
			return err
		}
	}
	return nil
}

func validCondition(c json.RawMessage) error {
	if len(bytes.TrimSpace(c)) == 0 {
		return fmt.Errorf("condition empty")
	}
	if !json.Valid(c) {
		return fmt.Errorf("condition not valid json")
	}
	return nil
}

func validEffect(e string) error {
	if e != "" && e != "allow" && e != "deny" {
		return fmt.Errorf("iac: invalid effect %q", e)
	}
	return nil
}
```
> 注：步骤 4 的 `permission code empty` 判定写得啰嗦，实现时简化为 `if strings.TrimSpace(p.Code) == ""`。审查时收紧。

- [ ] **步骤 5：运行验证通过**

运行：`go test ./internal/controlplane/iac/ -count=1`（PASS）。需先 `go get gopkg.in/yaml.v3`（写入 go.mod/go.sum）。

- [ ] **步骤 6：Commit**

```bash
go get gopkg.in/yaml.v3
git add internal/controlplane/iac/ go.mod go.sum
git commit -m "feat(iac): M4.1 策略即代码文件模型 + YAML/JSON 自动识别解析/序列化/校验(纯函数 fail-close,引用完整性+唯一性+condition 合法,唯一新依赖 yaml.v3)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 4：iac 包 — 收敛 diff（纯函数 TDD）

**文件：**
- 创建：`internal/controlplane/iac/diff.go`、`diff_test.go`

- [ ] **步骤 1：定义 Current 快照 + Plan 类型（diff.go 头部）**

```go
package iac

// Current 是 app 现状的来源感知快照（由 policy 包从 DB 读出后喂入）。
type Current struct {
	Permissions []CurrentPermission
	Roles       []CurrentRole
	DataPolicies []CurrentDataPolicy
}
type CurrentPermission struct { Code, Resource, Action, Type, Name, Description, Source string }
type CurrentRole struct { Key, Name, Description, Source string; PermissionCodes []string; DataScopes []DataScope; HasUserBindings bool }
type CurrentDataPolicy struct { SubjectType, SubjectID, Resource, Effect, Source string; Condition []byte }

// PlanItem 是一条收敛动作。Kind ∈ create|adopt|update|delete|conflict。
type PlanItem struct { Kind, EntityType, Identity, Detail string }

// Plan 是 dry-run 的结构化产物。
type Plan struct { Items []PlanItem }

func (p *Plan) Count(kind string) int { n := 0; for _, it := range p.Items { if it.Kind == kind { n++ } }; return n }
```

- [ ] **步骤 2：先写失败测试（diff_test.go）**

```go
package iac

import "testing"

func TestDiff_CreateUpdateDeleteAdopt(t *testing.T) {
	desired := &Document{
		Permissions: []Permission{{Code: "order:read", Resource: "order", Action: "read", Type: "app", Name: "查看订单"}},
		Roles:       []Role{{Key: "viewer", Name: "查看员", PermissionCodes: []string{"order:read"}}},
	}
	cur := &Current{
		Permissions: []CurrentPermission{
			{Code: "order:read", Resource: "order", Action: "read", Type: "app", Name: "旧名", Source: "iac"}, // update（name 变）
			{Code: "order:write", Resource: "order", Action: "write", Type: "app", Name: "写", Source: "iac"}, // delete（文件未声明）
			{Code: "order:list", Resource: "order", Action: "list", Type: "app", Name: "列", Source: "manual"}, // 不碰
		},
		Roles: []CurrentRole{{Key: "viewer", Name: "查看员", Source: "manual", PermissionCodes: nil}}, // adopt（manual→iac）
	}
	p := Diff(desired, cur)
	if p.Count("update") != 1 { t.Fatalf("want 1 update, got plan %+v", p.Items) }
	if p.Count("delete") != 1 { t.Fatalf("want 1 delete(order:write), got %+v", p.Items) }
	if p.Count("adopt") < 1 { t.Fatalf("want adopt for viewer manual→iac, got %+v", p.Items) }
	// manual 权限点 order:list 绝不出现在任何 delete 项（PC-3 有齿）。
	for _, it := range p.Items {
		if it.Kind == "delete" && it.Identity == "order:list" {
			t.Fatal("manual permission order:list must never be deleted")
		}
	}
}

func TestDiff_DeleteRoleWithBindings_Conflict(t *testing.T) {
	desired := &Document{} // 文件空 → iac 实体应删
	cur := &Current{Roles: []CurrentRole{{Key: "viewer", Source: "iac", HasUserBindings: true}}}
	p := Diff(desired, cur)
	if p.Count("conflict") != 1 { t.Fatalf("want 1 conflict(role with bindings), got %+v", p.Items) }
	if p.Count("delete") != 0 { t.Fatalf("bound role must not be a plain delete, got %+v", p.Items) }
}
```

- [ ] **步骤 3：运行验证失败**

运行：`go test ./internal/controlplane/iac/ -run TestDiff -count=1`
预期：FAIL（`Diff` 未定义）。

- [ ] **步骤 4：实现 Diff**

`internal/controlplane/iac/diff.go`（续步骤 1）：实现 `func Diff(desired *Document, cur *Current) *Plan`。算法（逐实体类型，identity 见下）：
- 权限点：identity=code。文件有/库无→`create`；文件有/库 manual→`adopt`；文件有/库 iac 且字段(resource/action/type/name/description)有别→`update`；库 iac 且文件未声明→`delete`；库 manual/auto 且文件未声明→**忽略**（不碰，PC-3）。
- 角色：identity=key。文件有/库无→`create`；文件有/库 manual→`adopt`；文件有/库 iac 且(name/description/授权码集/data_scopes)有别→`update`；库 iac 且文件未声明→若 `HasUserBindings` 则 `conflict`（删除安全 PC-6），否则 `delete`；库 manual/auto 未声明→忽略。
- 数据策略：identity=`subject_type:subject_id:resource`。同上 create/adopt/update/delete/忽略（数据策略无绑定，无 conflict）。
- `Detail` 写人读差异摘要（如 `name: 旧名 → 查看员`），**绝不含 secret**（这些表本无凭据）。

实现要点：用 map 建立 desired/current 的 identity 索引各跑一遍；字段比较对 data_scopes/permission_codes 排序后比，避免顺序误判 update。

- [ ] **步骤 5：运行验证通过 + Commit**

运行：`go test ./internal/controlplane/iac/ -count=1`（PASS）。
```bash
git add internal/controlplane/iac/diff.go internal/controlplane/iac/diff_test.go
git commit -m "feat(iac): M4.1 收敛 diff 纯函数(create/adopt/update/delete/conflict;限 source=iac 治理域 manual/auto 不碰有齿;带绑定 iac 角色删除→conflict 删除安全)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 5：来源感知 store 助手 + ImportAppPolicy/ExportAppPolicy apply（最关键）

**文件：**
- 创建：`internal/controlplane/store/source.go`、`source_test.go`
- 创建：`internal/controlplane/policy/policy_as_code.go`、`policy_as_code_test.go`

- [ ] **步骤 1：来源感知 store 助手（source.go）**

实现这些（带 source 的建/改/查，签名供后续 apply 调用；用直接 SQL，镜像 store.go 既有风格）：
```go
package store
// UpsertPermissionWithSource：INSERT ... ON CONFLICT(app_id,code) DO UPDATE SET resource/action/type/name/description/source。返回 id。
func UpsertPermissionWithSource(ctx context.Context, ex cp.DBTX, appID int64, code, resource, action, permType, name, description, source string) (int64, error)
// AdoptPermissionSource / AdoptRoleSource / AdoptDataPolicySource：UPDATE ... SET source='iac' WHERE app_id,code/id 且 source='manual'。
func AdoptPermissionSource(ctx context.Context, ex cp.DBTX, appID int64, code string) error
// InsertRoleWithSource：INSERT role(app_id,code,name,source) RETURNING id。
func InsertRoleWithSource(ctx context.Context, ex cp.DBTX, appID int64, code, name, source string) (int64, error)
// UpdateRoleMeta：UPDATE role SET name,description WHERE app_id,id。
// ListAppRolesWithSource / ListAppPermissionsWithSource：SELECT ... 含 source 列（供 Current 快照）。
// RoleHasUserBindings：SELECT EXISTS(SELECT 1 FROM user_role_binding WHERE app_id,role_id)。
```
配 `source_test.go`（testcontainers）：建带 source 的行、查回、adopt 翻转、RoleHasUserBindings 真/假各一。

- [ ] **步骤 2：先写 apply 失败测试（policy_as_code_test.go）**

镜像 `manager_apply_template_test.go` 范式（testcontainers，`policy.NewPolicyManager`）。核心用例：
```go
// Export→Import 同内容→空 diff（往返幂等）。
func TestImport_RoundTripIdempotent(t *testing.T) { /* seed app+1 perm+1 role(iac) → Export → Import(dry_run) → 断言 creates=updates=deletes=0 */ }
// Import 新建 + 数据面 bump（apply 后 version 增、outbox 有记录）。
func TestImport_AppliesCreateAndBumps(t *testing.T) { /* 空 app → Import(create 1 perm+1 role,apply) → 断言 perm/role 存在 source=iac、version 增 */ }
// PC-3 有齿：import 文件省略某 manual 权限点 → 不删它。
func TestImport_NeverDeletesManual(t *testing.T) { /* seed manual perm → Import 空文件 apply → 断言 manual perm 仍在 */ }
// PC-4 有齿：dry_run 零副作用。
func TestImport_DryRunNoSideEffects(t *testing.T) { /* seed → Import(dry_run) → 断言 DB 行数/version 全等 */ }
// PC-6：带绑定 iac 角色删除 → conflict、拒 apply、绑定仍在。
func TestImport_DeleteBoundRoleConflict(t *testing.T) {}
```

- [ ] **步骤 3：运行验证失败**

运行：`go test ./internal/controlplane/policy/ -run TestImport -count=1`
预期：FAIL（`ImportAppPolicy` 未定义）。

- [ ] **步骤 4：实现 export + import（policy_as_code.go）**

```go
package policy
// ExportAppPolicy：复用 tenanttemplate.Capture + store.ReadAppDataPolicies → 组 iac.Document（附 source 注记）→ iac.Serialize。纯读。
func (m *PolicyManager) ExportAppPolicy(ctx context.Context, appID int64, format string) (string, error)

// ImportAppPolicy：解析+校验→读 Current 快照→iac.Diff→
//   dryRun=true：只读，返回 Plan（不开写事务、不 bump）。
//   dryRun=false：若 Plan 有 conflict→返回错误(codes.FailedPrecondition 由 handler 映射)；否则单 runVersionedWrite 事务内按 FK 安全序套用 create/adopt/update/delete，返回 Plan + 新 version。
func (m *PolicyManager) ImportAppPolicy(ctx context.Context, appID int64, content []byte, dryRun bool) (*iac.Plan, int64, *cp.Delta, error)
```
apply 的 mutate 内（FK 安全序）：① 删除：对 delete 角色先 `DeleteRolePermission`/`DeleteRoleInheritance` 卸授权再 `DeleteRole`；delete 数据策略 `DeleteDataPolicy`；delete 权限点在无 role 引用后删。② 建/改：`UpsertPermissionWithSource(source=iac)` / `AdoptPermissionSource` → `PermissionIDsByCode` → `InsertRoleWithSource`/`UpdateRoleMeta` + 重设授权（先清后授或差量）+ data_scope（经 `UpsertDataPolicy`，subject_id=role.code）。③ 顶层 data_policy upsert/delete。data_scope/data_policy 变更收集进 `[]cp.DataPolicyChange` 返回（驱动 bump+广播，对齐 ApplyTemplate）。

- [ ] **步骤 5：运行验证通过 + Commit**

运行：`go test ./internal/controlplane/store/ ./internal/controlplane/policy/ -run 'Source|TestImport' -count=1`（PASS）。
```bash
git add internal/controlplane/store/source.go internal/controlplane/store/source_test.go internal/controlplane/policy/policy_as_code.go internal/controlplane/policy/policy_as_code_test.go
git commit -m "feat(policy): M4.1 ExportAppPolicy(复用 Capture)+ImportAppPolicy(dry-run/原子收敛 via runVersionedWrite,FK 安全序,conflict 拒 apply,PC-3/4/6 有齿)+来源感知 store 助手

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 6：mgmt handler + ruleTable + 跨租户矩阵

**文件：**
- 修改：`internal/controlplane/mgmt/server.go`、`authz.go`
- 创建：`internal/controlplane/mgmt/policy_as_code_test.go`

- [ ] **步骤 1：ruleTable +2（authz.go）**

在 `ruleTable` map 加：
```go
	"/sydom.admin.v1.AdminService/ExportAppPolicy": {"policy/export", "read", false, scopeApp},
	"/sydom.admin.v1.AdminService/ImportAppPolicy": {"policy/import", "update", true, scopeApp},
```

- [ ] **步骤 2：先写 handler + 跨租户测试**

`policy_as_code_test.go`（镜像既有 mgmt handler 测试范式，经 `AuthorizeRule` 上下文调用）：export 返回非空 content 且无 secret 子串；import dry_run 返回 diff、apply 改库；**跨租户**：app 属租户 A、调用者属租户 B → `ImportAppPolicy` 返回 `codes.PermissionDenied`（TenantDomainOf fail-close）；停用 app（status=2）→ import 被 `CheckStatusWrite` 拒。

- [ ] **步骤 3：实现 handler（server.go）**

```go
func (s *AdminServer) ExportAppPolicy(ctx context.Context, r *adminv1.ExportAppPolicyRequest) (*adminv1.ExportAppPolicyResponse, error) {
	content, err := s.mgr.ExportAppPolicy(ctx, int64(r.AppId), r.Format)
	if err != nil { return nil, /* 映射 */ }
	return &adminv1.ExportAppPolicyResponse{Content: content}, nil
}
func (s *AdminServer) ImportAppPolicy(ctx context.Context, r *adminv1.ImportAppPolicyRequest) (*adminv1.ImportAppPolicyResponse, error) {
	plan, version, _, err := s.mgr.ImportAppPolicy(ctx, int64(r.AppId), []byte(r.Content), r.DryRun)
	// conflict → codes.FailedPrecondition；解析/校验失败 → codes.InvalidArgument；其余 Internal（脱敏，复用 errorsanitize）。
	// 组装 PolicyDiffEntry + 计数 + applied=!r.DryRun。
}
```
> 鉴权由现有 gRPC 拦截器 + `AuthorizeRule`(ruleTable) 统一施加；status 写闸由现有 `CheckStatusWrite` 对 isWrite=true 的 ImportAppPolicy 自动覆盖。**不写第二套授权。**

- [ ] **步骤 4：运行验证通过 + Commit**

运行：`go test ./internal/controlplane/mgmt/ -run 'PolicyCode\|Import\|Export' -count=1`（PASS）。
```bash
git add internal/controlplane/mgmt/server.go internal/controlplane/mgmt/authz.go internal/controlplane/mgmt/policy_as_code_test.go
git commit -m "feat(mgmt): M4.1 Export/ImportAppPolicy handler + ruleTable +2(scopeApp,import isWrite 受 CheckStatusWrite)+跨租户矩阵(TenantDomainOf fail-close)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 7：REST 2 路由

**文件：**
- 修改：`internal/controlplane/restgw/`（路由注册文件，参考既有 appRoutes 范式）
- 创建：REST 测试文件（镜像既有 restgw 测试）

- [ ] **步骤 1：先写测试**

镜像既有 restgw 测试：`GET /v1/apps/{app_id}/policy/export?format=yaml` 经 REST-HMAC 鉴权返回 200 + 非空 body；`POST /v1/apps/{app_id}/policy/import?dry_run=true`（body=文件内容）返回 200 + diff JSON。app_id 取 path 权威。未知 app → 404/403 fail-close。

- [ ] **步骤 2：实现路由**

在 appRoutes 加 2 条：`GET /v1/apps/{app_id}/policy/export`（→ ExportAppPolicy，format 取 query）、`POST /v1/apps/{app_id}/policy/import`（→ ImportAppPolicy，content 取 body、dry_run 取 query）。app_id 从 path 覆写进请求（镜像既有 path 权威范式）。复用唯一 `AuthorizeRule`+`CheckStatusWrite`，路由计数更新。

- [ ] **步骤 3：运行验证通过 + Commit**

运行：`go test ./internal/controlplane/restgw/ -count=1`（PASS）。
```bash
git add internal/controlplane/restgw/
git commit -m "feat(restgw): M4.1 策略即代码 2 REST 路由(GET export?format / POST import?dry_run,app_id path 权威,复用 AuthorizeRule+CheckStatusWrite)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 8：Console「策略即代码」页（建模台，无新 JS）

**文件：**
- 创建：`internal/controlplane/console/routes_policy_code.go`
- 创建：`internal/controlplane/console/templates/policy_code.html`、`policy_code_diff.html`
- 修改：`internal/controlplane/console/handler.go`（路由注册）+ 建模台导航 partial

- [ ] **步骤 1：先写测试（routes_policy_code_test.go）**

镜像 `routes_datapolicy_test.go`/`routes_ops_test.go`：
- GET `/apps/{app_id}/policy-code` → 200，含「策略即代码」标题 + export 链接 + import textarea + 单 h1 + breadcrumb。
- POST import（dry_run）→ 200 渲染 diff 预览页（含分类计数）。
- POST import 确认 apply → PRG 303。
- export GET → 200，`Content-Disposition` attachment，body 非空且无 secret。

- [ ] **步骤 2：实现 handler + 模板**

`routes_policy_code.go`：GET 渲染 `policy_code.html`（export 按钮指向 export 子路由、import textarea + format 提示）。POST import：会话→CSRF→`AuthorizeRule`(ImportAppPolicy)→调 `ImportAppPolicy(dry_run=true)` 渲 `policy_code_diff.html`（分类列出 + 确认表单回显原 content 隐藏域 + `confirmed=1`）；带 `confirmed=1` 再 POST → `dry_run=false` apply → PRG flash。export 子路由：`AuthorizeRule`(ExportAppPolicy)→`ExportAppPolicy`→写 `Content-Disposition`。**镜像 `doWrite`/`requireConfirm` 安全管线，复用 M3.1 设计系统，无新 JS。** 模板用 breadcrumb + 单 h1 + 设计系统组件类（对齐 M3.4b 页头约定）。

- [ ] **步骤 3：注册路由 + 建模台导航入口**

`handler.go` 注册 GET/POST 路由；建模台导航（permissions/roles/datapolicies 同级）加「策略即代码」入口。

- [ ] **步骤 4：运行验证通过 + Commit**

运行：`go test ./internal/controlplane/console/ -run PolicyCode -count=1`（PASS）。
```bash
git add internal/controlplane/console/routes_policy_code.go internal/controlplane/console/routes_policy_code_test.go internal/controlplane/console/templates/policy_code.html internal/controlplane/console/templates/policy_code_diff.html internal/controlplane/console/handler.go
git commit -m "feat(console): M4.1 策略即代码页(建模台,export 下载+import textarea+服务端 diff 预览+确认 apply PRG,镜像 doWrite/requireConfirm,无新 JS,M3.1 设计系统)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 9：整体核验 PC-1..8 + 安全评审 + 走查 + FF

- [ ] **步骤 1：PC 不变量逐条核验**

```bash
BASE=85354f0
# PC-1 授权真相零触碰（authz.go 仅 +2 条；adminauthz/enforcer.go/sidecar = 0）
git diff $BASE..HEAD -- internal/controlplane/adminauthz/ casbin/enforcer.go internal/sidecar/ | wc -l   # 期望 0
git diff $BASE..HEAD -- internal/controlplane/mgmt/authz.go | grep -c '^+' # 仅 ExportAppPolicy/ImportAppPolicy 两条 + 头
```
预期：adminauthz/enforcer.go/sidecar diff=0；authz.go 仅 +2 ruleTable 条目。

- [ ] **步骤 2：格式/静态/全量测试**

```bash
[ -z "$(gofmt -l internal/ api/)" ] && echo GOFMT_CLEAN || gofmt -l internal/ api/
go vet ./...
go test ./... 2>&1 | grep -cE '^FAIL'   # 期望 0（含 e2e + iac/policy/store/mgmt/restgw/console testcontainers）
make proto-check
```

- [ ] **步骤 3：真实浏览器 axe 走查（diff 预览页）**

复用 M3.4/技术债清理走查脚手架范式（build-tag walkthrough、testcontainers、系统 Chrome + axe-core 4.10.2 页内注入、走查后删脚手架不提交）：对 `policy_code`（textarea 页）+ `policy_code_diff`（diff 预览页，需 seed 一个有变更的 import 才能渲 diff）走查，验单 h1 + breadcrumb + 0 违规。记录 `docs/superpowers/2026-06-28-m4-1-policy-as-code-walkthrough.md` 并 commit。

- [ ] **步骤 4：opus 整体评审（子代理 model=opus）**

逐条核 PC-1..8 + 深挖：收敛只动 iac 子集（manual/auto 不碰有齿）、dry-run 零副作用、原子 fail-close、删除安全（带绑定角色 conflict）、export 无 secret、租户隔离、数据面 bump+广播保真、三面 parity、M1.1 matcher/adminauthz/enforcer/sidecar 零触碰。READY 方可合并。（子代理撞会话限额则控制者 inline 复跑评审。）

- [ ] **步骤 5：更新记忆**

`project_detailed_design_progress.md` 加「M4.1」节；`MEMORY.md` 索引追加 M4.1 完成 + 关键涌现。

- [ ] **步骤 6：FF 合并本地 main（按既往）+ 视情况 push origin**

worktree 全绿 + opus READY 后 FF（`git -C <main> merge --ff-only <branch>`），核实 main==feature tip，清理 worktree。push origin 与否问用户（本轮已建立 push 习惯）。

---

## 自检记录

**规格覆盖度（对照 spec §1–§9）：** §3 文件模型+双格式 → 任务 3 ✓；§4 来源标记+采纳 → 任务 1（列）+任务 4（adopt diff）+任务 5（apply 翻 source）✓；§5 export → 任务 5（apply）+任务 6（handler）✓；§6 import 收敛算法（解析/校验/diff/dry-run/原子 apply/版本守护/删除安全）→ 任务 3+4+5 ✓；§7 三面 surface → 任务 6（gRPC）+7（REST）+8（Console）✓；§8 PC-1..8 → 任务 9 步骤 1+4 + 各任务有齿测试 ✓；§9 测试策略 → 各任务 TDD 步骤 ✓；§10 任务分组 → 任务 1–9 ✓。

**占位符扫描：** 任务 1/2/3/4 含完整 SQL/Go/proto/测试代码；任务 5/6/7/8 的 boilerplate（store 助手 SQL、handler 映射、REST 路由、Console 页）给出签名 + 算法 + 镜像范式引用（store.go / manager_apply_template_test.go / routes_datapolicy.go / routes_ops.go / 既有 restgw appRoutes），实现者按既有模式落地——与技术债清理计划「镜像 deleteRole 范式」同粒度。任务 3 步骤 4 的啰嗦校验已标注简化点。

**类型一致性：** `iac.Document/Permission/Role/DataScope/DataPolicy`（任务 3）↔ `iac.Current/Plan/PlanItem`（任务 4）↔ `policy.ExportAppPolicy/ImportAppPolicy`（任务 5）↔ proto `ExportAppPolicyRequest/ImportAppPolicyResponse/PolicyDiffEntry`（任务 2）签名贯通；store 助手签名（任务 5 步骤 1）与 apply 调用（任务 5 步骤 4）一致；`cp.DataPolicy`/`cp.PermissionPoint`/`runVersionedWrite`/`Capture` 均用核实的既有签名。
