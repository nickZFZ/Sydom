# M6.1f 成员页内联成员配额提示 — 设计规格

**日期**：2026-07-14
**里程碑**：M6.1 计量+配额 · 计量可见对称 fast-follow
**前序**：M6.1d 成员配额、M6.1e dashboard 内联用量提示（`tenantUsageRows` helper）

## 目标

与 dashboard 应用配额提示对称——在成员页（`/tenants/{id}/members`，邀请成员的地方）显示「成员配额：X/Y」，把配额摆在邀请点。**fail-soft、决策无关、零触碰授权核心、纯消费 `GetTenantUsage`。**

## 非目标（YAGNI）

- members 页显示 app 配额（离语境；留 dashboard）
- members 页显示 meter（留用量页；此处紧凑文本 + 详情链接）
- per-app 角色/数据策略配额（需 per-app 语义决策）

## 架构与数据流

### DRY 重构（改进经手代码）

M6.1e 的 `tenantUsageRows`（返 `[]usageRow`）拆出更低层的共享 fail-soft 取值 `tenantUsage`，两页各取所需：

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

- **dashboard**（`routes_apps.go`，改调用点，输出不变）：
  ```go
  if tid != 0 {
      if u := h.tenantUsage(r.Context(), principal, tid); u != nil {
          data["UsageRows"] = []usageRow{makeUsageRow("应用", u.Applications), makeUsageRow("成员", u.Members)}
      }
  }
  ```
- **members**（`routes_accounts.go`，新）：
  ```go
  if u := h.tenantUsage(r.Context(), principal, tid); u != nil {
      data["MemberUsage"] = makeUsageRow("成员", u.Members)
  }
  ```

两处共享同一 fail-soft 取值 + `makeUsageRow`（DRY）。`tenantUsageRows`（M6.1e）被 `tenantUsage` 取代；dashboard 渲染逐字不变（现有测试不动）。

## 模板 `members.html`（邀请表单处）

`<h2>邀请成员</h2>` 之后、邀请 `<form>` 之前加：

```html
{{if .MemberUsage}}<p class="hint">成员配额：{{.MemberUsage.Used}}/{{.MemberUsage.Limit}}{{if .MemberUsage.AtLimit}}（已达上限）{{end}} · <a href="/tenants/{{.TenantID}}/usage">详情</a></p>{{end}}
```

`.MemberUsage` 仅在 `u != nil` 时设入 data：缺键→nil→`{{if}}` 假；设入的 `usageRow` 结构值→`{{if}}` 真（Go template 结构值非空为真）。只示成员维（app 配额在此页离语境）。

## 测试（TDD）

- **提示渲染有齿**（`routes_accounts_test.go` 或新测试文件）：`SeedAppInTenant` → root `GET /tenants/{tid}/members` → 断言 body 含「成员配额：0/3」（SeedAppInTenant 租户 0 成员、free 限 3）+ `href="/tenants/{tid}/usage"`（详情链接钉死）+ 单 h1（复用 pagesweep 已覆盖，不重复）。
- **dashboard 不回归**：`tenantUsage` 重构后既有 `TestDashboard_UsageHint`/`TestDashboard_SuperAdmin_ListsApps` 仍 PASS（dashboard 输出不变）。
- 至上限 `.AtLimit` 分支复用 dashboard 已证的同模板逻辑，不重复 members 专测。

## 不变量

- **零触碰授权核心**：casbin/kernel/adminauthz 求值/`mgmt/authz.go` ruleTable 零改
- `GetTenantUsage` proto/handler/store 零改（纯消费）
- 改动仅：`routes_apps.go`（`tenantUsageRows`→`tenantUsage` 重构 + dashboard 调用点）、`routes_accounts.go`（members 接线）、`templates/members.html`、测试
- `go test ./...` EXIT 0

## 验收（M61F-1..6）

1. 零触碰授权核心（机器 diff 空）
2. `tenantUsage` 共享 fail-soft 取值（任何 err→nil）
3. dashboard 用 `tenantUsage` 组两行（输出不变，测试不回归）
4. members 页 `data["MemberUsage"]` 仅 u!=nil 时设；模板 `{{if .MemberUsage}}` 提示
5. members 提示渲染有齿（成员配额：0/3 + 详情链接钉死）
6. `go test ./...` EXIT 0；既有 dashboard/pagesweep/templates_lint 不回归
