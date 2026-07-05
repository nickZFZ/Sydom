# M4.3 条件构建器 v2（Condition Builder v2）实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 把数据策略可视化条件构建器重做为支持任意嵌套 AND/OR/NOT + 13 全算子的 v2，并从源头修复「构建器产出与引擎文法不一致的条件→评估时 fail-close 拒绝」这一潜在正确性 bug。

**架构：** 单一真相源——条件文法只在数据面引擎 `dataperm` 定义一处；导出 `dataperm.ValidateCondition`，控制面写入路径（mgmt handler 三面单点）+ 服务端谓词预览端点复用它（正如 effperm 已复用 dataperm），杜绝第二套文法。前端 `datapolicy.js`（仍是唯一 JS 文件，渐进增强）序列化 canonical 大写 JSON，防抖调预览端点显示符号谓词。

**技术栈：** Go、PostgreSQL、testcontainers（PG+Redis）、testify、html/template、`net/http`、vanilla JS（零构建、零网络库）、axe-core 4.10.2（走查）。

**规格：** `docs/superpowers/specs/2026-07-05-sydom-m4-3-condition-builder-v2-design.md`（BASE=main M4.2 后 `9ddbb54`；含 CB-1..8）。

**范式纪律：** 子代理驱动 + 两阶段审查（规格→质量）；TDD；每任务独立 commit；**禁 `git commit --amend`**；实现者不读本 plan（由控制者派发任务文本）。**数据面求值零触碰**（CB-7）：`dataperm` 仅 +1 导出 wrapper，`parseCondition`/`validate`/求值一字不改。

---

## 文件结构（先锁定分解）

| 文件 | 职责 | 任务 |
|---|---|---|
| `internal/sidecar/dataperm/condition.go` | +导出 `ValidateCondition`（薄包 `parseCondition`，零改求值） | 1 |
| `internal/sidecar/dataperm/condition_validate_test.go` | 导出入口单测（与内部 parseCondition 一致、13 算子/嵌套/NOT/非法） | 1 |
| `internal/controlplane/console/condition_predicate.go` | 补全 13 叶子算子（大写）+ 嵌套渲染 | 2 |
| `internal/controlplane/console/condition_predicate_test.go` | 表驱动补全 13 算子 + 嵌套 + NOT | 2 |
| `internal/controlplane/mgmt/server.go:101` | `UpsertDataPolicy` +写时 `ValidateCondition`（InvalidArgument） | 3 |
| `internal/controlplane/mgmt/data_policy_condition_test.go` | 写时校验有齿 + 反向验证 + 三面代表 | 3 |
| `internal/controlplane/console/routes_datapolicy.go` | +预览端点 handler + 注册路由 | 4 |
| `internal/controlplane/console/datapolicy_preview_test.go` | 预览端点测试（合法→谓词/非法→错误/会话鉴权/只读） | 4 |
| `internal/controlplane/console/static/datapolicy.js` | builder v2：嵌套盒 + 13 算子 + 自适应 value + field 校验 + 大写序列化 + 防抖预览 | 5 |
| `internal/controlplane/console/templates/datapolicies.html` | 预览容器 + 占位 JSON 改大写 + a11y | 5 |
| `internal/controlplane/console/static/console.css`（或既有样式文件） | 嵌套盒样式（复用 M3.1 设计系统 token） | 5 |
| `docs/superpowers/2026-07-05-m4-3-condition-builder-v2-walkthrough.md` | 走查记录（任务 6 产出） | 6 |

**关键分解决策：**
- 写时校验放 **mgmt handler**（三面单点、最小连带），非 manager（避免破坏大量直接调 mgr/store 的测试）。
- 预览**复用** `condition_predicate.go`（控制面既有谓词渲染器），**不在 JS 复制谓词逻辑**（CB-6 单源）。
- `dataperm` 只 +导出 wrapper，零碰求值（CB-7）。

---

## 任务 1：`dataperm.ValidateCondition` 导出（单一真相源入口）

**文件：**
- 修改：`internal/sidecar/dataperm/condition.go`
- 创建：`internal/sidecar/dataperm/condition_validate_test.go`

参考既有：`condition.go` 的 `parseCondition`/`validate`（已有）、`condition_test.go`（既有样例）。

- [ ] **步骤 1：写失败测试 `condition_validate_test.go`**

```go
package dataperm

import (
	"strings"
	"testing"
)

func TestValidateCondition(t *testing.T) {
	valid := []string{
		`{"op":"AND","children":[{"field":"dept","op":"EQ","value":"$user.dept"}]}`,
		`{"op":"OR","children":[{"field":"a","op":"IN","value":["x","y"]},{"op":"NOT","children":[{"field":"archived","op":"EQ","value":true}]}]}`,
		`{"field":"amount","op":"BETWEEN","value":[1,100]}`,
		`{"field":"note","op":"IS_NULL"}`,
	}
	for _, raw := range valid {
		if err := ValidateCondition(raw); err != nil {
			t.Errorf("期望合法，得 err: %s → %v", raw, err)
		}
	}
	invalid := []string{
		``,                                                     // 空串（parseCondition 报错，与 eval 中毒同源）
		`{"op":"ALL"}`,                                         // 未知算子
		`{"op":"and","children":[]}`,                           // 小写 + AND 空 children
		`{"field":"a;DROP","op":"EQ","value":"x"}`,             // 非法字段名
		`{"field":"a","op":"IN","value":"notarray"}`,          // IN 非数组
		`{"field":"a","op":"BETWEEN","value":[1]}`,            // BETWEEN 非 2 元
	}
	for _, raw := range invalid {
		if err := ValidateCondition(raw); err == nil {
			t.Errorf("期望非法被拒，得 nil: %s", raw)
		}
	}
	// 与内部 parseCondition 完全同源（同一 raw 同一结论）。
	if (ValidateCondition(`{"op":"ALL"}`) == nil) != func() bool { _, e := parseCondition(`{"op":"ALL"}`); return e == nil }() {
		t.Fatal("ValidateCondition 必须与 parseCondition 同源")
	}
	_ = strings.TrimSpace
}
```

- [ ] **步骤 2：运行确认失败**

运行：`go test ./internal/sidecar/dataperm/ -run TestValidateCondition -v`
预期：FAIL（`ValidateCondition` 未定义）。

- [ ] **步骤 3：实现导出（`condition.go`，加在 `parseCondition` 附近）**

```go
// ValidateCondition 校验不透明条件 JSON 是否符合 canonical 文法（fail-close）。
// 纯委托 parseCondition：与数据面 eval（table.go toStored → parseCondition，失败即中毒
// fail-close）完全同源——空串/非法一律拒（数据策略必须有合法非空条件）。
// 是全系统唯一的条件校验入口（控制面写入/预览与数据面 eval 同一文法定义）。
func ValidateCondition(raw string) error {
	_, err := parseCondition(raw)
	return err
}
```

- [ ] **步骤 4：运行确认通过 + 求值零改核验**

运行：`go test ./internal/sidecar/dataperm/ -v`（全包，含既有 condition_test）
预期：全 PASS。
运行：`git diff internal/sidecar/dataperm/condition.go`
预期：diff **仅新增 `ValidateCondition` 函数**，`parseCondition`/`validate`/`validateLeaf`/常量一字未改（CB-7）。

- [ ] **步骤 5：Commit**

```bash
git add internal/sidecar/dataperm/condition.go internal/sidecar/dataperm/condition_validate_test.go
git commit -m "feat(dataperm): M4.3 导出 ValidateCondition(薄包 parseCondition,条件文法单一真相源入口,求值零改)"
```

---

## 任务 2：`condition_predicate.go` 补全 13 叶子算子渲染

**文件：**
- 修改：`internal/controlplane/console/condition_predicate.go`
- 修改：`internal/controlplane/console/condition_predicate_test.go`

参考既有：`condition_predicate.go` 全文（`renderNode`/`symbol`/`renderValue`，已支持 AND/OR/NOT + EQ/NE/GT/GE/LT/LE + 透传 IN/BETWEEN）；引擎算子集见 `dataperm/condition.go`（NOT_IN/LIKE/NOT_LIKE/IS_NULL/IS_NOT_NULL/BETWEEN）。

- [ ] **步骤 1：写失败测试（扩充 `condition_predicate_test.go` 的表）**

在既有表驱动测试的 map 里加：

```go
	`{"field":"note","op":"IS_NULL"}`:                    "note IS NULL",
	`{"field":"note","op":"IS_NOT_NULL"}`:                "note IS NOT NULL",
	`{"field":"name","op":"LIKE","value":"%abc%"}`:       "name LIKE %abc%",
	`{"field":"name","op":"NOT_LIKE","value":"%x%"}`:     "name NOT LIKE %x%",
	`{"field":"s","op":"NOT_IN","value":["a","b"]}`:      "s NOT IN [a, b]",
	`{"field":"amount","op":"BETWEEN","value":[1,100]}`:  "amount BETWEEN [1, 100]",
	`{"op":"NOT","children":[{"field":"archived","op":"EQ","value":true}]}`: "NOT archived = true",
```
> 期望字符串以实现的实际渲染为准（LIKE/BETWEEN 的呈现风格可微调，但须稳定、可读、含算子 token）。IS_NULL/IS_NOT_NULL 无 value。

- [ ] **步骤 2：运行确认失败**

运行：`go test ./internal/controlplane/console/ -run TestConditionPredicate -v`（测试名以现有为准）
预期：FAIL（IS_NULL 等渲染不符）。

- [ ] **步骤 3：实现补全（`condition_predicate.go`）**

在 `renderNode` 的叶子分支处理 IS_NULL/IS_NOT_NULL（无 value）：

```go
	default:
		// 叶子：field op value。
		if n.Field == "" {
			return ""
		}
		op := n.Op
		if op == "" {
			op = "EQ"
		}
		switch op {
		case "IS_NULL":
			return n.Field + " IS NULL"
		case "IS_NOT_NULL":
			return n.Field + " IS NOT NULL"
		}
		return n.Field + " " + symbol(op) + " " + renderValue(n.Value)
```

在 `symbol` 补 NOT_IN/LIKE/NOT_LIKE（其余大写算子已透传原 token）：

```go
	case "NOT_IN":
		return "NOT IN"
	case "LIKE":
		return "LIKE"
	case "NOT_LIKE":
		return "NOT LIKE"
```
> `IN`/`BETWEEN` 已由既有 default 透传原 token（渲染为 `field IN [..]` / `field BETWEEN [1, 100]`）。渲染层的算子 token 与引擎大写一致。

- [ ] **步骤 4：运行确认通过**

运行：`go test ./internal/controlplane/console/ -run TestConditionPredicate -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/console/condition_predicate.go internal/controlplane/console/condition_predicate_test.go
git commit -m "feat(console): M4.3 符号谓词渲染补全 13 叶子算子(IS_NULL/IS_NOT_NULL/LIKE/NOT_LIKE/NOT_IN,大写对齐引擎)"
```

---

## 任务 3：写时 fail-close 校验（mgmt handler 三面单点）

**文件：**
- 修改：`internal/controlplane/mgmt/server.go`（`UpsertDataPolicy`，约 line 101）
- 创建：`internal/controlplane/mgmt/data_policy_condition_test.go`
- 可能修改：经 mgmt handler 播种数据策略、且用非法条件的既有测试（改为合法条件）

参考既有：`server.go:101` `UpsertDataPolicy`（已有 effect 校验范式）、`dataperm.ValidateCondition`（任务 1）、`effperm/effperm.go:17`（控制面 import dataperm 的既有先例）。

- [ ] **步骤 1：写失败测试 `data_policy_condition_test.go`**

复用 mgmt 既有 testcontainers 夹具（起 AdminServer + root/租户，参考 `data_policy_effect_test.go`/`policy_as_code_test.go`）。核心：非法条件经 handler → InvalidArgument 不落库；合法 → 成功。

```go
func TestUpsertDataPolicy_InvalidCondition_Rejected(t *testing.T) {
	env := newAdminTestEnv(t)          // 夹具名以现有 mgmt 测试为准
	appID := env.seedApp(t)
	_, err := env.srv.UpsertDataPolicy(env.rootCtx, &adminv1.UpsertDataPolicyRequest{
		AppId: appID, SubjectType: "role", SubjectId: "manager", Resource: "order",
		Condition: `{"op":"ALL"}`, Effect: "allow", // 非法算子
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code=%v want InvalidArgument", status.Code(err))
	}
	// 不落库
	if n := env.countDataPolicies(t, appID); n != 0 {
		t.Fatalf("非法条件不应落库,残留 %d", n)
	}
}

func TestUpsertDataPolicy_ValidCondition_OK(t *testing.T) {
	env := newAdminTestEnv(t)
	appID := env.seedApp(t)
	resp, err := env.srv.UpsertDataPolicy(env.rootCtx, &adminv1.UpsertDataPolicyRequest{
		AppId: appID, SubjectType: "role", SubjectId: "manager", Resource: "order",
		Condition: `{"field":"dept","op":"EQ","value":"$user.dept"}`, Effect: "allow",
	})
	if err != nil { t.Fatal(err) }
	if !resp.Changed { t.Fatalf("resp=%+v", resp) }
}
```
> 夹具函数名（`newAdminTestEnv`/`env.seedApp`/`env.countDataPolicies`/`env.rootCtx`）以现有 mgmt 测试为准；`countDataPolicies` 若无则直接 `db.QueryRow("SELECT count(*) FROM data_policy WHERE app_id=$1")`。

- [ ] **步骤 2：运行确认失败**

运行：`go test ./internal/controlplane/mgmt/ -run TestUpsertDataPolicy_InvalidCondition -v`
预期：FAIL（当前无校验，非法条件被接受落库）。

- [ ] **步骤 3：实现写时校验（`server.go` UpsertDataPolicy，effect 校验之后、调 mgr 之前）**

```go
	if err := dataperm.ValidateCondition(r.Condition); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid condition: %v", err)
	}
```
> 在 `server.go` import 加 `"github.com/nickZFZ/Sydom/internal/sidecar/dataperm"`（effperm 已先例，控制面→dataperm 依赖既有）。

- [ ] **步骤 4：运行确认通过 + 修复连带测试**

运行：`go test ./internal/controlplane/mgmt/ -run TestUpsertDataPolicy -v`
预期：PASS。
运行：`go test ./internal/controlplane/mgmt/...`（全 mgmt 包）
预期：全 PASS。**若** `data_policy_effect_test.go` 等经 handler 播种数据策略的用例用了 `{"op":"ALL"}` 致新失败 → 把其条件改为合法代表 `{"field":"dept","op":"EQ","value":"$user.dept"}`（语义无关，只需合法）。直接调 `mgr.UpsertDataPolicy`/`store` 的测试不受影响，不动。

- [ ] **步骤 5：反向验证（呼应 M2.4 教训，证明测试有齿）**

临时注释掉步骤 3 的校验行 → 重跑 `TestUpsertDataPolicy_InvalidCondition_Rejected` → 确认 **FAIL**（非法条件被接受）；恢复校验 → 重跑 PASS。**汇报贴 FAIL→PASS 证据；不提交注释版**。

- [ ] **步骤 6：Commit**

```bash
git add internal/controlplane/mgmt/server.go internal/controlplane/mgmt/data_policy_condition_test.go
# 若改了连带测试也 add
git commit -m "feat(mgmt): M4.3 UpsertDataPolicy 写时 fail-close 校验条件(复用 dataperm.ValidateCondition,三面单点,非法即 InvalidArgument 不落库,反向验证有齿)"
```

---

## 任务 4：预览端点（Console，服务端谓词单源）

**文件：**
- 修改：`internal/controlplane/console/routes_datapolicy.go`（+handler + 注册路由）
- 创建：`internal/controlplane/console/datapolicy_preview_test.go`

参考既有：`routes_datapolicy.go:14` `registerDataPolicy`（路由注册）、`upsertDataPolicy` handler（doWrite 范式、会话/CSRF）、`condition_predicate.go` `conditionPredicate`（任务 2 已补全）、`dataperm.ValidateCondition`（任务 1）、既有 Console 只读 handler（如 `listDataPolicies`）的会话鉴权范式。

- [ ] **步骤 1：写失败测试 `datapolicy_preview_test.go`**

镜像既有 Console 测试（`newConsole`/`loginAndCSRF`，见 `handler_test.go`）。覆盖：合法条件→谓词串；非法→错误信息；需登录会话；只读不落库。

```go
func TestConsole_PreviewCondition(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	// 合法 → 谓词
	resp := postForm(t, c, ts.URL+fmt.Sprintf("/apps/%d/data-policies/preview-condition", appID),
		url.Values{"csrf_token": {csrf}, "condition": {`{"op":"AND","children":[{"field":"dept","op":"EQ","value":"$user.dept"}]}`}})
	body := readBody(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, body, "dept = $user.dept")

	// 非法 → 错误信息(非 500)
	resp2 := postForm(t, c, ts.URL+fmt.Sprintf("/apps/%d/data-policies/preview-condition", appID),
		url.Values{"csrf_token": {csrf}, "condition": {`{"op":"ALL"}`}})
	require.NotEqual(t, http.StatusInternalServerError, resp2.StatusCode)
	require.Contains(t, readBody(t, resp2), "算子") // 校验错误信息含原因
}
```
> `postForm`/`readBody` 用既有 helper（以 `handler_test.go`/`bulk_test.go` 实际为准）。预览是幂等只读——用会话鉴权即可（`requireSession`+CSRF），**不调 doWrite**（不 bump、不写审计、不 CheckStatusWrite）。是否需 app 域 AuthorizeRule：预览只解析提交的 JSON、不读任何 app 数据、不泄露存在性，故会话认证足够；若与既有只读 handler 一致地施加 app 读授权更稳妥，照 `listDataPolicies` 的鉴权范式对齐。

- [ ] **步骤 2：运行确认失败**

运行：`go test ./internal/controlplane/console/ -run TestConsole_PreviewCondition -v`
预期：FAIL（路由未注册）。

- [ ] **步骤 3：实现 handler + 注册（`routes_datapolicy.go`）**

注册（`registerDataPolicy` 内）：
```go
	mux.HandleFunc("POST /apps/{app_id}/data-policies/preview-condition", h.previewCondition)
```
handler（会话+CSRF → 校验+渲染 → 返回谓词/错误 HTML 片段；镜像既有只读 handler 的 session 校验）：
```go
// previewCondition 服务端渲染条件的符号谓词预览（幂等只读，单一真相源：复用 dataperm 校验 + conditionPredicate 渲染）。
func (h *Handler) previewCondition(w http.ResponseWriter, r *http.Request) {
	_, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if !h.checkCSRF(r, sess) {
		h.renderError(w, r, codes.PermissionDenied, "CSRF 校验失败", nil)
		return
	}
	cond := r.FormValue("condition")
	if err := dataperm.ValidateCondition(cond); err != nil {
		// 非法：返回错误信息片段(200，内联展示；不是服务器错误)
		h.renderPreviewResult(w, r, "", err.Error())
		return
	}
	h.renderPreviewResult(w, r, conditionPredicate(cond), "")
}
```
> `renderPreviewResult` 渲染极小片段（`{Predicate, Error}`）或直接写文本/JSON——以最小实现为准（可以是一个小模板或 `w.Write` 转义文本）。`h.requireSession`/`h.checkCSRF`/`h.renderError` 用既有 helper。import 加 `dataperm`。**app_id path 参数**用于路由一致性与（若采用）app 读授权；预览本身不按 app_id 读数据。

- [ ] **步骤 4：运行确认通过**

运行：`go test ./internal/controlplane/console/ -run TestConsole_PreviewCondition -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/console/routes_datapolicy.go internal/controlplane/console/datapolicy_preview_test.go
git commit -m "feat(console): M4.3 条件预览端点(服务端复用 dataperm 校验+conditionPredicate 渲染符号谓词,幂等只读会话鉴权,单源不复制逻辑)"
```

---

## 任务 5：`datapolicy.js` builder v2 + 模板 + 样式（嵌套盒、13 算子、防抖预览、无新 JS）

**文件：**
- 重写：`internal/controlplane/console/static/datapolicy.js`（仍是唯一 JS 文件）
- 修改：`internal/controlplane/console/templates/datapolicies.html`（预览容器 + 占位 JSON 改大写）
- 修改：样式文件（嵌套盒类，复用 M3.1 设计系统 token；文件以既有 console 样式为准，如 `static/*.css`）
- 创建/修改：`internal/controlplane/console/datapolicy_builder_test.go`（CB-3 缺口：构建器序列化形状被引擎接受）

参考既有：`datapolicy.js` 现全文（渐进增强契约、`#dp-form`/`#cond-json`/`#builder`/`#builder-toggle`、serialize/hydrate/setMode）、`datapolicies.html`（表单元素 id、`<script src="/static/datapolicy.js">`）、`dataperm/condition.go`（13 算子 + value 规则，构建器须对齐）、M3.1 设计系统类、M3.4b axe select-name（每 select 须 aria-label）。

### 契约（builder v2 必须满足，CB-3/4/5）
- **渐进增强不变**：无 JS 基线 = `#cond-json` textarea 可见、`name="condition"` canonical 提交；有 JS 接管、提交前序列化写回 textarea。**无新增 `<script>`/.js 文件**（仍只有 `datapolicy.js` + `interactions.js`）。
- **序列化产出 canonical 大写**：`{op:"AND"|"OR"|"NOT", children:[...]}` 或叶子 `{field, op:<大写算子>, value}`；算子全大写；AND/OR ≥1 子、NOT 恰 1 子（空组/空行不产出非法 JSON）。
- **13 算子**：`EQ NE GT GE LT LE IN NOT_IN LIKE NOT_LIKE IS_NULL IS_NOT_NULL BETWEEN`。
- **value 按算子自适应**：IS_NULL/IS_NOT_NULL 无 value（隐藏输入，序列化不含 value）；IN/NOT_IN → 数组（逗号分隔或多值→JSON 数组）；BETWEEN → 恰 2 值数组；其余标量（`$user.xxx` 原样字符串；纯数字尽力转 number）。
- **field 实时校验** `^[A-Za-z_][A-Za-z0-9_]*$`，非法行内标红、序列化跳过或阻止。
- **a11y**：每 field 输入、每 op/组 select、每按钮有可访问名（aria-label）；嵌套组键盘可达；无违规（任务 6 axe 验）。

- [ ] **步骤 1：写失败测试 `datapolicy_builder_test.go`（CB-3 缺口——Go 侧断言构建器序列化形状被引擎接受）**

> JS 无单测框架；用 Go 测试固化「构建器会产出的 canonical 形状」并断言 `dataperm.ValidateCondition` 接受——这正是历史从未测过的一环。构建器实现须保证其 serialize() 产出与此形状一致（步骤 3 实现后由任务 6 真实浏览器走查端到端验证）。

```go
package console

import (
	"testing"
	"github.com/nickZFZ/Sydom/internal/sidecar/dataperm"
)

// builder v2 会产出的 canonical 大写形状必须被引擎接受(此前小写/contains/单层从未测过)。
func TestBuilderV2SerializedShapesAccepted(t *testing.T) {
	shapes := []string{
		`{"op":"AND","children":[{"field":"dept","op":"EQ","value":"$user.dept"}]}`,
		`{"op":"AND","children":[{"field":"dept","op":"EQ","value":"$user.dept"},{"op":"OR","children":[{"field":"status","op":"IN","value":["pending","approved"]},{"op":"NOT","children":[{"field":"archived","op":"EQ","value":true}]}]}]}`,
		`{"field":"amount","op":"BETWEEN","value":[1,100]}`,
		`{"field":"note","op":"IS_NULL"}`,
		`{"field":"name","op":"NOT_LIKE","value":"%x%"}`,
	}
	for _, s := range shapes {
		if err := dataperm.ValidateCondition(s); err != nil {
			t.Errorf("构建器 v2 形状必须被引擎接受: %s → %v", s, err)
		}
	}
}
```

- [ ] **步骤 2：运行确认通过（本测试应已 PASS——它固化契约；任务 1 的 ValidateCondition 已在）**

运行：`go test ./internal/controlplane/console/ -run TestBuilderV2SerializedShapesAccepted -v`
预期：PASS（形状本就合法）。这是**契约锚**：builder v2 的 serialize() 必须只产出此类形状。

- [ ] **步骤 3：重写 `datapolicy.js` builder v2**

保留文件头渐进增强注释精神，重写为递归嵌套构建器。核心结构（vanilla、零网络库、`fetch` 仅调本域预览端点）：

- `buildGroup(node)` 递归渲染一个组盒：组合算子 select（AND/OR/NOT，带 aria-label）+ 子项容器（叶子行 + 子组盒）+「+ 条件」「+ 子组」按钮；NOT 组限单子项（加子项后禁用「+」或替换）。
- `buildLeafRow()`：field 文本框（实时 `^[A-Za-z_]\w*$` 校验）+ op select（13 大写算子，aria-label）+ value 容器（按 op 自适应重建：无/单框/数组框/两框）+ 删除按钮。
- `serializeNode(el)` 递归：组 → `{op:<大写>, children:[非空子项...]}`（跳过空行/空组，保证 AND/OR≥1、NOT=1）；叶子 → `{field, op:<大写>, value:<按 op 成形>}`（IS_NULL/IS_NOT_NULL 无 value；IN/NOT_IN/BETWEEN → 数组）。产出全大写。
- `hydrate(raw)`：解析已有 canonical JSON（大写）预填嵌套；不可解析则保持空 + 专业模式可回原始 textarea。
- `preview()`：防抖（~300ms）`fetch('/apps/<id>/data-policies/preview-condition', {POST, csrf+condition})` → 内联显示谓词或错误。app_id 从表单 action 或页面取。
- 提交前（`form.submit`）：非专业模式则 `#cond-json.value = serializeRoot()`。
- 专业模式 toggle：同现有（builder ↔ 原始 textarea）。

> **实现纪律**：文件仍是唯一 JS；零新 `<script>`；`fetch` 只打本域预览端点（CSRF token 从页面隐藏字段取）；所有交互控件带 aria-label（M3.4b axe select-name 教训）。value 数组解析：输入按逗号切分 trim，数字尽力 `Number()`，否则字符串；`$user.xxx` 原样。序列化务必**大写算子**（这是修 bug 的核心）。

- [ ] **步骤 4：模板 + 样式**

`datapolicies.html`：把 `#cond-json` 的 placeholder 占位 JSON 从小写 `{"op":"and","children":[]}` 改为**大写** `{"op":"AND","children":[]}`（与文法一致、示范正确形状）；在 `#builder` 附近加一个预览容器（如 `<div id="cond-preview" role="status" aria-live="polite"></div>`）供 JS 填充谓词。
样式：嵌套盒类（边框/缩进/圆角）复用 M3.1 设计系统 token（`--border`/`--code-bg` 等），零新硬编码色值；NOT 组可用虚线边框区分（呼应布局 A 原型）。

- [ ] **步骤 5：验证（Go 全包 + gofmt + 契约测试）**

运行：`go test ./internal/controlplane/console/... `
预期：全 PASS（含步骤 1 契约测试；JS 行为由任务 6 真实浏览器走查端到端验证）。
运行：`gofmt -l internal/controlplane/console/`
预期：空。

- [ ] **步骤 6：Commit**

```bash
git add internal/controlplane/console/static/datapolicy.js internal/controlplane/console/templates/datapolicies.html internal/controlplane/console/datapolicy_builder_test.go
# 若改了 css 也 add
git commit -m "feat(console): M4.3 条件构建器 v2(嵌套盒 AND/OR/NOT+13 大写算子+按算子自适应 value+field 校验+大写序列化修文法 bug+防抖服务端预览,仍唯一 JS 文件渐进增强)"
```

---

## 任务 6：整体核验 CB-1..8 + 真实浏览器 axe 走查 + opus 评审 + FF

**文件：** 无代码改动（除走查涌现修复）；产出走查记录 `docs/superpowers/2026-07-05-m4-3-condition-builder-v2-walkthrough.md`。

- [ ] **步骤 1：CB-7 数据面/授权核心零触碰核验**

运行：
```bash
BASE=$(git merge-base main HEAD)
git diff $BASE..HEAD -- casbin/ internal/controlplane/adminauthz/ internal/kernel/ | wc -l   # 期望 0
git diff $BASE..HEAD -- internal/sidecar/dataperm/condition.go | grep -E '^[+-]' | grep -vE 'ValidateCondition|^[+-]{3}' | grep -vE '^[+-]\s*//' # 期望仅 ValidateCondition 相关新增,parseCondition/validate 未改
git diff $BASE..HEAD -- internal/sidecar/ | grep -c '^+' # 仅 dataperm +ValidateCondition,其余 sidecar 0
```
预期：casbin/adminauthz/kernel diff=0；`dataperm/condition.go` 仅 +ValidateCondition，求值逻辑一字未改；sidecar 其余零触碰。

- [ ] **步骤 2：全量验证**

运行：
```bash
gofmt -l internal/                # 期望空
go vet ./...                      # 期望干净
go test ./...                     # 期望 0 FAIL(含 e2e/sidecar/dataperm/mgmt/console)
```
预期：全绿。

- [ ] **步骤 3：真实浏览器 axe 走查（CB-8）**

复用 M4.2 走查脚手架范式（build-tag `walkthrough` 复用 `newConsole` 装配 + `dbtest` testcontainers、会话 TTL `time.Hour`、播种一个数据策略页可用的 app、URL 写文件）+ 系统 Chrome via **Playwright MCP**（已修 `--prefer-offline @playwright/mcp@0.0.77`）+ axe-core 4.10.2（jsdelivr 取、本地 `python3 -m http.server` 服、页内注入）。走查数据策略页：① 页 axe 0 违规 + 单 h1 + breadcrumb；② JS 接管后可视化构建器出现（textarea 隐藏）；③ 搭一个嵌套条件（AND 里放一叶子 + 一个 OR 子组含 NOT）→ 触发防抖预览 → 断言预览显示正确符号谓词；④ 提交保存 → 列表页谓词正确、无 fail-close（证明大写序列化被引擎接受，端到端修复那个 bug）；⑤ 构建器嵌套组/控件键盘可达 + aria-label。**走查纪律**：停后台进程按确切 PID（非 `pkill -f`）；脚手架/axe 静态服务走查后删除未提交。记录到 walkthrough.md 并 commit。

- [ ] **步骤 4：opus 整体安全评审**

派 opus 子代理（或控制者 inline）对全 diff 逐条核验 CB-1..8。重点：单一真相源（写入/预览/eval 同一 dataperm 校验器）、写时 fail-close 有齿、构建器产出被引擎接受（bug 真修）、数据面求值零触碰（diff 证明）、预览不复制谓词逻辑、渐进增强+无新 JS、字段白名单堵注入仍生效。产出 READY 或阻断清单。

- [ ] **步骤 5：更新记忆**

`project_detailed_design_progress.md` 加 M4.3 节；`MEMORY.md` 索引 M4 条目下 M4.3 标 ✅（一行，明细入 detailed 文件）。

- [ ] **步骤 6：FF 并入本地 main + 问用户 push**

```bash
git -C /home/tongyu/codes/Sydom merge --ff-only worktree-feat+m4-3-condition-builder-v2
# 核实 main==feature tip；push origin 与否问用户(本轮已建立 push 习惯)
```
清理 worktree（在主 checkout：`git worktree remove`）。

---

## 自检（写完计划后，全新视角对照规格）

**1. 规格覆盖度：**
- §2 单一真相源 → 任务 1（导出 ValidateCondition）✅
- §3a dataperm 导出 → 任务 1 ✅；§3b 写时校验 → 任务 3 ✅；§3c 预览端点 → 任务 4 ✅；§3d condition_predicate 补全 → 任务 2 ✅
- §4 前端 builder v2 → 任务 5 ✅
- §5 CB-1..8 → CB-1/7（任务 1+6 diff 核验）、CB-2（任务 3 有齿+反向验证）、CB-3（任务 5 契约锚 + 任务 6 端到端）、CB-4（任务 5 渐进增强+无新 JS）、CB-5（任务 5 序列化约束）、CB-6（任务 4 复用不复制）、CB-8（任务 6 axe）✅
- §6 三面 parity → 任务 3 mgmt handler 单点覆盖三面 ✅
- §7 测试策略 → 各任务 TDD + 任务 6 走查 ✅
- §8 任务分解 → 6 任务（JS+模板+样式合为任务 5）✅

**2. 占位符扫描：** 无「待定/TODO」；每步含实际代码/命令/预期。夹具函数名标注「以现有测试为准」是刻意的（实现者对齐真实夹具）。任务 5 的 JS 以「契约 + 结构 + 关键函数职责」描述（大型创造性前端，端到端由任务 6 真实浏览器走查验证）——非占位，是恰当粒度。

**3. 类型一致性：**
- `dataperm.ValidateCondition(raw string) error`（任务 1）→ 任务 3 mgmt handler、任务 4 预览 handler 一致引用 ✅
- `conditionPredicate(raw string) string`（任务 2 补全）→ 任务 4 预览 handler 一致 ✅
- 构建器序列化 canonical 大写形状（任务 5）→ 任务 1 `ValidateCondition` 接受（任务 5 步骤 1 契约锚）✅
- 写时校验放 mgmt handler（任务 3）→ 不破坏直接调 mgr/store 的测试（16 处 {op:ALL} 占位）；仅经 handler 的测试改合法 ✅

对照无缺口。
