# 技术债清理（一轮）— 真实浏览器 axe 走查记录（任务 5 步骤 3）

> 基准：plan `docs/superpowers/plans/2026-06-28-sydom-tech-debt-cleanup.md`；spec `docs/superpowers/specs/2026-06-28-sydom-tech-debt-cleanup-design.md` §7（TD-1..7）。BASE = 本地 main `8718aff`（spec/plan 提交 `6cfdbe3` 为本轮实现基线）。
> 走查方式：一次性脚手架（build tag `walkthrough`，`zz_walkthrough_scaffold_test.go`，复用 M3.4c 范式）起一套真依赖 Console——testcontainers Postgres 17 + Redis 7，`EnsureRootOperator` 种 `root@sydom`，`SeedApp` 播种一个应用，`seedTenantTemplateApp` 种 viewer 角色 + `order:read`（名「查看订单」）权限点 + 符号化 data_policy，**追加一个无 name 权限点 `order:export`**（触发 `capabilityName` 合成「order · 导出」验 B4），再经 HTTP `template-captures` 存一份租户模板「标准后台」（使「我的模板」非空 → 渲 B6 删除按钮 + 预览页可达）；会话 TTL 调长 `time.Hour` 防走查中途过期；URL 经 `os.WriteFile` 写文件传递（非 buffered stdout）。系统 **Google Chrome via Playwright MCP**（真浏览器）驱动；注入本地静态服务（`python3 -m http.server`）上的 **axe-core 4.10.2** 跑 a11y。脚手架、axe 静态服务均一次性，走查后已删除/停止、未提交（`.playwright-mcp` 已 gitignore）。
> 走查口径：root 同源 `fetch('/login')`（POST principal/secret，HttpOnly 会话 cookie 入浏览器上下文）→ 逐页 `page.goto` 真实渲染（CSS/cookie 全生效）→ 页内 `<script src>` 注入 axe-core 后 `axe.run(document,{resultTypes:['violations']})` 收 violations（页自身 realm，无跨 realm 问题）。

## 1. 覆盖页（4 页面 / axe-core 4.10.2 实测）

| 页 | 路由 | 改动 | axe violations | 单 h1 | breadcrumb | 关键断言（真实渲染核实） |
|---|---|---|---|---|---|---|
| 运营台·新建业务角色 | `/ops/apps/1/roles/new` | B4 | **0** | ✓「新建业务角色」 | ✓ | 命名权限点渲业务名「查看订单」；**无 name 权限点渲合成「order · 导出」**；**无裸 `order:export` / `order:read`** |
| 运营台·模板库 | `/ops/apps/1/templates` | B5/B6 | **0** | ✓「模板库」 | ✓ | 「我的模板」已由 h1 降为 **h2**（单 h1 成立）；`section [style]` = **null**（行内 style 已清）；删除按钮 `class="btn danger"`（B6） |
| 运营台·模板预览 | `/ops/apps/1/tenant-templates/1` | B6 | **0**（修后） | ✓ | ✓ | 删除模板按钮 `btn danger`；符号谓词 `$user.` 保留；页面无 secret。**走查发现 BASE 既有 1 个 `link-in-text-block`（serious）** —— 见 §2 |
| 系统·操作员 | `/operators` | A3 | **0** | ✓「操作员」 | ✓「系统 · 操作员」 | **无「算子」残留**；展示文案全「操作员」 |

- 每页 `document.querySelectorAll('h1').length === 1`；每页 `.breadcrumb` 在；axe-core 实测版本 `4.10.2`。
- **B4 capabilityName 真实渲染核实**：无 name 权限点 `order:export` 在浏览器中渲为「order · 导出」（resource「order」+ 动词「导出」），命名权限点渲「查看订单」，页面正文不含任何裸 `resource:action` 串——证 B4 消裸原语在真实渲染层成立。
- **B5/B6 真实渲染核实**：模板库页 `h1` 仅一个（「模板库」），原第二个 h1「我的模板」现为 `h2.ops-subsection`；`section` 子树无任何 `style=` 行内属性（CSS 类替代成立）；「我的模板」删除按钮 className 实测 `btn danger`。

## 2. 走查涌现：ops_tenant_template 面包屑 `link-in-text-block`（BASE 既有，已表现层修复）

- **现象**：`/ops/apps/1/tenant-templates/1` 预览页 axe 报 1 个 `link-in-text-block`（impact serious），节点为面包屑返回链接 `<a href="…/templates">模板库</a>`（位于 `nav.breadcrumb` 文本块「模板库 · 我的模板 · 预览」中，仅靠颜色与周围文本区分）。
- **归因（回源核实）**：该面包屑链接位于 `ops_tenant_template.html:5`，`git show 6cfdbe3:` 确认其在 **BASE 即存在**；本轮对该文件的唯一改动是第 29 行删除按钮加 `danger`（B6），与链接无关——故为 **BASE 既有 a11y 债，非本轮引入**。根因是全局 `a { text-decoration:none }`（base.css:11），导致文本块内链接仅靠颜色区分。该页（M3.2 页）未在 M3.4b 的 25 页 sweep 范围内，故此前未被发现。
- **修复**：`layout.css` 加 `.breadcrumb a { text-decoration:underline; }`（纯 CSS，对齐 M3.4b 既有 `.list-plain a { text-decoration:underline }` 处理同一 violation 类的范式）。**真实浏览器实测**：在该页 live 注入此规则后 `axe.run` violations 由 1 → **0**，`getComputedStyle(.breadcrumb a).textDecorationLine === 'underline'`。
- **一般性**：全仓 3 个模板含带链接面包屑（`ops_tenant_template` / `ops_role_graph` / `ops_role_simulate`），同一规则一并受益、消除同类 violation。
- **范围说明**：此修复不在原计划 4 任务范围内，属 Task 5 走查涌现的表现层快修（后端零触碰、提交 `664f7df` 独立可审/可回退）。

## 3. 结论（TD-4 a11y 维度 + 走查纪律）

- **a11y（TD-4）**：本轮 B 改动页 `ops_role_new`（B4）、`ops_templates`（B5/B6）真实浏览器 axe-core 4.10.2 实测 **0 违规**；A3 `operators` 页 **0 违规**且「算子」→「操作员」改名渲染核实；B6 另一页 `ops_tenant_template` 修复 BASE 既有面包屑债后 **0 违规**。每页恰一个 `<h1>` + breadcrumb。
- **对比度/色值**：B6 破坏按钮复用 M3.1 已验证达 AA 的 `.danger`（`--color-danger #c8332f`），本轮零新增硬编码色值；B5 三条新版式类（`.ops-subsection`/`.card-spaced`/`.card-spaced-top`）仅用 `var(--space-*)` token。
- **结构性回归另由 Go 测试守门**：`TestOpsRoleNew_NoNakedPrimitive`（无裸原语，反向验证有齿：模板回退 `{{.Resource}}:{{.Action}}` 则 FAIL）、`TestOpsTemplates_SingleH1`（恰一 h1）、`TestConfirm_DeleteDataPolicy_NoConfirmed_RendersConfirmPage`（C 确认门）、`TestCompositeFK_*`（A2 复合 FK 跨 app 拒绝有齿）。
- **走查纪律（呼应「回源/渲染核实」）**：① 脚手架 `time.Sleep` 阻塞 + URL 写文件传递（非 buffered stdout）；② 会话 TTL 复刻 `time.Hour` 防过期；③ 无 CSP → `page.goto` 后页内 `<script src>` 注入 axe 跑 `axe.run(document)`，规避 M3.4b 跨 realm iframe 注入问题；④ 停后台进程经 `TaskStop` 按确切 task-id（非 `pkill -f` 防自杀）；⑤ BASE 既有债经 `git show 6cfdbe3:` 回源核实确属 BASE、非本轮引入，再决定修复并 live 注入实测修复有齿。
