# M3.1 设计系统 + a11y 基座 — 设计

> **里程碑上下文**：M3（业务向运营台成体系）是一个里程碑，拆为 4 个子项目——**M3.1 设计系统 + a11y 基座** / M3.2 业务语言抽象层 + 预设·模板 / M3.3 关系可视化 + 决策模拟器 / M3.4 体验打磨横扫 + onboarding。本 spec 只覆盖 M3.1。i18n、深色模式切换控件、移动端 hamburger、交互行为（瞬时 toast / 模态确认 / 批量）均**不在** M3.1。

## 1. 目的

把 Console 从「138 行裸 `app.css` + 中文硬编码模板」升级为一套**零构建、嵌入式、token 化的设计系统 + a11y 基线**，作为 M3.2–M3.4 一切业务向体验的地基。M3 的验收是「真实非技术用户可用性达标（任务完成率 / SUS）」——可信的外观与可达性是该验收的前置条件。

## 2. 关键决策（brainstorm 收敛）

| 决策点 | 选择 | 理由 |
|---|---|---|
| 技术路线 | **纯手写 token 化 CSS**（CSS 自定义属性 + 组件 class），`//go:embed`，无构建/无框架/无 CDN | 契合单 Go 二进制 + 服务端渲染 + 「唯一 JS」ethos；对业务语言 UX 可完全掌控；离线/气隙部署友好 |
| 覆盖深度 | **基座式**：系统 + shell 骨架 + 旗舰页迁移，其余 ~25 页增量迁 | 子项目可控、不阻后续；共享 chrome 升级即全站自动受益 |
| 深色模式 | **延后交付，但 tokens 深色就绪**（语义变量、组件零硬编码色） | YAGNI（私有 Beta 单语言/桌面），但不把路堵死——将来加 `[data-theme=dark]` override 即可 |
| 旗舰页 | **业务面为主 + 1 建模台代表**：shell + login + dashboard + ops_person + permissions | 对齐 M3「业务向」与 PMF 风险，顺带证体系在技术页也成立 |

## 3. 架构形态（零构建、嵌入、纯 CSS）

现 `static/app.css`（138 行）拆为**分层 CSS**，仍 `//go:embed static/*`，`layout.html` 按序 `<link>`：

```
static/css/
  tokens.css       — design tokens（:root 上的 CSS 变量；[data-theme=dark] 预留空块 + TODO 注释）
  base.css         — reset/normalize + 元素默认（排版、链接、:focus-visible、表单基样、跳转链接）
  components.css   — 组件 class（.btn / .card / .table / .badge / .form-field / .dialog / .toast / .alert / .empty-state …）
  layout.css       — shell 结构（.app-shell / .topbar / .sidenav / .workspace / .breadcrumb 壳）
```

> 单文件可否？可，但分 4 个聚焦文件更易维护与推理（各文件单一职责）。`layout.html` 的 `<link>` 顺序固定：tokens → base → layout → components（后者可覆盖前者）。现有 `datapolicy.js` 原样保留，M3.1 **不新增任何 JS**。

**关键性质（增量迁移安全的根据）**：全部 ~30 页共用 `layout.html` + 这套 class，故**升级共享 CSS = 全站导航/排版/按钮/表格自动升级**；旗舰页只是额外做了页内结构精修 + 走查。未迁页在新 shell 内渲染、复用新 class，不会破坏（DS-4）。

## 4. Design tokens（语义化、深色就绪）

组件**只引用语义层 token**，绝不写死色值——这是深色就绪的命门（DS-5）。

```
原语层 primitive   --c-gray-{50,100,200,300,400,500,600,700,800,900}
                  --c-blue-{500,600,700}  --c-red-600  --c-green-600  --c-amber-500
语义层 semantic    --color-bg  --color-surface  --color-text  --color-text-muted
                  --color-border  --color-primary  --color-on-primary
                  --color-danger  --color-success  --color-warning  --focus-ring
间距   --space-{1..8}（4px 基：4/8/12/16/24/32/48/64）
圆角   --radius-{sm,md,lg}        阴影 --shadow-{sm,md}
排版   --font-sans  --font-mono  --text-{xs,sm,base,lg,xl,2xl}  --leading-{tight,normal}  --weight-{normal,medium,bold}
```

将来深色 = 仅在 `[data-theme=dark]` 重定义**语义层**变量，原语层与组件一行不改。切换控件本身（JS 或 `prefers-color-scheme`）延后。

## 5. 组件库（CSS class，服务端渲染友好）

| 组件 | class | 备注 |
|---|---|---|
| 按钮 | `.btn` `.btn-primary` `.btn-ghost` `.btn-danger` `.btn-sm` | 替换裸 `<button>` |
| 表单 | `.form-field`(label+控件) `.input` `.select` `.checkbox` | 校验态 `.is-invalid` + `.field-error` |
| 表格 | `.table` | 斑马纹/表头/紧凑；复用现有 `<table>` 结构 |
| 卡片 | `.card` `.card-header` | dashboard/详情 |
| 徽章 | `.badge` `.badge-success` `.badge-muted` | 状态（启用/停用）可视化 |
| 内联反馈 | `.alert` `.alert-error` `.alert-info` | 校验/提示，JS-free 服务端渲染 |
| 对话框壳 | `.dialog` | **仅视觉壳**；二次确认交互留 M3.4 |
| toast 壳 | `.toast` | **JS-free 静态 flash 样式**；瞬时/自动消失留 M3.4 |
| 空状态 | `.empty-state` | 列表空时引导 |
| 既有纳入 | `.searchbox` `.pager`（M2.4） | 纳入体系、重新着色 |

**边界**：M3.1 出**组件的视觉 CSS**；需要 JS 的**交互行为**（瞬时 toast、模态确认、批量选择）统一留 **M3.4**。M3.1 的 toast/dialog 是静态壳。

## 6. Shell / layout 重构

现 shell：`topbar(brand+nav+logout)` → `main > workspace > [aside.appnav] + section`。重构为：

```
┌─ .topbar ────────────────────────────────────────────┐
│ 司域 Sydom   [应用][租户][系统]        user · 登出     │  （主题切换槽位预留，延后）
├──────────────┬───────────────────────────────────────┤
│ .sidenav     │ .workspace                            │
│ (上下文导航,  │   .breadcrumb（壳；文案补全留 M3.4）   │
│  app 页=      │   <h1> 页标题                          │
│  appnav)     │   .page-section …                     │
└──────────────┴───────────────────────────────────────┘
```

**响应式**：桌面优先 + 流式；窄屏 sidenav 堆叠到内容上方、topbar nav 换行——**纯 CSS、无 hamburger/JS**（管理台 Beta 够用）。breadcrumb 在 M3.1 只出结构壳与样式，逐页文案补全留 M3.4。

## 7. 旗舰页迁移（5 个代表面）

1. **shell**（`layout.html` + topbar + sidenav + breadcrumb 壳 + `_appnav.html`）——全页共享 chrome
2. **login.html**——居中品牌卡，第一印象
3. **dashboard.html**——落地页：应用列表 + 租户上下文 + 空状态
4. **ops_person.html**——运营台业务旅程（业务面旗舰）
5. **permissions.html**——建模台代表（表格 + 建表单 + searchbox + pager，最密集压组件）

每页迁移 = 用新组件 class 重构页内标记 + Playwright 视觉走查 + a11y 核验。**不改 handler、不改 renderPage data 键**（DS-1）。

## 8. a11y 基线（DS-6 验收口径）

- 语义地标：`<header>`/`<nav>`/`<main>`/`<aside>` + 标题层级（每页单一 `<h1>`）
- 跳转到主内容链接（skip link）
- **全键盘可达 + `:focus-visible` 可见环**（token `--focus-ring`）
- 每 `<input>` 配 `<label>`；错误经 `aria-describedby` 关联；必填标记
- `aria-current="page"` 活动导航；`role="alert"` 错误条；图标控件 `aria-label`
- 表格 `<th scope>`
- **token 配色满足 WCAG AA 文本对比 4.5:1**

## 9. 测试策略

- **既有 console 测试全绿**：断言的是文本内容（"共 3"/"搜索"/principal 名）非精确标记，重着色/加 class 不影响 → `go test ./internal/controlplane/console/` 0 FAIL。若某测试断言了将被重构的精确 HTML 结构，就地调整为断言文本/语义。
- **Playwright 视觉走查**：5 个旗舰面（浅色），沿项目既有「N 屏走查」范式，截图归档。
- **axe-core a11y 冒烟**：Playwright + axe 跑旗舰页 → 断言**无 critical 违规**（自动 a11y 门）。若 axe 集成成本高，回退为「键盘走查截图 + a11y 检查清单（§8 逐条）」人工门，并在实现中说明。
- **对比度**：token 语义配对 AA 达标，文档化核验（列出关键配对的对比值）。

## 10. 不变量 DS-1..DS-7

- **DS-1 后端零触碰**：无 Go handler / AuthorizeRule / proto / mgmt / sidecar 改动；纯表现层（CSS + 模板标记）；renderPage data 键不变。`git diff` 对 `internal/controlplane/{mgmt,adminauthz}` 与 `internal/sidecar` = 0。
- **DS-2 零新增 JS**：唯一 JS 仍 `datapolicy.js`，M3.1 不加 JS。
- **DS-3 服务端渲染保持**：`html/template`，无客户端框架。
- **DS-4 增量不破坏**：未迁 ~25 页在新 shell 渲染、既有测试不回归。
- **DS-5 深色就绪**：组件只引语义 token，`components.css`/`layout.css` 零硬编码色（grep 守卫：除 `tokens.css` 外无 `#hex`/`rgb(`）。
- **DS-6 a11y 基线**：旗舰页 axe 无 critical + 键盘可操作 + AA 对比。
- **DS-7 内容行为不变**：表单 POST / CSRF / 链接功能原样，仅重着色与结构精修；无功能增删。

## 11. YAGNI / 范围边界（明确延后）

- 深色模式**切换控件**（tokens 已就绪）
- i18n（折入 M3.2/M3.4 按需）
- 移动端 hamburger / 完整移动适配（M3.1 只桌面优先 + 窄屏堆叠）
- 交互行为：瞬时 toast、模态确认弹窗、批量选择（**M3.4**）
- breadcrumb 逐页文案补全（M3.1 只出壳，**M3.4** 补全）
- 其余 ~25 页迁移（随 **M3.2–M3.4** 触达迁）
- 关系可视化 / 图（**M3.3**）

## 12. 自检记录

- **占位符扫描**：无 TODO/待定遗留在需求；`[data-theme=dark]` 的「预留空块」是刻意的就绪占位（非未完成需求）。
- **一致性**：§5 组件边界（M3.1 出 CSS 壳、JS 行为留 M3.4）与 §11 范围边界一致；§7 旗舰页与 §2 决策一致（业务面为主 + permissions 代表）。
- **范围**：聚焦单一实现计划可覆盖（系统 + shell + 5 旗舰页 + a11y 门），不需再拆。
- **模糊性**：a11y 自动门留了「axe 优先、清单回退」明确二选一；响应式明确「纯 CSS 堆叠、无 JS」。
