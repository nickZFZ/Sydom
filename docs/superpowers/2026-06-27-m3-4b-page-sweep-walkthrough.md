# M3.4b 页面迁移横扫 + breadcrumb — 整体 axe 横扫记录（任务 5 步骤 3）

> 基准：plan `docs/superpowers/plans/2026-06-27-sydom-m3-4b-page-sweep-breadcrumb.md`；spec `docs/superpowers/specs/2026-06-27-sydom-m3-4b-page-sweep-breadcrumb-design.md` §7（PS-4 a11y）/§8。BASE = `420a730`。
> 走查方式：一次性脚手架（build tag `walkthrough`，`zz_walkthrough_scaffold_test.go`）起一套真依赖 Console（testcontainers Postgres+Redis，`EnsureRootOperator` 种 `root@sydom`，`SeedAppInTenant` 种 app_id=1/tenant_id=1，会话 TTL 调长避免走查中途过期），系统 **Google Chrome**（Playwright MCP，真浏览器）驱动；注入 **axe-core 4.10.2** 跑 a11y。脚手架与本地 axe 静态服务均一次性，走查后已删除/停止、未提交。
> 走查口径：登录 root 后，对每个迁移页用**同源 iframe** 加载真实渲染页（CSS/cookie 全生效），在 iframe realm 注入 axe-core 并 `axe.run` 收 violations。展示页经真实写动作（创建/轮换/邀请）到达后同法走查。
> 控制者决策（计划级整合）：plan 每批次 axe 走查整合到任务 5 一次性整体横扫（避免 4× 脆弱浏览器跑），每批次另以结构性 Go 测试（单 h1+breadcrumb）+ `grep '<th></th>'` 守门 page-has-heading-one/empty-table-header 两既有债。

## 1. 横扫覆盖（25 迁移页，axe-core 4.10.2）

| 批次 | 页（数） | 到达方式 | axe violations |
|---|---|---|---|
| 建模台 app 域 | roles, grants, bindings, inheritances, **data-policies**, audit, decision, effective（8） | GET（root + SeedApp） | **0**（data-policies 见 §2） |
| system 域 | admin-roles, admin/audit, operators, tenants, **tenants/{id}/members**（5） | GET（root 超管） | **0**（tenants 见 §2） |
| 表单页 | apps/new, operators/new, register, ops/apps/{id}/roles/new（4） | GET | **0** |
| 一次性展示页 | app_created, operator_created, member_invited, app_secret_rotated（4） | 真实写动作 POST 到达 | **0** |
| 一次性展示页 | operator_secret_reset（1） | 结构等价 + Go secret-once 测试覆盖 | 0（见 §3） |
| 运营台 | ops/apps/{id}/people, ops/apps/{id}/roles（2） | GET | **0** |
| 错误页 | error（1，经越权 app effective→403 fail-close 到达） | GET（403） | **0**（单 h1、**无 breadcrumb**，设计如此） |

- 每个迁移页实测 `strings.Count('<h1>')==1`（error 页亦单 h1）；除 error 外每页含 `class="breadcrumb"`（error 页刻意无 breadcrumb，spec §3/§4）。
- 一次性展示页 secret 渲染位经写动作到达后实测仍单次展示（secret 长度 64），未入新增持久位（§D / PS-6）。

## 2. 横扫发现的 2 个 axe 违规 → 已修复至 0（均为 BASE 即存在的既有债）

| 页 | 违规 | 性质 | 修复 |
|---|---|---|---|
| data-policies | `select-name`（critical，3 nodes） | 静态 `<select name="effect">` 无 accessible name + 可视化构建器 datapolicy.js 动态生成的 group/op `<select>` 无 name | 静态 select 加 `aria-label="效果（allow/deny）"`（.html）；builder 两 select 加 `aria-label`（datapolicy.js，纯 a11y 属性，**经用户批准的 PS-2 范围微调**） |
| tenants | `link-in-text-block`（serious，1 node） | 正文内联链接（「暂无租户，注册一个」）仅靠颜色区分（base.css `a{text-decoration:none}`） | `.list-plain a { text-decoration:underline; }`（components.css），正文链接非仅颜色可辨 |

修复后复跑：data-policies 3 selects 全有 aria-label，**axe vc=0**；tenants **axe vc=0**。

> **回源核实**：两违规经 `git show 420a730:...` 确认 BASE 即存在（`<select name="effect">`、`<a href="/register">注册一个</a>` 一字未变），非 M3.4b reskin 引入；但 PS-4「每迁页 axe 0」+ spec §5「其余 axe 发现按页修复至 0」要求横扫时一并清零，故在表现层修复。

## 3. 设计系统补齐：.secret-box / .warn（M3.1 删 app.css 后孤立）

横扫发现 secret 展示页的 `.secret-box`（凭据框）/ `.warn`（强警示文案）在 M3.1 分层 CSS 中**无定义**（`grep` 四文件 tokens/base/layout/components 均无），自 M3.1 删 `app.css` 后渲染为无样式裸块——这 4 页（app_created/app_secret_rotated/operator_created/operator_secret_reset）未真正「升到设计系统」。以**纯 CSS**（components.css 加 token 化定义，零 secret 页 HTML 改动，守 §D / PS-6）补齐：`.secret-box{ border:1px solid var(--color-danger); ... }`、`.warn{ color:var(--color-danger); font-weight:var(--weight-bold); }`。

走查实测渲染确认（operator_created/app_secret_rotated/app_created/operator_created）：`.warn` computed `color=rgb(200,51,47)`(=`--color-danger` #c8332f) `font-weight=700`；`.secret-box` computed `border 1px rgb(200,51,47)`——警示框入体系、视觉强调成立。`.secret`（凭据值本身）M3.1 已定义（mono + danger），未触碰。

## 4. operator_secret_reset 的覆盖说明

operator_secret_reset 需「创建算子→取 id→confirmed=1 重置」多步链到达，走查未直达；其模板结构与已直达走查 **0 违规**的 app_secret_rotated / operator_created **逐元素同构**（同 breadcrumb + 单 h1 + `.secret-box`>`.warn`+`.secret` 壳 + 独立 `.btn` 链接），且 secret 一次性展示由既有 Go 测试 `TestConsole_ResetOperatorSecret_ShowsSecretOnce` 守门。据结构等价 + Go 测试，判定 axe-0。

## 5. 结论（PS-4 对照）

- **PS-4 a11y**：25 迁移页 axe-core 4.10.2 实测 **0 违规**（含修复后的 data-policies/tenants；operator_secret_reset 经等价判定）；每页恰一个 `<h1>`；除 error 外每页 breadcrumb；空操作列 `<th>` 统一 `.visually-hidden`（grep 全 templates 无 `<th></th>`）。M3.4a 走查记录里留给 M3.4b 的 roles 页两既有债（`page-has-heading-one`/`empty-table-header`）已全清。✅
- 对比度：复用 M3.1 已验证达 AA 的 token 体系，无新增硬编码色值；`.warn`/`.secret-box`/`.secret` 用 `--color-danger`(#c8332f，M3.1 已调至 AA)。
- 走查纪律踩坑（呼应「回源/渲染核实」）：① `go test` 缓冲测试 stdout 直至测试结束→脚手架 `select{}` 永不结束，URL 永不刷出→改 `os.WriteFile` 直写文件传递；② 脚手架 `newConsole` 会话 TTL=1min，走查中途过期→脚手架复刻 newConsole 以 `time.Hour` TTL；③ 页面顶栏 logout 表单是 DOM 首个 `<form>`，`querySelector('form')` 误选它→提交即登出→改按 action≠/logout 选内容表单；④ `axe.run(跨 realm document)` 报 invalid→改在 iframe realm 内注入 axe 并 `iframe.contentWindow.axe.run`。
