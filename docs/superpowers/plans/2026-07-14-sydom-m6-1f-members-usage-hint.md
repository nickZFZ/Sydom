# M6.1f 成员页内联成员配额提示 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 在成员页（`/tenants/{id}/members`）邀请点显示「成员配额：X/Y」，与 dashboard 应用配额提示对称，fail-soft，链到用量页。

**架构：** 把 M6.1e 的 `tenantUsageRows` 重构为更低层 `tenantUsage`（共享 fail-soft 取值），dashboard 组两行（输出不变）、members 组成员行。模板 `{{if .MemberUsage}}` 提示。零触碰授权核心；`GetTenantUsage` 纯消费。

**技术栈：** Go net/http、html/template、testify、dbtest。

规格：`docs/superpowers/specs/2026-07-14-sydom-m6-1f-members-usage-hint-design.md`

---

## 文件结构

- **修改** `internal/controlplane/console/routes_apps.go` — `tenantUsageRows`→`tenantUsage` 重构 + dashboard 调用点
- **修改** `internal/controlplane/console/routes_accounts.go` — `membersList` 接线 MemberUsage
- **修改** `internal/controlplane/console/templates/members.html` — 邀请表单前加 `{{if .MemberUsage}}` 提示
- **修改** `internal/controlplane/console/routes_accounts_test.go` — 加 `TestConsole_Members_UsageHint`（+ imports `fmt`/`dbtest`）

---

### 任务 1：tenantUsage 重构 + 成员页提示（helper + handler + 模板 + 测试）

**文件：**
- 修改：`internal/controlplane/console/routes_apps.go`
- 修改：`internal/controlplane/console/routes_accounts.go`
- 修改：`internal/controlplane/console/templates/members.html`
- 测试：`internal/controlplane/console/routes_accounts_test.go`

- [ ] **步骤 1：写失败的测试**

`routes_accounts_test.go` import 块改为（加 `fmt`、`dbtest`）：

```go
import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)
```

在文件末尾加新测试：

```go
// 成员页内联成员配额提示（M6.1f）：root GET 成员页 → 「成员配额：0/3」+ 详情链接。
func TestConsole_Members_UsageHint(t *testing.T) {
	ts, _, db := newConsole(t)
	tid, _ := dbtest.SeedAppInTenant(t, db, "mem-usage", "mem-app", "AK_memusage")
	c := loginClient(t, ts, "root@sydom", "rootsecret")
	resp, err := c.Get(ts.URL + fmt.Sprintf("/tenants/%d/members", tid))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := readBody(t, resp)
	require.Contains(t, body, "成员配额：0/3") // SeedAppInTenant 0 成员、free 限 3
	require.Contains(t, body, fmt.Sprintf(`href="/tenants/%d/usage"`, tid), "详情链接钉死")
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/console/ -run TestConsole_Members_UsageHint -v`
预期：FAIL（body 不含「成员配额：」——尚未实现）

- [ ] **步骤 3：重构 tenantUsageRows→tenantUsage（routes_apps.go）**

把 `routes_apps.go` 里 `tenantUsageRows` 整个函数定义替换为：

```go
// tenantUsage 取租户用量（fail-soft，任何 err 返 nil，绝不破坏页面）。
// 因页面已过同 scopeTenant 授权（dashboard ListApplications / members ListMembers），正常路径 GetTenantUsage 必然也过。
func (h *Handler) tenantUsage(ctx context.Context, principal string, tid uint64) *adminv1.GetTenantUsageResponse {
	msg := &adminv1.GetTenantUsageRequest{TenantId: tid}
	authCtx, err := mgmt.AuthorizeRule(ctx, h.enf, svc+"GetTenantUsage", principal, msg)
	if err != nil {
		return nil
	}
	resp, err := h.srv.GetTenantUsage(authCtx, msg)
	if err != nil {
		return nil
	}
	return resp
}
```

把 dashboard 里的调用点：

```go
	if tid != 0 { // 超管全量(tid=0)无单一租户用量；仅具体租户上下文取用量提示（fail-soft）
		data["UsageRows"] = h.tenantUsageRows(r.Context(), principal, tid)
	}
```

替换为（输出不变，仍是应用+成员两行）：

```go
	if tid != 0 { // 超管全量(tid=0)无单一租户用量；仅具体租户上下文取用量提示（fail-soft）
		if u := h.tenantUsage(r.Context(), principal, tid); u != nil {
			data["UsageRows"] = []usageRow{makeUsageRow("应用", u.Applications), makeUsageRow("成员", u.Members)}
		}
	}
```

- [ ] **步骤 4：接线 members handler + 模板**

`routes_accounts.go` 的 `membersList` 末尾 renderPage 替换为：

```go
	data := map[string]any{
		"Nav": "tenants", "TenantID": tid, "Members": resp.Members, "CSRF": sess.CSRF,
		"Pager": pagerData(r, resp.Total)}
	if u := h.tenantUsage(r.Context(), principal, tid); u != nil {
		data["MemberUsage"] = makeUsageRow("成员", u.Members)
	}
	h.renderPage(w, r, "members.html", http.StatusOK, data)
```

`members.html` 的 `<h2>邀请成员</h2>` 之后、邀请 `<form>` 之前加：

```html
<h2>邀请成员</h2>
{{if .MemberUsage}}<p class="hint">成员配额：{{.MemberUsage.Used}}/{{.MemberUsage.Limit}}{{if .MemberUsage.AtLimit}}（已达上限）{{end}} · <a href="/tenants/{{.TenantID}}/usage">详情</a></p>{{end}}
<form method="post" action="/tenants/{{.TenantID}}/members" class="stacked-form">
```

- [ ] **步骤 5：运行验证通过**

运行：`go test ./internal/controlplane/console/ -run 'TestConsole_Members_UsageHint|TestDashboard|TestPageSweep|TestTemplates_NoInlineStyle' -v`
预期：全 PASS（members 提示有齿 + dashboard 输出不变不回归 + pagesweep/templates_lint 不回归）

- [ ] **步骤 6：Commit**

```bash
git add internal/controlplane/console/routes_apps.go internal/controlplane/console/routes_accounts.go \
        internal/controlplane/console/templates/members.html internal/controlplane/console/routes_accounts_test.go
git commit -m "feat(cp): M6.1f 成员页内联成员配额提示(与 dashboard 对称;tenantUsageRows→tenantUsage DRY 重构 dashboard 输出不变;members 只示成员维+详情链接;fail-soft;root GET 成员页有齿)"
```

---

### 任务 2：全量验证 + 零触碰核验

**文件：** 无（仅验证）

- [ ] **步骤 1：全量测试**

运行：`go test ./...`
预期：EXIT 0（全绿）。

- [ ] **步骤 2：零触碰授权核心核验**

运行：
```bash
git diff --name-only 596bb60..HEAD | grep -E 'casbin/|internal/kernel/|internal/sidecar/|adminauthz/(enforcer|role_manager|dataperm)|mgmt/authz\.go|mgmt/tenant_usage\.go|store/quota\.go|^gen/|^api/' && echo "!!! TOUCHED" || echo "EMPTY ✓ 零触碰授权核心 + GetTenantUsage 纯消费"
```
（基线 `596bb60` = M6.1e 收官。）
预期：EMPTY ✓（改动仅 `console/` + docs）。

- [ ] **步骤 3：确认改动面**

运行：`git diff --stat 596bb60..HEAD -- ':(exclude)docs' | cat`
预期：仅 4 文件（routes_apps.go、routes_accounts.go、members.html、routes_accounts_test.go）。

- [ ] **步骤 4：无 commit（本任务仅验证）**

---

## 验收对照（M61F-1..6）

| # | 验收项 | 覆盖任务 |
|---|---|---|
| 1 | 零触碰授权核心（机器 diff 空） | 任务 2 步骤 2 |
| 2 | `tenantUsage` 共享 fail-soft（任何 err→nil） | 任务 1 步骤 3 |
| 3 | dashboard 用 tenantUsage 组两行（输出不变不回归） | 任务 1 步骤 3 + `TestDashboard` |
| 4 | members `data["MemberUsage"]` 仅 u!=nil 设 + 模板 `{{if}}` | 任务 1 步骤 4 |
| 5 | members 提示渲染有齿（成员配额 0/3 + 详情链接钉死） | 任务 1 `TestConsole_Members_UsageHint` |
| 6 | `go test ./...` EXIT 0；dashboard/pagesweep/templates_lint 不回归 | 任务 1 步骤 5 + 任务 2 |

## 自检

**1. 规格覆盖度：** tenantUsage 重构(任务1步3)、dashboard 调用点(任务1步3)、members 接线(任务1步4)、模板(任务1步4)、测试(任务1步1)、验证(任务2)。无遗漏。

**2. 占位符扫描：** 无 TODO；代码步骤含完整代码。

**3. 类型一致性：** `tenantUsage` 返 `*adminv1.GetTenantUsageResponse`；`makeUsageRow`/`usageRow`（M6.1d/e）复用；`data["MemberUsage"]` 是 `usageRow` 结构值，模板 `.MemberUsage.Used/.Limit/.AtLimit` 一致；dashboard 仍用 `[]usageRow`+`.UsageRows`；`svc`/`mgmt.AuthorizeRule`/`h.srv.GetTenantUsage`/`h.enf`/`loginClient`/`dbtest.SeedAppInTenant`/`readBody` 均已核实存在。旧 `tenantUsageRows` 被 `tenantUsage` 完全取代，无遗留引用（dashboard 是唯一调用方，已改）。
