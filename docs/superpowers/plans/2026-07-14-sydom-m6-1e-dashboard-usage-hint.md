# M6.1e dashboard 内联用量提示 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 在应用列表页（dashboard）把配额用量（应用+成员）以紧凑文本显示在创建点，消费既有 `GetTenantUsage`，fail-soft，链到用量页。

**架构：** dashboard handler 非降级分支（tid!=0）fail-soft 调 `tenantUsageRows`（复用 M6.1d `usageRow`/`makeUsageRow`）；降级/超管 tid=0 跳过；模板加一行 `{{if .UsageRows}}` 提示。零触碰授权核心；`GetTenantUsage` 纯消费。

**技术栈：** Go net/http、html/template、testify、dbtest。

规格：`docs/superpowers/specs/2026-07-14-sydom-m6-1e-dashboard-usage-hint-design.md`

---

## 文件结构

- **修改** `internal/controlplane/console/routes_apps.go` — 加 `tenantUsageRows` 助手 + dashboard 非降级分支接线（零新增 import：`context`/`adminv1`/`mgmt`/`svc` 均已在）
- **修改** `internal/controlplane/console/templates/dashboard.html` — 非降级 `{{else}}` 分支加 `{{if .UsageRows}}` 提示行
- **修改** `internal/controlplane/console/handler_test.go` — 加 `TestDashboard_UsageHint` + 既有 `TestDashboard_SuperAdmin_ListsApps` 补 NotContains 断言（零新增 import：`fmt`/`dbtest`/`http`/`require` 均已在）

---

### 任务 1：dashboard 内联用量提示（helper + handler + 模板 + 测试）

**文件：**
- 修改：`internal/controlplane/console/routes_apps.go`
- 修改：`internal/controlplane/console/templates/dashboard.html`
- 测试：`internal/controlplane/console/handler_test.go`

- [ ] **步骤 1：写失败的测试**

在 `handler_test.go` 的 `TestDashboard_SuperAdmin_ListsApps` 之后加新测试，并给 `TestDashboard_SuperAdmin_ListsApps` 补一条 NotContains 断言。

新测试：

```go
// dashboard 内联用量提示（M6.1e）：root + ?tenant_id 触发（root scopeTenant 看任意租户用量）。
func TestDashboard_UsageHint(t *testing.T) {
	ts, _, db := newConsole(t)
	tid, _ := dbtest.SeedAppInTenant(t, db, "dash-usage", "dash-app", "AK_dash")
	c := loginClient(t, ts, "root@sydom", "rootsecret")
	resp, err := c.Get(ts.URL + fmt.Sprintf("/?tenant_id=%d", tid))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := readBody(t, resp)
	require.Contains(t, body, "配额：")
	require.Contains(t, body, "应用 1/3") // SeedAppInTenant：1 应用、free 限 3
	require.Contains(t, body, "成员 0/3") // 无 membership、free 成员限 3
	require.Contains(t, body, fmt.Sprintf(`href="/tenants/%d/usage"`, tid), "详情链接钉死")
}
```

给 `TestDashboard_SuperAdmin_ListsApps` 的 `require.Contains(t, body, "应用")` 之后加：

```go
	require.NotContains(t, body, "配额：") // 超管全量(tid=0)：fail-soft 跳过用量提示（双向有齿）
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/console/ -run 'TestDashboard_UsageHint|TestDashboard_SuperAdmin_ListsApps' -v`
预期：`TestDashboard_UsageHint` FAIL（body 不含「配额：」——尚未实现）

- [ ] **步骤 3：加 helper + handler 接线**

`routes_apps.go` 里，`dashboard` 函数之后（或之前）加助手：

```go
// tenantUsageRows 为 dashboard 内联提示取租户用量行；任何错误返回 nil（fail-soft，绝不破坏页面）。
// 因 ListApplications(tid) 已过同 application:read/scopeTenant 授权，正常路径 GetTenantUsage 必然也过。
func (h *Handler) tenantUsageRows(ctx context.Context, principal string, tid uint64) []usageRow {
	msg := &adminv1.GetTenantUsageRequest{TenantId: tid}
	authCtx, err := mgmt.AuthorizeRule(ctx, h.enf, svc+"GetTenantUsage", principal, msg)
	if err != nil {
		return nil
	}
	resp, err := h.srv.GetTenantUsage(authCtx, msg)
	if err != nil {
		return nil
	}
	return []usageRow{
		makeUsageRow("应用", resp.Applications),
		makeUsageRow("成员", resp.Members),
	}
}
```

把 `dashboard` 末尾的 renderPage（第 193-195 行）替换为构建 data + 条件加 UsageRows：

```go
	data := map[string]any{
		"Nav": "apps", "Degraded": false, "Apps": resp.Applications, "CSRF": sess.CSRF,
		"Tenants": mine.Memberships, "TenantID": tid, "Pager": pagerData(r, resp.Total)}
	if tid != 0 { // 超管全量(tid=0)无单一租户用量；仅具体租户上下文取用量（fail-soft）
		data["UsageRows"] = h.tenantUsageRows(r.Context(), principal, tid)
	}
	h.renderPage(w, r, "dashboard.html", http.StatusOK, data)
```

- [ ] **步骤 4：改模板**

`dashboard.html` 的非降级 `{{else}}` 分支，在「当前租户」hint 行与「+ 新建应用」按钮之间插入用量提示：

```html
{{else}}
<p class="hint"><a href="/tenants">我的租户</a>{{if .TenantID}} · 当前租户 {{.TenantID}}{{end}}</p>
{{if .UsageRows}}<p class="hint">配额：{{range .UsageRows}}{{.Name}} {{.Used}}/{{.Limit}}{{if .AtLimit}}（已达上限）{{end}} · {{end}}<a href="/tenants/{{.TenantID}}/usage">详情</a></p>{{end}}
<a class="btn btn-primary" href="/apps/new?tenant_id={{.TenantID}}">+ 新建应用</a>
```

- [ ] **步骤 5：运行验证通过**

运行：`go test ./internal/controlplane/console/ -run 'TestDashboard' -v`
预期：`TestDashboard_UsageHint`（提示渲染有齿）+ `TestDashboard_SuperAdmin_ListsApps`（tid=0 无提示）+ `TestDashboard_NoSession_RedirectsLogin` 全 PASS

- [ ] **步骤 6：Commit**

```bash
git add internal/controlplane/console/routes_apps.go internal/controlplane/console/templates/dashboard.html internal/controlplane/console/handler_test.go
git commit -m "feat(cp): M6.1e dashboard 内联用量提示(应用+成员配额显示在创建点;tenantUsageRows fail-soft 复用 makeUsageRow;降级/超管 tid=0 跳过;链到用量页;root ?tenant_id 有齿+超管全量不含提示双向)"
```

---

### 任务 2：全量验证 + 零触碰核验

**文件：** 无（仅验证）

- [ ] **步骤 1：Console 包 + 全量测试**

运行：`go test ./internal/controlplane/console/`
预期：ok（含 pagesweep/templates_lint 不回归）。

运行：`go test ./...`
预期：EXIT 0（全绿）。

- [ ] **步骤 2：零触碰授权核心核验**

运行：
```bash
git diff --name-only 03b1f89..HEAD | grep -E 'casbin/|internal/kernel/|internal/sidecar/|adminauthz/(enforcer|role_manager|dataperm)|mgmt/authz\.go|mgmt/tenant_usage\.go|store/quota\.go|^gen/|^api/' && echo "!!! TOUCHED" || echo "EMPTY ✓ 零触碰授权核心 + GetTenantUsage 纯消费"
```
（基线 `03b1f89` 为 M6.1d plan commit；更精确用本片起点 M6.1d 收官 `e7b63bb`：`git diff --name-only e7b63bb..HEAD`。）
预期：EMPTY ✓（改动仅 `routes_apps.go` + `dashboard.html` + `handler_test.go` + docs）。

- [ ] **步骤 3：确认改动面**

运行：`git diff --stat e7b63bb..HEAD -- ':(exclude)docs' | cat`
预期：仅 3 文件（routes_apps.go、dashboard.html、handler_test.go）。

- [ ] **步骤 4：无 commit（本任务仅验证）**

---

## 验收对照（M61E-1..6）

| # | 验收项 | 覆盖任务 |
|---|---|---|
| 1 | 零触碰授权核心（机器 diff 空） | 任务 2 步骤 2 |
| 2 | `tenantUsageRows` fail-soft（任何 err→nil） | 任务 1 步骤 3 |
| 3 | 非降级 tid!=0 设 UsageRows；降级/tid=0 不设 | 任务 1 步骤 3 |
| 4 | 模板紧凑提示（应用+成员+至上限标记+详情链接） | 任务 1 步骤 4 |
| 5 | 提示渲染有齿 + 超管全量不含提示（双向） | 任务 1 `TestDashboard_UsageHint` + `_SuperAdmin_ListsApps` |
| 6 | 既有 dashboard 测试不回归；`go test ./...` EXIT 0 | 任务 2 |

## 自检

**1. 规格覆盖度：** helper(任务1步3)、handler 接线(任务1步3)、模板(任务1步4)、两向测试(任务1步1)、降级/tid=0 保护(handler `if tid != 0` + 降级分支不动)、验证(任务2)。无遗漏。

**2. 占位符扫描：** 无 TODO；代码步骤含完整代码。

**3. 类型一致性：** `tenantUsageRows` 返 `[]usageRow`（M6.1d 定义）；`makeUsageRow`（M6.1d）复用；`data["UsageRows"]` 与模板 `.UsageRows`/`range` 的 `.Name/.Used/.Limit/.AtLimit` 一致；`svc`/`mgmt.AuthorizeRule`/`h.srv.GetTenantUsage`/`h.enf` 签名均已核实；`loginClient`/`dbtest.SeedAppInTenant`/`fmt`/`readBody` 测试助手均已在。
