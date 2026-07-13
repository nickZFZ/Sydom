# M6.1c Console 用量页（消费 GetTenantUsage）— 设计规格

**日期**：2026-07-13
**里程碑**：M6.1 计量+配额 · 第三增量（UI 可见）
**前序**：M6.1a 应用配额强制（plan 表 + CreateApplication 行锁门）、M6.1b 计量 RPC `GetTenantUsage`（gRPC+REST，`scopeTenant` 授权，只读 `TenantUsageOf`）

## 目标

把 M6.1b 后端的「计量可见」落到业务向运营台（Console）：租户管理员能在 Web UI 里**看见**自己租户的套餐与应用配额用量。**纯读、决策无关、零触碰授权核心**——本片只消费既有 `GetTenantUsage`，不改 proto/handler/store。

## 非目标（YAGNI）

- dashboard 内联用量提示（后续易加的 fast-follow，本片不做——避免动 dashboard 脆弱的 PermissionDenied 降级分支）
- 租户列表逐行内联 meter（需 N 次 RPC，耦合高）
- 更多配额维度（角色/数据策略/成员）——需先扩 `plan` 表 + `GetTenantUsage` 返回多 `ResourceUsage`，是独立后续增量
- 套餐升级 / 计费入口——需产品/供应商决策（路线图 §6 明列留 brainstorm）

## 放置决策

采用**独立用量页** `GET /tenants/{tenant_id}/usage`：

- 与既有 `GET /tenants/{tenant_id}/members` 子资源同构，路由派生自然
- 自包含、低风险：新增一个路由 + 一个模板 + 「我的租户」列表一处链接；不触碰 dashboard
- 可扩展：未来更多配额维度都长在这一页

从「我的租户」列表（`tenants.html`）每条 membership 旁加「· 用量」链接进入。

否决 B（dashboard 内联）与 C（租户列表逐行 meter），理由见「非目标」。

## 架构与数据流

新增 `internal/controlplane/console/routes_usage.go`：

```
registerUsage(mux): mux.HandleFunc("GET /tenants/{tenant_id}/usage", h.usage)

h.usage(w, r):
  1. principal, sess, ok := requireSession(w, r)          // fail-close 302 /login
  2. tid := pathUint64(r, "tenant_id")                     // 解析失败 → renderGRPCError
  3. msg := &GetTenantUsageRequest{TenantId: tid}
  4. ctx := AuthorizeRule(ctx, enf, svc+"GetTenantUsage", principal, msg)  // scopeTenant
       err PermissionDenied → renderGRPCError（403，跨租户）
  5. resp := h.srv.GetTenantUsage(ctx, msg)
       err NotFound → renderGRPCError（404，未知租户）
  6. 薄视图模型：
       PlanLabel = planLabel(resp.PlanName)   // free→免费版 / pro→专业版 / 缺省原样
       Used  = resp.Applications.Used
       Limit = resp.Applications.Limit
       AtLimit = Used >= Limit
  7. renderPage(usage.html, {Nav:"tenants", TenantID, PlanLabel, Used, Limit, AtLimit})
```

授权、错误码、可见性与 gRPC/REST 三面一致——本 handler 只是第四个消费面（BFF），复用同一 `ruleTable` 条目 `{"application","read",false,scopeTenant}`。

### bizterm.go 增量

加 `planLabel(name string) string`：`{"free":"免费版","pro":"专业版"}`，缺省返回 `name` 自身（绝不臆造，与既有 `actionLabel`/`roleName` 回退范式一致）。放 bizterm.go 因其职责正是「技术原语→一致中文业务语言」。

## 模板 `usage.html`

- 单 `<h1>`（`templates_lint` + `pagesweep` 横扫强制）
- breadcrumb「租户 · 用量」，Nav=tenants
- 套餐：`<span class="badge">{{.PlanLabel}}</span>`
- 应用配额（文本为 AT 真相源 + 原生 meter 为渐进可视化）：
  - `应用：{{.Used}} / {{.Limit}}`
  - `{{if gt .Limit 0}}<meter class="usage-meter" min="0" max="{{.Limit}}" value="{{.Used}}">{{.Used}} / {{.Limit}}</meter>{{end}}`
  - meter 语义即「已知范围内的标量度量」，恰配配额；填充比例即 used/limit，AT 按 value/max 播报；**无内联样式**（值经 `value`/`max` 属性非 `style`，满足严格 CSP）
  - `Limit>0` 守卫：`<meter max="0">` 无效（max 须 > min），故 limit 为 0 时仅渲染文本不渲染 meter（防御性；现有套餐 free=3/pro=50 恒正）
  - 危险信号由下方 at-limit 红色告警承担，故 meter 不做三色分区（避免在 handler 算 low/high 阈值——YAGNI）
- 条件告警：`{{if .AtLimit}}<div class="alert alert-error">已达应用上限。升级套餐或删除闲置应用后可再创建。</div>{{end}}`

## CSS 增量（additive，CSP 安全）

`components.css` 加 `.usage-meter { width; height }`（外部样式表，非内联；填充色走浏览器原生度量着色）。不改任何既有规则。

## 严格 CSP 约束（M5.2a 铁律）

值驱动进度条**绝不**用 `style="width:X%"`（会被 CSP `style-src` 无 unsafe-inline 拦截）。原生 `<meter>` 是唯一 CSP 安全的值驱动可视化：值经 HTML 属性传递、填充由浏览器渲染、语义天然可访问。`pagesweep` 横扫会捕获任何新内联 style 回归。

## 测试（TDD，`routes_usage_test.go`）

真实 PG（`newConsole` 已迁移含 `plan` 表 + `tenant.plan_id` 默认 free/limit 3）+ 真实 AdminServer + root operator。

1. **渲染有齿**：`SeedAppInTenant(db,"acme",domain,key)` → 租户 free/limit3/used1 → root `GET /tenants/{tid}/usage` 200 → 断言含「免费版」「1 / 3」、`<meter`、`value="1"`、`max="3"`、单 `<h1`。
2. **至上限告警有齿**：同租户再插 2 应用（distinct domain 避 `uq_tenant_domain`）达 used=3=limit → 断言含 at-limit 告警文案 + `value="3"`；未达上限用例断言**不**含该文案（双向有齿）。
3. **需会话**：匿名 client → 302 /login。
4. **未知租户 404**：不存在的 tenant_id → `renderGRPCError` NotFound。
5. 既有 `pagesweep_test` / `templates_lint_test` 自动纳入新模板（单 h1、无内联 style、有 title/content block）。

跨租户 403（scopeTenant 拒绝）已在 M6.1b mgmt 层测过，此处不重复 DB 级断言——与 `developer` 页测试作用域一致（root 视角为主）。

## 不变量

- **零触碰授权核心**：casbin / kernel / adminauthz / dataperm / authz 求值路径零改（机器 diff 核验空）
- `GetTenantUsage` proto / handler / store **零改**（纯消费第四面）
- 机器 diff 仅动：`console/routes_usage.go`（新）、`console/routes_usage_test.go`（新）、`console/templates/usage.html`（新）、`console/templates/tenants.html`（加一链接）、`console/bizterm.go`（加 planLabel）、`static/css/components.css`（加 .usage-meter）、`console/handler.go` 或注册处（接线 registerUsage）
- `go test ./...` EXIT 0；`make proto-breaking` 无关（本片不碰 proto）

## 验收（M61C-1..7）

1. 零触碰授权核心（机器 diff 空）
2. `routes_usage.go` handler：requireSession + AuthorizeRule(scopeTenant) + GetTenantUsage + renderPage
3. `usage.html`：单 h1、meter 无内联 style、AtLimit 告警分支
4. 测试 1（渲染有齿，钉死 1/3 + 免费版 + meter 属性）PASS
5. 测试 2（至上限告警双向有齿）PASS
6. 测试 3/4（需会话 302、未知租户 404）PASS
7. `go test ./...` EXIT 0；pagesweep/templates_lint 纳入新模板 PASS
