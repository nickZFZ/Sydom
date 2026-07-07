# M5.2a 安全响应头基座 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 给控制面 Console（HTML BFF）与 REST 网关（JSON API）装上安全响应头（分面 CSP + nosniff/X-Frame-Options/Referrer-Policy/Permissions-Policy + 条件 HSTS），达成**严格 CSP 无 `'unsafe-inline'`**（连带从源清理 6 个内联 `style=`），**零触碰授权/求值/HMAC 认证核心**。

**架构：** 新 `internal/secheaders` 包（无状态，`Console(secure)`/`API(secure)` 两个 `func(http.Handler) http.Handler` 中间件，纯设响应头后透传 next）；在 `app.Run` 装配层组合于 M5.1 `obs.HTTPMiddleware` **外层**；严格 CSP 需把 Console 模板 6 个内联 `style=` 外提为 CSS 工具类，并把 `datapolicy.js` 的 `#builder`/`#builder-toggle` 揭示逻辑从 `style.display` 改为 `classList.remove("hidden")`（否则类的 `display:none` 盖过清空的内联 style）。

**技术栈：** Go、`net/http` 中间件、`httptest`、testify；Console 静态资产（html/template + go:embed CSS/JS）；真浏览器走查（Playwright MCP + axe-core，build-tag 脚手架复用 `newConsole`+dbtest）。

**规格：** `docs/superpowers/specs/2026-07-07-sydom-m5-2a-security-headers-design.md`（BASE=main `28f4b85`；含 SH-1..7）。

**范式纪律：** 子代理驱动 + 两阶段审查（规格→质量）；TDD；每任务独立 commit；**禁 `--amend`**。**零触碰判定/求值/认证核心**：`casbin/`、`internal/controlplane/adminauthz/`、`internal/sidecar/{kernel,dataperm,authz}/`、`internal/auth/`、`internal/obs/` 内容 diff=0；改动只在新 `internal/secheaders/` + `app/run.go` 接线 + Console 表现层（模板/CSS/datapolicy.js）。

---

## 文件结构（先锁定分解）

| 文件 | 职责 | 任务 |
|---|---|---|
| `internal/secheaders/secheaders.go` | `Console`/`API` 中间件 + 共享 `writeCommon` + 头集契约 | 1 |
| `internal/secheaders/secheaders_test.go` | 四态头集断言 + HSTS 条件 + SH-2 无 unsafe-inline + 透传 | 1 |
| `internal/controlplane/console/static/css/components.css` | 追加工具类 `.hidden`/`.mb-4`/`.d-inline`（末尾工具区，纯加法） | 2 |
| `internal/controlplane/console/templates/ops_role_simulate.html` | 1 个内联 `style=` → 类 | 2 |
| `internal/controlplane/console/templates/ops_tenant_template.html` | 3 个内联 `style=` → 类 | 2 |
| `internal/controlplane/console/templates/datapolicies.html` | 2 个内联 `style=display:none` → `class="hidden"` | 2 |
| `internal/controlplane/console/static/datapolicy.js` | 揭示逻辑 `style.display`→`classList.remove("hidden")` | 2 |
| `internal/controlplane/console/templates_lint_test.go` | 回归：模板无内联 `style=` 残留（SH-6） | 2 |
| `internal/controlplane/app/run.go` | 推导 `secure`；REST 包 `secheaders.API`、Console 包 `secheaders.Console`（obs 中间件外层） | 3 |
| `docs/superpowers/2026-07-07-m5-2a-security-headers-walkthrough.md` | SH-1..7 核验 + curl -I + 真浏览器走查记录 | 4 |

**关键决策：** secheaders 独立成包（单一职责，与 obs 观测性分离）；`secure` 复用既有 `!cfg.ConsoleCookieInsecure`（部署 HTTPS 声明信号，与 cookie `Secure` 同源），**不新增 flag**；ops 端口（`obs.OpsHandler`）**不套**安全头（非 HTML/非公网、明文健康探针，加 HSTS 反有锁死风险）；datapolicy.js 用「JS 起时 `classList.remove('hidden')` 一次接管、后续沿用 `style.display`」——最小改动且 CSSOM 的 `element.style` 不受 CSP style-src 约束。

---

## 任务 1：`internal/secheaders` 包（分面安全头中间件）

**文件：**
- 创建：`internal/secheaders/secheaders.go`、`internal/secheaders/secheaders_test.go`

参考既有：`internal/obs/http.go` 的 `HTTPMiddleware(logger, next) http.Handler` 中间件形态（本包中间件签名用更常规的 `func(http.Handler) http.Handler` 装饰器，便于 `app.Run` 组合）。

- [ ] **步骤 1：写失败测试 `internal/secheaders/secheaders_test.go`（TDD）**

```go
package secheaders

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// okHandler 是被包裹的下游：写一个可辨识的 body + 200，用于验证透传。
func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("downstream-ok"))
	})
}

func do(h http.Handler) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	return rec
}

func TestConsole_HeadersSecure(t *testing.T) {
	rec := do(Console(true)(okHandler()))
	h := rec.Header()
	require.Equal(t, "nosniff", h.Get("X-Content-Type-Options"))
	require.Equal(t, "DENY", h.Get("X-Frame-Options"))
	require.Equal(t, "no-referrer", h.Get("Referrer-Policy"))
	require.Equal(t, "geolocation=(), camera=(), microphone=()", h.Get("Permissions-Policy"))
	csp := h.Get("Content-Security-Policy")
	require.Contains(t, csp, "default-src 'self'")
	require.Contains(t, csp, "script-src 'self'")
	require.Contains(t, csp, "style-src 'self'")
	require.Contains(t, csp, "object-src 'none'")
	require.Contains(t, csp, "frame-ancestors 'none'")
	require.Contains(t, csp, "base-uri 'self'")
	require.Contains(t, csp, "form-action 'self'")
	// SH-2：严格 CSP，全程无 unsafe-inline / unsafe-eval / nonce / hash。
	require.NotContains(t, csp, "unsafe-inline")
	require.NotContains(t, csp, "unsafe-eval")
	// SH-3：secure=true 下发 HSTS。
	require.Equal(t, "max-age=63072000; includeSubDomains", h.Get("Strict-Transport-Security"))
	// 透传：下游 body + status 不变。
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "downstream-ok", rec.Body.String())
}

func TestConsole_NoHSTSWhenInsecure(t *testing.T) {
	rec := do(Console(false)(okHandler()))
	// SH-3：明文部署绝不下发 HSTS（防浏览器强制 HTTPS 锁死本地）。
	require.Empty(t, rec.Header().Get("Strict-Transport-Security"))
	// 其余安全头仍在。
	require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	require.Contains(t, rec.Header().Get("Content-Security-Policy"), "default-src 'self'")
}

func TestAPI_HeadersLockedDown(t *testing.T) {
	rec := do(API(true)(okHandler()))
	h := rec.Header()
	// SH-4：REST（JSON）锁死 CSP——不渲染任何内容。
	require.Equal(t, "default-src 'none'; frame-ancestors 'none'", h.Get("Content-Security-Policy"))
	require.Equal(t, "nosniff", h.Get("X-Content-Type-Options"))
	require.Equal(t, "DENY", h.Get("X-Frame-Options"))
	require.Equal(t, "no-referrer", h.Get("Referrer-Policy"))
	require.Equal(t, "max-age=63072000; includeSubDomains", h.Get("Strict-Transport-Security"))
	// API 面不应带 HTML 面才有的 Permissions-Policy。
	require.Empty(t, h.Get("Permissions-Policy"))
	require.Equal(t, "downstream-ok", rec.Body.String())
}

func TestAPI_NoHSTSWhenInsecure(t *testing.T) {
	rec := do(API(false)(okHandler()))
	require.Empty(t, rec.Header().Get("Strict-Transport-Security"))
	require.Equal(t, "default-src 'none'; frame-ancestors 'none'", rec.Header().Get("Content-Security-Policy"))
}

// SH-4：两面头集互不串味——Console 有 Permissions-Policy 且 CSP 允许 'self' 脚本；
// API 无 Permissions-Policy 且 CSP 为 default-src 'none'。
func TestSurfaceHeadersDoNotBleed(t *testing.T) {
	con := do(Console(true)(okHandler())).Header()
	api := do(API(true)(okHandler())).Header()
	require.NotEqual(t, con.Get("Content-Security-Policy"), api.Get("Content-Security-Policy"))
	require.NotEmpty(t, con.Get("Permissions-Policy"))
	require.Empty(t, api.Get("Permissions-Policy"))
	require.False(t, strings.Contains(api.Get("Content-Security-Policy"), "script-src"))
}
```

运行确认失败：`go test ./internal/secheaders/ -v` → 编译失败（包未建）。

- [ ] **步骤 2：写 `internal/secheaders/secheaders.go`**

```go
// Package secheaders 提供按内容类型裁剪的安全响应头中间件：Console（HTML BFF）与 API（JSON）。
// 纯设响应头后透传 next——不改 body、不改 status、不吞 next 返回；观测性/授权无关。
// HSTS 仅在 secure=true（部署已声明 HTTPS）下发，防明文部署被浏览器强制 HTTPS 锁死。
package secheaders

import "net/http"

const hstsValue = "max-age=63072000; includeSubDomains" // 2 年 + 子域

// cspConsole 是 Console（HTML）的严格 CSP：纯 'self' 白名单，无 unsafe-inline/eval/nonce。
// Console 全部 CSS/JS 外链（/static/…），无内联脚本/样式，故 'self' 足够。
const cspConsole = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; " +
	"object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'"

// cspAPI 是 REST（JSON）的锁死 CSP：API 不渲染任何内容，一律禁止。
const cspAPI = "default-src 'none'; frame-ancestors 'none'"

// writeCommon 设两面共有的安全头；secure=true 时附加 HSTS。
func writeCommon(h http.Header, secure bool) {
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	if secure {
		h.Set("Strict-Transport-Security", hstsValue)
	}
}

// Console 返回 HTML 面安全头中间件（完整 CSP + Permissions-Policy）。
func Console(secure bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			writeCommon(h, secure)
			h.Set("Content-Security-Policy", cspConsole)
			h.Set("Permissions-Policy", "geolocation=(), camera=(), microphone=()")
			next.ServeHTTP(w, r)
		})
	}
}

// API 返回 JSON 面安全头中间件（锁死 CSP，无 Permissions-Policy）。
func API(secure bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			writeCommon(h, secure)
			h.Set("Content-Security-Policy", cspAPI)
			next.ServeHTTP(w, r)
		})
	}
}
```

> 头必须在 `next.ServeHTTP`（下游首次 `WriteHeader`/`Write`）**之前**设置——本实现即如此。中间件返回 `func(http.Handler) http.Handler`，便于 `app.Run` 以 `secheaders.Console(secure)(inner)` 组合。

- [ ] **步骤 3：运行确认通过 + gofmt + Commit**

运行：`go test ./internal/secheaders/ -v`（全绿）；`gofmt -l internal/secheaders/`（应空）；`go vet ./internal/secheaders/`；`go build ./...`。
```bash
git add internal/secheaders/secheaders.go internal/secheaders/secheaders_test.go
git commit -m "$(cat <<'EOF'
feat(secheaders): M5.2a 分面安全响应头中间件(Console 完整严格 CSP+Permissions-Policy,API 锁死 default-src none,共享 nosniff/X-Frame/Referrer,HSTS 仅 secure 下发)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
EOF
)"
```
> **禁 --amend**。尾部带 Co-Authored-By 行。

---

## 任务 2：从源清理 6 个内联 `style=` → CSS 工具类 + datapolicy.js 揭示逻辑 + 回归测试

**文件：**
- 修改：`internal/controlplane/console/static/css/components.css`（末尾工具区追加，纯加法）
- 修改：`internal/controlplane/console/templates/ops_role_simulate.html`、`ops_tenant_template.html`、`datapolicies.html`
- 修改：`internal/controlplane/console/static/datapolicy.js`
- 创建：`internal/controlplane/console/templates_lint_test.go`

参考既有：`components.css` 已有 `.inline-form { display:inline-flex; … }`；`base.css` 已有 `.visually-hidden`。datapolicy.js 现以 `el.style.display` 揭示 `#builder`/`#builder-toggle`（`toggle.style.display=""` 于 init、`builder.style.display=""` 于 `setMode(false)`）。

- [ ] **步骤 1：写失败的回归测试 `internal/controlplane/console/templates_lint_test.go`（先证能捕获回归）**

```go
package console

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTemplates_NoInlineStyle 守住严格 CSP（SH-6）：模板不得含内联 style= 属性
// （style-src 'self' 不含 unsafe-inline，内联 style= 会被 CSP 拒）。有人再加即 FAIL。
func TestTemplates_NoInlineStyle(t *testing.T) {
	dir := "templates"
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".html") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		require.NoError(t, err)
		require.NotContains(t, string(b), "style=\"",
			"%s 含内联 style= 属性——严格 CSP style-src 'self' 会拒；请外提为 CSS 工具类", e.Name())
	}
}
```

- [ ] **步骤 2：运行确认失败（证明测试有齿）**

运行：`go test ./internal/controlplane/console/ -run TestTemplates_NoInlineStyle -v`
预期：FAIL——报 `datapolicies.html`/`ops_role_simulate.html`/`ops_tenant_template.html` 含 `style="`（当前 6 处内联样式）。**这证明测试能捕获回归。**

- [ ] **步骤 3：`components.css` 末尾追加工具类（纯加法）**

在 `internal/controlplane/console/static/css/components.css` 文件**末尾**追加：
```css

/* —— 工具类（M5.2a：内联 style= 外提，支撑严格 CSP style-src 'self'）—— */
.hidden { display: none; }
.mb-4 { margin-bottom: var(--space-4); }
.d-inline { display: inline; }
```
> `.d-inline` 定义在 `.inline-form`（`display:inline-flex`）**之后**，同为单类选择器 → 后定义者胜，`class="inline-form d-inline"` 得 `display:inline`（复刻原 `style="display:inline"` 语义）。

- [ ] **步骤 4：改 3 个模板（内联 style= → 类）**

`ops_role_simulate.html:9`：
```
<div class="card" style="margin-bottom:var(--space-4)">
```
→
```
<div class="card mb-4">
```

`ops_tenant_template.html`（3 处）：`:9` 与 `:16` 同上把 `<div class="card" style="margin-bottom:var(--space-4)">` → `<div class="card mb-4">`；`:27`
```
<form method="post" action="/ops/apps/{{.AppID}}/tenant-templates/{{.TemplateID}}/delete" class="inline-form" style="display:inline" data-confirm="确定删除该模板吗？此操作不可撤销。">
```
→（去掉 `style="display:inline"`，`class` 加 `d-inline`）
```
<form method="post" action="/ops/apps/{{.AppID}}/tenant-templates/{{.TemplateID}}/delete" class="inline-form d-inline" data-confirm="确定删除该模板吗？此操作不可撤销。">
```

`datapolicies.html:13-14`：
```
<button type="button" id="builder-toggle" style="display:none">专业模式（原始 JSON）</button>
<div id="builder" style="display:none"></div>
```
→
```
<button type="button" id="builder-toggle" class="hidden">专业模式（原始 JSON）</button>
<div id="builder" class="hidden"></div>
```

- [ ] **步骤 5：改 `datapolicy.js` 揭示逻辑（`style.display`→`classList`）**

在 `internal/controlplane/console/static/datapolicy.js` 找到 init 揭示行（现为）：
```js
    toggle.style.display = ""; // JS 在跑，露出切换按钮
    setMode(startRaw);
```
替换为：
```js
    // JS 在跑：移除 no-JS 隐藏类（.hidden），改由下方 setMode 的 style.display 接管显隐。
    // 注：CSP style-src 'self' 不约束 element.style（CSSOM），但会挡 .hidden 的 display:none——
    // 故此处必须 remove 类，否则清空内联 style 仍被类的 display:none 盖住，构建器/切换按钮永不显示。
    toggle.classList.remove("hidden");
    builder.classList.remove("hidden");
    setMode(startRaw);
```
> `setMode` 内既有的 `builder.style.display = ""`/`"none"`、`textarea.style.display` 等**不改**（CSSOM，CSP 豁免，`.hidden` 已移除后正常生效）。无 JS 基线：模板 `class="hidden"` → builder/toggle 隐藏、textarea（原生 required）可见——与原 `style=display:none` 行为一致。

- [ ] **步骤 6：运行回归测试确认通过 + 构建 + gofmt + Commit**

运行：`go test ./internal/controlplane/console/ -run TestTemplates_NoInlineStyle -v`（PASS——无内联 style 残留）；`go test ./internal/controlplane/console/ -count=1`（既有 console 测试全绿，确认模板/JS 改动未破坏渲染路径）；`gofmt -l internal/controlplane/console/`（应空）；`go build ./...`。
```bash
git add internal/controlplane/console/static/css/components.css internal/controlplane/console/templates/ops_role_simulate.html internal/controlplane/console/templates/ops_tenant_template.html internal/controlplane/console/templates/datapolicies.html internal/controlplane/console/static/datapolicy.js internal/controlplane/console/templates_lint_test.go
git commit -m "$(cat <<'EOF'
refactor(console): M5.2a 6 内联 style= 外提为 CSS 工具类+datapolicy.js 揭示改 classList(支撑严格 CSP style-src self,无 unsafe-inline)+模板无内联样式回归测试

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
EOF
)"
```
> **禁 --amend**。datapolicies 构建器切换的真浏览器验证在任务 4（渐进增强 JS 接管路径须真浏览器走查——呼应 M4.3 教训）。

---

## 任务 3：接线（`app.Run` 推导 secure + REST/Console 包 secheaders 中间件）

**文件：**
- 修改：`internal/controlplane/app/run.go`

参考既有（post-M5.1，行号约值以实际为准）：`run.go:120` `restSrv = &http.Server{Handler: m.HTTPMiddleware(logger, restgw.NewHandler(...))}`；`run.go:136-137` `consoleSrv = &http.Server{Handler: m.HTTPMiddleware(logger, console.NewHandler(..., !cfg.ConsoleCookieInsecure))}`；`cfg.ConsoleCookieInsecure bool` 存在。

- [ ] **步骤 1：在 `app.Run` 早期推导 `secure`（建 metrics `m := obs.New()` 附近、REST/Console 块之前）**

在 `m := obs.New()` 之后加一行：
```go
	secure := !cfg.ConsoleCookieInsecure // 部署已声明 HTTPS（与 cookie Secure 同源信号）→ 下发 HSTS
```
import 追加 `"github.com/nickZFZ/Sydom/internal/secheaders"`。

- [ ] **步骤 2：REST handler 外包 `secheaders.API(secure)`**

把
```go
		restSrv = &http.Server{Handler: m.HTTPMiddleware(logger, restgw.NewHandler(adminSrv, operatorResolver, enforcer, db, logger))}
```
改为
```go
		restSrv = &http.Server{Handler: secheaders.API(secure)(m.HTTPMiddleware(logger, restgw.NewHandler(adminSrv, operatorResolver, enforcer, db, logger)))}
```

- [ ] **步骤 3：Console handler 外包 `secheaders.Console(secure)`**

把
```go
		consoleSrv = &http.Server{Handler: m.HTTPMiddleware(logger, console.NewHandler(
			adminSrv, operatorResolver, enforcer, db, store, logger, !cfg.ConsoleCookieInsecure))}
```
改为
```go
		consoleSrv = &http.Server{Handler: secheaders.Console(secure)(m.HTTPMiddleware(logger, console.NewHandler(
			adminSrv, operatorResolver, enforcer, db, store, logger, !cfg.ConsoleCookieInsecure)))}
```
> secheaders 在**外层**（先设响应头再交给 obs 中间件 → 实际 handler）。obs 的 statusRecorder 不受影响（安全头在 next 写 body 前落定）。ops 端口（`obs.OpsHandler`）**不套**安全头。

- [ ] **步骤 4：验证 + 零触碰 + gofmt + Commit**

运行：`gofmt -l internal/controlplane/app/`（应空）；`go vet ./...`；`go build ./...`；`go test ./internal/controlplane/app/ -count=1`（既有 app 装配测试全绿）。
零触碰核验（务必跑并在汇报中贴结果）：
```bash
git diff main..HEAD -- casbin internal/controlplane/adminauthz internal/sidecar/kernel internal/sidecar/dataperm internal/sidecar/authz internal/auth internal/obs | wc -l
```
预期 **0**（授权/求值/HMAC 认证/M5.1 obs 核心内容零改）。
```bash
git add internal/controlplane/app/run.go
git commit -m "$(cat <<'EOF'
feat(cp): M5.2a 接线安全响应头(REST 包 secheaders.API+Console 包 secheaders.Console 于 obs 中间件外层;secure 复用 !ConsoleCookieInsecure;ops 端口不套)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
EOF
)"
```
> **禁 --amend**。

---

## 任务 4：整体核验 SH-1..7 + curl -I + 真浏览器走查 + 最终评审 + FF

**文件：** 无代码改动（除走查涌现修复）；产出 `docs/superpowers/2026-07-07-m5-2a-security-headers-walkthrough.md`。

- [ ] **步骤 1：SH-1 零触碰机器核验**

```bash
BASE=$(git merge-base main HEAD)
git diff $BASE..HEAD -- casbin internal/controlplane/adminauthz internal/sidecar/kernel internal/sidecar/dataperm internal/sidecar/authz internal/auth internal/obs
```
预期：**无输出**（授权/求值/HMAC/obs 核心内容 diff=0）。

- [ ] **步骤 2：全量验证**

```bash
gofmt -l internal/          # 空
go vet ./...                # 干净
go test ./...               # 0 FAIL（含 secheaders + console lint + 既有全量）
```

- [ ] **步骤 3：curl -I 抓头演示（SH-7）**

用 build-tag 脚手架或 `go run` 起控制面（Console + REST 监听器；`ConsoleCookieInsecure` 两态各一次以验 HSTS 条件），`curl -sI http://<console-addr>/` 与 `curl -sI http://<rest-addr>/v1/...`，核验：
- Console 响应含 `Content-Security-Policy: default-src 'self'; …`（无 unsafe-inline）、`X-Content-Type-Options: nosniff`、`X-Frame-Options: DENY`、`Referrer-Policy: no-referrer`、`Permissions-Policy: …`；
- REST 响应含 `Content-Security-Policy: default-src 'none'; frame-ancestors 'none'` + nosniff/X-Frame/Referrer；
- **SH-3**：`secure=false`（`ConsoleCookieInsecure=true`）时**无** `Strict-Transport-Security`；`secure=true` 时有。
记录到 walkthrough.md。**演示纪律**：停后台按确切 PID；脚手架/产物走查后删除未提交。

- [ ] **步骤 4：真浏览器走查（SH-2/SH-5，关键）**

build-tag 脚手架复用 `newConsole`+dbtest（沿用 M4.x 模式），Playwright MCP + axe-core：
- Console 代表性页（dashboard / **datapolicies** / ops_role_simulate / ops_tenant_template 等含改动页）devtools console **0 CSP 违规**；
- axe **0 违规**（内联样式外提未破坏 a11y）；
- **datapolicies 构建器切换**（呼应 M4.3 教训）：进入 `/apps/{id}/data-policies`，验证构建器 `#builder` 揭示可用（`classList` 改造正确）、点「专业模式」↔「可视化模式」切换正常、序列化/保存链路不受影响、**无 JS 基线**（禁用 JS 或看初始 DOM）builder/toggle 隐藏、textarea 可见；
- 记录 network 面板见安全头。
> 若走查发现 CSP 违规或构建器切换失效（`classList` 改造疏漏），**停下修复**并补 Go/回归断言，再重验（`go:embed` 静态资产改后须重建二进制重启）。

- [ ] **步骤 5：最终整体评审**

派子代理逐条核验 SH-1..7：SH-1 零触碰（diff 证明）、SH-2 严格 CSP 无 unsafe-inline（读 secheaders.go + 真浏览器 0 违规）、SH-3 HSTS 条件（单测两态 + curl 两态）、SH-4 分面头集（单测 + curl 两面）、SH-5 渲染/a11y 不变（axe 0 + 构建器切换可用）、SH-6 无内联 style 残留（lint 测试有齿）、SH-7 可演示（curl -I + 真浏览器）。产出 READY 或阻断清单。

- [ ] **步骤 6：更新记忆**

`project_detailed_design_progress.md` 加 M5.2a 节；`MEMORY.md` M5 索引下加 M5.2a ✅。

- [ ] **步骤 7：FF 并入本地 main + 问用户 push**

```bash
git -C /home/tongyu/codes/Sydom merge --ff-only worktree-feat+m5-2a-security-headers
```
核实 main==feature tip；push origin 与否问用户；清理 worktree。

---

## 自检（写完计划后，对照规格）

**1. 规格覆盖度：**
- §2.1 secheaders 包 Console/API + writeCommon → 任务 1 ✅
- §2.2 头集契约（Console 完整 / REST 锁死 / HSTS 条件）→ 任务 1 指标定义 + 任务 1 单测 ✅
- §2.3 严格 CSP 从源清理（6 内联 style + datapolicy.js 揭示）→ 任务 2 ✅
- §3 数据流/错误处理（设头时机、ops 不套、HSTS 防锁死）→ 任务 1（实现）+ 任务 3（接线，ops 不套）✅
- §4 配置（secure 复用 !ConsoleCookieInsecure，无新 flag）→ 任务 3 ✅
- §5 零触碰 → 任务 3/4 diff 核验 ✅
- SH-1..7 → 各任务 + 任务 4 ✅
- §7 测试策略（四态单测 + lint 回归 + 真浏览器走查）→ 任务 1/2/4 ✅；§8 任务分解 → 4 任务 ✅

**2. 占位符扫描：** 无 TODO。所有头值、CSP 指令、模板/JS 改动、命令均具体给出。

**3. 类型一致性：**
- `secheaders.Console(secure bool) func(http.Handler) http.Handler`、`API(secure bool) func(http.Handler) http.Handler` 任务 1 定义 → 任务 3 接线 `secheaders.API(secure)(...)` / `secheaders.Console(secure)(...)` 一致引用 ✅
- `secure := !cfg.ConsoleCookieInsecure`（任务 3）→ 传入两中间件一致 ✅
- `.hidden`/`.mb-4`/`.d-inline`（任务 2 步骤 3 定义）→ 任务 2 步骤 4 模板一致引用；`.hidden`（任务 2 步骤 4）→ datapolicy.js `classList.remove("hidden")`（任务 2 步骤 5）一致 ✅

对照无缺口。
