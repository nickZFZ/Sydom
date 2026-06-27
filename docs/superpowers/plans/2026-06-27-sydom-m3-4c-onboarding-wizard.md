# M3.4c Onboarding 向导 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 给新 app 一条「空 app → 可用 app」的首次引导旅程：选官方预设包 → 一键 ApplyTemplate bootstrap → 分配首个用户（可跳过）→ 完成；空 app 自动显示引导横幅。

**架构：** Console BFF 新增 onboarding 路由组（`/ops/apps/{app_id}/onboarding/...`），复用既有 `ApplyTemplate` / `BindUserRole` / `ListRoles` / `ListTemplates` RPC + 唯一 `AuthorizeRule`，**零新增鉴权规则**。presets 内嵌 JSON 加可选 `onboarding` 策展字段（loader 宽松解析）。「需要引导」派生自 app 是否为空（无业务角色），零持久化。有无 JS 都可用（服务端多步渲染）。**后端零触碰**：不改 adminauthz/enforcer/sidecar/proto/迁移；允许的 `.go` 改动仅 console handler + presets。

**技术栈：** Go html/template、M3.1 token 化分层 CSS、presets `//go:embed` JSON、testcontainers、系统 Chrome + Playwright + axe-core 4.10.2（复用 M3.4b 一次性走查脚手架范式）。

**spec：** `docs/superpowers/specs/2026-06-27-sydom-m3-4c-onboarding-wizard-design.md`（commit `d429d78`）。**BASE sha** = 本计划 commit。

---

## 文件结构

| 文件 | 职责 | 任务 |
|---|---|---|
| `internal/controlplane/presets/presets.go` | 加 `Onboarding` 类型 + `Template.Onboarding *Onboarding` 字段（loader 宽松，全可选 fail-soft） | 1 |
| `internal/controlplane/presets/{general-admin,ecommerce-ops}.json` | 补 `onboarding` 策展内容 | 1 |
| `internal/controlplane/presets/presets_test.go` | onboarding 解析测试（有/无/部分字段） | 1 |
| `internal/controlplane/console/routes_onboarding.go`（新建） | 向导路由组：select(GET)/apply(POST)/assign(GET+POST)/done(GET) | 2,3 |
| `internal/controlplane/console/templates/{onboarding_select,onboarding_assign,onboarding_done}.html`（新建） | 向导 3 个可见步骤模板 | 2,3 |
| `internal/controlplane/console/templates/_onboarding_banner.html`（新建） | 空 app 引导横幅 partial | 4 |
| `internal/controlplane/console/handler.go` | 注册 `registerOnboarding` | 2 |
| `internal/controlplane/console/templates/{ops_people,ops_roles,ops_templates,dashboard}.html` | ops appnav 加「引导」入口 + 注入横幅 partial | 4 |
| `internal/controlplane/console/onboarding_test.go`（新建） | 向导结构性 TDD + 行为 + 横幅 | 2,3,4 |

> 约束复述（每任务遵循）：**不改** adminauthz/`casbin/enforcer.go`/sidecar/`api/proto`/`gen/`/迁移；**不新增 ruleTable 条目**（向导 Console 路由按 RPC 方法名复用既有条目：`ApplyTemplate`=template/apply、`BindUserRole`=binding/create、`ListRoles`=role/read、`ListTemplates`=template/read，均已在 `mgmt/authz.go`）；**不新增 JS 文件**（横幅/向导是纯 HTML/CSS，有 JS 时复用 M3.4a 既有 toast/确认基元）；业务语言无原语（capabilityName/permNameMap 兜底，绝不裸 `resource:action`）。

---

## 任务 1：presets onboarding 策展 schema + loader + 2 官方包内容

**文件：**
- 修改：`internal/controlplane/presets/presets.go`
- 修改：`internal/controlplane/presets/general-admin.json`、`ecommerce-ops.json`
- 测试：`internal/controlplane/presets/presets_test.go`

- [ ] **步骤 1：先写失败测试（追加到 presets_test.go）**

```go
func TestOnboarding_Curation(t *testing.T) {
	tpl, ok := presets.Get("general-admin")
	require.True(t, ok)
	require.NotNil(t, tpl.Onboarding, "general-admin 应带 onboarding 策展")
	require.True(t, tpl.Onboarding.Recommended)
	require.NotEmpty(t, tpl.Onboarding.Intro)
	require.NotEmpty(t, tpl.Onboarding.NextSteps)
}

// 缺省 onboarding 不报错、解析为 nil（fail-soft，向后兼容）。
func TestOnboarding_AbsentIsNilNotError(t *testing.T) {
	fsys := fstest.MapFS{
		"x.json": &fstest.MapFile{Data: []byte(`{"id":"x","name":"X","version":1,
			"permissions":[{"code":"a.read","resource":"a","action":"read","type":"act","name":"看"}],
			"roles":[{"key":"r","name":"R","permission_codes":["a.read"]}]}`)},
	}
	ts, err := presets.LoadForTest(fsys) // 见步骤 3：导出测试钩子
	require.NoError(t, err)
	require.Len(t, ts, 1)
	require.Nil(t, ts[0].Onboarding)
}
```

> 说明：`presets` 的 `load` 当前未导出。本任务加一个仅测试用导出钩子 `LoadForTest(fs.FS)`（薄包装 `load`），以便注入 fstest.MapFS 验证 fail-soft。若不愿加导出钩子，可将 `TestOnboarding_AbsentIsNilNotError` 写为 `presets` 包内测试（`package presets`，直接调 `load`）——实现者择一，保持「缺省 onboarding 不报错且为 nil」被覆盖。

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/presets/ -run TestOnboarding -count=1`
预期：FAIL（`Onboarding` 字段未定义，编译错误）。

- [ ] **步骤 3：实现 schema + loader 宽松解析**

在 `presets.go` 加类型（放在 `Role` 之后）：

```go
// Onboarding 是预设包的可选首次引导策展（M3.4c）。全字段可选；缺省即无策展。
// 仅用于运营台向导的展示/排序，不参与 ApplyTemplate 的授权模型种入。
type Onboarding struct {
	Recommended bool     `json:"recommended"` // true → 向导选包步骤置顶并标「推荐」
	Intro       string   `json:"intro"`       // 业务语言简介（每包一句）
	NextSteps   []string `json:"next_steps"`  // 完成页「接下来你可以…」文案
}
```

在 `Template` 结构加字段（指针，缺省 nil = 无策展）：

```go
type Template struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Version     uint32       `json:"version"`
	Permissions []Permission `json:"permissions"`
	Roles       []Role       `json:"roles"`
	Onboarding  *Onboarding  `json:"onboarding"` // M3.4c 可选策展；loader 不强制、不校验内容
}
```

loader **不新增任何 onboarding 校验**（全可选，json.Unmarshal 自然处理缺省→nil）。若步骤 1 选了 `LoadForTest` 钩子，在 `presets.go` 末尾加：

```go
// LoadForTest 仅供测试注入 fs.FS 验证 loader 行为（生产用内嵌 files）。
func LoadForTest(fsys fs.FS) ([]Template, error) { return load(fsys) }
```

- [ ] **步骤 4：给 2 官方包加 onboarding 内容**

`general-admin.json`：在顶层（与 `roles` 同级）加：

```json
  "onboarding": {
    "recommended": true,
    "intro": "适合大多数后台：内容查看/编辑/删除 + 三个常用角色，一键起步",
    "next_steps": ["在「人员」给更多同事分配角色", "在「业务角色」按需微调能力", "在「模板库」追加更多预设"]
  }
```

`ecommerce-ops.json`：同样在顶层加（文案贴合电商运营，`recommended` 按产品取舍设 true 或 false——本计划设 true，两包均推荐）：

```json
  "onboarding": {
    "recommended": true,
    "intro": "适合电商运营：订单/商品/客服分级权限，开箱即用",
    "next_steps": ["在「人员」分配客服/运营同事", "在「业务角色」细化数据范围", "在「模板库」查看更多"]
  }
```

> 核对：加 `onboarding` 后两 JSON 仍合法（逗号/括号）；既有严格校验（permission.code/role.key 唯一、引用存在、condition 合法）不受影响——onboarding 是新顶层键，不碰既有结构。

- [ ] **步骤 5：运行测试 + 包回归**

```
go test ./internal/controlplane/presets/ -run TestOnboarding -count=1   # PASS
go test ./internal/controlplane/presets/ -count=1                        # 全绿（既有校验测试不破）
gofmt -l internal/controlplane/presets/ ; go vet ./internal/controlplane/presets/
```

- [ ] **步骤 6：Commit**

```bash
git add internal/controlplane/presets/
git commit -m "feat(presets): M3.4c onboarding 策展 schema(可选 recommended/intro/next_steps,loader 宽松 fail-soft)+2 官方包补内容

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 2：向导路由组骨架 + 选包(GET) + bootstrap(POST) + 完成(GET)

**文件：**
- 创建：`internal/controlplane/console/routes_onboarding.go`
- 创建：`internal/controlplane/console/templates/onboarding_select.html`、`onboarding_done.html`
- 修改：`internal/controlplane/console/handler.go`
- 测试：`internal/controlplane/console/onboarding_test.go`（新建）

- [ ] **步骤 1：先写失败的结构性测试（新建 onboarding_test.go）**

复用 `handler_test.go` 的 `newConsole`/`loginAndCSRF`/`readBody`/`dbtest.SeedApp`。

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

// getOK 取页面、断言 200 + 恰一个 <h1> + 含 breadcrumb。
func getOK(t *testing.T, c *http.Client, url string) string {
	t.Helper()
	resp, err := c.Get(url)
	require.NoError(t, err)
	body := readBody(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode, url)
	require.Equal(t, 1, strings.Count(body, "<h1>"), url+" 应恰一个 <h1>")
	require.Contains(t, body, `class="breadcrumb"`, url+" 应含 breadcrumb")
	return body
}

func TestOnboarding_SelectAndDone(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	a := strconv.FormatInt(appID, 10)
	// 选包步骤：列官方包（业务名），含「推荐」与 intro
	sel := getOK(t, c, ts.URL+"/ops/apps/"+a+"/onboarding")
	require.Contains(t, sel, "通用后台管理")
	require.Contains(t, sel, "推荐")
	require.Contains(t, sel, "一键起步") // general-admin intro 片段
	// 完成步骤可直达渲染
	done := getOK(t, c, ts.URL+"/ops/apps/"+a+"/onboarding/done?template_id=general-admin")
	require.Contains(t, done, "接下来")
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/console/ -run TestOnboarding_SelectAndDone -count=1`
预期：FAIL（路由未注册 → 404 → 断言 200 失败）。

- [ ] **步骤 3：实现 routes_onboarding.go（select + apply + done）**

```go
package console

import (
	"net/http"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/controlplane/presets"
	"google.golang.org/grpc/codes"
)

// registerOnboarding 注册新 app 首次引导向导（复用既有 RPC + AuthorizeRule，零新增鉴权）。
func (h *Handler) registerOnboarding(mux *http.ServeMux) {
	mux.HandleFunc("GET /ops/apps/{app_id}/onboarding", h.onboardingSelect)
	mux.HandleFunc("POST /ops/apps/{app_id}/onboarding/apply", h.onboardingApply)
	mux.HandleFunc("GET /ops/apps/{app_id}/onboarding/assign", h.onboardingAssignForm) // 任务 3
	mux.HandleFunc("POST /ops/apps/{app_id}/onboarding/assign", h.onboardingAssign)    // 任务 3
	mux.HandleFunc("GET /ops/apps/{app_id}/onboarding/done", h.onboardingDone)
}

// onboardingPack 是选包步骤的渲染视图（业务名 + 策展）。
type onboardingPack struct {
	ID, Name, Description, Intro string
	Recommended                  bool
	PermCount, RoleCount         int
}

// onboardingSelect：GET /ops/apps/{app_id}/onboarding —— 列官方预设包（推荐置顶 + intro）。
// 读经授权的 ListTemplates；策展（recommended/intro）从内嵌 presets.Get 合并（proto 不带 onboarding，
// 守 OB-3 不改 proto）。
func (h *Handler) onboardingSelect(w http.ResponseWriter, r *http.Request) {
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
	var recommended, others []onboardingPack
	for _, t := range resp.Templates {
		p := onboardingPack{ID: t.Id, Name: t.Name, Description: t.Description,
			PermCount: len(t.Permissions), RoleCount: len(t.Roles)}
		if ob := onboardingOf(t.Id); ob != nil {
			p.Intro = ob.Intro
			p.Recommended = ob.Recommended
		}
		if p.Recommended {
			recommended = append(recommended, p)
		} else {
			others = append(others, p)
		}
	}
	h.renderPage(w, r, "onboarding_select.html", http.StatusOK, map[string]any{
		"AppID": appID, "Recommended": recommended, "Others": others,
		"CSRF": sess.CSRF, "OpsNav": "onboarding",
	})
}

// onboardingOf 取内嵌预设包的 onboarding 策展（nil 安全）。
func onboardingOf(id string) *presets.Onboarding {
	t, ok := presets.Get(id)
	if !ok {
		return nil
	}
	return t.Onboarding
}

// onboardingApply：POST /ops/apps/{app_id}/onboarding/apply —— 一键 bootstrap。
// 安全管线镜像 doWrite：会话→CSRF→AuthorizeRule→status 闸→ApplyTemplate；
// 幂等故 PRG 重定向到分配步骤（继续旅程）。
func (h *Handler) onboardingApply(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if !h.checkCSRF(r, sess) {
		h.renderError(w, r, codes.PermissionDenied, "CSRF 校验失败", nil)
		return
	}
	appID, err := pathUint64(r, "app_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"ApplyTemplate", err)
		return
	}
	templateID := r.FormValue("template_id")
	msg := &adminv1.ApplyTemplateRequest{AppId: appID, TemplateId: templateID}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"ApplyTemplate", principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ApplyTemplate", err)
		return
	}
	if err := mgmt.CheckStatusWrite(ctx, h.db, svc+"ApplyTemplate", msg); err != nil {
		h.renderGRPCError(w, r, svc+"ApplyTemplate", err)
		return
	}
	if _, err := h.srv.ApplyTemplate(ctx, msg); err != nil {
		h.renderGRPCError(w, r, svc+"ApplyTemplate", err)
		return
	}
	http.Redirect(w, r, "/ops/apps/"+strconv.FormatUint(appID, 10)+"/onboarding/assign?template_id="+url.QueryEscape(templateID), http.StatusSeeOther)
}

// onboardingDone：GET /ops/apps/{app_id}/onboarding/done —— 完成页（next_steps 指向运营台）。
func (h *Handler) onboardingDone(w http.ResponseWriter, r *http.Request) {
	_, _, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	appID, err := pathUint64(r, "app_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListRoles", err)
		return
	}
	var nextSteps []string
	if ob := onboardingOf(r.FormValue("template_id")); ob != nil {
		nextSteps = ob.NextSteps
	}
	h.renderPage(w, r, "onboarding_done.html", http.StatusOK, map[string]any{
		"AppID": appID, "NextSteps": nextSteps, "OpsNav": "onboarding",
	})
}
```

> 需要的 import：`strconv`、`net/url`（apply 重定向用）。把它们加进 routes_onboarding.go 的 import 块。

- [ ] **步骤 4：实现 onboarding_select.html + onboarding_done.html**

`onboarding_select.html`（对齐 ops_templates.html 范式：workspace + appnav + breadcrumb + 单 h1 + .card；appnav 含「引导」入口由任务 4 统一加，本任务先放 4 链接版以便页面自洽）：

```html
{{define "title"}}开始引导 · 运营台 · App {{.AppID}}{{end}}
{{define "content"}}<div class="workspace">
<aside class="appnav" aria-label="运营台导航"><div class="appname">运营台 · App #{{.AppID}}</div>
<a href="/ops/apps/{{.AppID}}/onboarding" {{if eq .OpsNav "onboarding"}}class="active"{{end}}>引导</a>
<a href="/ops/apps/{{.AppID}}/people" {{if eq .OpsNav "people"}}class="active"{{end}}>人员</a>
<a href="/ops/apps/{{.AppID}}/roles" {{if eq .OpsNav "roles"}}class="active"{{end}}>业务角色</a>
<a href="/ops/apps/{{.AppID}}/templates" {{if eq .OpsNav "templates"}}class="active"{{end}}>模板库</a>
</aside>
<section>
<nav class="breadcrumb" aria-label="面包屑">运营台 · 开始引导</nav>
<h1>开始引导</h1>
<p class="hint">选一个官方预设包，一键为本应用建好权限点与业务角色，几步即可上手。</p>
{{define "packcard"}}
<div class="card">
<h2>{{.Name}}{{if .Recommended}} <span class="badge badge-success">推荐</span>{{end}}</h2>
{{if .Intro}}<p>{{.Intro}}</p>{{end}}
<p class="hint">{{.PermCount}} 个权限点 · {{.RoleCount}} 个业务角色</p>
<form method="post" action="/ops/apps/{{$.AppID}}/onboarding/apply" data-confirm="将为本应用应用预设「{{.Name}}」，建好权限点与业务角色，确定吗？">
<input type="hidden" name="csrf_token" value="{{$.CSRF}}">
<input type="hidden" name="template_id" value="{{.ID}}">
<button class="btn btn-primary">应用此预设</button></form>
</div>
{{end}}
{{range .Recommended}}{{template "packcard" .}}{{end}}
{{range .Others}}{{template "packcard" .}}{{end}}
{{if not (or .Recommended .Others)}}<p class="empty-state">暂无可用预设包。</p>{{end}}
</section></div>{{end}}
```

> 注意 `{{$.AppID}}`/`{{$.CSRF}}`：`packcard` 子模板内 `.` 是 pack，故用 `$` 取根上下文。`data-confirm` 复用 M3.4a 既有确认基元（有 JS = dialog，无 JS = 直提交，仍过 CSRF/授权/status）。

`onboarding_done.html`：

```html
{{define "title"}}引导完成 · 运营台 · App {{.AppID}}{{end}}
{{define "content"}}<div class="workspace">
<aside class="appnav" aria-label="运营台导航"><div class="appname">运营台 · App #{{.AppID}}</div>
<a href="/ops/apps/{{.AppID}}/onboarding" {{if eq .OpsNav "onboarding"}}class="active"{{end}}>引导</a>
<a href="/ops/apps/{{.AppID}}/people" {{if eq .OpsNav "people"}}class="active"{{end}}>人员</a>
<a href="/ops/apps/{{.AppID}}/roles" {{if eq .OpsNav "roles"}}class="active"{{end}}>业务角色</a>
<a href="/ops/apps/{{.AppID}}/templates" {{if eq .OpsNav "templates"}}class="active"{{end}}>模板库</a>
</aside>
<section>
<nav class="breadcrumb" aria-label="面包屑">运营台 · 引导完成</nav>
<h1>引导完成 🎉</h1>
<p>本应用已就绪。接下来你可以：</p>
<ul class="list-plain">
{{range .NextSteps}}<li>{{.}}</li>{{end}}
<li><a href="/ops/apps/{{.AppID}}/people">前往「人员」</a> · <a href="/ops/apps/{{.AppID}}/roles">前往「业务角色」</a></li>
</ul>
</section></div>{{end}}
```

> a11y：`.list-plain a` 已在 M3.4b 加下划线（消 link-in-text-block）。

- [ ] **步骤 5：注册路由（handler.go）**

在 `register*` 序列里加一行（紧接 `registerTemplates` 之后）：

```go
	h.registerTemplates(mux)       // M3.2 运营台模板库
	h.registerOnboarding(mux)      // M3.4c 新 app 首次引导向导
```

- [ ] **步骤 6：运行结构性测试 + 包回归**

```
go test ./internal/controlplane/console/ -run TestOnboarding_SelectAndDone -count=1   # PASS
go test ./internal/controlplane/console/ -count=1                                      # 全绿（≈110s，需 docker）
gofmt -l internal/controlplane/console/ ; go vet ./internal/controlplane/console/
```

- [ ] **步骤 7：Commit**

```bash
git add internal/controlplane/console/routes_onboarding.go internal/controlplane/console/templates/onboarding_select.html internal/controlplane/console/templates/onboarding_done.html internal/controlplane/console/handler.go internal/controlplane/console/onboarding_test.go
git commit -m "feat(console): M3.4c onboarding 向导骨架(选包 GET 推荐置顶+intro/bootstrap POST 复用 ApplyTemplate PRG 进分配/完成 GET next_steps,零新增鉴权)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 3：分配步骤（GET 表单 + POST，可跳过）

**文件：**
- 修改：`internal/controlplane/console/routes_onboarding.go`（加 assign 两 handler）
- 创建：`internal/controlplane/console/templates/onboarding_assign.html`
- 测试：`internal/controlplane/console/onboarding_test.go`（追加）

- [ ] **步骤 1：先写失败测试（追加）**

```go
func TestOnboarding_AssignFormAndBind(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	a := strconv.FormatInt(appID, 10)
	// 先 bootstrap 建出角色（直接调既有模板应用路由，幂等）
	_, err := c.PostForm(ts.URL+"/ops/apps/"+a+"/onboarding/apply",
		url.Values{"csrf_token": {csrf}, "template_id": {"general-admin"}})
	require.NoError(t, err)
	// 分配表单：应渲染角色下拉（业务名「管理员」），单 h1 + breadcrumb
	form := getOK(t, c, ts.URL+"/ops/apps/"+a+"/onboarding/assign?template_id=general-admin")
	require.Contains(t, form, "管理员")
	require.Contains(t, form, "跳过") // 可跳过链接
}
```

> 需要 import `net/url`。`PostForm` 默认跟随重定向；apply 重定向到 assign（200）无妨，本用例只为种出角色。

- [ ] **步骤 2：运行验证失败** → FAIL（assign 路由 handler 未实现 / 模板缺失）。

- [ ] **步骤 3：实现 assign 两 handler（追加到 routes_onboarding.go）**

```go
// onboardingAssignForm：GET …/onboarding/assign —— 选业务角色 + 输入首个用户标识（可跳过）。
func (h *Handler) onboardingAssignForm(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	appID, err := pathUint64(r, "app_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListRoles", err)
		return
	}
	msg := &adminv1.ListRolesRequest{AppId: appID}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"ListRoles", principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListRoles", err)
		return
	}
	resp, err := h.srv.ListRoles(ctx, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListRoles", err)
		return
	}
	h.renderPage(w, r, "onboarding_assign.html", http.StatusOK, map[string]any{
		"AppID": appID, "Roles": resp.Roles, "TemplateID": r.FormValue("template_id"),
		"CSRF": sess.CSRF, "OpsNav": "onboarding",
	})
}

// onboardingAssign：POST …/onboarding/assign —— 绑定首个用户到业务角色（doWrite + BindUserRole），
// 成功后进完成步骤。复用 decodeUserRoleRequest（app_id path + role_id/user_id form）。
func (h *Handler) onboardingAssign(w http.ResponseWriter, r *http.Request) {
	h.doWrite(w, r, svc+"BindUserRole",
		decodeUserRoleRequest,
		func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
			return s.BindUserRole(ctx, m.(*adminv1.UserRoleRequest))
		},
		func(r *http.Request) string {
			return "/ops/apps/" + r.PathValue("app_id") + "/onboarding/done?template_id=" + url.QueryEscape(r.FormValue("template_id"))
		})
}
```

> 需要 import `context`、`google.golang.org/protobuf/proto`（doWrite 闭包签名）。「跳过」是模板里直达 `…/onboarding/done?template_id=...` 的链接（GET，不写库）。`ListRoles` 返回的角色名经 `roleNameMap` 已是业务名？此处直接用 `RoleSummary.Name`（业务名，CreateBusinessRole/ApplyTemplate 种入的 name）。

- [ ] **步骤 4：实现 onboarding_assign.html**

```html
{{define "title"}}分配首个成员 · 运营台 · App {{.AppID}}{{end}}
{{define "content"}}<div class="workspace">
<aside class="appnav" aria-label="运营台导航"><div class="appname">运营台 · App #{{.AppID}}</div>
<a href="/ops/apps/{{.AppID}}/onboarding" {{if eq .OpsNav "onboarding"}}class="active"{{end}}>引导</a>
<a href="/ops/apps/{{.AppID}}/people" {{if eq .OpsNav "people"}}class="active"{{end}}>人员</a>
<a href="/ops/apps/{{.AppID}}/roles" {{if eq .OpsNav "roles"}}class="active"{{end}}>业务角色</a>
<a href="/ops/apps/{{.AppID}}/templates" {{if eq .OpsNav "templates"}}class="active"{{end}}>模板库</a>
</aside>
<section>
<nav class="breadcrumb" aria-label="面包屑">运营台 · 分配首个成员</nav>
<h1>分配首个成员</h1>
<p class="hint">把第一个同事分配到一个业务角色，应用就真正能用了。也可以现在跳过，稍后在「人员」里做。</p>
<form method="post" action="/ops/apps/{{.AppID}}/onboarding/assign" class="stacked-form">
<input type="hidden" name="csrf_token" value="{{.CSRF}}">
<input type="hidden" name="template_id" value="{{.TemplateID}}">
<label>用户标识 <input name="user_id" placeholder="如 alice@corp" required></label>
<label>业务角色
<select name="role_id" required>
{{range .Roles}}<option value="{{.RoleId}}">{{.Name}}</option>{{end}}
</select></label>
<button class="btn btn-primary">分配并完成</button>
</form>
<p><a href="/ops/apps/{{.AppID}}/onboarding/done?template_id={{.TemplateID}}">跳过这一步 →</a></p>
</section></div>{{end}}
```

- [ ] **步骤 5：测试 + 回归**

```
go test ./internal/controlplane/console/ -run TestOnboarding -count=1   # PASS（Select/Done + AssignForm）
go test ./internal/controlplane/console/ -count=1                        # 全绿
gofmt -l … ; go vet …
```

- [ ] **步骤 6：Commit**

```bash
git add internal/controlplane/console/routes_onboarding.go internal/controlplane/console/templates/onboarding_assign.html internal/controlplane/console/onboarding_test.go
git commit -m "feat(console): M3.4c onboarding 分配步骤(GET 角色下拉业务名+用户标识/POST doWrite BindUserRole 进完成,可跳过)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 4：空 app 引导横幅 + 运营台导航「引导」入口

**文件：**
- 创建：`internal/controlplane/console/templates/_onboarding_banner.html`
- 修改：`internal/controlplane/console/routes_ops.go`（opsPeople/opsRoles/opsTemplates 设 `ShowOnboarding`）、`routes_templates.go`（opsTemplates 设 `ShowOnboarding`）、`templates/{ops_people,ops_roles,ops_templates,dashboard}.html`
- 测试：`internal/controlplane/console/onboarding_test.go`（追加）

> 范围：①「引导」导航链接加进 3 个既有 ops 页的 appnav（people/roles/templates）——与向导页 appnav 一致（5 链接）。② 空 app 横幅 partial 注入 3 个 ops 页 + dashboard，由 handler 设 `ShowOnboarding`（app 无业务角色时 true）。

- [ ] **步骤 1：先写失败测试（追加）**

```go
func TestOnboarding_BannerWhenEmptyGoneWhenSeeded(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	a := strconv.FormatInt(appID, 10)
	// 空 app：人员页应含引导横幅
	empty := readBody(t, mustGet(t, c, ts.URL+"/ops/apps/"+a+"/roles"))
	require.Contains(t, empty, "开始引导", "空 app 应显示引导横幅")
	// bootstrap 后非空：横幅消失
	_, err := c.PostForm(ts.URL+"/ops/apps/"+a+"/onboarding/apply",
		url.Values{"csrf_token": {csrf}, "template_id": {"general-admin"}})
	require.NoError(t, err)
	seeded := readBody(t, mustGet(t, c, ts.URL+"/ops/apps/"+a+"/roles"))
	require.NotContains(t, seeded, "data-onboarding-banner", "非空 app 不应显示引导横幅")
}

func mustGet(t *testing.T, c *http.Client, url string) *http.Response {
	t.Helper()
	resp, err := c.Get(url)
	require.NoError(t, err)
	return resp
}
```

> 横幅根元素带 `data-onboarding-banner` 属性作稳定断言锚（避免误命中向导页内文案）；「开始引导」文案锚用于「存在」断言。

- [ ] **步骤 2：运行验证失败** → FAIL（横幅未注入）。

- [ ] **步骤 3：实现横幅 partial**

`_onboarding_banner.html`：

```html
{{define "onboarding_banner"}}{{if .ShowOnboarding}}
<div class="card" data-onboarding-banner role="note">
<strong>这个应用还是空的。</strong> 用官方预设几步建好权限与角色 →
<a class="btn btn-primary" href="/ops/apps/{{.AppID}}/onboarding">开始引导</a>
</div>
{{end}}{{end}}
```

> partial 用 `{{template "onboarding_banner" .}}` 调用；依赖渲染数据里的 `ShowOnboarding`（bool）与 `AppID`。`{{if .ShowOnboarding}}` 缺省（未设键）为 false → 默认不渲染（向后兼容其它页面）。

- [ ] **步骤 4：handler 设 ShowOnboarding（仅这几页，app 无业务角色时 true）**

加一个 helper（放 routes_onboarding.go）：

```go
// appHasNoBizRoles 判 app 是否无业务角色（用于空态横幅；fail-soft：出错→false 不显示横幅，
// 宁可不打扰也不在非空/异常时误显）。复用既有 AuthorizeRule + ListRoles（role/read scopeApp）。
func (h *Handler) appHasNoBizRoles(ctx context.Context, principal string, appID uint64) bool {
	msg := &adminv1.ListRolesRequest{AppId: appID}
	actx, err := mgmt.AuthorizeRule(ctx, h.enf, svc+"ListRoles", principal, msg)
	if err != nil {
		return false
	}
	resp, err := h.srv.ListRoles(actx, msg)
	if err != nil {
		return false
	}
	return len(resp.Roles) == 0
}
```

在 `opsRoles`（routes_ops.go）：它已有 `resp.Roles`，直接派生，免二次查询——在其 `renderPage` 的 data map 加：

```go
		"AppID": appID, "Roles": resp.Roles, "CSRF": sess.CSRF, "OpsNav": "roles",
		"ShowOnboarding": len(resp.Roles) == 0,
```

在 `opsPeople`（routes_ops.go）与 `opsTemplates`（routes_templates.go）的 `renderPage` data map 加（用 helper，principal/ctx 取自各 handler 既有变量）：

```go
		"ShowOnboarding": h.appHasNoBizRoles(r.Context(), principal, appID),
```

> dashboard：dashboard handler 已知当前 app 列表/选中 app？若 dashboard 是租户级（非单 app）则横幅不适用——**实现者核对 dashboard handler**：若其渲染聚焦单个 app 则同法设 `ShowOnboarding`；若是多 app 概览则**跳过 dashboard 横幅**（横幅仅 3 ops 页足够覆盖入口，spec「ops 区 + 仪表盘」中仪表盘为加分项，dashboard 不适配则不强加，并在任务汇报中说明）。

- [ ] **步骤 5：在 3 个 ops 页注入横幅 + 加「引导」appnav 链接**

对 `ops_people.html`、`ops_roles.html`、`ops_templates.html`：
1. appnav `<aside>` 内最前面加一行（与向导页一致）：
```html
<a href="/ops/apps/{{.AppID}}/onboarding" {{if eq .OpsNav "onboarding"}}class="active"{{end}}>引导</a>
```
2. 在 `<section>` 内 breadcrumb 之后、`<h1>` 之前注入横幅：
```html
{{template "onboarding_banner" .}}
```

- [ ] **步骤 6：测试 + 回归**

```
go test ./internal/controlplane/console/ -run TestOnboarding -count=1   # PASS（含 Banner）
go test ./internal/controlplane/console/ -count=1                        # 全绿
gofmt -l … ; go vet …
```

- [ ] **步骤 7：Commit**

```bash
git add internal/controlplane/console/templates/_onboarding_banner.html internal/controlplane/console/routes_onboarding.go internal/controlplane/console/routes_ops.go internal/controlplane/console/routes_templates.go internal/controlplane/console/templates/ops_people.html internal/controlplane/console/templates/ops_roles.html internal/controlplane/console/templates/ops_templates.html internal/controlplane/console/onboarding_test.go
git commit -m "feat(console): M3.4c 空 app 引导横幅(派生无业务角色 fail-soft 不打扰)+运营台导航引导入口

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 5：整体核验 OB-1..7 + opus 评审 + FF 合并

- [ ] **步骤 1：OB 不变量逐条核验**

```bash
BASE=<本计划 commit sha>
# OB-3 后端零触碰（期望全 0）
git diff $BASE..HEAD -- internal/controlplane/adminauthz/ casbin/enforcer.go internal/sidecar/ api/proto gen/ | wc -l
# OB-2 无新增 ruleTable 条目（authz.go 不应被改）
git diff $BASE..HEAD -- internal/controlplane/mgmt/authz.go | wc -l
# 无新增 JS 文件（static/*.js 不应有新增）
git diff $BASE..HEAD --name-status -- 'internal/controlplane/console/static/*.js'
# 无迁移新增
git diff $BASE..HEAD --name-only -- internal/db/migrations/ | wc -l
# 向导结构性测试复跑
go test ./internal/controlplane/console/ -run TestOnboarding -count=1
go test ./internal/controlplane/presets/ -run TestOnboarding -count=1
```
预期：前 4 项均 0 / 空；测试 PASS。

- [ ] **步骤 2：格式/静态/全量测试**

```bash
gofmt -l internal/ api/        # 空
go vet ./...                   # 净
go test ./... 2>&1 | grep -cE '^FAIL'   # 0
make proto-check               # 零漂移（onboarding 未动 proto）
```

- [ ] **步骤 3：真实浏览器 axe 走查（向导 4 步 + 横幅页）**

复用 M3.4b 一次性 build-tag `walkthrough` 脚手架（`zz_walkthrough_scaffold_test.go`，长 TTL store、`os.WriteFile` 传 URL、走查后删不提交）：起真依赖 Console + `SeedAppInTenant`，系统 Chrome + Playwright 注入 axe-core 4.10.2；登录 root 后走：① `/ops/apps/{id}/onboarding`（选包，空态横幅源页）② 提交 apply → `/onboarding/assign` ③ 分配/跳过 → `/onboarding/done` ④ 回 `/ops/apps/{id}/roles` 看横幅（空 app→有；apply 后→无）。每页 `axe.run` → **0 违规**；每页单一 `<h1>` + breadcrumb（done/select/assign 均有）。记录到 `docs/superpowers/2026-06-27-m3-4c-onboarding-walkthrough.md`，commit（脚手架/脚本不提交）。

- [ ] **步骤 4：opus 整体评审**（子代理 model=opus）：逐条核 OB-1..7 + 深挖：向导是否复用既有 RPC 无第二套授权（onboardingApply 镜像 doWrite 全闸、onboardingAssign 走 doWrite）；ListTemplates 授权读 + presets.Get 策展合并不泄露跨租户；apply 幂等、未知 template→InvalidArgument、跨租户 app→fail-close NotFound/PermissionDenied；横幅 fail-soft（出错不误显）；业务语言无原语（角色用 .Name、包用 .Name/intro，无 role_id/code/谓词裸露）；secret 不涉及。READY 方可合并。

- [ ] **步骤 5：更新记忆**：`project_detailed_design_progress.md` 加 M3.4c 节；`MEMORY.md` 索引追加 M3.4c 完成 + **M3.4 里程碑收官**。

- [ ] **步骤 6：FF 合并本地 main（不 push origin）**：worktree 全绿 + opus READY 后 FF（`git -C <main> merge --ff-only <branch>`），核实 main==feature tip，清理 worktree。

---

## 自检记录

**规格覆盖度（对照 spec）：** §4 onboarding schema → 任务 1 ✓；§5 路由与 4 步（select/apply/assign/done + 横幅）→ 任务 2（select/apply/done）+ 任务 3（assign）+ 任务 4（横幅 + nav 入口）✓；§6 状态模型（派生空态、零持久化、幂等）→ 任务 4 横幅派生 + apply 幂等（复用 ApplyTemplate）✓；§7 OB-1..7 → 任务 5 步骤 1–4 ✓；§8 测试策略（结构性 TDD + 行为 + presets loader + 回归 + axe）→ 各任务步骤 + 任务 5 ✓。

**规格偏差（计划期明确）：** ① dashboard 横幅设为「条件加分」——若 dashboard 是多 app 概览不适配单 app 横幅则跳过（任务 4 步骤 4 注明），横幅主入口落 3 ops 页 + nav，不影响「派生空态引导」核心。② apply 后 PRG 到 assign（非渲染 ops_template_applied 摘要）——向导要继续旅程，故重定向；既有 opsApplyTemplate（模板库页）仍渲摘要不变。

**占位符扫描：** 路由/handler/模板均给完整代码；BASE sha 待填（FF 时取本计划 commit）；dashboard 横幅给「实现者核对 + 择一」明确指令（适配则设 ShowOnboarding，不适配则跳过并汇报），非占位。

**类型一致性：** `Onboarding{Recommended bool; Intro string; NextSteps []string}`（任务 1）↔ `onboardingOf`/select/done 引用一致；`ShowOnboarding`（任务 4 handler 设）↔ `_onboarding_banner.html` `.ShowOnboarding` 一致；`onboarding_banner` 模板名（define）↔ `{{template "onboarding_banner" .}}` 调用一致；`decodeUserRoleRequest`（既有，app_id/role_id/user_id）↔ onboarding_assign.html 表单字段（user_id/role_id）一致；OpsNav `"onboarding"` ↔ 各向导页/ops 页 appnav 高亮一致；复用 RPC 方法名（ListTemplates/ApplyTemplate/ListRoles/BindUserRole）↔ mgmt/authz.go 既有 ruleTable 条目一致（零新增）。
