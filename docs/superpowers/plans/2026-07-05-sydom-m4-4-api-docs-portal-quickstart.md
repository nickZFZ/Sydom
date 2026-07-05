# M4.4 API 文档门户 + Quickstart 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 在 Web Console 新增 `/developer` 只读开发者文档区——数据面授权 Check 的 quickstart + 核心概念 + SDK 参考（手写，代码取自真实 orderservice），管理面 API 端点参考（从权威 `ruleTable`+`restgw` route 注册表自动派生，测试断言零漂移）。

**架构：** 管理面参考的数据在进程内从 `mgmt.ruleTable`（授权唯一真相）+ `restgw.allRoutes()`（REST method+path）只读派生——各加一个**只读导出访问器**（不改授权/route 内容），Console 组装渲染。数据面 quickstart/SDK 参考是手写静态内容（代码取自 `examples/orderservice`）。全程 html/template BFF、复用 M3.1 设计系统、**无新 JS**、会话只读鉴权。

**技术栈：** Go、html/template、`net/http`、testify、testcontainers（Console handler 测试）、axe-core 4.10.2（走查）。

**规格：** `docs/superpowers/specs/2026-07-05-sydom-m4-4-api-docs-portal-quickstart-design.md`（BASE=main `6339fb6`；含 DP-1..7）。

**范式纪律：** 子代理驱动 + 两阶段审查（规格→质量）；TDD；每任务独立 commit；**禁 `--amend`**；实现者不读本 plan（由控制者派发任务文本）。**授权核心/SDK/数据面零触碰**（DP-7）：`mgmt`/`restgw` 仅 +只读导出访问器，`ruleTable`/`rpcRule`/`route`/`allRoutes` 内容与授权逻辑一字不改。

---

## 文件结构（先锁定分解）

| 文件 | 职责 | 任务 |
|---|---|---|
| `internal/controlplane/mgmt/apidoc.go` | +导出 `RPCDoc` + `RuleEntries()`（从 `ruleTable` 只读派生，scope→字符串） | 1 |
| `internal/controlplane/mgmt/apidoc_test.go` | `RuleEntries()` 覆盖全 `ruleTable`、scope 映射、稳定排序 | 1 |
| `internal/controlplane/restgw/apidoc.go` | +导出 `RouteDoc` + `Routes()`（从 `allRoutes()` 只读派生） | 1 |
| `internal/controlplane/restgw/apidoc_test.go` | `Routes()` 覆盖全 `allRoutes()`、含 fullMethod/method/pattern | 1 |
| `internal/controlplane/console/apiref.go` | `APIRefEntry` + `buildAPIReference()`（join mgmt+restgw，稳定排序） | 2 |
| `internal/controlplane/console/apiref_test.go` | **DP-2 零漂移有齿**：`ruleTable` 每条都在 `buildAPIReference()`（+反向验证） | 2 |
| `internal/controlplane/console/routes_developer.go` | `registerDeveloper` + `developer` handler（会话只读，组装四块数据） | 3,4 |
| `internal/controlplane/console/routes_developer_test.go` | 页 200/单 h1/四块锚点/不含 secret/未登录挡/DP-3 SDK 符号真实 | 3,4 |
| `internal/controlplane/console/templates/developer.html` | 四块 `<section>`：Quickstart / 概念 / SDK 参考 / 管理面端点表 | 3,4 |
| `internal/controlplane/console/templates/_appnav.html:9` | +「开发者」tab（Tab=="developer"） | 4 |
| `internal/controlplane/console/handler.go:33` | +`h.registerDeveloper(mux)` | 3 |
| `internal/controlplane/console/static/css/components.css` | 端点表/代码块样式（复用设计系统 token，如需） | 4 |
| `docs/superpowers/2026-07-05-m4-4-api-docs-portal-walkthrough.md` | 走查记录（任务 5 产出） | 5 |

**关键分解决策：**
- 访问器放各自包（`mgmt`/`restgw`），返回**独立文档 struct**（不暴露内部 `rpcRule`/`route`），Console 仅依赖导出面。
- 防漂移锚定 `ruleTable`（授权唯一真相）：DP-2 测试断言 `ruleTable` ⊆ 渲染输出。
- 数据面 quickstart/SDK 参考=手写静态 HTML（代码取自真实 orderservice），无需派生逻辑。

---

## 任务 1：`mgmt`/`restgw` 只读导出访问器（防漂移数据源）

**文件：**
- 创建：`internal/controlplane/mgmt/apidoc.go`、`internal/controlplane/mgmt/apidoc_test.go`
- 创建：`internal/controlplane/restgw/apidoc.go`、`internal/controlplane/restgw/apidoc_test.go`

参考既有：`mgmt/authz.go`（`ruleTable map[string]rpcRule{resource,action,isWrite,scope}`、`ruleScope` 常量 `scopeSystem/scopeApp/scopeTenant/scopeSelf`）；`restgw/routes.go`（`route{method,pattern,fullMethod}`、`allRoutes() []route`）。

- [ ] **步骤 1：写失败测试 `mgmt/apidoc_test.go`**

```go
package mgmt

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRuleEntries_CoversRuleTableAndScopes(t *testing.T) {
	entries := RuleEntries()
	// 覆盖全 ruleTable（一一对应，无漏无多）。
	require.Len(t, entries, len(ruleTable))
	seen := map[string]RPCDoc{}
	for _, e := range entries {
		seen[e.FullMethod] = e
	}
	for fm, r := range ruleTable {
		e, ok := seen[fm]
		require.True(t, ok, "RuleEntries 漏了 %s", fm)
		require.Equal(t, r.resource, e.Resource)
		require.Equal(t, r.action, e.Action)
		require.Equal(t, r.isWrite, e.IsWrite)
	}
	// scope 映射为可读字符串（非空、属已知集）。
	for _, e := range entries {
		require.Contains(t, []string{"system", "app", "tenant", "self"}, e.Scope, "未知 scope: %s", e.FullMethod)
	}
	// 稳定排序（按 FullMethod 升序）。
	require.True(t, sort.SliceIsSorted(entries, func(i, j int) bool { return entries[i].FullMethod < entries[j].FullMethod }))
}
```

- [ ] **步骤 2：运行确认失败**

运行：`go test ./internal/controlplane/mgmt/ -run TestRuleEntries -v`
预期：FAIL（`RuleEntries`/`RPCDoc` 未定义）。

- [ ] **步骤 3：实现 `mgmt/apidoc.go`**

```go
package mgmt

import "sort"

// RPCDoc 是一条 admin RPC 的只读文档视图（从权威 ruleTable 派生，不暴露内部 rpcRule）。
// 文档面与授权面同源——改一条 RPC 授权要素，文档自动跟随，不漂移。
type RPCDoc struct {
	FullMethod string
	Resource   string
	Action     string
	Scope      string // "system" | "app" | "tenant" | "self"
	IsWrite    bool
}

func scopeName(s ruleScope) string {
	switch s {
	case scopeSystem:
		return "system"
	case scopeApp:
		return "app"
	case scopeTenant:
		return "tenant"
	case scopeSelf:
		return "self"
	default:
		return "unknown"
	}
}

// RuleEntries 返回全部 admin RPC 的授权文档视图，按 FullMethod 升序稳定排序。
// 纯只读派生自 ruleTable（授权唯一真相），不修改任何授权状态。
func RuleEntries() []RPCDoc {
	out := make([]RPCDoc, 0, len(ruleTable))
	for fm, r := range ruleTable {
		out = append(out, RPCDoc{FullMethod: fm, Resource: r.resource, Action: r.action, Scope: scopeName(r.scope), IsWrite: r.isWrite})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FullMethod < out[j].FullMethod })
	return out
}
```

- [ ] **步骤 4：运行确认通过**

运行：`go test ./internal/controlplane/mgmt/ -run TestRuleEntries -v`
预期：PASS。

- [ ] **步骤 5：写失败测试 `restgw/apidoc_test.go`**

```go
package restgw

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRoutes_CoversAllRoutes(t *testing.T) {
	docs := Routes()
	all := allRoutes()
	require.Len(t, docs, len(all))
	// 每条都有 method/pattern/fullMethod。
	for _, d := range docs {
		require.NotEmpty(t, d.Method)
		require.NotEmpty(t, d.Pattern)
		require.NotEmpty(t, d.FullMethod)
	}
	// 稳定排序（FullMethod 升序，同 method 再按 pattern）。
	require.True(t, sort.SliceIsSorted(docs, func(i, j int) bool {
		if docs[i].FullMethod != docs[j].FullMethod {
			return docs[i].FullMethod < docs[j].FullMethod
		}
		return docs[i].Pattern < docs[j].Pattern
	}))
}
```

- [ ] **步骤 6：运行确认失败**

运行：`go test ./internal/controlplane/restgw/ -run TestRoutes -v`
预期：FAIL（`Routes`/`RouteDoc` 未定义）。

- [ ] **步骤 7：实现 `restgw/apidoc.go`**

```go
package restgw

import "sort"

// RouteDoc 是一条 REST 路由的只读文档视图（从 allRoutes 派生，不暴露内部 route）。
type RouteDoc struct {
	Method     string // HTTP 动词
	Pattern    string // 路径模式
	FullMethod string // gRPC FullMethod（= ruleTable 键）
}

// Routes 返回全部 REST 网关路由的只读文档视图，稳定排序。纯只读派生自 allRoutes()。
func Routes() []RouteDoc {
	all := allRoutes()
	out := make([]RouteDoc, 0, len(all))
	for _, rt := range all {
		out = append(out, RouteDoc{Method: rt.method, Pattern: rt.pattern, FullMethod: rt.fullMethod})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].FullMethod != out[j].FullMethod {
			return out[i].FullMethod < out[j].FullMethod
		}
		return out[i].Pattern < out[j].Pattern
	})
	return out
}
```

- [ ] **步骤 8：运行确认通过 + 零触碰核验 + gofmt**

运行：`go test ./internal/controlplane/restgw/ ./internal/controlplane/mgmt/ -run 'TestRoutes|TestRuleEntries' -v`（PASS）。
运行：`git diff internal/controlplane/mgmt/authz.go internal/controlplane/restgw/routes.go`
预期：**空**（`ruleTable`/`rpcRule`/`route`/`allRoutes` 一字未改，只新增了 apidoc.go 文件）。
运行：`gofmt -l internal/controlplane/mgmt/ internal/controlplane/restgw/`（空）。

- [ ] **步骤 9：Commit**

```bash
git add internal/controlplane/mgmt/apidoc.go internal/controlplane/mgmt/apidoc_test.go internal/controlplane/restgw/apidoc.go internal/controlplane/restgw/apidoc_test.go
git commit -m "feat(mgmt+restgw): M4.4 只读导出 API 文档访问器(RuleEntries 派生 ruleTable+Routes 派生 allRoutes,授权/route 内容零改,文档面同源不漂移)"
```

---

## 任务 2：Console 管理面 API 参考组装 + DP-2 零漂移有齿测试

**文件：**
- 创建：`internal/controlplane/console/apiref.go`、`internal/controlplane/console/apiref_test.go`

参考既有：`mgmt.RuleEntries()`/`mgmt.RPCDoc`（任务 1）、`restgw.Routes()`/`restgw.RouteDoc`（任务 1）。

- [ ] **步骤 1：写失败测试 `apiref_test.go`（DP-2 零漂移锚 + 反向验证）**

```go
package console

import (
	"testing"

	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/stretchr/testify/require"
)

// DP-2：ruleTable 每条 RPC 都必须出现在组装的管理面参考里（漏一条即 FAIL）。
func TestBuildAPIReference_CoversEveryAdminRPC(t *testing.T) {
	ref := buildAPIReference()
	byFM := map[string]APIRefEntry{}
	for _, e := range ref {
		byFM[e.FullMethod] = e
	}
	for _, r := range mgmt.RuleEntries() {
		e, ok := byFM[r.FullMethod]
		require.True(t, ok, "管理面参考漏了 admin RPC: %s（DP-2 零漂移失败）", r.FullMethod)
		require.Equal(t, r.Resource, e.Resource)
		require.Equal(t, r.Action, e.Action)
		require.Equal(t, r.Scope, e.Scope)
		require.Equal(t, r.IsWrite, e.IsWrite)
	}
	// 有 REST 路由的条目应带 method+path（抽查一条已知有 REST 的写 RPC）。
	upsertDP, ok := byFM["/sydom.admin.v1.AdminService/UpsertDataPolicy"]
	require.True(t, ok)
	require.NotEmpty(t, upsertDP.RESTPath, "UpsertDataPolicy 应有 REST 路由信息")
}
```

- [ ] **步骤 2：运行确认失败**

运行：`go test ./internal/controlplane/console/ -run TestBuildAPIReference -v`
预期：FAIL（`buildAPIReference`/`APIRefEntry` 未定义）。

- [ ] **步骤 3：实现 `apiref.go`**

```go
package console

import (
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/controlplane/restgw"
)

// APIRefEntry 是渲染用的一条管理面 API 端点：授权要素（来自 ruleTable）+ REST 路由（若有）。
type APIRefEntry struct {
	FullMethod string
	RESTMethod string // 空 = 仅 gRPC
	RESTPath   string
	Resource   string
	Action     string
	Scope      string
	IsWrite    bool
}

// buildAPIReference 以 ruleTable（授权唯一真相）为锚，join REST route 注册表。
// 锚定 ruleTable 保证每条 admin RPC 都出现（DP-2），REST 列对 gRPC-only RPC 为空。
func buildAPIReference() []APIRefEntry {
	restByFM := map[string]restgw.RouteDoc{}
	for _, rt := range restgw.Routes() {
		if _, exists := restByFM[rt.FullMethod]; !exists { // 同一 RPC 多路由取首条（稳定排序后确定）
			restByFM[rt.FullMethod] = rt
		}
	}
	rules := mgmt.RuleEntries() // 已按 FullMethod 稳定排序
	out := make([]APIRefEntry, 0, len(rules))
	for _, r := range rules {
		e := APIRefEntry{FullMethod: r.FullMethod, Resource: r.Resource, Action: r.Action, Scope: r.Scope, IsWrite: r.IsWrite}
		if rt, ok := restByFM[r.FullMethod]; ok {
			e.RESTMethod, e.RESTPath = rt.Method, rt.Pattern
		}
		out = append(out, e)
	}
	return out
}
```

- [ ] **步骤 4：运行确认通过**

运行：`go test ./internal/controlplane/console/ -run TestBuildAPIReference -v`
预期：PASS。

- [ ] **步骤 5：反向验证（证明零漂移测试有齿，呼应 M2.4/M4.3 教训）**

临时在 `buildAPIReference` 的循环里 `if r.FullMethod == "/sydom.admin.v1.AdminService/CreateRole" { continue }`（人为漏一条）→ 重跑 `TestBuildAPIReference_CoversEveryAdminRPC` → 确认 **FAIL**（"管理面参考漏了 admin RPC: …CreateRole"）；删除该行恢复 → 重跑 PASS。**汇报贴 FAIL→PASS 证据；不提交漏条版**。

- [ ] **步骤 6：gofmt + Commit**

运行：`gofmt -l internal/controlplane/console/`（空）。
```bash
git add internal/controlplane/console/apiref.go internal/controlplane/console/apiref_test.go
git commit -m "feat(console): M4.4 管理面 API 参考组装(锚定 ruleTable join restgw route)+DP-2 零漂移有齿测试(每 admin RPC 必现,反向验证)"
```

---

## 任务 3：Console `/developer` handler + Quickstart/概念/SDK 参考内容（数据面，主）

**文件：**
- 创建：`internal/controlplane/console/routes_developer.go`、`internal/controlplane/console/routes_developer_test.go`
- 创建：`internal/controlplane/console/templates/developer.html`
- 修改：`internal/controlplane/console/handler.go`（+`h.registerDeveloper(mux)`）

参考既有：`routes_policy_code.go`（`registerPolicyCode` + 只读 GET handler 范式）、`render.go` `renderPage(w,r,page,status,data)`、`auth.go` `requireSession`、`templates/policy_code.html`（读页模板范式，单 h1+breadcrumb+appnav）。SDK 真实签名（`sdk/go/sydom/client.go`）：`sydom.New(target string, opts ...Option) (*Client, error)`、`(*Client).Check(ctx, subject, object, action string) (bool, error)`、`(*Client).FilterSQL(ctx, subject, resource string, attrs map[string]any) (FilterResult, error)`；`examples/orderservice/main.go`（`sydom.New(sidecar)` + `defer client.Close()`）、`examples/orderservice/app/handler.go:42`（`s.gw.FilterSQL(r.Context(), user, "order", attrs)`）。

- [ ] **步骤 1：写失败测试 `routes_developer_test.go`**

镜像既有 Console 测试（`newConsole`/`loginAndCSRF`/`readBody`，见 `handler_test.go`）。覆盖：页 200 + 单 h1「开发者文档」+ 四块锚点 + quickstart 含真实 SDK 符号 + 不泄露 secret + 未登录挡。

```go
package console

import (
	"net/http"
	"strings"
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestConsole_DeveloperPage(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	resp, err := c.Get(ts.URL + "/apps/" + itoa(appID) + "/developer")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := readBody(t, resp)

	require.Equal(t, 1, strings.Count(body, "<h1"), "须单 h1")
	require.Contains(t, body, "开发者文档")
	// 四块锚点。
	for _, anchor := range []string{"quickstart", "concepts", "sdk", "api-reference"} {
		require.Contains(t, body, `id="`+anchor+`"`, "缺锚点 %s", anchor)
	}
	// DP-3：quickstart 含真实 SDK 符号。
	require.Contains(t, body, "sydom.New")
	require.Contains(t, body, ".Check(")
	// 管理面参考含已知 RPC。
	require.Contains(t, body, "UpsertDataPolicy")
	// DP-4：不泄露 secret（种子 app 无真实 secret 展示；断言无 secret 字面）。
	require.NotContains(t, body, "app_secret")
}

func TestConsole_DeveloperPage_RequiresSession(t *testing.T) {
	ts, _, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	anon := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := anon.Get(ts.URL + "/apps/" + itoa(appID) + "/developer")
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode) // requireSession 302 /login
}
```
> `itoa` 用既有 helper 或 `strconv.FormatInt(appID,10)`（appID 是 int64，以实际为准）。`newConsole`/`loginAndCSRF`/`readBody` 签名以 `handler_test.go` 为准。

- [ ] **步骤 2：运行确认失败**

运行：`go test ./internal/controlplane/console/ -run TestConsole_DeveloperPage -v`
预期：FAIL（路由未注册 → 404/非 200）。

- [ ] **步骤 3：实现 handler + 注册（`routes_developer.go`）**

```go
package console

import "net/http"

// registerDeveloper 注册开发者文档区（建模台只读 tab）。
func (h *Handler) registerDeveloper(mux *http.ServeMux) {
	mux.HandleFunc("GET /apps/{app_id}/developer", h.developer)
}

// developer 渲染开发者文档区（会话只读：quickstart+概念+SDK 参考 手写 + 管理面 API 参考自 ruleTable/route 派生）。
// 幂等只读——不写、不 bump、不写审计、无 CSRF；不渲染任何 app secret。
func (h *Handler) developer(w http.ResponseWriter, r *http.Request) {
	_, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	appID, err := pathUint64(r, "app_id")
	if err != nil {
		h.renderError(w, r, codeInvalidArgument, "非法 app_id", err)
		return
	}
	h.renderPage(w, r, "developer.html", http.StatusOK, map[string]any{
		"Nav": "apps", "AppID": appID, "Tab": "developer",
		"CSRF": sess.CSRF, "APIRef": buildAPIReference(),
	})
}
```
> `codeInvalidArgument` 用既有错误码常量（见 errors.go，以实际名为准，如 `codes.InvalidArgument`）。`pathUint64` 既有 helper。在 `handler.go` 的 NewHandler 里加 `h.registerDeveloper(mux)`（紧接 `h.registerPolicyCode(mux)` 后）。

- [ ] **步骤 4：实现模板 `templates/developer.html`（四块 section）**

镜像 `policy_code.html` 的外壳（`{{define "title"}}`/`{{define "content"}}`/`workspace`/`{{template "appnav" .}}`/breadcrumb/单 h1）。四块 `<section>` 带 `id` 锚点：

```html
{{define "title"}}开发者文档 · App {{.AppID}}{{end}}
{{define "content"}}<div class="workspace">{{template "appnav" .}}
<section>
<nav class="breadcrumb" aria-label="面包屑">建模台 · 开发者</nav>
<h1>开发者文档</h1>

<section id="quickstart" aria-labelledby="h-quickstart">
<h2 id="h-quickstart">Quickstart：在你的应用里加一次授权检查</h2>
<p>司域数据面通过本机 Sidecar（回环 gRPC）鉴权，域由 Sidecar 按所属 App pin。以下用公开 Go SDK 三步接入。</p>
<ol>
<li>跑 Sidecar（pin 到本 App，凭据经环境注入，<strong>不在代码里硬编码</strong>）。</li>
<li><code>go get github.com/nickZFZ/Sydom/sdk/go/sydom</code></li>
<li>连接 Sidecar 并做功能权限检查：</li>
</ol>
<pre><code>client, err := sydom.New("127.0.0.1:8090")
if err != nil { log.Fatal(err) }
defer client.Close()

allowed, err := client.Check(ctx, "alice", "order", "read")
if err != nil { /* fail-close：SDK 不可用时按拒处理 */ }
if !allowed { http.Error(w, "forbidden", 403); return }
</code></pre>
<p>数据权限（行级过滤）用 <code>FilterSQL</code> 取参数化片段（值在 args，绝不进 SQL 文本）：</p>
<pre><code>fr, err := client.FilterSQL(ctx, "alice", "order", map[string]any{"dept": "shanghai"})
// fr.SQL 例如 "dept = ?"；fr.Args 例如 ["shanghai"]；拼进你的 WHERE
</code></pre>
</section>

<section id="concepts" aria-labelledby="h-concepts">
<h2 id="h-concepts">核心概念</h2>
<p><strong>subject / object / action</strong>：一次功能权限判定的三元组。</p>
<p><strong>功能权限 vs 数据权限</strong>：前者「能否做某动作」（Check）；后者「能看哪些行」（FilterSQL 返回参数化 WHERE 片段）。</p>
<p><strong>Sidecar 回环与强隔离</strong>：业务进程只调本机 Sidecar，域由 Sidecar pin，不在请求体传递。</p>
<p><strong>fail-close</strong>：SDK/Sidecar 不可用或判定失败时按「拒」处理，绝不放行。</p>
</section>

<section id="sdk" aria-labelledby="h-sdk">
<h2 id="h-sdk">SDK 参考</h2>
<h3><code>sydom</code></h3>
<ul>
<li><code>New(target string, opts ...Option) (*Client, error)</code> — 连接 Sidecar。</li>
<li><code>(*Client).Check(ctx, subject, object, action string) (bool, error)</code> — 单条功能权限。</li>
<li><code>(*Client).BatchCheck(ctx, reqs []CheckReq) ([]bool, error)</code> — 批量，等长同序。</li>
<li><code>(*Client).FilterSQL(ctx, subject, resource string, attrs map[string]any) (FilterResult, error)</code> — 数据权限参数化 SQL。</li>
<li><code>(*Client).ReportPermissions(ctx, perms []Permission) (ReportResult, error)</code> — 上报权限点目录（fail-soft）。</li>
</ul>
<h3><code>sydomhttp</code> / <code>sydomgorm</code> / <code>sydomsql</code></h3>
<p><code>sydomhttp</code> 提供 net/http 鉴权中间件；<code>sydomgorm</code>/<code>sydomsql</code> 把 <code>FilterSQL</code> 结果织入 GORM / database/sql 查询。</p>
</section>

<section id="api-reference" aria-labelledby="h-apiref">
<h2 id="h-apiref">管理面 API 参考</h2>
<p>以下端点由授权真相源自动派生，与实际鉴权面一致。字段级请求/响应见 <code>api/proto/sydom/admin/v1/admin.proto</code>。</p>
<table class="table"><thead><tr><th>gRPC 方法</th><th>REST</th><th>资源</th><th>动作</th><th>域</th><th>写</th></tr></thead><tbody>
{{range .APIRef}}<tr><td><code>{{.FullMethod}}</code></td><td>{{if .RESTPath}}<code>{{.RESTMethod}} {{.RESTPath}}</code>{{else}}<span class="text-muted">仅 gRPC</span>{{end}}</td><td>{{.Resource}}</td><td>{{.Action}}</td><td>{{.Scope}}</td><td>{{if .IsWrite}}✓{{end}}</td></tr>{{end}}
</tbody></table>
</section>

</section></div>{{end}}
```
> `html/template` 全自动转义。**无 `<script>`**。类名（`workspace`/`table`/`text-muted`）以既有设计系统为准；缺则任务 4 补 CSS。

- [ ] **步骤 5：运行确认通过**

运行：`go test ./internal/controlplane/console/ -run TestConsole_DeveloperPage -v`
预期：PASS。

- [ ] **步骤 6：gofmt + Commit**

运行：`gofmt -l internal/controlplane/console/`（空）。
```bash
git add internal/controlplane/console/routes_developer.go internal/controlplane/console/routes_developer_test.go internal/controlplane/console/templates/developer.html internal/controlplane/console/handler.go
git commit -m "feat(console): M4.4 /developer 文档区(数据面 quickstart+概念+SDK 参考手写取自真实 orderservice+管理面 API 参考自动派生渲染,会话只读不泄露 secret,无新 JS)"
```

---

## 任务 4：`_appnav` 开发者入口 + 样式 + 页整合 a11y

**文件：**
- 修改：`internal/controlplane/console/templates/_appnav.html`（+「开发者」tab）
- 可能修改：`internal/controlplane/console/static/css/components.css`（端点表/代码块样式，若既有类不足）

参考既有：`_appnav.html`（tab 范式 `<a href=… {{if eq .Tab "…"}}aria-current="page"{{end}}>`）、`static/css/tokens.css`/`components.css`（设计系统 token）。

- [ ] **步骤 1：加「开发者」tab（`_appnav.html`，放「策略即代码」后）**

在 `policy-code` 那行后加：
```html
<a href="/apps/{{.AppID}}/developer" {{if eq .Tab "developer"}}aria-current="page"{{end}}>开发者</a>
```

- [ ] **步骤 2：样式（`components.css`，仅在既有类不足时；复用 token）**

若端点表宽需横向滚动容器 + 代码块样式，加（复用 `--color-*`/`--space-*`/`--radius-*`/`--font-mono`，零硬编码色值）：
```css
.developer-doc pre { background: var(--color-code-bg, var(--color-surface)); border: 1px solid var(--color-border); border-radius: var(--radius-md); padding: var(--space-3); overflow-x: auto; font-family: var(--font-mono); }
.developer-doc .table-scroll { overflow-x: auto; }
```
> 若既有 `.table`/代码样式已够，跳过本步。`developer.html` 相应加 `class="developer-doc"` 包裹。

- [ ] **步骤 3：验证（Go 全包 + gofmt）**

运行：`go test ./internal/controlplane/console/...`
预期：全 PASS（含任务 2/3 测试）。
运行：`gofmt -l internal/controlplane/console/`（空）。

- [ ] **步骤 4：Commit**

```bash
git add internal/controlplane/console/templates/_appnav.html internal/controlplane/console/static/css/components.css internal/controlplane/console/templates/developer.html
git commit -m "feat(console): M4.4 _appnav 加开发者 tab + 文档区样式(复用设计系统 token,端点表横向滚动,无新 JS)"
```

---

## 任务 5：整体核验 DP-1..7 + 真实浏览器 axe 走查 + opus 评审 + FF

**文件：** 无代码改动（除走查涌现修复）；产出走查记录 `docs/superpowers/2026-07-05-m4-4-api-docs-portal-walkthrough.md`。

- [ ] **步骤 1：DP-7 零触碰核验**

运行：
```bash
BASE=$(git merge-base main HEAD)
git diff $BASE..HEAD -- casbin/ internal/controlplane/adminauthz/ internal/kernel/ internal/sidecar/ sdk/ | wc -l   # 期望 0
git diff $BASE..HEAD -- internal/controlplane/mgmt/authz.go internal/controlplane/restgw/routes.go | wc -l          # 期望 0（ruleTable/route 内容零改）
```
预期：授权核心/SDK/数据面 diff=0；`ruleTable`/route 内容零改（仅新增 apidoc.go）。

- [ ] **步骤 2：全量验证**

运行：
```bash
gofmt -l internal/                # 空
go vet ./...                      # 干净
go test ./...                     # 0 FAIL（含 mgmt/restgw/console 新测试 + DP-2 零漂移）
```

- [ ] **步骤 3：真实浏览器 axe 走查（DP a11y）**

复用 M4.2/M4.3 走查脚手架范式（build-tag `walkthrough` 复用 `newConsole`+`dbtest`、会话 TTL `time.Hour`、播种 app、URL 写文件）+ 系统 Chrome via Playwright MCP（`--prefer-offline @playwright/mcp@0.0.77`）+ axe-core 4.10.2（浏览器可达 jsdelivr、`<script src>` 注入）。走查 `/apps/1/developer`：① 页 axe 0 违规 + 单 h1「开发者文档」+ breadcrumb；② 四块 section 锚点可达、标题层级 h1>h2>h3 正确；③ 管理面端点表渲染、含真实 RPC；④ 页面不含 app secret；⑤ 键盘可 tab 到 appnav「开发者」+ 表格可读。记录到 walkthrough.md 并 commit。**走查纪律**：停后台按确切 PID；脚手架走查后删除未提交。

- [ ] **步骤 4：opus 整体评审**

派 opus（或控制者 inline）逐条核验 DP-1..7：单一真相源（参考派生 ruleTable/route）、DP-2 零漂移有齿（反向验证）、DP-3 quickstart 真实 SDK 符号、DP-4 只读不泄露 secret、DP-7 授权/SDK/数据面零触碰（diff 证明）、无新 JS。产出 READY 或阻断清单。

- [ ] **步骤 5：更新记忆**

`project_detailed_design_progress.md` 加 M4.4 节；`MEMORY.md` M4 索引下 M4.4 标 ✅。

- [ ] **步骤 6：FF 并入本地 main + 问用户 push**

```bash
git -C /home/tongyu/codes/Sydom merge --ff-only worktree-feat+m4-4-api-docs-portal
# 核实 main==feature tip；push origin 与否问用户
```
清理 worktree（主 checkout：`git worktree remove`）。

---

## 自检（写完计划后，全新视角对照规格）

**1. 规格覆盖度：**
- §2 in-Console /developer 区 → 任务 3（handler+模板）+ 任务 4（appnav+样式）✅
- §2 防漂移核心（只读访问器）→ 任务 1 ✅
- §3.1 数据面 Quickstart → 任务 3（真实 orderservice 代码）✅；§3.2 概念 → 任务 3 ✅；§3.3 SDK 参考 → 任务 3 ✅；§3.4 管理面端点参考 → 任务 2（组装）+ 任务 3（渲染）✅
- §4 DP-1（单源）任务 1/2、DP-2（零漂移有齿+反向验证）任务 2、DP-3（quickstart 真实）任务 3、DP-4（只读无 secret）任务 3、DP-5（会话鉴权）任务 3、DP-6（无新 JS）任务 3/4、DP-7（零触碰 diff）任务 1/5 ✅
- §5 a11y → 任务 5 走查；§6 测试策略 → 各任务 TDD + 任务 5 ✅；§7 任务分解 → 5 任务 ✅

**2. 占位符扫描：** 无「待定/TODO」；每步含实际代码/命令/预期。`itoa`/`codeInvalidArgument`/helper 名标注「以现有为准」是刻意的（实现者对齐真实符号）。任务 3 模板内容为手写文档（HTML 静态），代码块取自真实 SDK 签名与 orderservice——非占位。

**3. 类型一致性：**
- `mgmt.RPCDoc{FullMethod,Resource,Action,Scope,IsWrite}` + `RuleEntries()`（任务 1）→ 任务 2 `buildAPIReference` 一致引用 ✅
- `restgw.RouteDoc{Method,Pattern,FullMethod}` + `Routes()`（任务 1）→ 任务 2 一致 ✅
- `APIRefEntry{FullMethod,RESTMethod,RESTPath,Resource,Action,Scope,IsWrite}`（任务 2）→ 任务 3 模板 `.APIRef` range 字段一致 ✅
- handler `developer` 传 `"APIRef": buildAPIReference()`（任务 3）→ 模板 `{{range .APIRef}}` ✅

对照无缺口。
