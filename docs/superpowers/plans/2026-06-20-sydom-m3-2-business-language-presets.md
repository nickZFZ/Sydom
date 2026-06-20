# M3.2a+b 业务概念翻译层 + 模板核心 + 司域官方预设包 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 让非技术租户管理员能一键从司域官方预设包 bootstrap 一个空 app（权限点 + 业务角色），并在运营台始终看到一致的中文业务语言。

**架构：** ①集中化翻译层（`console/bizterm.go`：系统动词词表 + 能力名解析，无新表）；②代码内嵌预设包（`internal/controlplane/presets`：`//go:embed *.json` + loader + 校验）；③应用引擎（`policy.ApplyTemplate`：单 `runVersionedWrite` 复用 `UpsertAutoPermission` + 确定性 code 角色 upsert，幂等、auto 不覆盖 manual、原子）；④2 个 scopeApp RPC（`ListTemplates`/`ApplyTemplate`）；⑤运营台模板库/预览/应用摘要页（复用 M3.1 设计系统，无新 JS）。纯控制面 + 表现层，不碰 sidecar/数据面。

**技术栈：** Go、`//go:embed`、protobuf（buf）、testcontainers PG、`html/template`、casbin 元-RBAC（既有）。

**基准：** spec `docs/superpowers/specs/2026-06-20-sydom-m3-2-business-language-presets-design.md`。本计划在 off-main worktree 执行。

**关键不变量（TP-1..TP-8，贯穿全程）：** TP-1 一份授权真相（三面共用 `AuthorizeRule`+唯一 `ruleTable`）/ TP-2 fail-close 降级无枚举 / TP-3 auto 不覆盖 manual / TP-4 幂等（re-apply 无重复）/ TP-5 原子（单 `runVersionedWrite` 回滚）/ TP-6 租户隔离（`TenantDomainOf` 跨租户 403）/ TP-7 secret 不泄露 / TP-8 运营台无原语。

---

## 文件结构

- 创建 `internal/controlplane/console/bizterm.go` — 翻译层（动词词表 + `capabilityName`/`actionLabel`/`roleName` 纯函数）。
- 修改 `internal/controlplane/console/routes_ops.go` — `capName.label`/`roleName` 改委托 bizterm。
- 创建 `internal/controlplane/console/bizterm_test.go`。
- 创建 `internal/controlplane/presets/presets.go` — 类型 + `//go:embed *.json` + `Load`/`All`/`Get` + 启动校验。
- 创建 `internal/controlplane/presets/general-admin.json`、`ecommerce-ops.json` — 2 个官方包。
- 创建 `internal/controlplane/presets/presets_test.go`。
- 修改 `internal/controlplane/store/store.go` — 加 `PermissionIDsByCode` + `UpsertTemplateRole`。
- 修改 `internal/controlplane/store/store_test.go`。
- 修改 `internal/controlplane/policy/manager.go` — 加 `ApplyTemplateResult` 类型 + `TemplateRole` 输入类型 + `ApplyTemplate` 方法。
- 创建 `internal/controlplane/policy/manager_apply_template_test.go`。
- 修改 `api/proto/sydom/admin/v1/admin.proto` + regen `gen/`。
- 创建 `internal/controlplane/mgmt/templates.go` — `ListTemplates`/`ApplyTemplate` handler + presets→policy 转换。
- 修改 `internal/controlplane/mgmt/authz.go` — `ruleTable` +2 条。
- 创建 `internal/controlplane/mgmt/templates_test.go`。
- 创建 `internal/controlplane/console/routes_templates.go` — 模板库页 + 应用 handler + `registerTemplates`。
- 创建 `internal/controlplane/console/templates/ops_templates.html`、`ops_template_applied.html`。
- 修改 `internal/controlplane/console/handler.go` — 加 `h.registerTemplates(mux)`。
- 创建 `internal/controlplane/console/routes_templates_test.go`。

---

## 任务 1：业务概念翻译层（bizterm）

**文件：**
- 创建：`internal/controlplane/console/bizterm.go`
- 创建：`internal/controlplane/console/bizterm_test.go`
- 修改：`internal/controlplane/console/routes_ops.go`（`capName.label`、`roleName` 改委托）

- [ ] **步骤 1：写失败测试 `bizterm_test.go`**

```go
package console

import "testing"

func TestActionLabel(t *testing.T) {
	cases := map[string]string{
		"read": "查看", "list": "查看", "get": "查看", "view": "查看",
		"create": "新建", "add": "新建",
		"update": "编辑", "write": "编辑", "edit": "编辑",
		"delete": "删除", "remove": "删除",
		"export": "导出", "import": "导入",
		"approve": "审批", "reject": "驳回", "assign": "分配",
		"frobnicate": "frobnicate", // 未知 action 原样返回，不臆造
	}
	for action, want := range cases {
		if got := actionLabel(action); got != want {
			t.Errorf("actionLabel(%q)=%q want %q", action, got, want)
		}
	}
}

func TestCapabilityName(t *testing.T) {
	// 显式 name 最优。
	if got := capabilityName("查看订单", "order", "read"); got != "查看订单" {
		t.Errorf("explicit name: got %q", got)
	}
	// 缺 name → 合成「resource · 动词」，绝不裸 resource:action。
	got := capabilityName("", "order", "read")
	if got != "order · 查看" {
		t.Errorf("composed: got %q want %q", got, "order · 查看")
	}
	if got == "order:read" {
		t.Errorf("must not fall back to raw resource:action")
	}
}

func TestRoleName(t *testing.T) {
	m := map[string]string{"sales": "销售经理"}
	if got := roleName(m, "sales"); got != "销售经理" {
		t.Errorf("hit: got %q", got)
	}
	if got := roleName(m, "unknown"); got != "unknown" {
		t.Errorf("miss must fall back to code, got %q", got)
	}
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/console/ -run 'TestActionLabel|TestCapabilityName|TestRoleName' -count=1`
预期：FAIL（`actionLabel`/`capabilityName` 未定义）。

- [ ] **步骤 3：创建 `bizterm.go`**

```go
package console

// bizterm —— 业务概念翻译层：把技术原语（action/resource）渲染为一致的中文业务语言。
// 纯函数 + 系统动词词表，无 I/O、无新表。运营台所有面共用（TP-8 无原语）。

// actionVerb 是系统内置动作动词词表（原语 action → 中文动词）。
var actionVerb = map[string]string{
	"read": "查看", "list": "查看", "get": "查看", "view": "查看",
	"create": "新建", "add": "新建",
	"update": "编辑", "write": "编辑", "edit": "编辑",
	"delete": "删除", "remove": "删除",
	"export": "导出", "import": "导入",
	"approve": "审批", "reject": "驳回", "assign": "分配",
}

// actionLabel 返回 action 的中文动词；未在词表中则原样返回（不臆造）。
func actionLabel(action string) string {
	if v, ok := actionVerb[action]; ok {
		return v
	}
	return action
}

// capabilityName 解析一条能力的业务名：① 显式 name 非空→用之；② 否则合成「resource · 动词」。
// 绝不返回裸 "resource:action"（TP-8）。
func capabilityName(name, resource, action string) string {
	if name != "" {
		return name
	}
	return resource + " · " + actionLabel(action)
}

// roleName 从 code→name map 取业务名，缺省返回 code 自身（绝不回退到技术 role_id）。
func roleName(m map[string]string, code string) string {
	if n, ok := m[code]; ok {
		return n
	}
	return code
}
```

- [ ] **步骤 4：从 `routes_ops.go` 移除重复定义、改委托**

在 `routes_ops.go`：删除现有的 `func roleName(...)`（已移至 bizterm.go，签名相同）；把 `capName.label` 改为：

```go
func (m capName) label(resource, action string) string {
	if n, ok := m[[2]string{resource, action}]; ok {
		return n
	}
	return capabilityName("", resource, action) // 缺名→「resource · 动词」，不裸 resource:action
}
```

> 注：`label` 的回退从 `resource + ":" + action` 改为 `capabilityName("", ...)`（"resource · 动词"）。这是改进，不破坏既有测试——`routes_ops_test` 断言 `NotContains "orders:read"`，新回退 "order · 查看" 仍满足。

- [ ] **步骤 5：运行测试 + 既有 ops 回归**

运行：`go test ./internal/controlplane/console/ -run 'TestActionLabel|TestCapabilityName|TestRoleName|TestOps' -count=1 2>&1 | tail -5`
预期：PASS（新单测 + 既有 `TestOps_*` 全绿，约 80s 因 ops 测试起 testcontainers）。
再 `go build ./...`。

- [ ] **步骤 6：Commit**

```bash
git add internal/controlplane/console/bizterm.go internal/controlplane/console/bizterm_test.go internal/controlplane/console/routes_ops.go && \
git commit -m "feat(console): 业务概念翻译层 bizterm(动词词表+能力名解析,集中化 ops 映射)"
```

---

## 任务 2：预设包 presets（embed + loader + 2 官方包）

**文件：**
- 创建：`internal/controlplane/presets/presets.go`
- 创建：`internal/controlplane/presets/general-admin.json`、`ecommerce-ops.json`
- 创建：`internal/controlplane/presets/presets_test.go`

- [ ] **步骤 1：写失败测试 `presets_test.go`**

```go
package presets

import "testing"

func TestLoad_ValidatesAndExposes(t *testing.T) {
	all := All()
	if len(all) < 2 {
		t.Fatalf("want >=2 packs, got %d", len(all))
	}
	tpl, ok := Get("general-admin")
	if !ok {
		t.Fatal("general-admin not found")
	}
	if tpl.Name == "" || len(tpl.Permissions) == 0 || len(tpl.Roles) == 0 {
		t.Fatalf("general-admin incomplete: %+v", tpl)
	}
	// 每个权限点都有中文业务名（运营台无原语前提）。
	for _, p := range tpl.Permissions {
		if p.Name == "" {
			t.Errorf("permission %q missing name", p.Code)
		}
	}
}

func TestGet_Unknown(t *testing.T) {
	if _, ok := Get("nope"); ok {
		t.Error("Get(nope) should be false")
	}
}

// validate 在 Load 失败时返回 error；这里直接对内置内容跑校验确保发布内容合法。
func TestValidate_BuiltinPacksAreConsistent(t *testing.T) {
	for _, tpl := range All() {
		codes := map[string]bool{}
		for _, p := range tpl.Permissions {
			if codes[p.Code] {
				t.Errorf("%s: dup permission code %q", tpl.ID, p.Code)
			}
			codes[p.Code] = true
		}
		for _, r := range tpl.Roles {
			for _, pc := range r.PermissionCodes {
				if !codes[pc] {
					t.Errorf("%s role %s: references unknown permission code %q", tpl.ID, r.Key, pc)
				}
			}
		}
	}
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/presets/ -count=1`
预期：FAIL（包不存在）。

- [ ] **步骤 3：创建 `presets.go`**

```go
// Package presets 提供司域官方预设包（//go:embed 内嵌、随二进制版本化、租户不可改）。
// 启动期严格校验：包 id 唯一、包内 permission.code 唯一、role.permission_codes 引用存在；
// 任一违例 panic（fail-close，绝不带损坏内容运行）。
package presets

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"
)

//go:embed *.json
var files embed.FS

// Permission 是预设包中的一条权限点。
type Permission struct {
	Code        string `json:"code"`
	Resource    string `json:"resource"`
	Action      string `json:"action"`
	Type        string `json:"type"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Role 是预设包中的一个业务角色（key 用于确定性 code，permission_codes 引用本包权限点）。
type Role struct {
	Key             string   `json:"key"`
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	PermissionCodes []string `json:"permission_codes"`
	// data_scopes / onboarding 预留：本片不解析（M3.2c/M3.4），未知字段被 json 忽略。
}

// Template 是一个预设包。
type Template struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Version     uint32       `json:"version"`
	Permissions []Permission `json:"permissions"`
	Roles       []Role       `json:"roles"`
}

var loaded []Template
var byID map[string]Template

func init() {
	ts, err := load()
	if err != nil {
		panic("presets: " + err.Error()) // fail-close：损坏内容拒绝启动
	}
	loaded = ts
	byID = map[string]Template{}
	for _, t := range ts {
		byID[t.ID] = t
	}
}

func load() ([]Template, error) {
	entries, err := files.ReadDir(".")
	if err != nil {
		return nil, err
	}
	var out []Template
	seenID := map[string]bool{}
	for _, e := range entries {
		b, err := files.ReadFile(e.Name())
		if err != nil {
			return nil, err
		}
		var t Template
		if err := json.Unmarshal(b, &t); err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		if t.ID == "" {
			return nil, fmt.Errorf("%s: empty id", e.Name())
		}
		if seenID[t.ID] {
			return nil, fmt.Errorf("duplicate template id %q", t.ID)
		}
		seenID[t.ID] = true
		codes := map[string]bool{}
		for _, p := range t.Permissions {
			if p.Code == "" {
				return nil, fmt.Errorf("%s: empty permission code", t.ID)
			}
			if codes[p.Code] {
				return nil, fmt.Errorf("%s: duplicate permission code %q", t.ID, p.Code)
			}
			codes[p.Code] = true
		}
		for _, r := range t.Roles {
			for _, pc := range r.PermissionCodes {
				if !codes[pc] {
					return nil, fmt.Errorf("%s role %q: unknown permission code %q", t.ID, r.Key, pc)
				}
			}
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// All 返回全部内置预设包（按 id 排序、稳定）。
func All() []Template { return loaded }

// Get 按 id 取预设包。
func Get(id string) (Template, bool) { t, ok := byID[id]; return t, ok }
```

- [ ] **步骤 4：创建 `general-admin.json`**

```json
{
  "id": "general-admin",
  "name": "通用后台管理",
  "description": "适合大多数后台系统：内容的查看/编辑/删除，配三个常用角色",
  "version": 1,
  "permissions": [
    {"code": "content.read", "resource": "content", "action": "read", "type": "act", "name": "查看内容", "description": ""},
    {"code": "content.write", "resource": "content", "action": "write", "type": "act", "name": "编辑内容", "description": ""},
    {"code": "content.delete", "resource": "content", "action": "delete", "type": "act", "name": "删除内容", "description": ""},
    {"code": "user.read", "resource": "user", "action": "read", "type": "act", "name": "查看用户", "description": ""},
    {"code": "user.write", "resource": "user", "action": "write", "type": "act", "name": "编辑用户", "description": ""}
  ],
  "roles": [
    {"key": "admin", "name": "管理员", "description": "全部能力", "permission_codes": ["content.read", "content.write", "content.delete", "user.read", "user.write"]},
    {"key": "editor", "name": "编辑", "description": "查看与编辑内容", "permission_codes": ["content.read", "content.write"]},
    {"key": "viewer", "name": "只读", "description": "仅查看", "permission_codes": ["content.read", "user.read"]}
  ]
}
```

- [ ] **步骤 5：创建 `ecommerce-ops.json`**

```json
{
  "id": "ecommerce-ops",
  "name": "电商运营",
  "description": "电商后台常用：订单/商品/退款，配运营常见角色",
  "version": 1,
  "permissions": [
    {"code": "order.read", "resource": "order", "action": "read", "type": "act", "name": "查看订单", "description": ""},
    {"code": "order.export", "resource": "order", "action": "export", "type": "act", "name": "导出订单", "description": ""},
    {"code": "product.read", "resource": "product", "action": "read", "type": "act", "name": "查看商品", "description": ""},
    {"code": "product.write", "resource": "product", "action": "write", "type": "act", "name": "编辑商品", "description": ""},
    {"code": "refund.approve", "resource": "refund", "action": "approve", "type": "act", "name": "审批退款", "description": ""}
  ],
  "roles": [
    {"key": "ops-manager", "name": "运营经理", "description": "订单商品退款全管", "permission_codes": ["order.read", "order.export", "product.read", "product.write", "refund.approve"]},
    {"key": "customer-service", "name": "客服", "description": "看订单、审退款", "permission_codes": ["order.read", "refund.approve"]},
    {"key": "merchandiser", "name": "商品运营", "description": "管商品", "permission_codes": ["product.read", "product.write"]}
  ]
}
```

- [ ] **步骤 6：运行测试 + 构建**

运行：`go test ./internal/controlplane/presets/ -count=1 -v 2>&1 | tail -10`，预期 PASS；再 `go build ./...`。

- [ ] **步骤 7：Commit**

```bash
git add internal/controlplane/presets/ && \
git commit -m "feat(presets): 官方预设包内嵌+loader+启动校验(通用后台/电商运营 2 包)"
```

---

## 任务 3：store 应用引擎辅助（PermissionIDsByCode + UpsertTemplateRole）

**文件：**
- 修改：`internal/controlplane/store/store.go`
- 修改：`internal/controlplane/store/store_test.go`

- [ ] **步骤 1：写失败测试 `store_test.go`**（沿用该文件既有 testcontainers 夹具风格，参考现有 `TestUpsertAutoPermission_*`）

```go
func TestPermissionIDsByCode(t *testing.T) {
	db := newTestDB(t) // 复用本文件既有夹具（如无此名，照搬同文件其它测试的建库方式）
	appID := seedAppForStore(t, db)
	ctx := context.Background()
	id1, err := store.UpsertPermission(ctx, db, appID, "a.read", "a", "read", "act", "查看A")
	require.NoError(t, err)
	_, err = store.UpsertPermission(ctx, db, appID, "b.read", "b", "read", "act", "查看B")
	require.NoError(t, err)

	m, err := store.PermissionIDsByCode(ctx, db, appID, []string{"a.read", "b.read", "missing"})
	require.NoError(t, err)
	require.Equal(t, id1, m["a.read"])
	require.Contains(t, m, "b.read")
	require.NotContains(t, m, "missing") // 不存在的 code 不入 map
}

func TestUpsertTemplateRole_Idempotent(t *testing.T) {
	db := newTestDB(t)
	appID := seedAppForStore(t, db)
	ctx := context.Background()

	id1, created1, err := store.UpsertTemplateRole(ctx, db, appID, "tpl:x:admin", "管理员")
	require.NoError(t, err)
	require.True(t, created1)

	id2, created2, err := store.UpsertTemplateRole(ctx, db, appID, "tpl:x:admin", "改了的名") // re-apply
	require.NoError(t, err)
	require.False(t, created2)      // 已存在 → 跳过
	require.Equal(t, id1, id2)      // 同一行
	// 名称不被覆盖（不改人工后续编辑）。
	var name string
	require.NoError(t, db.QueryRow(`SELECT name FROM role WHERE id=$1`, id1).Scan(&name))
	require.Equal(t, "管理员", name)
}
```

> 注：`newTestDB`/`seedAppForStore` 用 store_test.go 已有的夹具函数名；若名称不同，照搬同文件其它测试的建库与 SeedApp 调用方式（`dbtest.MigratedDSN` + `dbtest.SeedApp`）。

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/store/ -run 'TestPermissionIDsByCode|TestUpsertTemplateRole' -count=1`
预期：FAIL（函数未定义）。

- [ ] **步骤 3：在 `store.go` 加两个 helper**（紧邻 `UpsertAutoPermission` 后）

```go
import "github.com/lib/pq" // 若文件未导入 pq.Array，则加；否则用现有数组写法

// PermissionIDsByCode 解析本 app 一组 code → permission_id（用于模板应用按 code 授权）。
// 不存在的 code 不入 map（调用方自行处理缺失，loader 已保证包内引用一致）。
func PermissionIDsByCode(ctx context.Context, ex cp.DBTX, appID int64, codes []string) (map[string]int64, error) {
	out := map[string]int64{}
	if len(codes) == 0 {
		return out, nil
	}
	rows, err := ex.QueryContext(ctx,
		`SELECT code, id FROM permission WHERE app_id=$1 AND code = ANY($2)`,
		appID, pq.Array(codes))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var code string
		var id int64
		if err := rows.Scan(&code, &id); err != nil {
			return nil, err
		}
		out[code] = id
	}
	return out, rows.Err()
}

// UpsertTemplateRole 幂等建模板角色：不存在→建（created=true）；已存在（同 app_id,code）→
// 跳过返回现有 id（created=false），绝不覆盖 name（不改人工后续编辑，TP-3）。
func UpsertTemplateRole(ctx context.Context, ex cp.DBTX, appID int64, code, name string) (id int64, created bool, err error) {
	err = ex.QueryRowContext(ctx,
		`INSERT INTO role (app_id, code, name) VALUES ($1,$2,$3)
		 ON CONFLICT (app_id, code) DO NOTHING RETURNING id`,
		appID, code, name).Scan(&id)
	if err == nil {
		return id, true, nil // 新建
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false, err
	}
	// 冲突（已存在）→ 取现有 id，不改 name。
	err = ex.QueryRowContext(ctx,
		`SELECT id FROM role WHERE app_id=$1 AND code=$2`, appID, code).Scan(&id)
	if err != nil {
		return 0, false, err
	}
	return id, false, nil
}
```

> 注：确认 `store.go` 顶部已 import `errors`、`database/sql`（既有 `UpsertAutoPermission` 已用 `errors.Is(err, sql.ErrNoRows)`，故已在）。`pq.Array` 若 store.go 尚未引 `github.com/lib/pq`，加该 import（go.mod 已有 lib/pq）。

- [ ] **步骤 4：运行测试 + 构建**

运行：`go test ./internal/controlplane/store/ -run 'TestPermissionIDsByCode|TestUpsertTemplateRole' -count=1 2>&1 | tail -5`，预期 PASS；`go build ./...`。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/store/store.go internal/controlplane/store/store_test.go && \
git commit -m "feat(store): 模板应用辅助 PermissionIDsByCode + UpsertTemplateRole(确定性 code 幂等)"
```

---

## 任务 4：应用引擎 policy.ApplyTemplate

**文件：**
- 修改：`internal/controlplane/policy/manager.go`
- 创建：`internal/controlplane/policy/manager_apply_template_test.go`

- [ ] **步骤 1：写失败测试**（testcontainers，参考 `manager_business_role_test.go` 夹具）

```go
package policy

import (
	"context"
	"testing"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestApplyTemplate_CreatesAndIsIdempotent(t *testing.T) {
	db := mustDB(t) // 复用本包测试既有建库夹具名；若不同照搬同包其它测试
	appID := dbtest.SeedApp(t, db)
	m := NewPolicyManager(db, nil)
	ctx := context.Background()

	perms := []cp.PermissionPoint{
		{Code: "order.read", Resource: "order", Action: "read", Type: "act", Name: "查看订单"},
		{Code: "order.export", Resource: "order", Action: "export", Type: "act", Name: "导出订单"},
	}
	roles := []TemplateRole{
		{Key: "ops", Name: "运营", PermissionCodes: []string{"order.read", "order.export"}},
	}

	res, _, err := m.ApplyTemplate(ctx, appID, "ecommerce-ops", perms, roles)
	require.NoError(t, err)
	require.Equal(t, 2, res.PermsUpserted)
	require.Equal(t, 1, res.RolesCreated)
	require.Equal(t, 0, res.RolesSkipped)

	// 角色 code 确定性。
	var cnt int
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM role WHERE app_id=$1 AND code=$2`, appID, "tpl:ecommerce-ops:ops").Scan(&cnt))
	require.Equal(t, 1, cnt)
	// 角色已授到 2 个权限点。
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM role_permission rp JOIN role r ON r.id=rp.role_id WHERE r.code=$1`, "tpl:ecommerce-ops:ops").Scan(&cnt))
	require.Equal(t, 2, cnt)

	// re-apply：幂等——无重复角色、role 计入 skipped。
	res2, _, err := m.ApplyTemplate(ctx, appID, "ecommerce-ops", perms, roles)
	require.NoError(t, err)
	require.Equal(t, 1, res2.RolesSkipped)
	require.Equal(t, 0, res2.RolesCreated)
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM role WHERE app_id=$1 AND code=$2`, appID, "tpl:ecommerce-ops:ops").Scan(&cnt))
	require.Equal(t, 1, cnt) // 仍只有一个角色
}

func TestApplyTemplate_NeverClobbersManual(t *testing.T) {
	db := mustDB(t)
	appID := dbtest.SeedApp(t, db)
	m := NewPolicyManager(db, nil)
	ctx := context.Background()

	// 预置一个 manual 权限点同 code。
	_, err := store.UpsertPermission(ctx, db, appID, "order.read", "order", "read", "act", "人工命名")
	require.NoError(t, err)

	perms := []cp.PermissionPoint{{Code: "order.read", Resource: "order", Action: "read", Type: "act", Name: "查看订单"}}
	res, _, err := m.ApplyTemplate(ctx, appID, "x", perms, nil)
	require.NoError(t, err)
	require.Equal(t, 1, res.PermsSkipped) // manual 命中→跳过保留
	var name string
	require.NoError(t, db.QueryRow(`SELECT name FROM permission WHERE app_id=$1 AND code=$2`, appID, "order.read").Scan(&name))
	require.Equal(t, "人工命名", name) // 名称未被覆盖
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/policy/ -run TestApplyTemplate -count=1`
预期：FAIL（`ApplyTemplate`/`TemplateRole` 未定义）。

- [ ] **步骤 3：在 `manager.go` 加类型 + 方法**（紧邻 `CreateBusinessRole` 后）

```go
// TemplateRole 是 ApplyTemplate 的角色输入（与 presets 解耦，由 mgmt 转换填入）。
type TemplateRole struct {
	Key             string
	Name            string
	PermissionCodes []string
}

// ApplyTemplateResult 是一次模板应用的写入统计。
type ApplyTemplateResult struct {
	PermsUpserted int // 权限点新增/刷新（source=auto）
	PermsSkipped  int // 权限点命中 manual 被保留
	RolesCreated  int // 角色新建
	RolesSkipped  int // 角色已存在被跳过（确定性 code）
}

// ApplyTemplate 原子幂等应用一个模板到 app（单 runVersionedWrite）：
//  1. 逐 permission 复用 UpsertAutoPermission（auto 不覆盖 manual，TP-3）；
//  2. 解析 code→id；
//  3. 逐 role 用确定性 code `tpl:<templateID>:<key>` upsert，仅新建时按 permission_codes 授权。
// 任一步失败整事务回滚（TP-5）。投影变化照常 bump+广播。
func (m *PolicyManager) ApplyTemplate(ctx context.Context, appID int64, templateID string,
	perms []cp.PermissionPoint, roles []TemplateRole) (ApplyTemplateResult, *cp.Delta, error) {
	var res ApplyTemplateResult
	d, err := m.runVersionedWrite(ctx, appID, writeOp{
		action: "apply_template", entityType: "template", entityID: templateID,
		mutate: func(ctx context.Context, tx *sql.Tx) ([]cp.DataPolicyChange, error) {
			// 1. 权限点。
			var codes []string
			for _, p := range perms {
				applied, e := store.UpsertAutoPermission(ctx, tx, appID,
					p.Code, p.Resource, p.Action, p.Type, p.Name, p.Description)
				if e != nil {
					return nil, e
				}
				if applied {
					res.PermsUpserted++
				} else {
					res.PermsSkipped++
				}
				codes = append(codes, p.Code)
			}
			// 2. code→id（含 manual 命中行，权限点不论 auto/manual 都参与授权）。
			idByCode, e := store.PermissionIDsByCode(ctx, tx, appID, codes)
			if e != nil {
				return nil, e
			}
			// 3. 角色（确定性 code 幂等）。
			for _, r := range roles {
				code := "tpl:" + templateID + ":" + r.Key
				roleID, created, e := store.UpsertTemplateRole(ctx, tx, appID, code, r.Name)
				if e != nil {
					return nil, e
				}
				if !created {
					res.RolesSkipped++
					continue // 已存在 → 不改授权（不动人工后续编辑）
				}
				res.RolesCreated++
				for _, pc := range r.PermissionCodes {
					pid, ok := idByCode[pc]
					if !ok {
						continue // 理论不达（loader 已校验引用），防御性跳过
					}
					if e := store.InsertRolePermission(ctx, tx, appID, roleID, pid, cp.EffectAllow); e != nil {
						return nil, e
					}
				}
			}
			return nil, nil
		},
	})
	if err != nil {
		return ApplyTemplateResult{}, nil, err
	}
	return res, d, nil
}
```

> 注：`ctx` 的 audit actor 由调用方（mgmt handler）经 `AuthorizeRule` 注入的 `cp.WithOperator` 提供，无需在此再设。

- [ ] **步骤 4：运行测试 + 构建**

运行：`go test ./internal/controlplane/policy/ -run TestApplyTemplate -count=1 2>&1 | tail -8`，预期 PASS；`go build ./...`。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/policy/manager.go internal/controlplane/policy/manager_apply_template_test.go && \
git commit -m "feat(policy): ApplyTemplate 应用引擎(幂等/auto 不覆盖 manual/确定性角色 code/原子)"
```

---

## 任务 5：proto 加 2 RPC + 生成代码

**文件：**
- 修改：`api/proto/sydom/admin/v1/admin.proto`
- 生成：`gen/`（`make proto-gen`）

- [ ] **步骤 1：在 `admin.proto` 的 `service AdminService` 块内加 2 RPC**（紧邻其它 List RPC）

```proto
  rpc ListTemplates(ListTemplatesRequest) returns (ListTemplatesResponse);
  rpc ApplyTemplate(ApplyTemplateRequest) returns (ApplyTemplateResponse);
```

- [ ] **步骤 2：在文件 message 区加 message**（放在文件末尾或 List 类 message 附近）

```proto
message TemplatePermission {
  string code = 1;
  string resource = 2;
  string action = 3;
  string type = 4;
  string name = 5;
  string description = 6;
}

message TemplateRole {
  string key = 1;
  string name = 2;
  string description = 3;
  repeated string permission_codes = 4;
}

message Template {
  string id = 1;
  string name = 2;
  string description = 3;
  uint32 version = 4;
  repeated TemplatePermission permissions = 5;
  repeated TemplateRole roles = 6;
}

message ListTemplatesRequest { uint64 app_id = 1; }
message ListTemplatesResponse { repeated Template templates = 1; }

message ApplyTemplateRequest {
  uint64 app_id = 1;
  string template_id = 2;
}
message ApplyTemplateResponse {
  uint32 permissions_upserted = 1;
  uint32 permissions_skipped = 2;
  uint32 roles_created = 3;
  uint32 roles_skipped = 4;
}
```

> 注：Request/Response 每 RPC 唯一（无 `RPC_REQUEST_RESPONSE_UNIQUE` 冲突）；`Template`/`TemplatePermission`/`TemplateRole` 是普通响应内嵌 message（非 request/response 共享），无需 buf except。

- [ ] **步骤 3：生成 + 校验无漂移**

运行：`make proto-gen`（或仓库既定 proto 生成命令）→ `make proto-check`（预期无漂移）→ `go build ./...`（生成的 `adminv1.*` 类型可编译）。

- [ ] **步骤 4：Commit**

```bash
git add api/proto/sydom/admin/v1/admin.proto gen/ && \
git commit -m "feat(proto): AdminService +ListTemplates/ApplyTemplate RPC + 6 message"
```

---

## 任务 6：mgmt handler + ruleTable

**文件：**
- 创建：`internal/controlplane/mgmt/templates.go`
- 修改：`internal/controlplane/mgmt/authz.go`（ruleTable +2）
- 创建：`internal/controlplane/mgmt/templates_test.go`

- [ ] **步骤 1：在 `authz.go` 的 `ruleTable` 加 2 条**

```go
	"/sydom.admin.v1.AdminService/ListTemplates":          {"template", "read", false, scopeApp},
	"/sydom.admin.v1.AdminService/ApplyTemplate":          {"template", "apply", true, scopeApp},
```

> `ApplyTemplate` `isWrite=true` → 受 `CheckStatusWrite` 拦截（停用 app 不可应用，与其它写一致）。既有通配 grant（super-admin `(*,*,*)`、租户管理员 `(t:<id>,*,*)`）经 matcher 自动覆盖新 "template" 资源——无需改 seeder。

- [ ] **步骤 2：写失败测试 `templates_test.go`**（参考 `business_role_test.go` 跨租户夹具）

```go
package mgmt_test

// TestApplyTemplate_OwnTenant：本租户管理员应用预设包 → 成功，创建权限点+角色。
// TestApplyTemplate_CrossTenant403：对他人 app 应用 → PermissionDenied（TP-6）。
// TestApplyTemplate_UnknownTemplate：未知 template_id → InvalidArgument/NotFound。
// TestListTemplates_ReturnsBuiltins：返回 >=2 个内置包，含中文 name。
```

> 实现者：照搬 `business_role_test.go` 的 `setupAdmin`/跨租户夹具，断言上述 4 个行为。`ApplyTemplate` 调用经 gRPC server（带认证+鉴权拦截器）或直调 `AuthorizeRule`+handler，与既有测试范式一致。

- [ ] **步骤 3：运行验证失败**

运行：`go test ./internal/controlplane/mgmt/ -run 'TestApplyTemplate|TestListTemplates' -count=1`，预期 FAIL（方法未定义）。

- [ ] **步骤 4：创建 `templates.go`**

```go
package mgmt

import (
	"context"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/policy"
	"github.com/nickZFZ/Sydom/internal/controlplane/presets"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ListTemplates 返回司域官方预设包（全局产品资料；鉴权以 app 为上下文 scopeApp read）。
func (s *AdminServer) ListTemplates(ctx context.Context, r *adminv1.ListTemplatesRequest) (*adminv1.ListTemplatesResponse, error) {
	resp := &adminv1.ListTemplatesResponse{}
	for _, t := range presets.All() {
		resp.Templates = append(resp.Templates, toProtoTemplate(t))
	}
	return resp, nil
}

// ApplyTemplate 原子幂等应用预设包到 app。
func (s *AdminServer) ApplyTemplate(ctx context.Context, r *adminv1.ApplyTemplateRequest) (*adminv1.ApplyTemplateResponse, error) {
	tpl, ok := presets.Get(r.TemplateId)
	if !ok {
		return nil, status.Errorf(codes.InvalidArgument, "unknown template %q", r.TemplateId)
	}
	perms := make([]cp.PermissionPoint, 0, len(tpl.Permissions))
	for _, p := range tpl.Permissions {
		perms = append(perms, cp.PermissionPoint{
			Code: p.Code, Resource: p.Resource, Action: p.Action,
			Type: p.Type, Name: p.Name, Description: p.Description,
		})
	}
	roles := make([]policy.TemplateRole, 0, len(tpl.Roles))
	for _, rr := range tpl.Roles {
		roles = append(roles, policy.TemplateRole{Key: rr.Key, Name: rr.Name, PermissionCodes: rr.PermissionCodes})
	}
	res, _, err := s.mgr.ApplyTemplate(ctx, int64(r.AppId), tpl.ID, perms, roles)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "apply template: %v", err)
	}
	return &adminv1.ApplyTemplateResponse{
		PermissionsUpserted: uint32(res.PermsUpserted),
		PermissionsSkipped:  uint32(res.PermsSkipped),
		RolesCreated:        uint32(res.RolesCreated),
		RolesSkipped:        uint32(res.RolesSkipped),
	}, nil
}

func toProtoTemplate(t presets.Template) *adminv1.Template {
	pt := &adminv1.Template{Id: t.ID, Name: t.Name, Description: t.Description, Version: t.Version}
	for _, p := range t.Permissions {
		pt.Permissions = append(pt.Permissions, &adminv1.TemplatePermission{
			Code: p.Code, Resource: p.Resource, Action: p.Action, Type: p.Type, Name: p.Name, Description: p.Description,
		})
	}
	for _, r := range t.Roles {
		pt.Roles = append(pt.Roles, &adminv1.TemplateRole{
			Key: r.Key, Name: r.Name, Description: r.Description, PermissionCodes: r.PermissionCodes,
		})
	}
	return pt
}
```

- [ ] **步骤 5：运行测试 + 构建**

运行：`go test ./internal/controlplane/mgmt/ -run 'TestApplyTemplate|TestListTemplates' -count=1 2>&1 | tail -8`，预期 PASS；`go build ./...`。

- [ ] **步骤 6：Commit**

```bash
git add internal/controlplane/mgmt/templates.go internal/controlplane/mgmt/authz.go internal/controlplane/mgmt/templates_test.go && \
git commit -m "feat(mgmt): ListTemplates/ApplyTemplate handler + ruleTable 2 条(scopeApp template read/apply)"
```

---

## 任务 7：运营台模板库页（list + preview + apply summary）

**文件：**
- 创建：`internal/controlplane/console/routes_templates.go`
- 创建：`internal/controlplane/console/templates/ops_templates.html`、`ops_template_applied.html`
- 修改：`internal/controlplane/console/handler.go`（加 `h.registerTemplates(mux)`）
- 创建：`internal/controlplane/console/routes_templates_test.go`

- [ ] **步骤 1：写失败测试 `routes_templates_test.go`**（参考 `routes_ops_test.go` 夹具）

```go
// TestConsole_Templates_ListAndPreview：登录 root + SeedApp → GET /ops/apps/{id}/templates
//   200，body 含「通用后台管理」「电商运营」（业务名），含权限点业务名「查看订单」，无原语「order:read」。
// TestConsole_ApplyTemplate_CSRF：POST .../templates/apply 缺 CSRF → 403；带 CSRF → 200 摘要页含计数。
// TestConsole_ApplyTemplate_Idempotent：连应用两次第二次摘要显示角色全部「已存在/跳过」。
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/console/ -run 'TestConsole_Templates|TestConsole_ApplyTemplate' -count=1`，预期 FAIL（路由未注册）。

- [ ] **步骤 3：创建 `routes_templates.go`**

```go
package console

import (
	"fmt"
	"net/http"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
)

// registerTemplates 注册运营台模板库路由。
func (h *Handler) registerTemplates(mux *http.ServeMux) {
	mux.HandleFunc("GET /ops/apps/{app_id}/templates", h.opsTemplates)
	mux.HandleFunc("POST /ops/apps/{app_id}/templates/apply", h.opsApplyTemplate)
}

// opsTemplates：GET /ops/apps/{app_id}/templates —— 模板库 + 预览（经 bizterm 渲染业务名）。
func (h *Handler) opsTemplates(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	appID, err := pathUint64(r, "app_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListTemplates", err)
		return
	}
	msg := &adminv1.ListTemplatesRequest{AppId: appID}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"ListTemplates", principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListTemplates", err)
		return
	}
	resp, err := h.srv.ListTemplates(ctx, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListTemplates", err)
		return
	}
	// 渲染视图：每模板的权限点/角色经 bizterm 渲染业务名（capabilityName 兜底缺名）。
	type capRow struct{ Name string }
	type roleRow struct {
		Name string
		Caps []string
	}
	type tplView struct {
		ID, Name, Description string
		PermCount, RoleCount  int
		Caps                  []capRow
		Roles                 []roleRow
	}
	var views []tplView
	for _, t := range resp.Templates {
		v := tplView{ID: t.Id, Name: t.Name, Description: t.Description,
			PermCount: len(t.Permissions), RoleCount: len(t.Roles)}
		nameByCode := map[string]string{}
		for _, p := range t.Permissions {
			cn := capabilityName(p.Name, p.Resource, p.Action)
			v.Caps = append(v.Caps, capRow{Name: cn})
			nameByCode[p.Code] = cn
		}
		for _, role := range t.Roles {
			rr := roleRow{Name: role.Name}
			for _, pc := range role.PermissionCodes {
				rr.Caps = append(rr.Caps, nameByCode[pc])
			}
			v.Roles = append(v.Roles, rr)
		}
		views = append(views, v)
	}
	h.renderPage(w, r, "ops_templates.html", http.StatusOK, map[string]any{
		"AppID": appID, "Templates": views, "CSRF": sess.CSRF, "OpsNav": "templates",
	})
}

// opsApplyTemplate：POST /ops/apps/{app_id}/templates/apply —— 应用后直接渲染摘要（非 PRG，
// 因 ApplyTemplate 幂等、刷新重提交无害；安全管线镜像 doWrite：会话→CSRF→鉴权→status 闸→调用）。
func (h *Handler) opsApplyTemplate(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if !h.checkCSRF(r, sess) {
		h.renderError(w, r, codesPermissionDenied, "CSRF 校验失败", nil)
		return
	}
	appID, err := pathUint64(r, "app_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"ApplyTemplate", err)
		return
	}
	msg := &adminv1.ApplyTemplateRequest{AppId: appID, TemplateId: r.FormValue("template_id")}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"ApplyTemplate", principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ApplyTemplate", err)
		return
	}
	if err := mgmt.CheckStatusWrite(ctx, h.db, svc+"ApplyTemplate", msg); err != nil {
		h.renderGRPCError(w, r, svc+"ApplyTemplate", err)
		return
	}
	resp, err := h.srv.ApplyTemplate(ctx, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ApplyTemplate", err)
		return
	}
	h.renderPage(w, r, "ops_template_applied.html", http.StatusOK, map[string]any{
		"AppID":         appID,
		"PermsUpserted": resp.PermissionsUpserted,
		"PermsSkipped":  resp.PermissionsSkipped,
		"RolesCreated":  resp.RolesCreated,
		"RolesSkipped":  resp.RolesSkipped,
		"OpsNav":        "templates",
	})
}
```

> 注：`codesPermissionDenied` —— 用 `routes_*.go` 中现有的 codes 引用方式（`handler.go` doWrite 用 `codes.PermissionDenied`，本文件 import `google.golang.org/grpc/codes` 并用 `codes.PermissionDenied`，替换占位 `codesPermissionDenied`）。`h.checkCSRF`/`h.renderError`/`h.renderGRPCError`/`h.renderPage`/`h.requireSession`/`h.srv`/`h.enf`/`h.db` 均为既有。

- [ ] **步骤 4：创建模板 `ops_templates.html`**（复用 M3.1 设计系统 class：workspace/appnav/card/btn/badge/empty-state）

```html
{{define "title"}}模板库 · 运营台 · App {{.AppID}}{{end}}
{{define "content"}}<div class="workspace">
<aside class="appnav" aria-label="运营台导航"><div class="appname">运营台 · App #{{.AppID}}</div>
<a href="/ops/apps/{{.AppID}}/people">人员</a>
<a href="/ops/apps/{{.AppID}}/roles">业务角色</a>
<a href="/ops/apps/{{.AppID}}/templates" aria-current="page">模板库</a>
</aside>
<section>
<nav class="breadcrumb" aria-label="面包屑">运营台 · 模板库</nav>
<h1>模板库</h1>
{{if .Templates}}
{{range .Templates}}
<div class="card" style="margin-bottom:var(--space-4)">
<div class="card-header">{{.Name}} <span class="badge badge-muted">{{.PermCount}} 权限点 · {{.RoleCount}} 角色</span></div>
<p class="hint">{{.Description}}</p>
<details>
<summary>预览</summary>
<h2>能力</h2>
<ul class="list-plain">{{range .Caps}}<li>{{.Name}}</li>{{end}}</ul>
<h2>角色</h2>
<ul class="list-plain">{{range .Roles}}<li>{{.Name}}：{{range $i, $c := .Caps}}{{if $i}}、{{end}}{{$c}}{{end}}</li>{{end}}</ul>
</details>
<form method="post" action="/ops/apps/{{$.AppID}}/templates/apply" class="inline-form">
<input type="hidden" name="csrf_token" value="{{$.CSRF}}">
<input type="hidden" name="template_id" value="{{.ID}}">
<button class="btn btn-primary">应用到本应用</button>
</form>
</div>
{{end}}
{{else}}
<div class="empty-state">暂无可用模板。</div>
{{end}}
</section></div>{{end}}
```

- [ ] **步骤 5：创建模板 `ops_template_applied.html`**

```html
{{define "title"}}模板已应用 · 运营台 · App {{.AppID}}{{end}}
{{define "content"}}<div class="workspace">
<aside class="appnav" aria-label="运营台导航"><div class="appname">运营台 · App #{{.AppID}}</div>
<a href="/ops/apps/{{.AppID}}/people">人员</a>
<a href="/ops/apps/{{.AppID}}/roles">业务角色</a>
<a href="/ops/apps/{{.AppID}}/templates" aria-current="page">模板库</a>
</aside>
<section>
<nav class="breadcrumb" aria-label="面包屑">运营台 · 模板库 · 应用结果</nav>
<h1>模板已应用</h1>
<div class="alert alert-info" role="status">
权限点：新增/刷新 {{.PermsUpserted}}，跳过（已有人工配置）{{.PermsSkipped}}；
角色：新建 {{.RolesCreated}}，跳过（已存在）{{.RolesSkipped}}。
</div>
<p><a class="btn btn-primary" href="/ops/apps/{{.AppID}}/people">去人员页分配角色</a>
<a class="btn" href="/ops/apps/{{.AppID}}/templates">返回模板库</a></p>
</section></div>{{end}}
```

- [ ] **步骤 6：在 `handler.go` 注册**

在既有 register 序列后加：`h.registerTemplates(mux) // M3.2 运营台模板库`。

- [ ] **步骤 7：运行测试 + 构建**

运行：`go build ./...`、`go test ./internal/controlplane/console/ -run 'TestConsole_Templates|TestConsole_ApplyTemplate' -count=1 2>&1 | tail -8`，预期 PASS。

- [ ] **步骤 8：Commit**

```bash
git add internal/controlplane/console/routes_templates.go internal/controlplane/console/templates/ops_templates.html internal/controlplane/console/templates/ops_template_applied.html internal/controlplane/console/handler.go internal/controlplane/console/routes_templates_test.go && \
git commit -m "feat(console): 运营台模板库页(list/preview/apply 摘要,复用 M3.1 设计系统,无新 JS)"
```

---

## 任务 8：整体验证 + TP-1..TP-8 + FF 合并

- [ ] **步骤 1：TP 不变量逐条核验**

```bash
BASE=<worktree base sha>
# TP-1 一份授权真相：ListTemplates/ApplyTemplate 在 ruleTable，Console 经 AuthorizeRule（grep 确认）
grep -n 'ListTemplates\|ApplyTemplate' internal/controlplane/mgmt/authz.go
grep -n 'AuthorizeRule' internal/controlplane/console/routes_templates.go   # 两处
# TP-7 secret 不泄露：apply/list 链路不碰 app_secret
git diff $BASE..HEAD | grep -i 'secret' || echo "TP-7 OK: 无 secret 触碰"
# TP-8 无原语 + 无新 JS
ls internal/controlplane/console/static/*.js   # 仅 datapolicy.js
# DS-5（沿用 M3.1）：新模板若加内联 style 仅用 var()，无硬编码色
grep -nE '#[0-9a-fA-F]{3,6}|rgb\(' internal/controlplane/console/templates/ops_templates.html internal/controlplane/console/templates/ops_template_applied.html || echo "无硬编码色"
```
TP-2 fail-close / TP-3 auto 不覆盖 manual / TP-4 幂等 / TP-5 原子 / TP-6 租户隔离：由任务 4/6 单测覆盖（复跑确认）。

- [ ] **步骤 2：格式/静态/proto/全量测试**

```bash
gofmt -l internal/ api/   # 空
go vet ./...              # 净
make proto-check          # 无漂移
go test ./... 2>&1 | tail -40   # 0 FAIL（含 presets/store/policy/mgmt/console/e2e）
```

- [ ] **步骤 3：更新进度记忆**

`project_detailed_design_progress.md` 加 M3.2a+b 节（翻译层 + presets + apply 引擎 + 运营台模板库；TP-1..8；下一步 M3.2c 租户自有模板 + 数据范围预设）；`MEMORY.md` 索引钩子追加 M3.2a+b 完成 + 下一步 M3.2c。

- [ ] **步骤 4：FF 合并本地 main（不 push origin）**

worktree 全绿 + opus 整体评审 READY 后 FF 并入本地 main，清 worktree（沿用 M3.1 范式：ExitWorktree keep → 主仓 `git merge --ff-only` → `git worktree remove` → `git branch -d`）。

---

## 自检记录

**规格覆盖度（对照 spec）：** §3 翻译层→任务1 ✓；§4 预设包模型→任务2 ✓；§5 应用引擎（ListTemplates/ApplyTemplate + 幂等/auto 不覆盖 manual/确定性 code/原子）→任务3+4+6 ✓；§6 UI→任务7 ✓；§7 后端契约 + ruleTable + 不变量→任务5+6+8 ✓；§8 测试策略→各任务 TDD + 任务8 全量 ✓；§9 YAGNI（租户模板/数据范围/onboarding/术语表/dry-run 均未触）✓。

**占位符扫描：** 各任务给出实际代码（bizterm/presets/store helpers/ApplyTemplate/proto/handler/模板/路由均有完整代码）；测试夹具名（`newTestDB`/`mustDB`/`seedAppForStore`）注明「照搬同包既有夹具」——因各包测试建库夹具名不一，实现者首步应先看同包现有测试的建库方式对齐（非占位、是适配既有夹具的明确指令）。

**类型一致性：** `presets.Template/Permission/Role` ↔ `adminv1.Template/TemplatePermission/TemplateRole`（任务5 proto）↔ mgmt `toProtoTemplate` 转换（任务6）字段一一对应；`policy.TemplateRole{Key,Name,PermissionCodes}` ↔ mgmt 转换填入（任务6）一致；`policy.ApplyTemplateResult{PermsUpserted,PermsSkipped,RolesCreated,RolesSkipped}` ↔ `adminv1.ApplyTemplateResponse{permissions_upserted,permissions_skipped,roles_created,roles_skipped}`（任务6 映射）一致；`store.UpsertTemplateRole`(任务3) 返回 `(id,created,err)` ↔ `policy.ApplyTemplate`(任务4) 消费 `created` 一致；`store.PermissionIDsByCode`(任务3) ↔ 任务4 `idByCode` 一致；确定性角色 code `tpl:<templateID>:<key>` 在任务3 测试、任务4 实现、任务7 摘要语义一致。

**关键口径固化：** apply 非 PRG 直接渲染摘要（幂等使刷新重提交无害，镜像一次性 secret 页范式）；`ApplyTemplate` `isWrite=true` 受 status 闸（停用 app 不可应用）；既有通配 grant 自动覆盖新 "template" 资源（无需改 seeder）；翻译层回退 "resource · 动词"（非裸 resource:action）不破坏既有 `routes_ops_test` 的 NotContains 断言。
