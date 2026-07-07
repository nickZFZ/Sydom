# M5.2a 安全响应头基座（Security Response Headers）— 设计

> 里程碑：M5.2a（M5「运维就绪 + 生产硬化」→ M5.2 安全硬化 的**第 1 可演示切片**）。BASE=main `28f4b85`（M5.1 tip）。
> 路线图定位：M5.2「安全硬化」经范围评估拆为可演示纵向切片，**M5.2a 安全响应头**（本节）为首片——延续 M5.1「服务边界中间件 / 加法 / 零触碰授权求值核心」的模式，是 M5.1 HTTP 中间件工作的自然延续，闭合当前最明显的 web 安全缺口（Console/REST 无任何安全响应头）。后续候选切片：mTLS、供应链/漏洞扫描、威胁模型、intra-window nonce 防重放等（本片不含）。

## 1. 目标与范围

给司域控制面的两个 HTTP 面——**Console（html/template BFF）**与 **REST 网关（JSON API）**——装上**安全响应头基座**，全在服务边界中间件层做加法，**零触碰授权判定与数据面求值逻辑**：

- **件① 安全响应头中间件**：新 `internal/secheaders` 包，提供两个按内容类型裁剪的变体——`Console`（HTML：完整 CSP + 点击劫持/嗅探/引用来源/特性策略防护）与 `API`（JSON：锁死 CSP `default-src 'none'` + nosniff），在 `app.Run` 装配层与 M5.1 的 `obs.HTTPMiddleware` 组合。
- **件② 严格 CSP（无 `'unsafe-inline'`）+ 从源清理**：核实 Console 的 CSS/JS 全外链、无内联 `<script>`、无真实内联事件处理器；唯一阻碍严格 CSP 的是 **6 个内联 `style=` 属性**（3 模板）。将其迁为 CSS 工具类，达成 `script-src 'self'` + `style-src 'self'` 全程无 `'unsafe-inline'`。其中 `datapolicies.html` 的 2 个 `display:none`（构建器/切换按钮）有 JS 交互，连带把 `datapolicy.js` 的**揭示逻辑**从 `el.style.display` 改为 `classList` 切换（否则清空内联 style 仍被类的 `display:none` 盖住）。
- **件③ HSTS 条件下发**：`Strict-Transport-Security` **仅在 HTTPS 部署下发**，明文/开发环境不发（防浏览器强制 HTTPS 锁死本地访问）。门控复用既有部署 HTTPS 信号。

**为什么这样切**：安全响应头是 web 面加固的地基——一层薄边界中间件即可关闭点击劫持、MIME 嗅探、混合内容、大量 XSS 注入面，且**可演示**（`curl -I` 见头 + 真浏览器 devtools 0 CSP 违规）、**零触碰授权真相**、风险低。它直接复用并延续 M5.1 的 HTTP 中间件模式。

**非目标 / 裁剪（YAGNI，留后续 M5.2 其它切片）**：mTLS（双向 TLS，另片）；CSP nonce/hash 机制（本片无内联脚本/样式需求，纯 `'self'` 即够，不引入 nonce 机制）；CSP report-uri/report-to 上报管线（无内联内容、直接 enforce）；供应链/漏洞扫描（govulncheck/gosec，另片）；HMAC intra-window nonce 防重放（另片）；速率限制（另片）；`Permissions-Policy` 全特性穷举（只 deny 明确不用的 geolocation/camera/microphone）。

## 2. 架构

新增 `internal/secheaders` 包（无状态，纯响应头设置中间件），控制面 `app.Run` 两处 HTTP 面接线复用它：

```
                    ┌──────────── internal/secheaders ────────────┐
                    │ Console(secure bool)(next) http.Handler       │  # HTML 面完整头集
                    │ API(secure bool)(next) http.Handler           │  # JSON 面锁死头集
                    │  （设响应头 → 调 next；HSTS 仅 secure=true 下发）│
                    └───────────────────────────────────────────────┘
控制面进程：
  REST     = secheaders.API(secure)( obs.HTTPMiddleware(logger, restgw.NewHandler(...)) )
  Console  = secheaders.Console(secure)( obs.HTTPMiddleware(logger, console.NewHandler(...)) )
  secure = 部署已声明 HTTPS（!ConsoleCookieInsecure 或 srvTLS != nil）
```

### 2.1 `internal/secheaders` 包
- `func Console(secure bool) func(http.Handler) http.Handler`：返回中间件，在调用 `next` **之前**设置 Console（HTML）头集；`secure=true` 时附加 HSTS。
- `func API(secure bool) func(http.Handler) http.Handler`：同上，设置 REST（JSON）锁死头集。
- 内部共享 `writeCommon(h http.Header, secure bool)` 设通用头（nosniff / X-Frame-Options / Referrer-Policy / 条件 HSTS），各变体再叠加自己的 CSP + 特有头。
- **纯设头，绝不改写 body、不吞 next、不改 status**：`ResponseWriter` 直接透传给 next（obs 中间件的 statusRecorder 不受影响）。头在 next 写任何东西之前设置（中间件在 `next.ServeHTTP` 前 `w.Header().Set(...)`）。

### 2.2 头集契约

**Console（HTML BFF）：**
| 头 | 值 |
|---|---|
| `Content-Security-Policy` | `default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'` |
| `X-Content-Type-Options` | `nosniff` |
| `X-Frame-Options` | `DENY`（与 `frame-ancestors 'none'` 双保险，兼容旧浏览器）|
| `Referrer-Policy` | `no-referrer` |
| `Permissions-Policy` | `geolocation=(), camera=(), microphone=()` |
| `Strict-Transport-Security` | `max-age=63072000; includeSubDomains`（**仅 secure=true**）|

**REST（JSON API）：**
| 头 | 值 |
|---|---|
| `Content-Security-Policy` | `default-src 'none'; frame-ancestors 'none'`（API 不渲染任何内容）|
| `X-Content-Type-Options` | `nosniff` |
| `X-Frame-Options` | `DENY` |
| `Referrer-Policy` | `no-referrer` |
| `Strict-Transport-Security` | `max-age=63072000; includeSubDomains`（**仅 secure=true**）|

> CSP 无 `'unsafe-inline'`/`'unsafe-eval'`/nonce/hash——纯 `'self'` 白名单足以覆盖 Console 现有全外链资产（核实：4 个 `/static/css/*.css` + `/static/*.js`，无内联脚本/样式块/真实 `on*=` 处理器，无图片/`data:`/外链）。

### 2.3 严格 CSP 的从源清理（件②）

6 个内联 `style=` → CSS 工具类（新增于 `components.css` 或 `base.css` 工具区）：
- `ops_role_simulate.html` ×1、`ops_tenant_template.html` ×3：`margin-bottom:var(--space-4)` / `display:inline` → 工具类（如 `.mb-4 { margin-bottom: var(--space-4); }`、`.inline { display: inline; }`；`.inline` 若与既有 `.inline-form` 语义冲突则另命名）。**静态、无 JS 交互，纯类替换。**
- `datapolicies.html` ×2（`#builder-toggle`、`#builder` 的 `style="display:none"`）→ `class="hidden"`（`.hidden { display: none; }`）。**⚠️ 连带改 `datapolicy.js`**：现靠 `el.style.display` 揭示的地方改为 `el.classList.remove("hidden")`（揭示）/ `add("hidden")`（隐藏）。否则严格 CSP 下模板改成类隐藏后，JS 清空内联 style 仍被类的 `display:none` 盖住 → 构建器永不显示。**此处呼应 M5.1 附近的 M4.3 教训（`required`+`display:none` 渐进增强 bug）：渐进增强 JS 接管路径须真浏览器端到端走查。**

> 注：CSP `style-src 'self'` 只约束 HTML 内联 `style=`/`<style>`/样式表；JS 运行时设 `element.style.foo`（CSSOM）**不受 style-src 约束**——故 datapolicy.js 其它以 `el.style.display=...` 做的动态显隐（非本次迁移的 6 个静态属性）无需改动，仍可用。本次只改与「模板初始 `display:none` + 类隐藏」协同的揭示点。

## 3. 数据流与错误处理

- **观测性/安全头 fail-open 无关**：设头是纯同步 `w.Header().Set`，不涉 error 路径；中间件不 panic、不吞 next 的返回。
- **HSTS 防锁死**：明文部署（`secure=false`）绝不下发 HSTS——否则浏览器缓存 `max-age` 后强制 HTTPS，本地/内网明文访问被锁死。`secure` 由 `app.Run` 从既有部署信号推导（见 §5）。
- **ops 端口不套**：`/metrics`·health 的 ops 端口（M5.1 `obs.OpsHandler`）**不加**这些头（非 HTML/非公网面，明文健康探针约定；加 HSTS 反而有锁死风险）。安全头只套 Console + REST 两个业务 HTTP 面。
- **头设置时机**：中间件在 `next.ServeHTTP` **之前** `w.Header().Set(...)`（响应头必须在首次 `WriteHeader`/`Write` 前落定）；next（obs 中间件 → 实际 handler）后续写 body 不影响已设头。

## 4. 配置

- 复用既有部署 HTTPS 信号推导 `secure`，**不新增 flag**（YAGNI）：`secure := !cfg.ConsoleCookieInsecure`（Console 已用此信号决定 cookie `Secure` 属性，语义一致「部署在 HTTPS 后」）；REST 面同一进程同一部署，复用同一 `secure`。若将来需独立控制，再加 flag。
- 无其它新配置。

## 5. 零触碰硬约束（SH-1）

**零触碰边界 = 判定/求值算法核心**（内容 diff=0，机器验证）：
- `casbin/`、`internal/controlplane/adminauthz/`、`internal/sidecar/kernel/`、`internal/sidecar/dataperm/`、`internal/sidecar/authz/`、`internal/auth/`（HMAC 认证逻辑）、`internal/obs/`（M5.1 已固化，不改）。

**改动只在服务边界与表现层**：
- 新增 `internal/secheaders/`（纯新增包）。
- `internal/controlplane/app/run.go`：REST/Console 两处包 secheaders 中间件（+ 推导 `secure`）。
- 表现层清理：3 个 `console/templates/*.html`（6 个内联 `style=` → 类）、`console/static/css/*.css`（加工具类）、`console/static/datapolicy.js`（揭示逻辑 `style.display`→`classList`）。

即：授权/求值/HMAC 认证逻辑零改；纯加一层响应头 + 表现层内联样式外提。

## 6. 安全与正确性不变量（SH-1..SH-7）

- **SH-1 零触碰授权/求值/认证**：决策核心 + `internal/auth` + `internal/obs` 内容 diff=0（§5，机器验证）。
- **SH-2 严格 CSP 无 `'unsafe-inline'`**：Console CSP 的 `script-src`/`style-src` 均为 `'self'`，全程无 `'unsafe-inline'`/`'unsafe-eval'`/nonce/hash——测试断言 CSP 字符串 + 真浏览器 0 CSP 违规。
- **SH-3 HSTS 仅 HTTPS**：`secure=true` 下发 HSTS、`secure=false` **绝不**下发——中间件单测两态各断言。
- **SH-4 分面头集裁剪**：Console（HTML 完整 CSP）与 REST（JSON `default-src 'none'`）头集各自正确、互不串味——两变体单测逐头断言。
- **SH-5 渲染/a11y 不变**：内联样式外提后页面渲染与可访问性不变——真浏览器 axe 0 违规 + 关键页视觉/行为不变（尤其 datapolicies 构建器切换可用）。
- **SH-6 无内联 `style=`/`<script>` 残留**：模板不再含内联 `style=`（也不含内联 `<script>` 内容）——回归测试 grep 断言，守住严格 CSP 可持续（有人再加内联样式即 FAIL）。
- **SH-7 可演示**：`curl -I` 见 Console/REST 头；真浏览器 devtools 0 CSP 违规、页面渲染/行为不变。

## 7. 测试策略

- **`secheaders` 单测**：`Console(true)`/`Console(false)`/`API(true)`/`API(false)` 四态，用 `httptest` 断言每个头存在且值精确；**HSTS 仅 secure=true 下present**（secure=false 断言 `Strict-Transport-Security` 不存在）；断言 CSP 字符串不含 `unsafe-inline`（SH-2）；断言 next 被调用且 status/body 透传不变。
- **接线测**：控制面 `app.Run` 装配后，Console/REST 响应带对应头（可在既有 app 集成测试或新增薄测断言一个代表性响应头存在）。
- **模板无残留内联样式回归测**：Go 测试遍历 `console/templates/*.html`，断言无 `style="` 内联属性（SH-6）——**先证它能捕获回归**（临时插一个 `style=` 应 FAIL）。
- **真浏览器走查（SH-5/SH-7，关键）**：build-tag 脚手架复用 `newConsole`+dbtest（沿用 M4.x 模式），Playwright MCP + axe-core：
  - Console 代表性页（dashboard / datapolicies / ops_role_simulate / ops_tenant_template 等含改动页）devtools **0 CSP 违规**；
  - axe **0 违规**（内联样式外提未破坏 a11y）；
  - **datapolicies 构建器切换**：点「专业模式」/进入构建器，验证 `#builder` 揭示可用（`classList` 改造正确）、序列化/保存链路不受影响；
  - `curl -I`（或 network 面板）见 CSP/nosniff/X-Frame-Options/Referrer-Policy/Permissions-Policy 头。
- **零触碰 diff 核验**：`git diff BASE..HEAD -- <决策核心 + internal/auth + internal/obs>` = 0（SH-1）。

## 8. 任务分解（留给 writing-plans）

1. `internal/secheaders` 包：`Console`/`API` 中间件 + 共享 `writeCommon` + 头集契约 + 单测（四态 + HSTS 条件 + SH-2 无 unsafe-inline）。
2. 从源清理：6 个内联 `style=` → CSS 工具类（3 模板 + css）；`datapolicy.js` 揭示逻辑 `style.display`→`classList`；模板无残留内联样式回归测（SH-6，先证能捕获回归）。
3. 接线：`app.Run` 推导 `secure`、REST/Console 各包 secheaders 中间件（组合于 obs 中间件外层）；接线测。
4. 整体核验 SH-1..7 + 真浏览器走查（0 CSP 违规 + axe 0 + datapolicies 构建器切换 + curl -I）+ 最终评审 + FF。

## 9. 自检小结

- **占位符**：无 TODO；头集契约、CSP 指令、清理清单、门控信号、零触碰路径均具体。
- **一致性**：延续 M5.1「边界中间件 / 加法 / 零触碰核心」与项目「从源头修、不弱化」纪律（严格 CSP 而非 `'unsafe-inline'`）；HSTS 门控复用既有 `ConsoleCookieInsecure` 信号，不引入新配置。
- **范围**：单一实现计划可覆盖（4 任务，小于 M5.1）；mTLS/供应链/nonce 机制/report 管线已明确裁剪。
- **模糊性**：CSP 严格度（strict + 从源清理）、HSTS 条件下发、分面头集、datapolicy.js 揭示逻辑改造均已明确取舍。

相关：[[feedback-consistency-over-simplicity]]、[[project-detailed-design-progress]]
