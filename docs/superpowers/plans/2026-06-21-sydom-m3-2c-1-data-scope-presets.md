# M3.2c-1 数据范围（符号化）预设 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 让司域官方预设包的业务角色携带**符号化数据范围**（`$user.属性`），应用模板时在同一原子事务内种入 `data_policy` 并同步到数据面；运营台预览渲染为符号谓词（不枚举）。

**架构：** ①预设包 `Role` 加 `data_scopes` 字段 + loader 校验（resource/condition/effect）+ 2 官方示意；②`policy.ApplyTemplate` 在角色「新建」时按 data_scopes 调 `store.UpsertDataPolicy` 种入（懒读 tx 锁定版本得 vNew，runVersionedWrite 随后 bump 到同值；返回 `DataPolicyChange` → 真实下发数据面）；③proto 3 改（`TemplateRole.data_scopes` + `TemplateDataScope` + `ApplyTemplateResponse.data_scopes_created`）；④mgmt 映射；⑤运营台预览符号谓词（控制面自足渲染器，不碰 sidecar）+ apply 摘要计数。

**技术栈：** Go、`//go:embed`、protobuf（buf）、testcontainers PG、`html/template`、既有 `data_policy`/dataperm 符号谓词基础设施。

**基准：** spec `docs/superpowers/specs/2026-06-21-sydom-m3-2c-data-scope-presets-design.md`。本计划在 off-main worktree 执行。

**关键不变量（DSC-1..7，贯穿全程）：** DSC-1 一份授权真相（ruleTable 不变）/ DSC-2 符号口径忠实（预览符号谓词不枚举）/ DSC-3 condition 原样透传（不预解析语义，仅校验合法 JSON）/ DSC-4 幂等（只在角色新建时种入，再 apply 不重复）/ DSC-5 原子（data_policy 与角色同事务回滚）/ DSC-6 数据面同步保真（产生 DataPolicyChange → bump → 广播）/ DSC-7 租户隔离 + secret 不泄露 + M1.1 matcher 一字未改。

---

## 文件结构

- 修改 `internal/controlplane/presets/presets.go` — 加 `DataScope` 类型 + `Role.DataScopes` 字段 + loader 校验。
- 修改 `internal/controlplane/presets/general-admin.json`、`ecommerce-ops.json` — 各加 1 个示意 data_scope。
- 修改 `internal/controlplane/presets/presets_test.go`。
- 修改 `internal/controlplane/policy/manager.go` — `TemplateRole` 加 `DataScopes`、`ApplyTemplateResult` 加 `DataScopesCreated`、`TemplateDataScope` 类型、`ApplyTemplate` 种入逻辑。
- 修改 `internal/controlplane/policy/manager_apply_template_test.go`。
- 修改 `api/proto/sydom/admin/v1/admin.proto` + regen `gen/`。
- 修改 `internal/controlplane/mgmt/templates.go` — `toProtoTemplate` 映射 data_scopes、ApplyTemplate handler 转换 + 响应计数。
- 修改 `internal/controlplane/mgmt/templates_test.go`。
- 创建 `internal/controlplane/console/condition_predicate.go` — condition 树 → 符号谓词纯函数（fail-soft）。
- 创建 `internal/controlplane/console/condition_predicate_test.go`。
- 修改 `internal/controlplane/console/routes_templates.go` — 视图加 data_scopes 符号谓词。
- 修改 `internal/controlplane/console/templates/ops_templates.html`、`ops_template_applied.html`。
- 修改 `internal/controlplane/console/routes_templates_test.go`。
- 修改 `internal/controlplane/restgw/routes_templates_test.go` — 断言 data_scopes_created 透出。

---

## 任务 1：预设包 data_scopes 模型 + loader 校验 + 2 官方示意

**文件：**
- 修改：`internal/controlplane/presets/presets.go`
- 修改：`internal/controlplane/presets/general-admin.json`、`ecommerce-ops.json`
- 修改：`internal/controlplane/presets/presets_test.go`

- [ ] **步骤 1：写失败测试（追加到 `presets_test.go`）**

```go
func TestLoad_ParsesDataScopes(t *testing.T) {
	tpl, ok := Get("general-admin")
	if !ok {
		t.Fatal("general-admin not found")
	}
	var found bool
	for _, r := range tpl.Roles {
		for _, ds := range r.DataScopes {
			if ds.Resource == "" {
				t.Errorf("role %s data_scope missing resource", r.Key)
			}
			if len(ds.Condition) == 0 {
				t.Errorf("role %s data_scope missing condition", r.Key)
			}
			found = true
		}
	}
	if !found {
		t.Error("general-admin should ship >=1 illustrative data_scope")
	}
}
```

并在既有 `TestLoad_RejectsCorrupt` 的 `cases` map 内追加 3 个 fail-close 子用例：

```go
		"empty data_scope resource": `{"id":"a","permissions":[],` +
			`"roles":[{"key":"r","data_scopes":[{"resource":"","condition":{"field":"x","op":"EQ","value":"1"}}]}]}`,
		"invalid data_scope condition json": `{"id":"a","permissions":[],` +
			`"roles":[{"key":"r","data_scopes":[{"resource":"order","condition":"not-json"}]}]}`,
		"bad data_scope effect": `{"id":"a","permissions":[],` +
			`"roles":[{"key":"r","data_scopes":[{"resource":"order","effect":"maybe","condition":{"field":"x","op":"EQ","value":"1"}}]}]}`,
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/presets/ -run 'TestLoad_ParsesDataScopes|TestLoad_RejectsCorrupt' -count=1`
预期：FAIL（`DataScope`/`Role.DataScopes` 未定义；新 corrupt 子用例未被拒）。

- [ ] **步骤 3：在 `presets.go` 加类型 + 字段**

加 import `"encoding/json"`（已在）。新增类型，并给 `Role` 加字段：

```go
// DataScope 是预设角色的一条符号化数据范围（condition 为既有条件树 JSON，$user.xxx 符号保留，原样透传）。
type DataScope struct {
	Resource  string          `json:"resource"`
	Effect    string          `json:"effect"` // 空串按 allow
	Condition json.RawMessage `json:"condition"`
}
```

在 `Role` 结构体内，把预留注释替换为真实字段：

```go
	PermissionCodes []string    `json:"permission_codes"`
	DataScopes      []DataScope `json:"data_scopes"`
	// onboarding 预留：本片不解析（M3.4），未知字段被 json 忽略。
```

- [ ] **步骤 4：在 `load()` 的 role 循环内加 data_scopes 校验**

在既有 `seenKey[r.Key] = true` 之后、`permission_codes` 引用校验旁，追加：

```go
			for _, ds := range r.DataScopes {
				if ds.Resource == "" {
					return nil, fmt.Errorf("%s role %q: empty data_scope resource", t.ID, r.Key)
				}
				if len(ds.Condition) == 0 || !json.Valid(ds.Condition) {
					return nil, fmt.Errorf("%s role %q: data_scope condition not valid json", t.ID, r.Key)
				}
				if ds.Effect != "" && ds.Effect != "allow" && ds.Effect != "deny" {
					return nil, fmt.Errorf("%s role %q: bad data_scope effect %q", t.ID, r.Key, ds.Effect)
				}
			}
```

> 注：只校验 condition 可解析为合法 JSON（`json.Valid`），**绝不**解析条件树语义（DSC-3 透传，语义 fail-close 留 sidecar）。

- [ ] **步骤 5：给 2 官方包各加 1 个示意 data_scope**

`general-admin.json` 的 `editor` 角色对象内（与 `permission_codes` 同级）加：

```json
"data_scopes": [{"resource": "content", "effect": "allow", "condition": {"field": "owner_id", "op": "EQ", "value": "$user.id"}}]
```

`ecommerce-ops.json` 的 `customer-service` 角色对象内加：

```json
"data_scopes": [{"resource": "order", "effect": "allow", "condition": {"field": "department", "op": "EQ", "value": "$user.department"}}]
```

- [ ] **步骤 6：运行测试 + 构建**

运行：`go test ./internal/controlplane/presets/ -count=1 -v 2>&1 | tail -20`，预期 PASS（含新 ParsesDataScopes + 3 corrupt 子用例有齿）；再 `go build ./...`、`gofmt -l internal/controlplane/presets/`（空）。

- [ ] **步骤 7：Commit**

```bash
git add internal/controlplane/presets/ && \
git commit -m "feat(presets): 角色 data_scopes 符号数据范围模型+loader 校验(resource/合法 JSON/effect)+2 官方示意"
```

---

## 任务 2：应用引擎 `ApplyTemplate` 种入数据范围

**文件：**
- 修改：`internal/controlplane/policy/manager.go`
- 修改：`internal/controlplane/policy/manager_apply_template_test.go`

- [ ] **步骤 1：写失败测试（追加到 `manager_apply_template_test.go`）**

```go
func TestApplyTemplate_SeedsDataScopesOnNewRole(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	m := policy.NewPolicyManager(db, nil)
	ctx := context.Background()

	perms := []cp.PermissionPoint{{Code: "order.read", Resource: "order", Action: "read", Type: "act", Name: "查看订单"}}
	roles := []policy.TemplateRole{{
		Key: "cs", Name: "客服", PermissionCodes: []string{"order.read"},
		DataScopes: []policy.TemplateDataScope{
			{Resource: "order", Effect: "allow", Condition: `{"field":"department","op":"EQ","value":"$user.department"}`},
		},
	}}

	res, d, err := m.ApplyTemplate(ctx, appID, "ecommerce-ops", perms, roles)
	require.NoError(t, err)
	require.Equal(t, 1, res.DataScopesCreated)
	require.NotNil(t, d)                  // 数据范围产生 Delta（数据面同步，DSC-6）
	require.Len(t, d.DataChanges, 1)      // 1 条 DataPolicyChange 下发

	// data_policy 落库：subject_type=role、subject_id=确定性 role code、condition 透传。
	var stype, sid, cond string
	require.NoError(t, db.QueryRow(
		`SELECT subject_type, subject_id, condition FROM data_policy WHERE app_id=$1 AND resource='order'`, appID).
		Scan(&stype, &sid, &cond))
	require.Equal(t, "role", stype)
	require.Equal(t, "tpl:ecommerce-ops:cs", sid)
	require.JSONEq(t, `{"field":"department","op":"EQ","value":"$user.department"}`, cond)

	// re-apply 幂等：角色已存在→跳过，不种数据范围、无重复 data_policy 行（DSC-4）。
	res2, _, err := m.ApplyTemplate(ctx, appID, "ecommerce-ops", perms, roles)
	require.NoError(t, err)
	require.Equal(t, 0, res2.DataScopesCreated)
	var cnt int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM data_policy WHERE app_id=$1`, appID).Scan(&cnt))
	require.Equal(t, 1, cnt)
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/policy/ -run TestApplyTemplate_SeedsDataScopes -count=1`
预期：FAIL（`TemplateDataScope`/`DataScopes`/`DataScopesCreated` 未定义）。

- [ ] **步骤 3：在 `manager.go` 加类型 + 字段**

`TemplateRole` 加字段、`ApplyTemplateResult` 加计数、新增 `TemplateDataScope`：

```go
// TemplateDataScope 是 ApplyTemplate 的数据范围输入（condition 原样透传 JSON 串）。
type TemplateDataScope struct {
	Resource  string
	Effect    string
	Condition string
}
```

在既有 `TemplateRole` 内加：`DataScopes []TemplateDataScope`。
在既有 `ApplyTemplateResult` 内加：`DataScopesCreated int // 数据范围新建（仅角色新建时种入）`。

- [ ] **步骤 4：在 `ApplyTemplate` 的 mutate 内种入数据范围**

把 mutate 闭包改为累积并返回 `dataChanges`。在闭包顶部声明：

```go
		mutate: func(ctx context.Context, tx *sql.Tx) ([]cp.DataPolicyChange, error) {
			var dataChanges []cp.DataPolicyChange
			var vNew int64 // 懒读：首个 data_scope 时取 tx 锁定版本得 vNew（runVersionedWrite 随后 bump 到同值）
```

在 role 循环的 `created` 分支内（既有 `for _, pc := range r.PermissionCodes` 授权之后），追加数据范围种入：

```go
				// 数据范围预设：仅新建角色种入（幂等，DSC-4），condition 原样透传（DSC-3）。
				for _, ds := range r.DataScopes {
					if vNew == 0 {
						cur, e := store.LockAppVersion(ctx, tx, appID) // 同 tx 已持锁，返回当前版本
						if e != nil {
							return nil, e
						}
						vNew = cur + 1
					}
					eff := ds.Effect
					if eff == "" {
						eff = cp.EffectAllow
					}
					p := cp.DataPolicy{
						SubjectType: "role", SubjectID: code, Resource: ds.Resource,
						Condition: ds.Condition, Effect: eff,
					}
					id, _, e := store.UpsertDataPolicy(ctx, tx, appID, p, vNew) // 模板种入恒为新增（id=0 插入）
					if e != nil {
						return nil, e
					}
					p.ID = id
					dataChanges = append(dataChanges, cp.DataPolicyChange{Op: cp.ChangeAdd, Policy: p})
					res.DataScopesCreated++
				}
```

把闭包结尾的 `return nil, nil` 改为 `return dataChanges, nil`。

> 注：`code` 是该 role 的确定性 code `tpl:<templateID>:<key>`（既有变量）。`vNew` 懒读使「无数据范围的模板」零额外查询、行为不变。runVersionedWrite 见 `dataChanges` 非空即 bump+广播（DSC-6），失败整笔回滚（DSC-5）。

- [ ] **步骤 5：运行测试 + 构建**

运行：`go test ./internal/controlplane/policy/ -run TestApplyTemplate -count=1 2>&1 | tail -12`，预期 PASS（既有 4 测试 + 新 SeedsDataScopes 全绿）；再 `go build ./...`、`gofmt -l internal/controlplane/policy/`（空）、`go vet ./internal/controlplane/policy/`。

- [ ] **步骤 6：Commit**

```bash
git add internal/controlplane/policy/manager.go internal/controlplane/policy/manager_apply_template_test.go && \
git commit -m "feat(policy): ApplyTemplate 种入数据范围预设(仅新建角色/原子/产生 DataPolicyChange 同步数据面)"
```

---

## 任务 3：proto 加 TemplateDataScope + 字段 + 生成代码

**文件：**
- 修改：`api/proto/sydom/admin/v1/admin.proto`
- 生成：`gen/`

- [ ] **步骤 1：在 `admin.proto` 改 message**

`TemplateRole` 加字段（紧接 `repeated string permission_codes = 4;`）：

```proto
  repeated TemplateDataScope data_scopes = 5;
```

新增 message（放在 `TemplateRole` 附近）：

```proto
message TemplateDataScope {
  string resource = 1;
  string effect = 2;
  string condition = 3; // 条件树 JSON 串（$user.xxx 符号；原样透传）
}
```

`ApplyTemplateResponse` 加字段（紧接 `uint32 roles_skipped = 4;`）：

```proto
  uint32 data_scopes_created = 5;
```

- [ ] **步骤 2：生成 + 校验无漂移**

运行：`make proto-gen` → `make proto-check`（无漂移）→ `go build ./...`（`mgmt.AdminServer` 经 Unimplemented embedding 仍编译；但本片 handler 已存在，下一任务改）。

> 注：`TemplateDataScope` 是普通内嵌 message，无 buf lint 冲突（buf.yaml 已 except RPC_REQUEST_* 三条）。

- [ ] **步骤 3：Commit**

```bash
git add api/proto/sydom/admin/v1/admin.proto gen/ && \
git commit -m "feat(proto): Template +data_scopes/TemplateDataScope + ApplyTemplateResponse.data_scopes_created"
```

---

## 任务 4：mgmt handler 映射 data_scopes + 计数

**文件：**
- 修改：`internal/controlplane/mgmt/templates.go`
- 修改：`internal/controlplane/mgmt/templates_test.go`

- [ ] **步骤 1：写失败测试（追加到 `templates_test.go`）**

```go
func TestApplyTemplate_DataScopesCreated(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	srv := accountsSrv(db)

	resp, err := srv.ApplyTemplate(context.Background(),
		&adminv1.ApplyTemplateRequest{AppId: uint64(appID), TemplateId: "ecommerce-ops"})
	require.NoError(t, err)
	require.GreaterOrEqual(t, resp.DataScopesCreated, uint32(1)) // ecommerce-ops customer-service 带 1 示意
}

func TestListTemplates_IncludesDataScopes(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	srv := accountsSrv(db)

	resp, err := srv.ListTemplates(context.Background(), &adminv1.ListTemplatesRequest{AppId: uint64(appID)})
	require.NoError(t, err)
	var found bool
	for _, tpl := range resp.Templates {
		for _, r := range tpl.Roles {
			if len(r.DataScopes) > 0 && r.DataScopes[0].Condition != "" {
				found = true
			}
		}
	}
	require.True(t, found, "内置包须透出 data_scopes（含 condition）")
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/mgmt/ -run 'TestApplyTemplate_DataScopesCreated|TestListTemplates_IncludesDataScopes' -count=1`
预期：FAIL（toProtoTemplate 未映射 data_scopes；handler 未填 data_scopes_created / 未传 DataScopes）。

- [ ] **步骤 3：在 `templates.go` 的 `toProtoTemplate` 映射 data_scopes**

在 role 映射循环内，构建 `TemplateRole` 时加 data_scopes：

```go
		tr := &adminv1.TemplateRole{
			Key: r.Key, Name: r.Name, Description: r.Description, PermissionCodes: r.PermissionCodes,
		}
		for _, ds := range r.DataScopes {
			tr.DataScopes = append(tr.DataScopes, &adminv1.TemplateDataScope{
				Resource: ds.Resource, Effect: ds.Effect, Condition: string(ds.Condition),
			})
		}
		pt.Roles = append(pt.Roles, tr)
```

> 注：`ds.Condition` 是 `json.RawMessage`，转 proto string 用 `string(ds.Condition)`。

- [ ] **步骤 4：在 `ApplyTemplate` handler 传 DataScopes + 回填计数**

在构建 `roles []policy.TemplateRole` 的循环内，转换 data_scopes：

```go
	for _, rr := range tpl.Roles {
		tr := policy.TemplateRole{Key: rr.Key, Name: rr.Name, PermissionCodes: rr.PermissionCodes}
		for _, ds := range rr.DataScopes {
			tr.DataScopes = append(tr.DataScopes, policy.TemplateDataScope{
				Resource: ds.Resource, Effect: ds.Effect, Condition: string(ds.Condition),
			})
		}
		roles = append(roles, tr)
	}
```

并在响应加计数：`DataScopesCreated: uint32(res.DataScopesCreated),`。

- [ ] **步骤 5：运行测试 + 构建**

运行：`go test ./internal/controlplane/mgmt/ -run 'TestApplyTemplate|TestListTemplates' -count=1 2>&1 | tail -12`，预期 PASS（既有 4 + 新 2）；`go build ./...`、`gofmt -l internal/controlplane/mgmt/`（空）。

- [ ] **步骤 6：Commit**

```bash
git add internal/controlplane/mgmt/templates.go internal/controlplane/mgmt/templates_test.go && \
git commit -m "feat(mgmt): ListTemplates/ApplyTemplate 映射 data_scopes + data_scopes_created 计数"
```

---

## 任务 5：运营台预览符号谓词 + apply 摘要计数 + REST 断言

**文件：**
- 创建：`internal/controlplane/console/condition_predicate.go`、`condition_predicate_test.go`
- 修改：`internal/controlplane/console/routes_templates.go`
- 修改：`internal/controlplane/console/templates/ops_templates.html`、`ops_template_applied.html`
- 修改：`internal/controlplane/console/routes_templates_test.go`、`internal/controlplane/restgw/routes_templates_test.go`

- [ ] **步骤 1：写失败测试 `condition_predicate_test.go`**

```go
package console

import "testing"

func TestConditionPredicate(t *testing.T) {
	cases := map[string]string{
		`{"field":"owner_id","op":"EQ","value":"$user.id"}`:                     "owner_id = $user.id",
		`{"field":"status","op":"IN","value":["a","b"]}`:                        "status IN [a, b]",
		`{"op":"AND","children":[{"field":"a","op":"EQ","value":"1"},{"field":"b","op":"EQ","value":"$user.x"}]}`: "(a = 1 AND b = $user.x)",
	}
	for cond, want := range cases {
		if got := conditionPredicate(cond); got != want {
			t.Errorf("conditionPredicate(%s)=%q want %q", cond, got, want)
		}
	}
	// fail-soft：非法 JSON 不 panic、不泄露原串，回安全占位。
	if got := conditionPredicate("not-json"); got != "（自定义条件）" {
		t.Errorf("bad json fallback got %q", got)
	}
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/console/ -run TestConditionPredicate -count=1`
预期：FAIL（`conditionPredicate` 未定义）。

- [ ] **步骤 3：创建 `condition_predicate.go`（控制面自足渲染器，不碰 sidecar，DSC-2）**

```go
package console

import "encoding/json"

// condition_predicate —— 把 data_policy 条件树渲染为只读符号谓词（展示用，$user.xxx 保留）。
// 控制面自足，不调用/不解析数据面求值逻辑（Sidecar 零漂移）；解析失败 fail-soft 回安全占位。

type condNode struct {
	Op       string          `json:"op"`
	Field    string          `json:"field"`
	Value    json.RawMessage `json:"value"`
	Children []condNode      `json:"children"`
}

// conditionPredicate 渲染条件树为人类可读谓词；非法/空回「（自定义条件）」。
func conditionPredicate(conditionJSON string) string {
	if conditionJSON == "" {
		return "（自定义条件）"
	}
	var n condNode
	if err := json.Unmarshal([]byte(conditionJSON), &n); err != nil {
		return "（自定义条件）"
	}
	s := renderNode(n)
	if s == "" {
		return "（自定义条件）"
	}
	return s
}

func renderNode(n condNode) string {
	switch n.Op {
	case "AND", "OR":
		var parts []string
		for _, c := range n.Children {
			parts = append(parts, renderNode(c))
		}
		if len(parts) == 0 {
			return ""
		}
		sep := " " + n.Op + " "
		out := parts[0]
		for _, p := range parts[1:] {
			out += sep + p
		}
		return "(" + out + ")"
	case "NOT":
		if len(n.Children) != 1 {
			return ""
		}
		return "NOT " + renderNode(n.Children[0])
	default:
		// 叶子：field op value。
		if n.Field == "" {
			return ""
		}
		op := n.Op
		if op == "" {
			op = "EQ"
		}
		return n.Field + " " + symbol(op) + " " + renderValue(n.Value)
	}
}

func symbol(op string) string {
	switch op {
	case "EQ":
		return "="
	case "NE":
		return "!="
	case "GT":
		return ">"
	case "GE":
		return ">="
	case "LT":
		return "<"
	case "LE":
		return "<="
	default:
		return op // IN/BETWEEN 等保留原 token
	}
}

func renderValue(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "?"
	}
	// 字符串值（含 $user.xxx）去引号直显；数组渲染为 [a, b]；其余原样。
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) == nil {
		var parts []string
		for _, e := range arr {
			parts = append(parts, renderValue(e))
		}
		out := "["
		for i, p := range parts {
			if i > 0 {
				out += ", "
			}
			out += p
		}
		return out + "]"
	}
	return string(raw)
}
```

> 注：渲染器把 `IN` 叶子渲染为 `status IN [a, b]`（symbol("IN")="IN"）。fail-soft 绝不泄露原始 JSON 串、绝不 panic。

- [ ] **步骤 4：在 `routes_templates.go` 视图加 data_scopes**

在 `opsTemplates` 的 `roleRow` 视图类型加字段 `Scopes []string`；构建角色视图时填：

```go
			rr := roleRow{Name: role.Name}
			for _, pc := range role.PermissionCodes {
				cn := nameByCode[pc]
				if cn == "" {
					cn = "（未知能力）"
				}
				rr.Caps = append(rr.Caps, cn)
			}
			for _, ds := range role.DataScopes {
				rr.Scopes = append(rr.Scopes, ds.Resource+"：仅 "+conditionPredicate(ds.Condition))
			}
			v.Roles = append(v.Roles, rr)
```

（`roleRow` 类型定义加 `Scopes []string`。）

- [ ] **步骤 5：模板渲染数据范围 + apply 摘要计数**

`ops_templates.html` 在角色预览 `<li>` 内，能力之后加数据范围（仅当有）：

```html
<ul class="list-plain">{{range .Roles}}<li>{{.Name}}：{{range $i, $c := .Caps}}{{if $i}}、{{end}}{{$c}}{{end}}{{if .Scopes}}<br><span class="hint">数据范围：{{range $i, $s := .Scopes}}{{if $i}}；{{end}}{{$s}}{{end}}</span>{{end}}</li>{{end}}</ul>
```

`ops_template_applied.html` 的 `alert` 内追加一句：

```html
数据范围：新建 {{.DataScopesCreated}}。
```

并在 `routes_templates.go` 的 `opsApplyTemplate` 渲染 data map 加 `"DataScopesCreated": resp.DataScopesCreated,`。

- [ ] **步骤 6：写 Console + REST 断言测试**

`routes_templates_test.go`（追加）：`TestConsole_Templates_ShowsDataScope` —— GET 模板库 body 含符号谓词「`$user.department`」或「`$user.id`」，**NotContains 真实枚举值**（如不含某具体部门名）。`routes_templates_test.go` 的 apply 测试断言摘要含「数据范围：新建」。

`restgw/routes_templates_test.go`（追加）：`TestREST_ApplyTemplate_DataScopesCreated` —— POST apply ecommerce-ops，protoUnmarshal `ApplyTemplateResponse`，`require.GreaterOrEqual(t, ar.DataScopesCreated, uint32(1))`。

```go
// console（routes_templates_test.go）
func TestConsole_Templates_ShowsDataScope(t *testing.T) {
	ts, store, db := newConsole(t)
	dbtest.SeedApp(t, db)
	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	appID := dbtest.SeedApp(t, db)
	resp, _ := c.Get(ts.URL + "/ops/apps/" + u(uint64(appID)) + "/templates") // 若无 u 助手，用 fmt.Sprint
	body := readBody(t, resp)
	require.Contains(t, body, "$user.")            // 符号保留
	require.NotContains(t, body, "department = '") // 不枚举真实行（无 SQL 字面行）
}
```

> 实现者：`newConsole`/`loginAndCSRF`/读 body 助手照搬 `routes_templates_test.go` 既有用法（与任务 7/M3.2a+b 同）；REST 助手照搬 `restgw` 既有 `newTestGW`/`rootClient`/`protoUnmarshal`。

- [ ] **步骤 7：运行测试 + 构建**

运行：`go build ./...`、`go test ./internal/controlplane/console/ -run 'TestConditionPredicate|TestConsole_Templates|TestConsole_ApplyTemplate' -count=1 2>&1 | tail -12`、`go test ./internal/controlplane/restgw/ -run 'TestREST_ApplyTemplate' -count=1 2>&1 | tail -6`，预期全 PASS；`gofmt -l internal/controlplane/console/ internal/controlplane/restgw/`（空）。

- [ ] **步骤 8：Commit**

```bash
git add internal/controlplane/console/ internal/controlplane/restgw/routes_templates_test.go && \
git commit -m "feat(console): 模板库预览数据范围符号谓词(控制面自足渲染器 fail-soft)+apply 摘要计数+REST 断言"
```

---

## 任务 6：整体验证 + DSC-1..7 + FF 合并

- [ ] **步骤 1：DSC 不变量逐条核验**

```bash
BASE=<worktree base sha>
# DSC-7 M1.1 matcher 一字未改 / adminauthz 零触碰
git diff $BASE..HEAD -- internal/controlplane/adminauthz/ | wc -l   # 0
# DSC-1 ruleTable 不变（template 仅既有 2 条，未新增/改）
git diff $BASE..HEAD -- internal/controlplane/mgmt/authz.go | wc -l  # 0
# DSC-2 预览符号：渲染器不 import sidecar/dataperm（控制面自足）
grep -n 'sidecar\|dataperm' internal/controlplane/console/condition_predicate.go || echo "DSC-2 OK: 渲染器零数据面耦合"
# DSC-7 secret：链路不碰 secret
git diff $BASE..HEAD | grep -i 'secret' || echo "DSC-7 OK: 无 secret 触碰"
# 无新 JS
ls internal/controlplane/console/static/*.js   # 仅 datapolicy.js
```
DSC-3 透传 / DSC-4 幂等 / DSC-5 原子 / DSC-6 数据面同步：由任务 1/2 单测覆盖（复跑确认）。

- [ ] **步骤 2：格式/静态/proto/全量测试**

```bash
gofmt -l internal/ api/   # 空
go vet ./...              # 净
make proto-check          # 无漂移
go test ./... 2>&1 | tail -40   # 0 FAIL（含 presets/policy/mgmt/console/restgw/sidecar）
```

- [ ] **步骤 3：更新进度记忆**

`project_detailed_design_progress.md` 加 M3.2c-1 节（数据范围符号预设：presets 模型 + apply 种入 + proto + 三面 + 运营台符号谓词；DSC-1..7；下一步 M3.2c-2 租户自有模板）；`MEMORY.md` 索引钩子追加 M3.2c-1 完成 + 下一步 M3.2c-2。

- [ ] **步骤 4：FF 合并本地 main（不 push origin）**

worktree 全绿 + opus 整体评审 READY 后 FF 并入本地 main，清 worktree（沿用范式：ExitWorktree keep → 主仓 `git merge --ff-only` → `git worktree remove` → `git branch -d`）。

---

## 自检记录

**规格覆盖度（对照 spec）：** §3 数据模型→任务1 ✓；§4 apply 引擎（种入/vNew/DataPolicyChange/幂等/原子）→任务2 ✓；§5 proto→任务3 ✓ + mgmt 映射→任务4 ✓；§6 UI 符号谓词预览+摘要→任务5 ✓；§7 DSC-1..7→任务2/4/5/6 ✓；§8 测试策略→各任务 TDD + 任务6 全量 ✓；§9 YAGNI（租户模板/onboarding/去重键/构建器/user 主体均未触）✓。

**占位符扫描：** 各任务给出实际代码（DataScope/loader 校验/ApplyTemplate 种入/proto/mgmt 映射/conditionPredicate 渲染器/模板/路由均完整）；测试助手（newConsole/loginAndCSRF/读 body/newTestGW）注明「照搬既有」——非占位，是适配既有夹具的明确指令（实现者首步对齐同包现有测试写法）。

**类型一致性：** `presets.DataScope{Resource,Effect,Condition json.RawMessage}` ↔ `adminv1.TemplateDataScope{resource,effect,condition string}`（任务3）↔ mgmt `toProtoTemplate`/handler 转换 `string(ds.Condition)`（任务4）↔ `policy.TemplateDataScope{Resource,Effect,Condition string}`（任务2）字段一一对应；`policy.ApplyTemplateResult.DataScopesCreated` ↔ `adminv1.ApplyTemplateResponse.data_scopes_created`（任务3/4）一致；确定性 role code `tpl:<templateID>:<key>` 作 data_policy.subject_id（任务2 实现、测试断言一致）；`conditionPredicate(string)`（任务5）消费 proto/presets 的 condition 串一致。

**关键口径固化：** vNew 懒读 tx 锁定版本（同 tx 已持锁，再 SELECT FOR UPDATE 返回当前 cur），runVersionedWrite 随后 bump 到同值——data_policy.version 与 app 版本一致；数据范围只在角色「新建」时种入（再 apply DataScopesCreated=0、无重复行）；condition 控制面只校验合法 JSON + 只读渲染、绝不解析语义（透传，语义 fail-close 留 sidecar）；预览渲染器控制面自足、零数据面耦合（Sidecar 零漂移）；ruleTable/adminauthz 零改（复用既有 template/apply 鉴权）。
