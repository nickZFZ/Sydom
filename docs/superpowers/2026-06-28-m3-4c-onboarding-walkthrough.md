# M3.4c Onboarding 向导 — 真实浏览器 axe 走查记录（任务 5 步骤 3）

> 基准：plan `docs/superpowers/plans/2026-06-27-sydom-m3-4c-onboarding-wizard.md`；spec `docs/superpowers/specs/2026-06-27-sydom-m3-4c-onboarding-wizard-design.md` §7（OB-1..7）/§8。BASE = `ef17fe3`。
> 走查方式：一次性脚手架（build tag `walkthrough`，`zz_walkthrough_scaffold_test.go`）起一套真依赖 Console（testcontainers Postgres 17 + Redis 7，`EnsureRootOperator` 种 `root@sydom`，`SeedAppInTenant` 播种**两个**应用：app#1 空态 / app#2 服务端 `ApplyTemplate(general-admin)` 已建角色，会话 TTL 调长 `time.Hour` 避免走查中途过期），系统 **Google Chrome 147**（Playwright MCP，真浏览器）驱动；注入 **axe-core 4.10.2** 跑 a11y。脚手架、axe 本地静态服务（`python3 -m http.server`）均一次性，走查后已删除/停止、未提交。
> 走查口径：root 同源 `fetch('/login')` 建会话（cookie 入浏览器上下文）→ 逐页 `page.goto` 真实渲染（CSS/cookie 全生效）→ 页内 `<script src>` 注入 axe-core 后 `axe.run(document)` 收 violations（页自身 realm，无跨 realm 问题）。

## 1. 覆盖页（5 页面 / 6 次渲染态，axe-core 4.10.2）

| 页 | 路由 | 到达态 | axe violations | 单 h1 | breadcrumb | 关键断言 |
|---|---|---|---|---|---|---|
| 运营台·业务角色（空 app） | `/ops/apps/1/roles` | 空 app | **0** | ✓ | ✓ | **横幅显示**（`[data-onboarding-banner]` 在）；appnav 4 链接含「引导」；`aria-current=业务角色` |
| 向导·开始引导（选包） | `/ops/apps/1/onboarding` | 空 app | **0** | ✓ | ✓ | 推荐 badge 在（`.badge-success`）；2 张包卡；`aria-current=引导` |
| 向导·分配首个成员 | `/ops/apps/2/onboarding/assign?template_id=general-admin` | 已建角色 | **0** | ✓ | ✓ | 角色下拉为**业务名**「管理员/编辑/只读」（无 role_id/code 裸露）；跳过链接在；`aria-current=引导` |
| 向导·引导完成 | `/ops/apps/1/onboarding/done?template_id=general-admin` | — | **0** | ✓ | ✓ | next_steps 列表 4 项；`aria-current=引导` |
| 运营台·业务角色（已建角色 app） | `/ops/apps/2/roles` | 已 apply | **0** | ✓ | ✓ | **横幅消失**（`[data-onboarding-banner]` 不在）；角色「管理员/编辑/只读」已建 |

- 每页 `document.querySelectorAll('h1').length === 1`；每页 `.breadcrumb` 在；axe-core 实测版本 `4.10.2`。
- **横幅派生态双向有齿**：同一 `/roles` 模板，空 app（#1）横幅在、已建角色 app（#2）横幅无——证 `ShowOnboarding` 派生「无业务角色」成立、且 fail-soft 不误显。
- **业务语言无原语（OB-4 / TP-8）渲染核实**：assign 下拉与 roles 表格均渲染角色 `.Name`（管理员/编辑/只读），未见 role_id / code / `resource:action` 裸串。

## 2. 共享导航 partial 的 a11y 统一（任务 4 抽 `_ops_appnav.html` 的现场验证）

- 走查实测每页 `.appnav` 为 4 链接「引导 / 人员 / 业务角色 / 模板库」，且高亮用 `aria-current="page"`（select/assign/done 页高亮「引导」、roles 页高亮「业务角色」）——证 12 处内联 appnav 统一为单一 partial 后：引导入口出现在每个运营台页、aria-current 一致、子页高亮态补齐。
- 旧内联 appnav 的既有不一致（部分 `class="active"` / 部分 `aria-current` / 子页只高亮 roles / `ops_role_new` 缺 `aria-label`）已随 partial 抽取一并消除。axe-core 对统一后的导航 **0 违规**。

## 3. 结论（OB-1..7 中 a11y 维度，对照 spec §7 / §8）

- **a11y**：M3.4c 新增/改动的 5 个渲染态 axe-core 4.10.2 实测 **0 违规**；每页恰一个 `<h1>` + breadcrumb（done/select/assign 均有）。新组件（推荐 `.badge-success`、横幅 `.card[data-onboarding-banner] role="note"`、assign `<label>` 包裹的 `<select>`）均复用 M3.4b 已验证 0 违规的设计系统组件，无新违规。
- **对比度/色值**：复用 M3.1 已验证达 AA 的 token 体系，本轮零新增硬编码色值（横幅用 `.card`/`.btn-primary`，badge 用既有 `.badge-success`）。
- 结构性回归另由 Go 测试守门：`TestOnboarding_SelectAndDone` / `TestOnboarding_AssignFormAndBind` / `TestOnboarding_AssignBindsAndRedirectsToDone` / `TestOnboarding_BannerWhenEmptyGoneWhenSeeded`（getOK 断言单 h1 + breadcrumb；横幅 `data-onboarding-banner` 锚双向断言）。
- 走查纪律（呼应「回源/渲染核实」）：① 脚手架 `select{}` + `go test` 缓冲 stdout → 用 `os.WriteFile` 写 URL 文件传递（非 stdout）；② `SeedApp` 硬编码租户名 `acme` 不可调两次（uq_tenant_name 冲突）→ 改 `SeedAppInTenant` 两不同租户；③ 会话 TTL 复刻为 `time.Hour` 防走查中途过期；④ 无 CSP → 直接 `page.goto` 后页内 `<script src>` 注入 axe 跑 `axe.run(document)`，规避 M3.4b 的跨 realm iframe 注入问题；⑤ 停后台进程按确切 pid（非 `pkill -f` 防自杀）。
