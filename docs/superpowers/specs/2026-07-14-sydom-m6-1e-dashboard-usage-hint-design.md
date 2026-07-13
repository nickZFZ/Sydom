# M6.1e dashboard 内联用量提示 — 设计规格

**日期**：2026-07-14
**里程碑**：M6.1 计量+配额 · 计量可见 fast-follow
**前序**：M6.1c Console 用量页（`GET /tenants/{id}/usage`，资源行列表）、M6.1d 成员配额（`GetTenantUsage` 加 members）

## 目标

在应用列表页（dashboard）把配额用量（应用 + 成员）显示在用户实际创建资源的地方，完成 M6.1c 显式延后的 fast-follow。**消费既有 `GetTenantUsage`、fail-soft、决策无关、零触碰授权核心。**

## 非目标（YAGNI）

- dashboard 显示 meter（留用量页；此处只紧凑文本 + 链到详情）
- 成员页内联成员配额（对称的另一片，可后续）
- per-app 角色/数据策略配额（需 per-app 语义决策）

## 架构与数据流

`dashboard` handler（`internal/controlplane/console/routes_apps.go:152`）非降级分支（tid 已知、`ListApplications` 已成功、第 193-195 行 renderPage）末尾，加 fail-soft 用量取值：

```
if tid != 0 {
    rows := h.tenantUsageRows(r.Context(), principal, tid) // 任何 err → nil
    data["UsageRows"] = rows
}
```

新增小助手（同文件或 `routes_usage.go`，复用 M6.1d 的 `usageRow`/`makeUsageRow`，DRY）：

```go
// tenantUsageRows 为 dashboard 内联提示取租户用量行；任何错误返回 nil（fail-soft，绝不破坏页面）。
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

因 `ListApplications(tid)` 已过同 `application:read`/scopeTenant 授权，正常路径 `GetTenantUsage(tid)` 必然也过；fail-soft 仅兜底异常（不破坏 dashboard 渲染）。

### 保护的两条路径

- **降级分支**（`ListApplications` PermissionDenied，第 181-183 行）：逐字不动，不设 `UsageRows`（同 scope 也看不到用量）。
- **超管全量**（tid=0，超管无 membership 列全量）：跳过（无单一租户可示用量）。

## 模板 `dashboard.html`（非降级 `{{else}}` 分支）

在「+ 新建应用」按钮附近加一行紧凑提示（无 meter，复用 `.hint`）：

```html
{{if .UsageRows}}<p class="hint">配额：{{range .UsageRows}}{{.Name}} {{.Used}}/{{.Limit}}{{if .AtLimit}}（已达上限）{{end}} · {{end}}<a href="/tenants/{{.TenantID}}/usage">详情</a></p>{{end}}
```

渲染如 `配额：应用 1/3 · 成员 0/3 · 详情`；至上限 `应用 3/3（已达上限）`——把"为什么建不了"摆在创建点，链到用量页看 meter 全貌。

## 测试（TDD）

- **提示渲染有齿**（`handler_test.go` 或新 `routes_dashboard_usage_test.go`）：`SeedAppInTenant` → root `GET /?tenant_id={tid}` → 断言 body 含「配额：」「应用 1/3」「成员 0/3」，且含 `href="/tenants/{tid}/usage"`（详情链接钉死）。
- **超管全量不炸**：既有 `TestDashboard_SuperAdmin_ListsApps`（root `GET /`，tid=0）仍 PASS；补断言 body **不含**「配额：」（fail-soft 跳过，双向有齿）。
- 既有 dashboard 测试（`TestDashboard_SuperAdmin_ListsApps`/`TestDashboard_NoSession_RedirectsLogin`）不回归。

## 不变量

- **零触碰授权核心**：casbin/kernel/adminauthz 求值/`mgmt/authz.go` ruleTable 零改
- `GetTenantUsage` proto/handler/store 零改（纯消费）
- dashboard 降级路径逐字不动
- 改动仅：`routes_apps.go`（handler + `tenantUsageRows` 助手）、`templates/dashboard.html`、测试
- `go test ./...` EXIT 0

## 验收（M61E-1..6）

1. 零触碰授权核心（机器 diff 空）
2. `tenantUsageRows` 助手：AuthorizeRule + GetTenantUsage + makeUsageRow，任何 err → nil（fail-soft）
3. dashboard 非降级分支 tid!=0 时设 UsageRows；降级/tid=0 不设
4. 模板紧凑提示（应用+成员 + 至上限标记 + 详情链接）
5. 提示渲染有齿（root ?tenant_id= → 应用 1/3 · 成员 0/3 · 详情链接）+ 超管全量不含提示（双向）
6. 既有 dashboard 测试不回归；`go test ./...` EXIT 0
