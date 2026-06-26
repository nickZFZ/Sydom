# M3.4b 页面迁移横扫 + breadcrumb — 设计

> **里程碑上下文**：M3.4（体验打磨横扫 + onboarding）拆 3 子项目——M3.4a 交互打磨基元（✅ `8920e49`：toast + 二次确认渐进增强）/ **M3.4b 页面迁移横扫 + breadcrumb**（本文档）/ M3.4c Onboarding 向导（待）。
>
> **总览 spec**：`docs/superpowers/specs/2026-06-23-sydom-m3-4-experience-polish-onboarding-overview.md`（贯穿不变量 EX-1..8）。本文档是 M3.4b 子项目实现 spec，随后走 plan → 子代理执行。

## 1. 背景与目标

M3.1 建立了零构建、token 化分层 CSS 设计系统（`static/css/tokens·base·layout·components`）并**删除了旧 `app.css`**，使新分层 CSS 自足兜底——但只把 5 个旗舰页（shell/login/dashboard/ops_person/permissions）迁到成熟页头约定。M3.2/M3.3 新建的运营台 ops 页（ops_person/ops_templates/ops_role_graph/ops_role_simulate/ops_tenant_template/ops_template_applied）也已采纳该约定。

其余 **25 个页面**仍是「legacy 标记 + 兜底 CSS」：用 `<h2>` 作页标题（无 `<h1>`）、无 breadcrumb 文案、内容多为裸 `<table>`/裸按钮。它们在 axe-core 下触发 `page-has-heading-one`（缺单一 h1）等违规——M3.4a 走查已在 roles 页实测到这一既有结构债。

**目标**：把这 25 页升到 M3.1 已验证的成熟页头约定（breadcrumb + 单一 h1 + 设计系统组件类），axe 全清、对比度达 AA，**纯表现层、后端零触碰、行为/路由/data 键一字不改**。

## 2. 关键决策

- **不发明，复用既有成熟模式**：页头约定照 ops_person/ops_templates 已验证写法——`<nav class="breadcrumb" aria-label="面包屑">区 · 页</nav>` + 单一 `<h1>`，页内子标题降为 `<h2>`。`.breadcrumb` CSS 已在 `layout.css`（注释「文案补全留 M3.4」）。
- **per-page 字面 breadcrumb，不抽 partial**：与现有 10 页一致，breadcrumb 文案是一行字面 `<nav>`，不引入数据驱动 partial（YAGNI；不改 renderPage 数据契约）。
- **轻量一致化，非重构**：只换表现（组件类 + 页头 + axe 修复），不改布局结构/文案语义/路由/表单 action/data 键/任何 `.go`。
- **不含批量多选**：批量从 M3.4b 排除（umbrella §3.1 原列 M3.4a、已推迟；M3.4b 严格照 umbrella §3.2 = 迁移 + breadcrumb + 顺手核验确认基元）。批量另议或随后续评。
- **一个 spec + 一个 plan，按区域分批，整体一次 FF**：25 页按授权域/页型分 5 批次任务，每任务两阶段审查 + axe 走查，整体一次 FF 本地 main（与 M3.1/M2.4 节奏一致）。
- **后端零触碰**：不改 adminauthz/enforcer/sidecar/proto/数据面；不改 console `.go`（仅 `.html`/`.css`）。

## 3. 范围（25 页）

清点口径 = 有 `<h2>` 且无 `<h1>` 的非 partial 模板（正是 umbrella「~25 页」），按批次：

| 批次（任务） | 页面（数） |
|---|---|
| **建模台 app 域** | grants, bindings, inheritances, datapolicies, audit, decision, effective, **roles**（8）— roles 直接消 M3.4a 既有 axe 债 |
| **system 域** | admin_roles, admin_audit, operators, members, tenants（5） |
| **表单 / 一次性展示** | app_new, app_created, app_secret_rotated, operator_new, operator_created, operator_secret_reset, register, member_invited, ops_role_new（9） |
| **运营台剩余** | ops_people, ops_roles（2） |
| **错误页** | error（1） |

合计 8+5+9+2+1 = **25**。partials（`_appnav`/`_pager`）与已迁旗舰/ops 页不在范围（但若 reskin 触及共享 partial 须保证不破坏已迁页）。

## 4. 页头约定（逐页应用）

每页 `{{define "content"}}` 顶部统一为：

```html
<nav class="breadcrumb" aria-label="面包屑">{{区}} · {{页}}</nav>
<h1>{{页标题}}</h1>
```

- **breadcrumb 文案**逐页业务语言：建模台页用「建模台 · X」（app 域，X=角色绑定/权限授予/角色继承/数据策略/审计/决策解释/有效权限/角色）；system 页用「系统 · X」（X=管理员角色/系统审计/算子/成员/租户）；表单/展示页用其归属区（如「应用 · 新建」「算子 · 凭据已重置」）。**不漏原语**（TP-8）：用业务名，不渲 role_id/code/谓词。
- **单一 h1**：原 `<h2>页标题</h2>` → `<h1>页标题</h1>`；页内分节标题（如表单区/列表区小标题）保留/降为 `<h2>`，保证每页恰一个 h1。
- app 域页（grants/bindings/inheritances/datapolicies/audit/decision/effective/roles）已含 `{{template "appnav" .}}` 的 `.workspace` 网格——breadcrumb 置于 `.workspace` 内容区顶部，与 ops 页一致。

## 5. 迁移机制（轻量一致化）

逐页换表现，**不改语义**：

- 裸 `<table>` → `.table`；表头排序链接/搜索框/pager 保留原样（已是 partial/既有标记）。
- 卡片/分节容器 → `.card`（含 `.card-header` 小标题），与 ops 页一致。
- 状态/来源/计数标签 → `.badge`（`.badge-success`/`.badge-muted` 等既有变体）。
- 裸 `<button>`/`<a>` 动作 → 按钮变体类（`.btn-primary` 主动作、`.danger` 破坏动作、ghost 次动作）；**破坏动作表单的 `data-confirm` 不得删除**（M3.4a 已接，见 §6）。
- 颜色一律 token 变量，零硬编码色值。
- **axe 债修复**：
  - `page-has-heading-one`：§4 单一 h1 解决。
  - `empty-table-header`：操作列空 `<th></th>` → **统一用视觉隐藏文案** `<th><span class="visually-hidden">操作</span></th>`。`.visually-hidden` 标准工具类（`position:absolute;width:1px;height:1px;…;clip`）落 `base.css`（首个用到的任务前置落地，见 §9），全仓统一引用；不混用 `aria-label`。
  - 其余 axe 发现按页修复至 0 违规。

## 6. 确认基元与 secret（核验不回退）

- **确认基元**：M3.4a 已给 8 个 §4.3 破坏动作表单接 `data-confirm`（覆盖 admin_roles 的 RevokeAdminGrant、operators 的 UnbindOperatorRole/ResetOperatorSecret、inheritances 的 RemoveRoleInheritance、roles 的 DeleteRole 等，均在本 25 页内）。M3.4b reskin 这些页时**必须保留** `data-confirm` 与表单 action/CSRF 隐藏字段不变——仅核验，不新增也不回退。
- **一次性 secret 展示页**（app_secret_rotated/operator_secret_reset/app_created/operator_created/member_invited）：仅换表现壳，**专管线一字不动**——secret 仍仅一次性展示、不入会话/日志、不 PRG。reskin 不得把 secret 值写入任何新增持久位。

## 7. 验收不变量 PS-1..7（落 EX-1..8）

- **PS-1 渐进增强基线不回退（EX-1）**：迁移纯表现，无 JS 时页面功能完整（既有服务端渲染不变）；既有 `datapolicy.js`/`interactions.js` 行为不受影响。
- **PS-2 后端零触碰（EX-3）**：`git diff <BASE>..HEAD -- internal/controlplane/adminauthz/ casbin/enforcer.go internal/sidecar/ api/proto gen/` = **0 行**；**生产** `.go` 零改——`git diff <BASE>..HEAD -- internal/controlplane/console/ ':(exclude)*_test.go' ':(exclude)*.html' ':(exclude)*.css'` = **0 行**（M3.4b 仅改 `.html`/`.css`；§8 允许**新增** `_test.go` 内容断言，不改生产 handler/render）；renderPage 注入的 `data` 键集合不变；M1.1 matcher 一字未改。
- **PS-3 行为·路由·data 键不变（EX-5）**：既有 Console Go 测试**全绿**（它们断言页面内容子串/CSRF 隐藏字段/secret 无 value/sortHref/searchbox/pager 等）——作为行为不变的回归网；表单 action/method、路由、handler 全不动。
- **PS-4 a11y（EX-4）**：每迁页 axe-core **0 违规**（消 `page-has-heading-one`/`empty-table-header` 及其余）；对比度 ≥ AA 4.5:1；键盘全可达；每页恰一个 `<h1>`；`<th scope>`/`label`/`aria-label` 齐。
- **PS-5 业务语言无原语（EX-7）**：breadcrumb 与标题用业务语言，不漏 role_id/code/谓词；能力/角色名经既有 bizterm/permNameMap（本子项目不改翻译层，仅复用）。
- **PS-6 secret 不泄露（EX-6）**：一次性 secret 专管线不动；secret 绝不入会话/日志/新增持久位（§6）。
- **PS-7 零构建（EX-8）**：无新增 JS（breadcrumb 是纯 HTML/CSS，不需要 JS）；CSS 全 token 化分层；不引入框架/打包器。

## 8. 测试策略

- **回归网（行为不变）**：每批次 reskin 后跑 `go test ./internal/controlplane/console/ -count=1` 全绿——既有测试断言页面内容/CSRF/secret-no-value/sortHref/pager，迁移不得破坏任一断言。若某迁页缺测试覆盖关键内容子串，**先补一条最小内容断言测试**（TDD-lite：迁前先确认测试覆盖该页关键标记，再 reskin，再验证仍绿）。
- **a11y 走查**：每批次后复用 M3.4a 一次性走查脚手架（build-tag `walkthrough` Go 脚手架起真依赖 Console + 系统 Chrome + Playwright + axe-core 4.10.2），对该批迁后页 `axe.run` → 0 违规 + 对比度抽验；走查脚本/脚手架一次性、走查后删、不提交。
- **末任务整体**：PS-1..7 逐条核验 + 全量 `go test ./...` 0 FAIL + gofmt/vet 干净 + opus 整体安全/质量评审 READY + FF 合并本地 main（不 push origin）。

## 9. 任务分解（预览，详见 plan）

| 任务 | 内容 |
|---|---|
| 1 | 建模台 app 域 8 页 reskin（含 roles 消 axe 债）+ 补缺失内容断言 + axe 走查 |
| 2 | system 域 5 页 reskin + 测试 + axe 走查 |
| 3 | 表单 / 一次性展示 9 页 reskin（secret 专管线不动）+ 测试 + axe 走查 |
| 4 | 运营台剩余 2 页 reskin（对齐 ops_person/ops_templates）+ 测试 + axe 走查 |
| 5 | error 页 reskin + `.visually-hidden` 工具类落 base.css（若需）+ 全页 axe 横扫 |
| 6 | 整体 PS-1..7 核验 + 全量测试 + opus 评审 + FF 合并本地 main |

> 任务 5 顺手落 `.visually-hidden`（若 §5 空 th 修复选视觉隐藏方案）；该工具类一旦落地，前序任务空 th 统一引用——plan 阶段决定是否前移到任务 1。

## 10. 不做（YAGNI / 推后）

- **批量多选**：已定排除（§2）。
- **布局重构 / 深度重设计**：违「轻量一致化」决策，明确不做。
- **抽 breadcrumb partial / 数据驱动页头**：YAGNI，保 per-page 字面（§2）。
- **改任何后端 / 行为 / 路由 / data 键 / 翻译层**：违 PS-2/PS-3。
- **i18n**：中文单语，照 umbrella §7。
- **partials/已迁页的非必要重构**：仅在 reskin 触及共享 partial 时保证不破坏，不主动重构已迁页。

相关：[[feedback-consistency-over-simplicity]]、[[feedback-verify-casbin-before-asserting]]、[[project-detailed-design-progress]]
