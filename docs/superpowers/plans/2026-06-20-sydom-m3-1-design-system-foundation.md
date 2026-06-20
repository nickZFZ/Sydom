# M3.1 设计系统 + a11y 基座 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 把 Console 从「138 行裸 `app.css` + 中文硬编码模板」升级为零构建、嵌入式、token 化的设计系统 + a11y 基线，并在 5 个旗舰面验证。

**架构：** 纯手写 token 化 CSS（CSS 自定义属性做 design tokens + 组件 class），拆 `static/css/{tokens,base,layout,components}.css`（仍 `//go:embed static/*`，`http.FileServerFS` 服务子目录），`layout.html` 按序 `<link>`。组件只引语义 token（深色就绪）。shell 重构为 topbar + sidenav + workspace 网格。不新增 JS、不碰后端。旗舰页：shell/login/dashboard/ops_person/permissions；其余 ~25 页随后续子项目增量迁，因共享 shell 升级即自动受益。

**技术栈：** Go `html/template`、CSS 自定义属性、`//go:embed`、Playwright + axe-core（视觉/a11y 走查）。

**基准：** spec `docs/superpowers/specs/2026-06-20-sydom-m3-1-design-system-foundation-design.md`。本计划在 off-main worktree 执行。

**关键不变量（DS-1..DS-7，贯穿全程）：** DS-1 后端零触碰（mgmt/adminauthz/sidecar diff=0、renderPage data 键不变）/ DS-2 零新增 JS（唯一 JS 仍 datapolicy.js）/ DS-3 服务端渲染保持 / DS-4 增量不破坏（未迁页 + 既有测试不回归）/ DS-5 深色就绪（components.css/layout.css 零硬编码色，仅 tokens.css 有色值）/ DS-6 a11y 基线（旗舰页 axe 无 critical + 键盘可操作 + AA 对比）/ DS-7 内容行为不变（表单/CSRF/链接功能原样）。

---

## 共享约定（全任务一致）

**CSS 文件与加载顺序**：`static/css/tokens.css → base.css → layout.css → components.css`（后者可覆盖前者）。`layout.html` 的 `<head>` 按此序 4 个 `<link>`。旧 `static/app.css` 任务 7 删除（删前 grep 确认无引用）。

**静态服务已就绪**：`handler.go:25` `mux.Handle("GET /static/", http.FileServerFS(staticFS))`，`render.go` `//go:embed static/*`（递归含子目录）。`/static/css/tokens.css` 直接可服务，无需改 embed/路由。

**DS-5 守卫**：`components.css`、`layout.css`、`base.css` 内**除继承/transparent/currentColor 外不得出现 `#`16进制色或 `rgb(`/`hsl(` 字面色**——一律走 `var(--color-*)`。tokens.css 是唯一定义色值处。任务 2/3 末尾各跑一次 grep 守卫。

**测试口径**：CSS/模板无经典单测；本计划的「测试」= (a) 既有 console 渲染测试保持绿（`go test ./internal/controlplane/console/`，断言文本内容不受重着色影响）；(b) 旗舰页 Playwright 视觉走查 + axe a11y 冒烟（任务 6）；(c) DS-5 grep 守卫。每个改 CSS/模板的任务都跑 `go build ./...` + `go test ./internal/controlplane/console/ -count=1`。

---

## 任务 1：tokens.css + base.css + 接入 layout.html

**文件：**
- 创建：`internal/controlplane/console/static/css/tokens.css`
- 创建：`internal/controlplane/console/static/css/base.css`
- 修改：`internal/controlplane/console/templates/layout.html`（替换 `<link>`）

- [ ] **步骤 1：创建 `tokens.css`（完整 token 契约，深色就绪）**

```css
/* 设计 token —— 唯一定义色值/尺度处。组件只引语义层 var()。 */
:root {
  /* 原语层 primitive（仅语义层引用，组件不直接用） */
  --c-gray-50:#f7f8fa; --c-gray-100:#eef0f4; --c-gray-200:#e2e6ee; --c-gray-300:#cbd2de;
  --c-gray-400:#9aa3b2; --c-gray-500:#6b7280; --c-gray-600:#4b5563; --c-gray-700:#374151;
  --c-gray-800:#262c39; --c-gray-900:#1a1a2e;
  --c-blue-500:#3b6ef0; --c-blue-600:#2f5ad9; --c-blue-700:#2547b0;
  --c-red-600:#d23b3b; --c-green-600:#1f9d57; --c-amber-500:#d99317;

  /* 语义层 semantic（组件唯一引用层；深色仅重定义这层） */
  --color-bg:var(--c-gray-50);
  --color-surface:#ffffff;
  --color-text:var(--c-gray-900);
  --color-text-muted:var(--c-gray-500);
  --color-border:var(--c-gray-200);
  --color-primary:var(--c-blue-600);
  --color-primary-hover:var(--c-blue-700);
  --color-on-primary:#ffffff;
  --color-danger:var(--c-red-600);
  --color-success:var(--c-green-600);
  --color-warning:var(--c-amber-500);
  --color-topbar-bg:var(--c-gray-900);
  --color-topbar-text:var(--c-gray-300);
  --focus-ring:var(--c-blue-500);

  /* 间距（4px 基） */
  --space-1:4px; --space-2:8px; --space-3:12px; --space-4:16px;
  --space-5:24px; --space-6:32px; --space-7:48px; --space-8:64px;
  /* 圆角 / 阴影 */
  --radius-sm:4px; --radius-md:8px; --radius-lg:12px;
  --shadow-sm:0 1px 2px rgba(16,24,40,.06); --shadow-md:0 4px 12px rgba(16,24,40,.10);
  /* 排版 */
  --font-sans:system-ui,-apple-system,"PingFang SC","Microsoft YaHei",sans-serif;
  --font-mono:ui-monospace,SFMono-Regular,Menlo,monospace;
  --text-xs:12px; --text-sm:13px; --text-base:14px; --text-lg:16px; --text-xl:20px; --text-2xl:26px;
  --leading-tight:1.25; --leading-normal:1.55;
  --weight-normal:400; --weight-medium:600; --weight-bold:700;
}

/* 深色就绪占位：将来只在此重定义语义层变量，组件零改动。M3.1 不交付切换控件。 */
/* [data-theme="dark"] { --color-bg:var(--c-gray-900); --color-surface:var(--c-gray-800); ... } */
```

- [ ] **步骤 2：创建 `base.css`（reset + 元素默认 + focus-visible + skip link）**

```css
*,*::before,*::after { box-sizing:border-box; }
html,body { margin:0; }
body {
  font-family:var(--font-sans); font-size:var(--text-base); line-height:var(--leading-normal);
  color:var(--color-text); background:var(--color-bg);
}
h1 { font-size:var(--text-2xl); }
h2 { font-size:var(--text-xl); }
h3 { font-size:var(--text-lg); }
h1,h2,h3 { line-height:var(--leading-tight); font-weight:var(--weight-bold); margin:0 0 var(--space-4); }
a { color:var(--color-primary); text-decoration:none; }
a:hover { text-decoration:underline; }
/* 全局可见焦点环（a11y 基线） */
:focus-visible { outline:2px solid var(--focus-ring); outline-offset:2px; border-radius:var(--radius-sm); }
/* 跳转到主内容（键盘用户），默认藏到屏外，聚焦时出现 */
.skip-link {
  position:absolute; left:var(--space-3); top:-48px; z-index:100;
  background:var(--color-surface); color:var(--color-primary);
  padding:var(--space-2) var(--space-3); border-radius:var(--radius-sm); box-shadow:var(--shadow-md);
  transition:top .15s;
}
.skip-link:focus { top:var(--space-3); }
```

- [ ] **步骤 3：改 `layout.html` 的 `<link>`（其余结构留任务 3）**

把 `<link rel="stylesheet" href="/static/app.css">` 替换为：
```html
<link rel="stylesheet" href="/static/css/tokens.css">
<link rel="stylesheet" href="/static/css/base.css">
<link rel="stylesheet" href="/static/css/layout.css">
<link rel="stylesheet" href="/static/css/components.css">
```
> layout.css/components.css 任务 2/3 才创建——本任务先建 tokens/base，缺的两个 `<link>` 先写上（浏览器对 404 的 CSS 容错，但为避免控制台噪音，可在任务 2/3 前接受；**或**本步只写 tokens/base 两行，任务 2/3 各自补自己的 `<link>`）。**采用后者：本任务只加 tokens + base 两行 `<link>`，删 app.css 那行留任务 7。** 即此刻 `<head>` 同时有旧 `app.css` 与新 tokens/base（并存过渡，base 会覆盖 app.css 同名规则——可接受，任务 7 删 app.css）。

实际本步：在 `app.css` 那行**之后**插入：
```html
<link rel="stylesheet" href="/static/css/tokens.css">
<link rel="stylesheet" href="/static/css/base.css">
```

- [ ] **步骤 4：验证服务 + 构建 + 既有测试**

运行：`go build ./...`（embed 纳入新 css）、`go test ./internal/controlplane/console/ -count=1 2>&1 | tail -3`（既有页测试不回归——base 只改观感不改文本）。手动核验服务可加一条最小测试或在任务 6 的 Playwright 走查覆盖；本步至少确认 `go:embed` 无报错（构建通过即含 css 子目录）。

- [ ] **步骤 5：Commit**
```bash
git add internal/controlplane/console/static/css/tokens.css internal/controlplane/console/static/css/base.css internal/controlplane/console/templates/layout.html && \
git commit -m "feat(console): design tokens + base.css(语义化深色就绪 + a11y focus/skip)"
```

---

## 任务 2：components.css（组件库）

**文件：** 创建 `internal/controlplane/console/static/css/components.css`；修改 `layout.html`（补 components 的 `<link>`）

- [ ] **步骤 1：创建 `components.css`（每组件具体 CSS，只引语义 token）**

```css
/* 按钮 */
.btn { display:inline-flex; align-items:center; gap:var(--space-2);
  font:inherit; font-weight:var(--weight-medium); cursor:pointer;
  padding:var(--space-2) var(--space-4); border-radius:var(--radius-sm);
  border:1px solid var(--color-border); background:var(--color-surface); color:var(--color-text); }
.btn:hover { border-color:var(--color-primary); }
.btn-primary { background:var(--color-primary); border-color:var(--color-primary); color:var(--color-on-primary); }
.btn-primary:hover { background:var(--color-primary-hover); border-color:var(--color-primary-hover); }
.btn-ghost { background:transparent; border-color:transparent; color:var(--color-primary); }
.btn-danger { background:var(--color-danger); border-color:var(--color-danger); color:#fff; }
.btn-sm { padding:var(--space-1) var(--space-3); font-size:var(--text-sm); }

/* 表单 */
.form-field { display:flex; flex-direction:column; gap:var(--space-1); margin-bottom:var(--space-4); }
.form-field > label { font-size:var(--text-sm); font-weight:var(--weight-medium); color:var(--color-text-muted); }
.input,.select,input[type=text],input[type=password],input:not([type]),select,textarea {
  font:inherit; padding:var(--space-2) var(--space-3); border:1px solid var(--color-border);
  border-radius:var(--radius-sm); background:var(--color-surface); color:var(--color-text); }
.input:focus,select:focus,input:focus { border-color:var(--color-primary); }
.is-invalid { border-color:var(--color-danger); }
.field-error { color:var(--color-danger); font-size:var(--text-sm); }
/* 内联表单（保留既有 .inline-form 语义，重着色） */
.inline-form { display:inline-flex; gap:var(--space-2); align-items:center; flex-wrap:wrap; }
.stacked-form { display:flex; flex-direction:column; gap:var(--space-3); max-width:420px; }

/* 表格 */
.table,table { border-collapse:collapse; width:100%; background:var(--color-surface);
  border:1px solid var(--color-border); border-radius:var(--radius-md); overflow:hidden; }
.table th,table th { text-align:left; font-size:var(--text-sm); color:var(--color-text-muted);
  background:var(--color-bg); padding:var(--space-2) var(--space-3); border-bottom:1px solid var(--color-border); }
.table td,table td { padding:var(--space-2) var(--space-3); border-bottom:1px solid var(--color-border); }
.table tbody tr:last-child td { border-bottom:none; }
.table tbody tr:hover { background:var(--color-bg); }

/* 卡片 */
.card { background:var(--color-surface); border:1px solid var(--color-border);
  border-radius:var(--radius-md); box-shadow:var(--shadow-sm); padding:var(--space-5); }
.card-header { font-weight:var(--weight-bold); margin-bottom:var(--space-3); }

/* 徽章 */
.badge { display:inline-block; font-size:var(--text-xs); padding:2px var(--space-2);
  border-radius:999px; background:var(--color-bg); color:var(--color-text-muted); border:1px solid var(--color-border); }
.badge-success { color:var(--color-success); border-color:var(--color-success); }
.badge-muted { color:var(--color-text-muted); }

/* 内联反馈 alert（JS-free） */
.alert { padding:var(--space-3) var(--space-4); border-radius:var(--radius-sm);
  border:1px solid var(--color-border); background:var(--color-surface); margin-bottom:var(--space-4); }
.alert-error { border-color:var(--color-danger); color:var(--color-danger); }
.alert-info { border-color:var(--color-primary); color:var(--color-primary); }

/* 对话框壳（仅视觉；交互留 M3.4） */
.dialog { background:var(--color-surface); border:1px solid var(--color-border);
  border-radius:var(--radius-lg); box-shadow:var(--shadow-md); padding:var(--space-5); max-width:480px; }

/* toast 壳（JS-free 静态 flash；瞬时/自动消失留 M3.4） */
.toast { display:inline-block; padding:var(--space-2) var(--space-4); border-radius:var(--radius-sm);
  background:var(--color-text); color:var(--color-surface); box-shadow:var(--shadow-md); }

/* 空状态 */
.empty-state { text-align:center; color:var(--color-text-muted); padding:var(--space-7) var(--space-4); }

/* 既有纳入：searchbox / pager（M2.4），重着色 */
.searchbox { display:inline-flex; gap:var(--space-2); margin-bottom:var(--space-4); }
.pager { display:flex; gap:var(--space-3); align-items:center; margin-top:var(--space-4);
  color:var(--color-text-muted); font-size:var(--text-sm); }
.pager .count { margin-right:auto; }

/* 一次性 secret 展示（保留既有 .secret class——routes_secret_revoke_test 断言它存在） */
.secret { font-family:var(--font-mono); background:var(--color-bg); color:var(--color-danger);
  padding:var(--space-2) var(--space-3); border-radius:var(--radius-sm); border:1px solid var(--color-border); word-break:break-all; }
```
> 实现者可在此契约内微调视觉，但**不得引入硬编码色**（DS-5）、不得改 class 名（模板依赖）。保留 `.inline-form`/`.stacked-form`/`.secret`/`.searchbox`/`.pager`/`.appname` 等既有 class 名（模板与既有测试依赖）。

- [ ] **步骤 2：补 `layout.html` 的 components `<link>`**（在 base.css 那行后加）
```html
<link rel="stylesheet" href="/static/css/components.css">
```

- [ ] **步骤 3：DS-5 守卫 + 构建 + 既有测试**

运行：
```bash
grep -nE '#[0-9a-fA-F]{3,6}|rgb\(|hsl\(' internal/controlplane/console/static/css/components.css | grep -v 'rgba(16,24,40' || echo "DS-5 OK: components 无硬编码色"
```
> 注：components.css 里允许的例外只有 token 引用；上面 grep 若有命中（除 shadow 的 rgba 已在 tokens，不该出现在 components），需改为 var()。`.toast` 的 `#fff`/`.btn-danger` 的 `#fff` → 改用 `var(--color-on-primary)` 或新增 `--color-on-danger:#fff` 到 tokens。**实现时确保 components.css 零字面色**，必要的「白」走 `--color-on-primary`。
再 `go build ./...`、`go test ./internal/controlplane/console/ -count=1 2>&1 | tail -3`（不回归）。

- [ ] **步骤 4：Commit**
```bash
git add internal/controlplane/console/static/css/components.css internal/controlplane/console/templates/layout.html && \
git commit -m "feat(console): 组件库 components.css(btn/form/table/card/badge/alert/dialog/toast/empty，零硬编码色)"
```

---

## 任务 3：layout.css + shell 重构（layout.html + _appnav.html）

**文件：** 创建 `internal/controlplane/console/static/css/layout.css`；修改 `templates/layout.html`、`templates/_appnav.html`

> **上下文**：现 `layout.html` 是 `{{if .Nav}}topbar{{end}} + <main>{{block content}}`。多数页的 content 自己包了 `<div class="workspace"><aside class="appnav">…</aside><section>…</section></div>`（见 permissions/ops_person）。shell 重构要：topbar 不变语义但重着色；新增 `.app-shell` 容器 + skip link + `<main id="main">` 地标；workspace/sidenav 改走 layout.css 网格。**保持 content block 内既有 `.workspace`/`.appnav`/`.section` class 名**（仅重定义其 CSS），避免逐页大改——页内 sidenav 仍由各页的 content 提供（如 _appnav partial / ops 的 aside）。

- [ ] **步骤 1：创建 `layout.css`（shell 网格 + 响应式，零硬编码色）**

```css
.skip-link { /* 见 base.css，已定义 */ }
/* 顶栏 */
.topbar { display:flex; align-items:center; gap:var(--space-4); padding:0 var(--space-5);
  height:52px; background:var(--color-topbar-bg); color:var(--color-topbar-text); }
.topbar .brand { font-weight:var(--weight-bold); font-size:var(--text-lg); color:var(--color-on-primary); margin-right:var(--space-2); }
.topbar nav { display:flex; gap:var(--space-1); flex:1; flex-wrap:wrap; }
.topbar nav a { color:var(--color-topbar-text); padding:var(--space-2) var(--space-3); border-radius:var(--radius-sm); }
.topbar nav a:hover { color:var(--color-on-primary); background:rgba(255,255,255,.08); text-decoration:none; }
.topbar nav a[aria-current="page"],.topbar nav a.active { color:var(--color-on-primary); background:rgba(255,255,255,.15); }
.topbar .logout button { background:transparent; border:1px solid rgba(255,255,255,.25); color:var(--color-topbar-text);
  padding:var(--space-1) var(--space-3); border-radius:var(--radius-sm); cursor:pointer; }
.topbar .logout button:hover { color:var(--color-on-primary); background:rgba(255,255,255,.1); }
/* 主区 + workspace 网格 */
main { display:block; }
.workspace { display:grid; grid-template-columns:200px 1fr; gap:var(--space-5); padding:var(--space-5); align-items:start; }
.workspace > section { min-width:0; }
/* 上下文侧栏 */
.appnav { display:flex; flex-direction:column; gap:var(--space-1);
  background:var(--color-surface); border:1px solid var(--color-border); border-radius:var(--radius-md); padding:var(--space-3); }
.appnav .appname { font-weight:var(--weight-bold); color:var(--color-text-muted); font-size:var(--text-sm); margin-bottom:var(--space-2); }
.appnav a { color:var(--color-text); padding:var(--space-2) var(--space-3); border-radius:var(--radius-sm); }
.appnav a:hover { background:var(--color-bg); text-decoration:none; }
.appnav a[aria-current="page"],.appnav a.active { background:var(--color-bg); color:var(--color-primary); font-weight:var(--weight-medium); }
/* 无侧栏页（system 域等）：content 直接是 section 时给内边距 */
main > section { padding:var(--space-5); max-width:1100px; }
/* breadcrumb 壳（文案补全留 M3.4） */
.breadcrumb { color:var(--color-text-muted); font-size:var(--text-sm); margin-bottom:var(--space-3); }
/* 响应式：窄屏 sidenav 堆叠、nav 换行（纯 CSS 无 JS） */
@media (max-width:720px) {
  .workspace { grid-template-columns:1fr; }
  .topbar { height:auto; padding:var(--space-2) var(--space-4); flex-wrap:wrap; }
}
```
> `rgba(255,255,255,.08)` 这类「白色叠加」是 topbar hover 的中性叠层，非语义色——可接受保留（DS-5 grep 排除 `rgba(255,255,255` 白叠层）。其余一律 var()。

- [ ] **步骤 2：改 `layout.html`——加 skip link + `<main id="main">` + topbar `aria-current` + role**

```html
{{define "layout.html"}}<!DOCTYPE html>
<html lang="zh">
<head><meta charset="utf-8"><title>{{block "title" .}}司域 Console{{end}}</title>
<link rel="stylesheet" href="/static/css/tokens.css">
<link rel="stylesheet" href="/static/css/base.css">
<link rel="stylesheet" href="/static/css/layout.css">
<link rel="stylesheet" href="/static/css/components.css">
<link rel="stylesheet" href="/static/app.css"></head>
<body>
<a class="skip-link" href="#main">跳到主内容</a>
{{if .Nav}}
<header class="topbar"><span class="brand">司域 Sydom</span>
  <nav aria-label="主导航"><a href="/" {{if eq .Nav "apps"}}aria-current="page"{{end}}>应用</a>
  <a href="/tenants" {{if eq .Nav "tenants"}}aria-current="page"{{end}}>租户</a>
  <a href="/operators" {{if eq .Nav "system"}}aria-current="page"{{end}}>系统</a></nav>
  <form method="post" action="/logout" class="logout"><button>登出</button></form>
</header>{{end}}
<main id="main">{{block "content" .}}{{end}}</main>
</body></html>{{end}}
```
> 注：本任务仍保留 `app.css` 的 `<link>`（最后一条，过渡兜底），任务 7 删。`aria-current` 替代 `class="active"`（layout.css 同时认两者，过渡安全）。

- [ ] **步骤 3：改 `_appnav.html`——`aria-current` + 包一层 `<nav aria-label>`**（appnav 是 app 域侧栏 partial）

把外层 `<aside class="appnav">…</aside>` 内的 `{{if eq .Tab "x"}}class="active"{{end}}` 改为 `{{if eq .Tab "x"}}aria-current="page"{{end}}`，并给 aside 加 `aria-label`：
```html
{{define "appnav"}}
<aside class="appnav" aria-label="应用导航"><div class="appname">App #{{.AppID}}</div>
<a href="/apps/{{.AppID}}/roles" {{if eq .Tab "roles"}}aria-current="page"{{end}}>角色</a>
... (其余各项同样 class="active" → aria-current="page") ...
</aside>{{end}}
```

- [ ] **步骤 4：构建 + 既有测试不回归**

`go build ./...`、`go test ./internal/controlplane/console/ -count=1 2>&1 | tail -5`。**重点**：既有测试断言文本（"共 3"/"搜索"/principal/动作名）+ `class="secret"`——shell 重构不动这些，应全绿。若某测试断言了 `class="active"` 字面（grep 确认：`grep -rn 'class=\\"active\\"' internal/controlplane/console/*_test.go`，预期无），则调整。DS-5 守卫：`grep -nE '#[0-9a-fA-F]{3,6}|rgb\(' internal/controlplane/console/static/css/layout.css | grep -vE 'rgba\(255,255,255'`（预期空）。

- [ ] **步骤 5：Commit**
```bash
git add internal/controlplane/console/static/css/layout.css internal/controlplane/console/templates/layout.html internal/controlplane/console/templates/_appnav.html && \
git commit -m "feat(console): shell 重构(topbar/sidenav/workspace 网格 + skip link + aria-current 响应式)"
```

---

## 任务 4：旗舰页迁移 login + dashboard

**文件：** 修改 `templates/login.html`、`templates/dashboard.html`

- [ ] **步骤 1：改 `login.html`——居中品牌卡，用新组件 class**

```html
{{define "title"}}登录 · 司域 Console{{end}}
{{define "content"}}
<div class="login-wrap">
  <div class="card login-card">
    <h1>司域 Console</h1>
    {{if .Error}}<div class="alert alert-error" role="alert">{{.Error}}</div>{{end}}
    <form method="post" action="/login" class="stacked-form">
      <div class="form-field"><label for="principal">Principal</label>
        <input id="principal" name="principal" autofocus></div>
      <div class="form-field"><label for="secret">Secret</label>
        <input id="secret" name="secret" type="password"></div>
      <button type="submit" class="btn btn-primary">登录</button>
    </form>
  </div>
</div>
{{end}}
```
并在 `layout.css` 末尾加（登录页无 .Nav、无 workspace 网格，需自己居中）：
```css
.login-wrap { min-height:100vh; display:grid; place-items:center; padding:var(--space-5); }
.login-card { width:100%; max-width:360px; }
```

- [ ] **步骤 2：改 `dashboard.html`——卡片化 + 空状态 + badge 状态 + 新按钮**

参照现 `dashboard.html` 结构（降级分支 + 非降级 Apps 表 + searchbox/pager from M2.4），重构为：降级分支用 `.alert-info` + `.btn`；非降级 Apps 表用 `.table`，状态列用 `.badge`/`.badge-success`，操作按钮用 `.btn .btn-sm`（切换状态）/`.btn-ghost`（轮换密钥）；空列表（`{{else}}` of range）给 `.empty-state`。新建按钮用 `.btn .btn-primary`。**保留所有既有 query/表单 action/CSRF/searchbox/pager partial 调用与字段**（DS-7）。

> 实现者：读现 dashboard.html，逐元素套 class，不增删功能、不改 action/name/CSRF。状态文案 `{{if eq .Status 1}}启用{{else}}停用{{end}}` 包进 `<span class="badge {{if eq .Status 1}}badge-success{{end}}">`。

- [ ] **步骤 3：构建 + 既有测试 + DS-5**

`go build ./...`、`go test ./internal/controlplane/console/ -count=1 2>&1 | tail -3`（dashboard/login 相关测试断言文本，应绿）。DS-5 grep layout.css（含新 login-wrap，预期无硬编码色）。

- [ ] **步骤 4：Commit**
```bash
git add internal/controlplane/console/templates/login.html internal/controlplane/console/templates/dashboard.html internal/controlplane/console/static/css/layout.css && \
git commit -m "feat(console): 旗舰页 login+dashboard 迁移设计系统(卡片/badge/空状态/新按钮)"
```

---

## 任务 5：旗舰页迁移 ops_person + permissions

**文件：** 修改 `templates/ops_person.html`、`templates/permissions.html`

- [ ] **步骤 1：改 `ops_person.html`（运营台业务旗舰）**

逐元素套 class（**不改 action/name/CSRF/字段/逻辑**，DS-7）：
- 查询表单 → `class="inline-form"`（已是），button → `class="btn btn-primary"`
- 角色/能力 `<ul>` → 加 `class="list-plain"`（在 components.css 加 `.list-plain{list-style:none;padding:0;margin:0;display:flex;flex-direction:column;gap:var(--space-1)}` 或直接复用默认 ul 样式）；空态 `<p>暂无…</p>` → `<p class="empty-state">…</p>`
- 「调整角色」select+button → `.select` + `.btn`
- 移除按钮 → `.btn .btn-sm .btn-danger`
- 数据范围 `<table>` → `.table`
- 顶部 `<section>` 前加 breadcrumb 壳：`<nav class="breadcrumb" aria-label="面包屑">运营台 · 人员能力</nav>`（M3.4 补全层级）

- [ ] **步骤 2：改 `permissions.html`（建模台代表，最密集）**

现结构：appnav + section（h2 + 建权限点 post 表单 + searchbox + 排序表头 table + pager）。套 class（**保留 M2.4 的 searchbox/pager/排序链接 sortHref + 所有 name/CSRF**，DS-7）：
- 建表单 `class="inline-form"`（已是）→ 各 input 默认走 base/components 输入样式；button → `.btn .btn-primary`
- `<table>` → `.table`（排序表头链接保持不变）
- 来源列若想加 badge：`<td><span class="badge">{{.Source}}</span></td>`（可选，保持简单）

- [ ] **步骤 3：构建 + 既有测试 + DS-5**

`go build ./...`、`go test ./internal/controlplane/console/ -count=1 2>&1 | tail -5`（permissions 分页测试 `TestConsole_Permissions_*` + ops 测试断言文本/链接，应全绿）。若任务 5 在 components.css 加了 `.list-plain`，重跑 DS-5 grep（预期无硬编码色）。

- [ ] **步骤 4：Commit**
```bash
git add internal/controlplane/console/templates/ops_person.html internal/controlplane/console/templates/permissions.html internal/controlplane/console/static/css/components.css && \
git commit -m "feat(console): 旗舰页 ops_person+permissions 迁移设计系统"
```

---

## 任务 6：a11y 强化 + Playwright 视觉走查 + axe 冒烟 + 对比度核验

**文件：** 可能微调 5 个旗舰模板补 a11y 属性；新增走查脚本/截图（归档，不入测试包）

- [ ] **步骤 1：a11y 静态核验（旗舰页逐条对 spec §8）**

逐个旗舰页确认：单一 `<h1>`（注意现多数页用 `<h2>` 作页标题——本步把旗舰页的页主标题升为 `<h1>`，或确认 layout 的标题层级合理）、每 input 有关联 `<label for>`（login 已加 for；permissions/dashboard 的建表单 input 多为 placeholder-only → 加 `aria-label` 或 visually-hidden label）、图标/无文字按钮加 `aria-label`、错误条 `role="alert"`、表格 `<th scope="col">`。改动仅加属性（DS-7 不动功能）。

- [ ] **步骤 2：Playwright 视觉走查（5 旗舰面，浅色）**

用项目既有 Playwright 走查范式（参照历史 SP3「N 屏走查」）：起 console（compose 或测试夹具 + 种子 root@sydom），登录后逐面截图：login、dashboard、apps→permissions、/ops/apps/{id}/people/view（ops_person）、+ shell（topbar/sidenav）。截图归档到 `docs/` 或 worktree 临时目录，人工核对设计系统生效、无破版。**这是验收走查不是自动断言**——记录结论。

- [ ] **步骤 3：axe-core a11y 冒烟（旗舰页）**

用 Playwright 注入 axe-core（`@axe-core/playwright` 或 CDN 注入 axe.min.js 后 `axe.run()`），对 5 旗舰页跑，断言**无 critical 违规**。逐页记录 violations 摘要。**若 axe 集成在本环境成本过高/不可用 → 回退**：执行 spec §8 的人工 a11y 检查清单（键盘 Tab 走查截图 + 逐条勾选），并在汇报中说明用了回退路径及结果。

- [ ] **步骤 4：对比度核验**

列出关键语义配对的对比值并确认 ≥ AA 4.5:1（正文）：`--color-text`(#1a1a2e) on `--color-surface`(#fff)、`--color-text-muted`(#6b7280) on `--color-surface`、`--color-on-primary`(#fff) on `--color-primary`(#2f5ad9)、topbar text on topbar bg。任一不达标则调 token 值（在 tokens.css 调，组件零改）。记录对比值表。

- [ ] **步骤 5：Commit（若有 a11y 属性微调）**
```bash
git add internal/controlplane/console/templates/ && \
git commit -m "feat(console): 旗舰页 a11y 强化(h1/label/aria/scope) + 走查/axe/对比度核验记录"
```

---

## 任务 7：删旧 app.css + 全量验证 + DS-1..DS-7 + FF 合并

- [ ] **步骤 1：确认无引用后删 `app.css`**

```bash
grep -rn "app.css" internal/controlplane/console/ | grep -v "static/app.css 的内容"
```
确认仅 `layout.html` 的过渡 `<link>` 引用它（及 app.css 文件本身）。删除 `layout.html` 里 `<link rel="stylesheet" href="/static/app.css">` 那行 + 删文件 `internal/controlplane/console/static/app.css`。

- [ ] **步骤 2：删后回归（关键——确认新 4 文件已覆盖所有用到的 class）**

`go build ./...`、`go test ./internal/controlplane/console/ -count=1`（全绿）。若有页面因删 app.css 丢样式（用到了 app.css 独有、新文件未覆盖的 class），补进 components.css/layout.css（仍零硬编码色）。**重点回归未迁的 ~25 页**：它们的 class（如 .secret/.inline-form/.appname/各页特有）必须在新 4 文件里有定义——任务 2/3 已纳入主要既有 class，本步补漏。

- [ ] **步骤 3：DS-1..DS-7 逐条核验**

```bash
BASE=<worktree base>
# DS-1 后端零触碰
git diff $BASE..HEAD -- internal/controlplane/mgmt internal/controlplane/adminauthz internal/sidecar | wc -l   # 预期 0
git diff $BASE..HEAD --stat -- internal/controlplane/console/*.go    # 仅期望 render.go(若动 FuncMap—本计划未动)/无 handler 逻辑改；理想为空或仅注释
# DS-2 零新增 JS
git diff $BASE..HEAD --stat -- internal/controlplane/console/static/*.js   # 预期空（datapolicy.js 未改）
ls internal/controlplane/console/static/*.js   # 仅 datapolicy.js
# DS-5 深色就绪
grep -rnE '#[0-9a-fA-F]{3,6}|rgb\(|hsl\(' internal/controlplane/console/static/css/components.css internal/controlplane/console/static/css/layout.css internal/controlplane/console/static/css/base.css | grep -vE 'rgba\(255,255,255|rgba\(16,24,40' || echo "DS-5 OK"
```
DS-3 服务端渲染（无客户端框架，仍 html/template）；DS-4 既有测试全绿 + 未迁页渲染正常（步骤2）；DS-6 任务6 axe/对比度通过；DS-7 表单/CSRF/链接功能不变（既有测试覆盖）。

- [ ] **步骤 4：格式/静态/全量测试**

`gofmt -l internal/`（空——本计划基本不动 .go，若动则格式化）、`go vet ./...`（净）、`go test ./...`（0 FAIL，含 console testcontainers）。

- [ ] **步骤 5：更新进度记忆**

`project_detailed_design_progress.md` 加 M3.1 节 + `MEMORY.md` 索引指针（M3.1 完成、M3 余 M3.2–M3.4）。

- [ ] **步骤 6：FF 合并本地 main（不 push origin）**

worktree 全绿 + 评审 READY 后 FF 并入本地 main，清 worktree。

---

## 自检记录

**规格覆盖度（对照 spec）：** §3 分层 CSS→任务1/2/3 ✓；§4 tokens→任务1 ✓；§5 组件→任务2 ✓；§6 shell→任务3 ✓；§7 旗舰页→任务4/5 ✓；§8 a11y→任务3(focus/skip/aria-current)+任务6(h1/label/aria/scope/axe) ✓；§9 测试→各任务既有测试绿 + 任务6 视觉/axe/对比度 ✓；§10 DS-1..DS-7→任务7 逐条 ✓；§11 YAGNI→计划全程未触延后项（toast/dialog 仅 CSS 壳、无 JS、无深色切换、无 i18n） ✓。

**与 spec 的偏差（已记录）：** CSS 文件落 `static/css/` 子目录（spec §3 illustrative 一致）；过渡期 `app.css` 与新 4 文件并存到任务 7 删（避免中途丢样式破未迁页）；`class="active"` → `aria-current="page"`，layout.css 过渡期同时认两者。

**类型/契约一致性：** token 名（--color-*/--space-*/--radius-*/--text-*）、组件 class（.btn/.form-field/.table/.card/.badge/.alert/.dialog/.toast/.empty-state/.searchbox/.pager/.secret/.inline-form/.appname/.workspace/.appnav）跨任务一致；保留既有 class 名（模板 + routes_secret_revoke_test 依赖 .secret）。

**占位符扫描：** tokens/base/components/layout 均给具体 CSS；旗舰页给具体 markup 或「逐元素套既定 class」的精确指令（permissions/dashboard 因含 M2.4 既有结构，指令为「套 class 不改功能」而非整段重贴——避免与既有 searchbox/pager/sortHref 冲突）。axe 给了「优先 + 回退清单」明确二选一。
