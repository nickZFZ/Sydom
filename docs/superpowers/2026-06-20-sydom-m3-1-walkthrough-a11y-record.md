# M3.1 设计系统 + a11y 基座 — 走查 / axe / 对比度核验记录（任务 6）

> 基准：spec `docs/superpowers/specs/2026-06-20-sydom-m3-1-design-system-foundation-design.md` §8。
> 走查方式：临时脚手架起一套真依赖 Console（testcontainers Postgres+Redis，种子 root@sydom + 1 应用「订单系统」+ 业务角色「销售经理」+ alice@corp 绑定 + 3 权限点），Playwright 实浏览器逐面截图 + 注入 axe-core 4.10.2 跑 a11y。脚手架为一次性，走查后已删除、未提交。截图为 gitignore 瞬时产物。

## 1. axe-core 自动 a11y 门（DS-6 口径：无 critical）

axe-core **4.10.2**，对 4 个旗舰页 `axe.run(document)` 全规则集：

| 旗舰页 | 路由 | violations 总数 | critical | serious |
|---|---|---|---|---|
| 登录 | `/login` | **0** | 0 | 0 |
| 仪表盘（应用列表）| `/` | **0** | 0 | 0 |
| 权限点（建模台代表）| `/apps/1/permissions` | **0** | 0 | 0 |
| 人员能力（运营台旗舰）| `/ops/apps/1/people/view?user_id=alice@corp` | **0** | 0 | 0 |

**结论**：4 旗舰页 axe 零违规（任何 impact 级别），远超 DS-6「无 critical」门槛。

## 2. 对比度核验（WCAG AA 4.5:1）

token 语义配对全部 ≥ AA 4.5:1（正文/小字门槛）：

| 配对 | 比值 | AA 4.5 |
|---|---|---|
| 正文 `#1a1a2e` on 表面 `#ffffff` | 17.06:1 | PASS |
| 正文 on 页底 `#f7f8fa` | 16.05:1 | PASS |
| 弱化文本 `#6b7280` on 表面 | 4.83:1 | PASS |
| 弱化文本 on 页底 | 4.55:1 | PASS |
| 主按钮白字 on 主色 `#2f5ad9` | 5.86:1 | PASS |
| 链接/主色 on 表面 | 5.86:1 | PASS |
| 危险文字 `#c8332f` on 表面 | 5.29:1 | PASS |
| 危险文字 on 页底 | 4.98:1 | PASS |
| 危险按钮白字 on 危险 | 5.29:1 | PASS |
| 成功 badge `#15803d` on 表面 | 5.02:1 | PASS |
| 成功 badge on 页底 | 4.72:1 | PASS |
| 顶栏文字 `#cbd2de` on 顶栏底 `#1a1a2e` | 11.22:1 | PASS |
| 品牌白字 on 顶栏底 | 17.06:1 | PASS |

**两处 token 调整（仅 tokens.css，组件零改）**：
- `--c-green-600` `#1f9d57` → `#15803d`：原值在 12px `.badge-success`（小字）上仅 3.49:1，不达 AA；新值 5.02/4.72。
- `--c-red-600` `#d23b3b` → `#c8332f`：原值在 `.secret`/`.field-error`（页底 `#f7f8fa`）上 4.46:1，差 0.04；新值 4.98。

## 3. 实浏览器计算样式核验（级联 + a11y 落地）

仪表盘 `getComputedStyle` 实测：
- `+新建应用` 按钮背景 = `rgb(47,90,217)` = `#2f5ad9`（**token 主色生效**，components.css 覆盖 app.css）。
- 「启用」badge 文字 = `rgb(21,128,61)` = `#15803d`、`font-weight=400`（新可达绿 + 自足 font-weight 挡住 app.css 的 600 泄漏）。
- 顶栏背景 = `rgb(26,26,46)` = `#1a1a2e`。
- `<h1>` 数量 = 1（单一 h1）。
- skip-link `href="#main"`、活动导航 `aria-current="page"`、表头全部 `<th scope="col">`。

## 4. 视觉走查结论（无破版）

- **登录**：居中品牌卡（`.card .login-card`）、`.form-field` 标注字段、聚焦蓝色焦点环、主按钮、卡片垂直居中（`margin:0` 自足修复生效）。
- **仪表盘**：深色顶栏（品牌 + 应用/租户/系统 + 活动高亮 + 登出描边按钮）、h1、token 蓝主按钮、searchbox、`.table` 排序表头、绿色「启用」success badge、ghost「轮换密钥」、pager。
- **权限点**：`.workspace` 网格（左 appnav 卡片侧栏，活动「权限点」高亮 + 右内容区）、建表单、searchbox、密集排序表 + 3 行权限点 + manual 来源 badge、pager。
- **人员能力（运营台）**：ops 侧栏卡片、breadcrumb 壳、h1、查询表单、`.list-plain` 角色/能力列表（业务名「销售经理」「导出订单」「查看订单」，**不漏 orders:read/sales 技术原语**）、`.select` + 分配按钮、`.btn-danger`「移除」（新可达红）。

## 5. a11y 属性强化（任务 6 步骤 1，提交 03dab44）

纯加属性（DS-7 功能不变）：searchbox/`app_id`/`user_id` input `aria-label`；permissions 建表单 5 input `aria-label`；`role_id` select `aria-label`；dashboard/permissions/ops_person 表头 `<th scope="col">`。login 已合规（`<label for>` + `role="alert"` + 单一 h1）未改。

## 6. 已知过渡期物（任务 7 删 app.css 后消失）

登录页按钮/输入框在过渡期仍取 app.css 的 `.login-card button`/`.login-card input`（specificity 0,1,1 > components.css `.btn-primary` 0,1,0，因新登录页复用 `.login-card` 类名）。这是登录页专有现象；其余页（dashboard/permissions/ops_person）的 `.btn-primary` 不在 `.login-card` 内，token 主色已生效。任务 7 删除 app.css 后，登录页按钮亦转为 token 主色 `#2f5ad9`。
