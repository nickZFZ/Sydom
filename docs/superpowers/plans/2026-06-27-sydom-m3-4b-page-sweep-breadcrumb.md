# M3.4b 页面迁移横扫 + breadcrumb 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 把 25 个仍是「legacy 标记 + 兜底 CSS」的 Console 页面升到 M3.1 设计系统的成熟页头约定（breadcrumb + 单一 h1 + 组件类），axe 全清，纯表现层、后端零触碰、行为/路由/data 键一字不改。

**架构：** 逐页只改 `.html`（+ 一处 `.visually-hidden` 工具类落 `base.css`）。每页统一加 `<nav class="breadcrumb">` + 把页顶 `<h2>X</h2>` 提为 `<h1>X</h1>`，裸 `<table>`→`.table`、裸按钮→按钮变体类、空操作列 `<th>`→视觉隐藏文案。破坏动作表单的 `data-confirm`/CSRF/action 原样保留（M3.4a 已接）。一次性 secret 展示页专管线不动。既有 Go 测试作行为不变回归网；每批次新增「单一 h1 + breadcrumb」结构性 TDD 测试；每批次 axe 走查 0 违规。

**技术栈：** Go html/template、M3.1 token 化分层 CSS、testcontainers、系统 Chrome + Playwright + axe-core 4.10.2（复用 M3.4a 一次性走查脚手架）。

**spec：** `docs/superpowers/specs/2026-06-27-sydom-m3-4b-page-sweep-breadcrumb-design.md`（commit `cb6dd9d`）。**BASE sha** = 本计划 commit。

---

## 文件结构

| 文件 | 职责 | 任务 |
|---|---|---|
| `internal/controlplane/console/static/css/base.css` | 新增 `.visually-hidden` 标准工具类 | 1 |
| `internal/controlplane/console/templates/{roles,grants,bindings,inheritances,datapolicies,audit,decision,effective}.html` | 建模台 app 域 8 页 reskin | 1 |
| `internal/controlplane/console/templates/{admin_roles,admin_audit,operators,members,tenants}.html` | system 域 5 页 reskin | 2 |
| `internal/controlplane/console/templates/{app_new,app_created,app_secret_rotated,operator_new,operator_created,operator_secret_reset,register,member_invited,ops_role_new}.html` | 表单/一次性展示 9 页 reskin | 3 |
| `internal/controlplane/console/templates/{ops_people,ops_roles,error}.html` | 运营台剩余 2 页 + 错误页 reskin | 4 |
| `internal/controlplane/console/pagesweep_test.go`（新建） | 每批次「单一 h1 + breadcrumb」结构性测试（表驱动，按批次追加用例） | 1–4 |

> 偏差（相对 spec §9）：spec §9 预览 6 任务（error 单列任务 5、核验任务 6）。本计划把 error（1 页，trivial）并入任务 4，核验为任务 5，共 **5 任务**，更均衡。spec §9 标「预览，详见 plan」，此为 plan 级细化。

---

## 共享约定（所有任务遵循；非占位，是逐页统一变换的精确配方）

### A. 页头变换（每页 `{{define "content"}}` 内）

1. **插入 breadcrumb**：在该页主标题前插入一行（文案见下表 B，逐页 pin 死）：
   ```html
   <nav class="breadcrumb" aria-label="面包屑">{{批次/页 文案}}</nav>
   ```
   - app 域页（含 `{{template "appnav" .}}` 的）：breadcrumb 置于 `<section>` 内、紧接 appnav 之后、原 `<h2>` 之前（与 ops 页一致）。
   - 非 app 域页：breadcrumb 作为 content 第一个可见元素。
   - error 页**不加 breadcrumb**（独立错误页，仅单一 h1）。
2. **提升主标题**：把该页**顶部第一个** `<h2>X</h2>`（页标题）改为 `<h1>X</h1>`，**X 文案逐字保留**（用页面现有标题文本，不改写）。页内其余分节小标题保持 `<h2>`（保证每页恰一个 `<h1>`）。

### B. breadcrumb 文案表（逐页 pin）

| 页 | breadcrumb 文案 | 保留 data-confirm |
|---|---|---|
| roles | `建模台 · 角色` | ✅ DeleteRole |
| grants | `建模台 · 授权` | — |
| bindings | `建模台 · 用户绑定` | — |
| inheritances | `建模台 · 角色继承` | ✅ RemoveRoleInheritance |
| datapolicies | `建模台 · 数据策略` | — |
| audit | `建模台 · 审计` | — |
| decision | `建模台 · 决策解释` | — |
| effective | `建模台 · 有效权限` | — |
| admin_roles | `系统 · 管理员角色` | ✅ RevokeAdminGrant |
| admin_audit | `系统 · 系统审计` | — |
| operators | `系统 · 算子` | ✅ UnbindOperatorRole + ResetOperatorSecret |
| members | `租户 · 成员` | — |
| tenants | `租户 · 我的租户` | — |
| app_new | `应用 · 新建` | — |
| app_created | `应用 · 已创建` | — |
| app_secret_rotated | `应用 · 凭据已轮换` | — |
| operator_new | `算子 · 新建` | — |
| operator_created | `算子 · 已创建` | — |
| operator_secret_reset | `算子 · 凭据已重置` | — |
| register | `租户 · 注册` | — |
| member_invited | `成员 · 已邀请` | — |
| ops_role_new | `运营台 · 新建业务角色` | — |
| ops_people | `运营台 · 人员` | — |
| ops_roles | `运营台 · 业务角色` | — |
| error | （无 breadcrumb） | — |

### C. 组件类变换（逐页按需）

- 裸 `<table>` → `<table class="table">`。
- 空操作列表头 `<th></th>` → `<th><span class="visually-hidden">操作</span></th>`。
- 卡片/分节容器（已有 `<section>`）：如页面用裸 `<div>` 包成块状内容，换 `class="card"`；已是 `<section>` 的保留（layout.css 已样式化）。
- 状态/来源/计数文本标签 → `<span class="badge badge-muted">…</span>`（或既有 `.badge-success` 语义变体）。
- 主动作按钮 → `class="btn-primary"`；破坏动作按钮保留既有 `class="danger"`；次动作/取消 → ghost 既有类。
- 颜色一律 token；不新增硬编码色值。

### D. 必须保留（不得在 reskin 中改动）

- 破坏动作表单的 `data-confirm="…"`、`action="…"`、`method`、`<input type="hidden" name="csrf_token" …>`、`name="confirmed"` 相关——M3.4a 已接，**仅核验不回退**。
- 排序链接 `{{sortHref …}}`、`{{template "searchbox" .Pager}}`、`{{template "pager" .Pager}}`、`{{template "appnav" .}}`。
- 一次性 secret 展示页（app_created/app_secret_rotated/operator_created/operator_secret_reset/member_invited）的 secret 渲染位、专管线语义——只换外层表现壳，secret 值渲染不得移入任何新增持久位。
- 所有 `{{.Field}}` 数据绑定、表单字段 `name`、路由 action。

### E. 走查脚手架（每批次 axe，复用 M3.4a 范式）

新建一次性 build-tag `walkthrough` Go 脚手架（`zz_walkthrough_scaffold_test.go`，走查后删除、绝不提交）起真依赖 Console + `dbtest.SeedApp`，打印 URL 后阻塞；node Playwright 驱动（系统 Chrome `channel:chrome` headless）登录 root@sydom，逐迁页注入 axe-core 4.10.2 `axe.run` → 收集 violations。axe 文件本地缓存 `axe.min.js`。脚本与脚手架均 scratchpad 一次性，走查后删。

---

## 任务 1：建模台 app 域 8 页 + `.visually-hidden`

**文件：**
- 修改：`static/css/base.css`
- 修改：`templates/{roles,grants,bindings,inheritances,datapolicies,audit,decision,effective}.html`
- 测试：`pagesweep_test.go`（新建）

- [ ] **步骤 1：`.visually-hidden` 落 base.css**

在 `base.css` 末尾追加标准实现：

```css
/* 视觉隐藏但屏幕阅读器可读（用于空操作列表头等无障碍文案） */
.visually-hidden {
  position: absolute;
  width: 1px; height: 1px;
  padding: 0; margin: -1px;
  overflow: hidden;
  clip: rect(0, 0, 0, 0);
  white-space: nowrap;
  border: 0;
}
```

- [ ] **步骤 2：先写失败的结构性测试 `pagesweep_test.go`**

复用 `handler_test.go` 的 `newConsole`/`loginAndCSRF`/`readBody`/`dbtest.SeedApp`。建一个表驱动测试，断言每个建模台页**恰一个 `<h1>`** 且**含 breadcrumb**。app 域页需先 `SeedApp` 并以 root 登录；`decision`/`effective` 取 query 参数（无参时渲表单页，仍应有 h1+breadcrumb）。

```go
package console

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// assertSweptPage 断言迁后页：恰一个 <h1> + 含 breadcrumb（error 页 wantCrumb=false）。
func assertSweptPage(t *testing.T, c *http.Client, url string, wantCrumb bool) {
	t.Helper()
	resp, err := c.Get(url)
	require.NoError(t, err)
	body := readBody(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, url)
	require.Equal(t, 1, strings.Count(body, "<h1"), url+" 应恰一个 <h1>")
	if wantCrumb {
		require.Contains(t, body, `class="breadcrumb"`, url+" 应含 breadcrumb")
	}
}

func TestPageSweep_Modeling(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	a := strconv.FormatInt(appID, 10)
	for _, p := range []string{
		"/apps/" + a + "/roles",
		"/apps/" + a + "/grants",
		"/apps/" + a + "/bindings",
		"/apps/" + a + "/inheritances",
		"/apps/" + a + "/datapolicies",
		"/apps/" + a + "/audit",
		"/apps/" + a + "/decision",
		"/apps/" + a + "/effective",
	} {
		assertSweptPage(t, c, ts.URL+p, true)
	}
}
```

> 核对真实路由：用 `grep -n 'apps/{app_id}' internal/controlplane/console/handler.go` 或 routes_*.go 确认每页 GET 路径（decision/effective/audit 的 path 段以实际注册为准；若带子路径按实调整）。若某页 GET 需必填 query（如 effective 需 user_id），传一个最小占位值或断言其表单页仍 200 + h1+breadcrumb。

- [ ] **步骤 3：运行验证失败**

运行：`go test ./internal/controlplane/console/ -run TestPageSweep_Modeling -count=1`
预期：FAIL（这些页当前是 `<h2>` 无 `<h1>`、无 breadcrumb → `strings.Count(<h1)==0`）。

- [ ] **步骤 4：逐页 reskin（worked example = roles.html）**

**roles.html**（完整前后对照，其余 7 页同配方）：

前：
```html
{{define "content"}}<div class="workspace">{{template "appnav" .}}
<section><h2>角色</h2>
<form method="post" action="/apps/{{.AppID}}/roles" class="inline-form">
<input type="hidden" name="csrf_token" value="{{.CSRF}}">
<input name="code" placeholder="code" required><input name="name" placeholder="名称">
<button>+ 新建角色</button></form>
{{template "searchbox" .Pager}}
<table><thead><tr>
<th><a href="?{{sortHref $.Pager.SortQuery "id" "asc"}}">ID ↑</a> <a href="?{{sortHref $.Pager.SortQuery "id" "desc"}}">↓</a></th>
<th><a href="?{{sortHref $.Pager.SortQuery "code" "asc"}}">Code ↑</a> <a href="?{{sortHref $.Pager.SortQuery "code" "desc"}}">↓</a></th>
<th><a href="?{{sortHref $.Pager.SortQuery "name" "asc"}}">名称 ↑</a> <a href="?{{sortHref $.Pager.SortQuery "name" "desc"}}">↓</a></th>
<th></th></tr></thead><tbody>
{{range .Roles}}<tr><td>{{.RoleId}}</td><td>{{.Code}}</td><td>{{.Name}}</td>
<td><form method="post" action="/apps/{{$.AppID}}/roles/{{.RoleId}}/delete" data-confirm="确定删除该业务角色吗？此操作不可撤销。">
<input type="hidden" name="csrf_token" value="{{$.CSRF}}"><button class="danger">删除</button></form></td></tr>{{end}}
</tbody></table>
{{template "pager" .Pager}}</section></div>{{end}}
```

后（仅改：+breadcrumb、h2→h1、`<table>`→`.table`、空 `<th>`→视觉隐藏、新建按钮→`.btn-primary`；**data-confirm/action/csrf/sortHref/pager/searchbox 全保留**）：
```html
{{define "content"}}<div class="workspace">{{template "appnav" .}}
<section>
<nav class="breadcrumb" aria-label="面包屑">建模台 · 角色</nav>
<h1>角色</h1>
<form method="post" action="/apps/{{.AppID}}/roles" class="inline-form">
<input type="hidden" name="csrf_token" value="{{.CSRF}}">
<input name="code" placeholder="code" required><input name="name" placeholder="名称">
<button class="btn-primary">+ 新建角色</button></form>
{{template "searchbox" .Pager}}
<table class="table"><thead><tr>
<th><a href="?{{sortHref $.Pager.SortQuery "id" "asc"}}">ID ↑</a> <a href="?{{sortHref $.Pager.SortQuery "id" "desc"}}">↓</a></th>
<th><a href="?{{sortHref $.Pager.SortQuery "code" "asc"}}">Code ↑</a> <a href="?{{sortHref $.Pager.SortQuery "code" "desc"}}">↓</a></th>
<th><a href="?{{sortHref $.Pager.SortQuery "name" "asc"}}">名称 ↑</a> <a href="?{{sortHref $.Pager.SortQuery "name" "desc"}}">↓</a></th>
<th><span class="visually-hidden">操作</span></th></tr></thead><tbody>
{{range .Roles}}<tr><td>{{.RoleId}}</td><td>{{.Code}}</td><td>{{.Name}}</td>
<td><form method="post" action="/apps/{{$.AppID}}/roles/{{.RoleId}}/delete" data-confirm="确定删除该业务角色吗？此操作不可撤销。">
<input type="hidden" name="csrf_token" value="{{$.CSRF}}"><button class="danger">删除</button></form></td></tr>{{end}}
</tbody></table>
{{template "pager" .Pager}}</section></div>{{end}}
```

**其余 7 页**（grants/bindings/inheritances/datapolicies/audit/decision/effective）：读各页现有 `{{define "content"}}`，按**同配方**改：
- 在 `<section>` 内、appnav 后、原 `<h2>` 前插 §B 表对应 breadcrumb 行；
- 原页顶 `<h2>X</h2>` → `<h1>X</h1>`（X 逐字保留）；
- 每个裸 `<table>` → `<table class="table">`（effective 有 3 个表，全部加）；
- 每个空操作列 `<th></th>` → `<th><span class="visually-hidden">操作</span></th>`；
- 表单主提交按钮加 `class="btn-primary"`（破坏按钮保留 `danger`）；
- **inheritances**：保留 RemoveRoleInheritance 表单的 `data-confirm`（§D）。

- [ ] **步骤 5：运行结构性测试 + 全包回归**

运行：
```
go test ./internal/controlplane/console/ -run TestPageSweep_Modeling -count=1   # PASS
go test ./internal/controlplane/console/ -count=1                                # 全绿（行为不变回归网）
gofmt -l internal/controlplane/console/ ; go vet ./internal/controlplane/console/
```
预期：结构性测试 PASS；既有全部测试仍绿（reskin 未动内容/CSRF/action）。

- [ ] **步骤 6：axe 走查（建模台 8 页）**

按 §E 起脚手架 + Playwright 驱动，对 8 页 `axe.run` → **0 违规**（重点确认 `page-has-heading-one`/`empty-table-header` 已消）。记录违规数（应全 0）。走查后删脚手架/脚本。

- [ ] **步骤 7：Commit**

```bash
git add internal/controlplane/console/static/css/base.css internal/controlplane/console/templates/{roles,grants,bindings,inheritances,datapolicies,audit,decision,effective}.html internal/controlplane/console/pagesweep_test.go
git commit -m "feat(console): M3.4b 建模台 app 域 8 页迁设计系统(breadcrumb+单 h1+.table+视觉隐藏操作列,data-confirm 保留)+.visually-hidden 工具类

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 2：system 域 5 页

**文件：**
- 修改：`templates/{admin_roles,admin_audit,operators,members,tenants}.html`
- 测试：`pagesweep_test.go`（追加 `TestPageSweep_System`）

- [ ] **步骤 1：先写失败测试（追加）**

```go
func TestPageSweep_System(t *testing.T) {
	ts, store, db := newConsole(t)
	_ = db
	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	for _, p := range []string{
		"/admin/roles",
		"/admin/audit",
		"/operators",
		"/members",
		"/tenants",
	} {
		assertSweptPage(t, c, ts.URL+p, true)
	}
}
```

> 核对真实路由（`grep -n '"/admin\|"/operators\|"/members\|"/tenants' internal/controlplane/console/*.go`）。root@sydom 为系统超管可访问 system 域页；`members`/`tenants` 若需租户上下文，root 应仍渲列表或空态 200。按实际路径修正常量。

- [ ] **步骤 2：运行验证失败** → FAIL（h2 无 h1）。

- [ ] **步骤 3：逐页 reskin（同任务 1 配方）**

读 5 页现有 content，按 §A/§B/§C 改：插 breadcrumb（§B 表）、h2→h1、裸 table→`.table`、空操作 th→视觉隐藏。
- **admin_roles**：保留 RevokeAdminGrant 表单 `data-confirm`（§D）。
- **operators**：保留 UnbindOperatorRole + ResetOperatorSecret 两表单 `data-confirm`（§D）。
- **tenants**：无 table（用 list-plain 或卡片）——仅加 breadcrumb + h1，列表容器按需 `.card`/`.list-plain`（用既有类）。

- [ ] **步骤 4：测试 + 回归**

```
go test ./internal/controlplane/console/ -run TestPageSweep_System -count=1   # PASS
go test ./internal/controlplane/console/ -count=1                              # 全绿
gofmt -l … ; go vet …
```

- [ ] **步骤 5：axe 走查（system 5 页）** → 0 违规。脚手架一次性、走查后删。

- [ ] **步骤 6：Commit**

```bash
git add internal/controlplane/console/templates/{admin_roles,admin_audit,operators,members,tenants}.html internal/controlplane/console/pagesweep_test.go
git commit -m "feat(console): M3.4b system 域 5 页迁设计系统(breadcrumb+单 h1+.table,撤权/解绑/重置 data-confirm 保留)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 3：表单 / 一次性展示 9 页

**文件：**
- 修改：`templates/{app_new,app_created,app_secret_rotated,operator_new,operator_created,operator_secret_reset,register,member_invited,ops_role_new}.html`
- 测试：`pagesweep_test.go`（追加 `TestPageSweep_Forms`）

- [ ] **步骤 1：先写失败测试（追加）**

表单页多为 GET 渲表单、展示页需写动作后到达。结构性断言用「GET 可达的表单/展示页」直接断言；一次性展示页（app_created 等）经其写动作到达后断言，或退化为只对 GET 可达的 app_new/operator_new/register/ops_role_new 断言 h1+breadcrumb，展示页在 axe 走查覆盖。

```go
func TestPageSweep_Forms(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	a := strconv.FormatInt(appID, 10)
	for _, p := range []string{
		"/apps/new",
		"/operators/new",
		"/register",
		"/apps/" + a + "/ops/roles/new",
	} {
		assertSweptPage(t, c, ts.URL+p, true)
	}
}
```

> 核对路由（`grep -n 'apps/new\|operators/new\|"/register\|ops/roles/new' internal/controlplane/console/*.go`）。展示页（app_created/app_secret_rotated/operator_created/operator_secret_reset/member_invited）由写动作渲染，结构性测试不易直达——其 h1+breadcrumb 由 §E axe 走查（经真实写动作到达）核验；本测试覆盖 4 个 GET 表单页即可，避免脆弱播种。

- [ ] **步骤 2：运行验证失败** → FAIL。

- [ ] **步骤 3：逐页 reskin（同配方，含展示页）**

读 9 页现有 content，按 §A/§B/§C 改。展示页（app_created/app_secret_rotated/operator_created/operator_secret_reset/member_invited）：
- 加 breadcrumb（§B）+ h2→h1；
- secret 渲染位（如 `<code>{{.Secret}}</code>` 一次性展示）**原样保留**（§D），仅外层壳换 `.card`；
- 不把 secret 移入任何新增隐藏域/链接/持久位。
- 表单页（app_new/operator_new/register/ops_role_new）：表单容器 `.card`、主提交按钮 `.btn-primary`、字段沿用既有 `.form-field`/`.stacked-form`（参照 login.html）。

- [ ] **步骤 4：测试 + 回归**

```
go test ./internal/controlplane/console/ -run TestPageSweep_Forms -count=1   # PASS
go test ./internal/controlplane/console/ -count=1                            # 全绿(含既有展示页 secret 一次性断言)
gofmt -l … ; go vet …
```

> 特别核对：既有 `TestConsole_RotateAppSecret_ShowsSecretOnce`/`TestConsole_ResetOperatorSecret_ShowsSecretOnce` 仍 PASS（展示页 reskin 未破坏一次性 secret 展示）。

- [ ] **步骤 5：axe 走查（9 页，展示页经真实写动作到达）** → 0 违规。

- [ ] **步骤 6：Commit**

```bash
git add internal/controlplane/console/templates/{app_new,app_created,app_secret_rotated,operator_new,operator_created,operator_secret_reset,register,member_invited,ops_role_new}.html internal/controlplane/console/pagesweep_test.go
git commit -m "feat(console): M3.4b 表单/一次性展示 9 页迁设计系统(breadcrumb+单 h1+.card,一次性 secret 专管线不动)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 4：运营台剩余 2 页 + 错误页

**文件：**
- 修改：`templates/{ops_people,ops_roles,error}.html`
- 测试：`pagesweep_test.go`（追加 `TestPageSweep_OpsAndError`）

- [ ] **步骤 1：先写失败测试（追加）**

```go
func TestPageSweep_OpsAndError(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	a := strconv.FormatInt(appID, 10)
	assertSweptPage(t, c, ts.URL+"/apps/"+a+"/ops/people", true)
	assertSweptPage(t, c, ts.URL+"/apps/"+a+"/ops/roles", true)
	// error 页：触发一个已知 404/错误路由，断言单一 h1（无 breadcrumb）
	assertSweptPage(t, c, ts.URL+"/apps/"+a+"/ops/people?probe=err", true) // 占位：见步骤 3 备注
}
```

> 核对 ops_people/ops_roles 真实路由（`grep -n 'ops/people\|ops/roles' internal/controlplane/console/*.go`，对齐已迁 ops_person）。**error 页**触发方式以实际为准：error.html 经 `renderError` 渲染——找一条会渲 error.html 的请求（如越权/不存在资源）断言其单一 h1（`wantCrumb=false`，改用 `assertSweptPage(t,c,url,false)`）；若不易直达，error 页 h1 由 §E axe 走查覆盖，本测试可只覆盖 ops_people/ops_roles。**实现者择一并把 error 用例落实或移除占位行。**

- [ ] **步骤 2：运行验证失败** → FAIL。

- [ ] **步骤 3：reskin ops_people/ops_roles/error**

- **ops_people/ops_roles**：对齐已迁 `ops_person.html`/`ops_templates.html`（breadcrumb「运营台 · X」+ h1 + `.card`/`.table`/`.list-plain`/`.badge`、bizterm 业务名不漏原语）。
- **error.html**：仅单一 `<h1>`（如「出错了」）+ 设计系统壳（`.card`/`.error-page` 既有类），**不加 breadcrumb**；保留错误文案 `{{.Message}}` 绑定。

- [ ] **步骤 4：测试 + 回归**

```
go test ./internal/controlplane/console/ -run TestPageSweep_OpsAndError -count=1   # PASS
go test ./internal/controlplane/console/ -count=1                                  # 全绿
gofmt -l … ; go vet …
```

- [ ] **步骤 5：axe 走查（ops_people/ops_roles/error）** → 0 违规。

- [ ] **步骤 6：Commit**

```bash
git add internal/controlplane/console/templates/{ops_people,ops_roles,error}.html internal/controlplane/console/pagesweep_test.go
git commit -m "feat(console): M3.4b 运营台 2 页 + 错误页迁设计系统(breadcrumb+单 h1,error 页无 breadcrumb)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 5：整体核验 PS-1..7 + opus 评审 + FF 合并

- [ ] **步骤 1：PS 不变量逐条核验**

```bash
BASE=<本计划 commit sha>
# PS-2 后端 + 生产 .go 零触碰（期望全 0）
git diff $BASE..HEAD -- internal/controlplane/adminauthz/ casbin/enforcer.go internal/sidecar/ api/proto gen/ | wc -l
git diff $BASE..HEAD -- internal/controlplane/console/ ':(exclude)*_test.go' ':(exclude)*.html' ':(exclude)*.css' | wc -l
# PS-4 axe 债已消：迁后页源码无空 <th></th>（操作列已换视觉隐藏）
grep -rn '<th></th>' internal/controlplane/console/templates/ && echo "!!! 仍有空 th" || echo "OK: 空 th 已清"
# 每页恰一个 h1（结构性测试已守门，复跑）
go test ./internal/controlplane/console/ -run 'TestPageSweep' -count=1
```

- [ ] **步骤 2：格式/静态/全量测试**

```bash
gofmt -l internal/ api/        # 空
go vet ./...                   # 净
go test ./... 2>&1 | grep -cE '^FAIL'   # 0
```

- [ ] **步骤 3：整体 axe 横扫**

按 §E 对全部 25 迁页（+ 抽查 5 旗舰页未回归）`axe.run` → 0 违规；对比度抽验 ≥ AA 4.5:1。记录到 `docs/superpowers/2026-06-27-m3-4b-page-sweep-walkthrough.md`，commit（脚手架/脚本一次性删除不提交）。

- [ ] **步骤 4：opus 整体评审**（子代理 model=opus）：逐条核 PS-1..7 + 深挖（reskin 是否误改行为/data 键/路由；data-confirm 是否被回退；一次性 secret 是否仍只一次性展示不入新增持久位；每页单一 h1；axe 0；无新 JS；CSS 全 token）。READY 方可合并。

- [ ] **步骤 5：更新记忆**：`project_detailed_design_progress.md` 加 M3.4b 节；`MEMORY.md` 索引追加 M3.4b 完成 + 下一步 M3.4c。

- [ ] **步骤 6：FF 合并本地 main（不 push origin）**：worktree 全绿 + opus READY 后 FF（`git -C <main> merge --ff-only <branch>`），核实 main==feature tip。

---

## 自检记录

**规格覆盖度（对照 spec）：** §3 范围 25 页 → 任务 1（8 建模台）+2（5 system）+3（9 表单/展示）+4（2 运营台+error）全覆盖 ✓；§4 页头约定 → 共享约定 §A/§B + 各任务步骤 ✓；§5 迁移机制（组件类/axe 修复/`.visually-hidden`）→ 共享 §C + 任务 1 步骤 1 ✓；§6 确认基元不回退 + secret 不动 → 共享 §D + 任务 1（inheritances）/2（admin_roles/operators）/3（展示页）✓；§7 PS-1..7 → 任务 5 步骤 1–4 ✓；§8 测试策略（回归网 + 结构性 TDD + axe 走查）→ 各任务步骤 + 任务 5 ✓。

**规格偏差（计划期明确）：** ① spec §9 预览 6 任务（error 单列任务 5），本计划并 error 入任务 4、核验为任务 5，共 5 任务（spec §9 标「预览」，plan 级细化）。② 一次性展示页（app_created 等）结构性测试不直接覆盖（播种脆弱），其 h1+breadcrumb 由 axe 走查经真实写动作到达核验——既有 secret-once 测试仍守 secret 不泄露。

**占位符扫描：** 路由常量标注「核对真实路由」+ grep 指令（适配既有路由注册，非占位）；worked example（roles.html）给完整前后对照；其余页给精确配方 + §B 文案表 + §D 保留清单。error 页触发方式给「实现者择一」明确指令。

**类型一致性：** `assertSweptPage(t, c, url, wantCrumb)`（任务 1 定义）↔ 任务 2/3/4 调用一致；`pagesweep_test.go` package console、复用 `newConsole`/`loginAndCSRF`/`readBody`/`dbtest.SeedApp`（与 handler_test.go 一致）；`.visually-hidden`（任务 1 base.css）↔ 各页空 th 引用一致；breadcrumb 文案表（§B）↔ 各页 reskin 一致。
