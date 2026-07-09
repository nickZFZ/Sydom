# M5.2a 安全响应头基座 — 整体核验 / 走查记录

> 日期：2026-07-09　BASE=main `28f4b85`（M5.1 完结）　分支 `worktree-feat+m5-2a-security-headers`
> 范式：子代理驱动 + 两阶段审查（规格→质量）；TDD；每任务独立 commit；**禁 `--amend`**。
> 规格 `docs/superpowers/specs/2026-07-07-sydom-m5-2a-security-headers-design.md`（含 SH-1..7）；计划 `docs/superpowers/plans/2026-07-07-sydom-m5-2a-security-headers.md`（4 任务）。
> 定位：M5「运维就绪 + 生产硬化」→ M5.2「安全硬化」的**第 1 可演示切片**。延续 M5.1「服务边界中间件 / 加法 / 零触碰授权求值核心」的模式。

## 做了什么（一句话）

给控制面两个 HTTP 面（Console HTML BFF + REST JSON API）装上**按内容类型裁剪的安全响应头基座**——新增无状态 `internal/secheaders` 包（`Console`/`API` 两变体中间件），并**从源清理** 6 个内联 `style=` 达成**严格 CSP（无 `'unsafe-inline'`）**，全在服务边界与表现层做加法，**零触碰授权判定与数据面求值**。

## 提交序列（4 实现/修复 commit，clean，无 --amend）

| SHA | 任务 | 内容 |
|---|---|---|
| `da8812e` | 1 | `internal/secheaders`：`Console`/`API` 分面中间件 + 共享 `writeCommon` + 头集契约 + 单测（四态 + HSTS 条件 + SH-2 无 unsafe-inline） |
| `76c2ec9` | 2 | 从源清理 6 内联 `style=`→CSS 工具类（`.mb-4`/`.d-inline`/`.hidden`）+ `datapolicy.js` 揭示改 `classList` + 模板无内联样式回归测（先证 FAIL 再转 PASS） |
| `d235a42` | 3 | 接线 `app.Run`：REST=`secheaders.API(secure)(obs.HTTPMiddleware(...))`、Console=`secheaders.Console(secure)(...)`（secheaders 在 obs **外层**）；ops 端口**不套**；端到端接线核验分面头集 |
| `5d57297` | 4（修复） | **审查捕获真实缺陷**：确认页取消 `javascript:history.back()` 被严格 CSP 拒→改真实链接 `confirmCancelURL` 按作用域推导 + lint 守卫扩 `javascript:`/`on*=` + 单测/集成测钉死 |

## 头集契约

**Console（HTML BFF）：**
| 头 | 值 |
|---|---|
| `Content-Security-Policy` | `default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'` |
| `X-Content-Type-Options` | `nosniff` |
| `X-Frame-Options` | `DENY`（与 `frame-ancestors 'none'` 双保险）|
| `Referrer-Policy` | `no-referrer` |
| `Permissions-Policy` | `geolocation=(), camera=(), microphone=()` |
| `Strict-Transport-Security` | `max-age=63072000; includeSubDomains`（**仅 secure=true**）|

**REST（JSON API）：**`Content-Security-Policy: default-src 'none'; frame-ancestors 'none'` + nosniff + X-Frame-Options DENY + Referrer-Policy no-referrer + 条件 HSTS（无 Permissions-Policy）。

**门控**：`secure := !cfg.ConsoleCookieInsecure`（复用既有部署 HTTPS 信号，不新增 flag）。CSP 与 secure 无关（永远严格）；HSTS 仅 secure 下发（防明文部署被浏览器强制 HTTPS 锁死）。

## SH-1..7 逐条核验（最终评审 READY）

| 不变量 | 判定 | 证据 |
|---|---|---|
| **SH-1** 零触碰授权/求值/认证/obs | **PASS** | `git diff --numstat 28f4b85..HEAD -- casbin/ adminauthz/ sidecar/{kernel,dataperm,authz}/ internal/auth/ internal/obs/` = **空**。改动仅：新 `internal/secheaders`、`app/run.go` 接线、表现层 console。（注：本机 ugrep 对某些正则误短路，用 git numstat 权威核验，非计划里的 grep）|
| **SH-2** 严格 CSP 无 unsafe-inline | **PASS** | `cspConsole` 逐字符合契约、无 `unsafe-inline`/`unsafe-eval`/nonce/hash；6 内联 style 全清（grep 空）；lint 测有齿。**真浏览器现场证据**：严格 CSP `default-src 'self'` 实拦跨域 fetch jsdelivr（console 报 `Refused to connect … violates CSP`）——CSP 确在强制 |
| **SH-3** HSTS 仅 HTTPS | **PASS** | `writeCommon` 仅 `secure` 下 Set；`TestConsole/API_NoHSTSWhenInsecure` 两态各断言；curl -I（secure=false）实测**无** HSTS |
| **SH-4** 分面头集裁剪 | **PASS** | `cspAPI`=锁死串；`TestSurfaceHeadersDoNotBleed` 断言两面 CSP 不同、API 无 script-src/Permissions-Policy；`run_test.go` 端到端两面各断言（REST 锁死串精确等于、Console 含 script-src 'self' 且 NotContains unsafe-inline）。**bonus**：404 响应亦带完整头（中间件包裹全部响应）|
| **SH-5** 渲染/a11y 不变 | **PASS** | 工具类计算样式等价（真浏览器实测 `.mb-4`=16px=原内联、`.d-inline`=inline 靠声明顺序压过 `.inline-form`、`.hidden`=none）；datapolicies 构建器揭示 + 满 toggle 循环真浏览器验证可用；axe 三页各 0 违规 |
| **SH-6** 无内联 style=/`<script>`/`javascript:`/`on*=` 残留 + 有齿 | **PASS** | `templates_lint_test.go` 遍历 embed templatesFS 断言四类内联脚本/样式源皆无；**实证有齿**（清理前对各违规模板报错、修后 PASS）|
| **SH-7** 可演示 | **PASS** | `curl -I` 见 Console/REST 分面头集（无 HSTS@secure=false、无 unsafe-*）；中间件在 `next.ServeHTTP` 前设头（不改 body/status/不吞 next）；ops 端口（`obs.OpsHandler`）**未套**安全头；真浏览器 0 CSP 违规 |

## 审查捕获并修复的真实缺陷（子代理规格审查）

**🔴 确认页取消 `javascript:history.back()` 被本里程碑自身的严格 CSP 拒**（`ops_confirm.html:10`，全仓唯一 `javascript:` 命中）：

- `ops_confirm.html` 由 `requireConfirm` 为**所有破坏性动作**（删除角色/数据策略/模板、撤权、解绑、轮换/重置凭据、停用应用、5 个批量移除族…10+ 处）渲染。其「取消」用 `href="javascript:history.back()"`。
- 新 CSP `script-src 'self'`（无 `'unsafe-inline'`）**在浏览器中拦截 `javascript:` URI 导航**——点「取消」静默失效。且我同时设了 `Referrer-Policy: no-referrer`，无 Referer 可回溯。**直接证伪 SH-2/5/7 的「真浏览器 0 CSP 违规」claim**——而首轮走查列的代表页恰不含确认页（需触发破坏性动作才达）。**又一次复现 M4.3/M4.4/M4.5「渐进增强/浏览器行为路径须真浏览器端到端走查」教训。**
- **根因**：规格 §2.3 清理清单只盘「6 个内联 style=」，漏了 `javascript:` URI 这一 script-src 违规源。
- **从源修（不弱化 CSP）**：`confirmCancelURL(r)` 按 Action 路径作用域推导**真实返回目标**（`/apps/{id}/roles`、`/ops/apps/{id}/roles`、`/operators`、`/admin-roles`、兜底 `/`），取消改普通同源链接（CSP 无关、无 JS 亦可用、比原 `javascript:` 更健壮——原本无 JS 就不工作）。lint 守卫**扩**禁 `javascript:` 与内联 `on*=`。`TestConfirmCancelURL` 钉各作用域、`ConfirmGate` 断言真实 href 无 `javascript:`。
- **真浏览器复验（严格 CSP 强制态）**：数据策略页删除→服务端确认页，**0 CSP 违规**、取消 `href=/apps/1/roles`、点击**真实导航**至角色页（title「角色 · App 1」，非静默失效）。

## 真实浏览器 axe 走查（2026-07-09 完成）

一次性 build-tag `walkthrough` 脚手架（`zz_walkthrough_scaffold_test.go`，复用 `newConsole` 同款真依赖 + dbtest PG+Redis，**handler 包在 `secheaders.Console(false)` —— 与 app.Run 生产同一中间件**，播种 1 app + 1 数据策略、打印 URL、阻塞待 SIGTERM）+ 系统 Chrome via Playwright MCP + axe-core 4.10.2。走查后脚手架文件已删、进程按 PID 停、无残留容器，均未提交。

| 页 | axe 4.10.2 违规 | 关键（真实渲染核实） |
|---|---|---|
| `/apps/1/data-policies`（改动页，JS 构建器） | **0**（38 passes） | 单 h1「数据策略」；`.hidden` 类经 `classList.remove` 揭示构建器 + 切换按钮（`classList` 改造正确）；满 toggle 循环（构建器↔专业模式）0 CSP 违规、textarea `required` 随模式正确切换（M4.3 修复仍在） |
| `/login`（代表页，严格 CSP） | **0**（22 passes） | 单 h1；全 CSS 同源 `/static/css/*` |
| `/apps/1/roles`（app shell） | **0**（32 passes） | title/lang=zh/main/h1 齐全；4 CSS 同源；**0 内联 style 元素** |
| 确认页（`/apps/1/data-policies/1/delete`，修复后） | **0 CSP 违规** | 取消真实链接、点击真实导航（见上「审查捕获」）|
| `ops_role_simulate` / `ops_tenant_template`（各 1/3 静态内联 style→类） | 计算样式等价核验 | `.mb-4`=16px=原内联、`.d-inline`=inline 压过 `.inline-form`、`.hidden`=none（真浏览器 `getComputedStyle` 实测，避免播种 role/template 的重设置）|

**CSP 强制现场证据**：严格 `default-src 'self'` 实拦跨域 fetch jsdelivr（console `Refused to connect … violates CSP`）——证明 CSP 非纸面。axe 工具本身经 Playwright CDP `Page.setBypassCSP` 注入（仅为加载 axe 工具，DOM a11y 与 CSP 无关；CSP 强制态另经 curl + securitypolicyviolation 监听 + 上述拦截独立验证）。

**curl -I（secure=false）**：Console `/login` 见 6 头（CSP 严格串 + nosniff + X-Frame DENY + Referrer no-referrer + Permissions-Policy），**无 HSTS**、**无 unsafe-***。

## 技术债 / 非阻断观察（留后续 M5.2 其它切片）

- **lint `<script>` 匹配偏窄**（子代理非阻断 #2）：精确 `<script>`（无属性开标签），`<script type=module>`/`<script nonce>` 会漏。当前无此类，优先级低。
- **裁剪项**（规格 §1 已明确，非本片）：mTLS、CSP nonce/hash 机制、report-uri/report-to 上报管线、供应链/漏洞扫描（govulncheck/gosec）、HMAC intra-window nonce 防重放、速率限制、`Permissions-Policy` 全特性穷举。

## 最终评审

**READY。** SH-1..7 逐条满足；`go test ./...` EXIT 0 全绿；授权/求值/HMAC 认证/obs 核心经 numstat 硬证明零触碰（diff=0）；三面头集契约 + 分面裁剪 + HSTS 条件下发 + 接线（obs 外层 + ops 不套）单测/集成测 + curl -I + 真浏览器多页 axe 0 违规 + 0 CSP 违规 + 构建器揭示/toggle 全链路真实浏览器验证。规格审查捕获并从源修复 1 个真实阻断缺陷（确认页 `javascript:` 取消被严格 CSP 拒）——修后真浏览器复验闭合。**M5.2a 全部关卡闭合，下一步 M5.2 其它安全硬化切片。**

相关：[[feedback-consistency-over-simplicity]]、[[project-detailed-design-progress]]
