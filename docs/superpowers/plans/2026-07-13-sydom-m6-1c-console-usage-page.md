# M6.1c Console 用量页 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 在业务向运营台（Console）加一个独立用量页 `GET /tenants/{tenant_id}/usage`，消费既有 `GetTenantUsage` RPC，让租户管理员看见自己的套餐与应用配额用量。

**架构：** 纯读 BFF 页，复用 Console 既有管线（requireSession → AuthorizeRule(scopeTenant) → h.srv.GetTenantUsage → renderPage）。可视化用原生 `<meter>`（值经 HTML 属性驱动，满足严格 CSP 无内联 style）。零触碰授权核心；`GetTenantUsage` proto/handler/store 零改（纯消费第四面）。

**技术栈：** Go net/http（方法感知 ServeMux）、html/template、testify、真实 Postgres（dbtest）。

规格：`docs/superpowers/specs/2026-07-13-sydom-m6-1c-console-usage-page-design.md`

---

## 文件结构

- **创建** `internal/controlplane/console/routes_usage.go` — `registerUsage` + `h.usage` handler（单一职责：租户用量页）
- **创建** `internal/controlplane/console/routes_usage_test.go` — 用量页测试（渲染有齿 / 至上限告警 / 需会话 / 未知租户 404）
- **创建** `internal/controlplane/console/templates/usage.html` — 用量页模板（套餐徽章 + meter + at-limit 告警）
- **修改** `internal/controlplane/console/handler.go:34` — 接线 `h.registerUsage(mux)`
- **修改** `internal/controlplane/console/bizterm.go` — 加 `planLabel`（业务语言翻译层）
- **修改** `internal/controlplane/console/bizterm_test.go` — `planLabel` 单测
- **修改** `internal/controlplane/console/static/css/components.css` — 加 `.usage-meter`（additive，CSP 安全外部样式）
- **修改** `internal/controlplane/console/templates/tenants.html` — 「我的租户」每条加「用量」链接（可发现性）
- **修改** `internal/controlplane/console/pagesweep_test.go` — System 横扫加 `/tenants/{tid}/usage`

---

### 任务 1：planLabel 业务语言翻译（纯函数，TDD）

**文件：**
- 修改：`internal/controlplane/console/bizterm.go`
- 测试：`internal/controlplane/console/bizterm_test.go`

- [ ] **步骤 1：编写失败的测试**

在 `bizterm_test.go` 末尾追加：

```go
func TestPlanLabel(t *testing.T) {
	cases := map[string]string{
		"free": "免费版",
		"pro":  "专业版",
		"":     "",          // 空原样
		"team": "team",      // 未知原样（绝不臆造，与 actionLabel/roleName 回退范式一致）
	}
	for in, want := range cases {
		if got := planLabel(in); got != want {
			t.Errorf("planLabel(%q)=%q，want %q", in, got, want)
		}
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/controlplane/console/ -run TestPlanLabel -v`
预期：FAIL，`undefined: planLabel`

- [ ] **步骤 3：编写最少实现代码**

在 `bizterm.go` 末尾追加：

```go
// planName 是套餐技术名 → 中文业务名词表。
var planName = map[string]string{
	"free": "免费版",
	"pro":  "专业版",
}

// planLabel 返回套餐的中文业务名；未在词表中则原样返回（不臆造，
// 与 actionLabel/roleName 的回退范式一致）。
func planLabel(name string) string {
	if v, ok := planName[name]; ok {
		return v
	}
	return name
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/controlplane/console/ -run TestPlanLabel -v`
预期：PASS

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/console/bizterm.go internal/controlplane/console/bizterm_test.go
git commit -m "feat(cp): M6.1c bizterm planLabel(free→免费版/pro→专业版,未知原样回退)"
```

---

### 任务 2：用量页 handler + 模板 + CSS + 接线（TDD）

**文件：**
- 创建：`internal/controlplane/console/routes_usage.go`
- 创建：`internal/controlplane/console/templates/usage.html`
- 创建：`internal/controlplane/console/routes_usage_test.go`
- 修改：`internal/controlplane/console/handler.go:34`
- 修改：`internal/controlplane/console/static/css/components.css`

- [ ] **步骤 1：编写失败的测试**

创建 `internal/controlplane/console/routes_usage_test.go`：

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

// 渲染有齿：free 套餐/used=1/limit=3，钉死可见数字 + meter 属性 + 未达上限不含告警（双向）。
func TestConsole_UsagePage(t *testing.T) {
	ts, store, db := newConsole(t)
	tid, _ := dbtest.SeedAppInTenant(t, db, "usage-acme", "usage-app", "AK_usage")
	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	resp, err := c.Get(ts.URL + "/tenants/" + strconv.FormatInt(tid, 10) + "/usage")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := readBody(t, resp)

	require.Equal(t, 1, strings.Count(body, "<h1>"), "须单 h1")
	require.Contains(t, body, "免费版")        // planLabel(free)
	require.Contains(t, body, "1 / 3")        // used=1 limit=3（free 种子，有齿）
	require.Contains(t, body, "<meter")       // 原生 meter（CSP 安全可视化）
	require.Contains(t, body, `value="1"`)    // 有齿：钉死当前用量
	require.Contains(t, body, `max="3"`)      // 有齿：钉死套餐上限
	require.NotContains(t, body, "已达应用上限") // 未达上限：不含告警（双向有齿）
}

// 至上限告警有齿：插满 free 上限（3）→ 出现 at-limit 告警 + value="3"。
func TestConsole_UsagePage_AtLimitWarning(t *testing.T) {
	ts, store, db := newConsole(t)
	tid, _ := dbtest.SeedAppInTenant(t, db, "usage-full", "usage-full-1", "AK_full1")
	// 再插 2 应用达 free 上限 3（distinct domain 避 uq_tenant_domain；app_key 全局唯一）。
	_, err := db.Exec(`INSERT INTO application (tenant_id, domain, name, app_key, app_secret_enc)
	  VALUES ($1,'usage-full-2','app2','AK_full2','\xab'::bytea),
	         ($1,'usage-full-3','app3','AK_full3','\xab'::bytea)`, tid)
	require.NoError(t, err)
	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	resp, err := c.Get(ts.URL + "/tenants/" + strconv.FormatInt(tid, 10) + "/usage")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := readBody(t, resp)

	require.Contains(t, body, "3 / 3")
	require.Contains(t, body, `value="3"`)
	require.Contains(t, body, "已达应用上限") // 至上限告警（有齿）
}

func TestConsole_UsagePage_RequiresSession(t *testing.T) {
	ts, _, db := newConsole(t)
	tid, _ := dbtest.SeedAppInTenant(t, db, "usage-anon", "usage-anon-app", "AK_anon")
	anon := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := anon.Get(ts.URL + "/tenants/" + strconv.FormatInt(tid, 10) + "/usage")
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode) // requireSession 302 /login
}

func TestConsole_UsagePage_UnknownTenant(t *testing.T) {
	ts, store, _ := newConsole(t)
	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	resp, err := c.Get(ts.URL + "/tenants/999999/usage")
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, resp.StatusCode) // 未知租户 → GetTenantUsage NotFound 404
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/controlplane/console/ -run TestConsole_UsagePage -v`
预期：编译失败（`h.usage`/`registerUsage` 未定义）或 404（路由未注册）

- [ ] **步骤 3：编写 handler + 接线**

创建 `internal/controlplane/console/routes_usage.go`：

```go
package console

import (
	"net/http"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
)

// registerUsage 注册租户用量页（M6.1c 计量可见）。
func (h *Handler) registerUsage(mux *http.ServeMux) {
	mux.HandleFunc("GET /tenants/{tenant_id}/usage", h.usage)
}

// usage 渲染租户套餐 + 应用配额用量页（纯读，消费 GetTenantUsage 第四面）。
// 授权经 ruleTable(scopeTenant)：租户看自己、root 看全、跨租户 PermissionDenied(403)；
// 未知租户 NotFound(404)。幂等只读——零 bump、零写、零审计、无 CSRF。
func (h *Handler) usage(w http.ResponseWriter, r *http.Request) {
	principal, _, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	tid, err := pathUint64(r, "tenant_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"GetTenantUsage", err)
		return
	}
	msg := &adminv1.GetTenantUsageRequest{TenantId: tid}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"GetTenantUsage", principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"GetTenantUsage", err)
		return
	}
	resp, err := h.srv.GetTenantUsage(ctx, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"GetTenantUsage", err)
		return
	}
	used, limit := 0, 0
	if resp.Applications != nil { // 本 in-process 服务恒设，防御性 nil 守卫
		used = int(resp.Applications.Used)
		limit = int(resp.Applications.Limit)
	}
	h.renderPage(w, r, "usage.html", http.StatusOK, map[string]any{
		"Nav":       "tenants",
		"TenantID":  tid,
		"PlanLabel": planLabel(resp.PlanName),
		"Used":      used,
		"Limit":     limit,
		"AtLimit":   used >= limit,
		"ShowMeter": limit > 0, // max="0" 无效（须 > min）；limit 为 0 时仅文本
	})
}
```

在 `handler.go` 第 34 行（`h.registerAccounts(mux)` 之后）加一行接线：

```go
	h.registerAccounts(mux)        // M1.2 账户层：注册/我的租户/成员
	h.registerUsage(mux)           // M6.1c 租户用量页（消费 GetTenantUsage）
```

- [ ] **步骤 4：创建模板**

创建 `internal/controlplane/console/templates/usage.html`：

```html
{{define "title"}}用量 · 司域 Console{{end}}
{{define "content"}}
<nav class="breadcrumb" aria-label="面包屑">租户 · 用量</nav>
<h1>用量</h1>
<p class="hint">当前租户 {{.TenantID}}</p>
<p>套餐：<span class="badge">{{.PlanLabel}}</span></p>
<section aria-label="应用配额">
  <p>应用：{{.Used}} / {{.Limit}}</p>
  {{if .ShowMeter}}<meter class="usage-meter" min="0" max="{{.Limit}}" value="{{.Used}}">{{.Used}} / {{.Limit}}</meter>{{end}}
</section>
{{if .AtLimit}}<div class="alert alert-error">已达应用上限。升级套餐或删除闲置应用后可再创建。</div>{{end}}
{{end}}
```

- [ ] **步骤 5：加 CSS**

在 `static/css/components.css` 末尾追加：

```css
/* M6.1c 用量 meter：配额用量条（值经 value/max 属性驱动，无内联 style，满足严格 CSP） */
.usage-meter { width: 20rem; max-width: 100%; height: 0.9rem; vertical-align: middle; }
```

- [ ] **步骤 6：运行测试验证通过**

运行：`go test ./internal/controlplane/console/ -run TestConsole_UsagePage -v`
预期：4 个用例全 PASS（渲染有齿 / 至上限告警 / 需会话 302 / 未知租户 404）

- [ ] **步骤 7：Commit**

```bash
git add internal/controlplane/console/routes_usage.go internal/controlplane/console/routes_usage_test.go \
        internal/controlplane/console/templates/usage.html internal/controlplane/console/handler.go \
        internal/controlplane/console/static/css/components.css
git commit -m "feat(cp): M6.1c Console 用量页 GET /tenants/{id}/usage(消费 GetTenantUsage;原生 meter CSP 安全;at-limit 告警;scopeTenant 授权 unknown→404;渲染/告警双向有齿)"
```

---

### 任务 3：可发现性链接 + 横扫接入 + 整合验证

**文件：**
- 修改：`internal/controlplane/console/templates/tenants.html`
- 修改：`internal/controlplane/console/pagesweep_test.go:31-39`

- [ ] **步骤 1：加「我的租户」用量链接**

在 `tenants.html` 把 membership 行改为（在「成员」后插「用量」，镜像既有链接样式）：

```html
{{range .Memberships}}
<li>{{.TenantName}}（tier {{.Tier}}）—
  <a href="/tenants/{{.TenantId}}/members">成员</a> ·
  <a href="/tenants/{{.TenantId}}/usage">用量</a> ·
  <a href="/?tenant_id={{.TenantId}}">应用</a></li>
{{else}}
<li>暂无租户，<a href="/register">注册一个</a></li>
{{end}}
```

- [ ] **步骤 2：把用量页纳入 System 横扫**

在 `pagesweep_test.go` 的 `TestPageSweep_System` 路径列表里，`/members` 之后加一行（`tid` 已由该测试 `SeedAppInTenant` 播种，root 经 scopeTenant 可看任意租户用量）：

```go
		"/tenants/" + strconv.FormatInt(tid, 10) + "/members",
		"/tenants/" + strconv.FormatInt(tid, 10) + "/usage",
	} {
		assertSweptPage(t, c, ts.URL+p, true)
	}
```

- [ ] **步骤 3：运行相关测试验证通过**

运行：`go test ./internal/controlplane/console/ -run 'TestPageSweep|TestTemplates_NoInlineStyle|TestConsole_UsagePage|TestPlanLabel' -v`
预期：全 PASS（横扫纳入用量页单 h1+breadcrumb；templates_lint 自动扫到 usage.html 无内联 style/script）

- [ ] **步骤 4：全量测试**

运行：`go test ./...`
预期：EXIT 0（全绿）

- [ ] **步骤 5：验证零触碰授权核心**

运行：
```bash
git diff --stat d0d23b5..HEAD -- ':(exclude)docs' | cat
```
预期：改动仅落在 `internal/controlplane/console/`（含 templates/static）；**无** `casbin/`、`internal/kernel/`、`internal/sidecar/`、`internal/controlplane/adminauthz/`、`internal/controlplane/mgmt/authz.go`、`internal/controlplane/mgmt/tenant_usage.go`、`internal/controlplane/store/quota.go`、`gen/`、`api/` 的任何改动（GetTenantUsage proto/handler/store 零改，纯消费）。

- [ ] **步骤 6：Commit**

```bash
git add internal/controlplane/console/templates/tenants.html internal/controlplane/console/pagesweep_test.go
git commit -m "feat(cp): M6.1c 我的租户列表加用量链接 + pagesweep 纳入 /tenants/{id}/usage(单 h1+breadcrumb 横扫覆盖)"
```

---

## 验收对照（M61C-1..7）

| # | 验收项 | 覆盖任务 |
|---|---|---|
| 1 | 零触碰授权核心（机器 diff 空） | 任务 3 步骤 5 |
| 2 | handler：requireSession + AuthorizeRule(scopeTenant) + GetTenantUsage + renderPage | 任务 2 步骤 3 |
| 3 | usage.html：单 h1、meter 无内联 style、AtLimit 告警分支 | 任务 2 步骤 4；templates_lint（任务 3 步骤 3） |
| 4 | 渲染有齿（钉死 1/3 + 免费版 + meter value/max） | 任务 2 `TestConsole_UsagePage` |
| 5 | 至上限告警双向有齿 | 任务 2 `TestConsole_UsagePage`（NotContains）+ `_AtLimitWarning`（Contains） |
| 6 | 需会话 302 + 未知租户 404 | 任务 2 `_RequiresSession` / `_UnknownTenant` |
| 7 | `go test ./...` EXIT 0；pagesweep/templates_lint 纳入新模板 | 任务 3 步骤 3-4 |

## 自检

**1. 规格覆盖度：** 规格全部章节均有任务落地——放置决策（任务 2 路由）、数据流（任务 2 handler）、bizterm planLabel（任务 1）、模板 meter+告警（任务 2）、CSS（任务 2）、四项测试（任务 2）、pagesweep/templates_lint 纳入（任务 3）、可发现性链接（任务 3）、零触碰验证（任务 3 步骤 5）。无遗漏。

**2. 占位符扫描：** 无 TODO/待定；每个代码步骤含完整可编译代码。

**3. 类型一致性：** `planLabel`（任务 1 定义）在任务 2 handler 调用；`h.usage`/`registerUsage`（任务 2 定义）在 handler.go 接线；视图模型键（PlanLabel/Used/Limit/AtLimit/ShowMeter/TenantID）与 usage.html 引用一致；`GetTenantUsageRequest{TenantId}`、`resp.PlanName`、`resp.Applications.{Used,Limit}` 与既有 gen 类型一致（M6.1b 已生成）。`svc`/`pathUint64`/`requireSession`/`renderGRPCError`/`renderPage`/`AuthorizeRule` 签名均已核实。
