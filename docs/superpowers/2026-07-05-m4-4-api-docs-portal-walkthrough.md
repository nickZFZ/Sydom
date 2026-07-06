# M4.4 API 文档门户 + Quickstart 整体核验记录

> 里程碑：M4.4（技术向建模台 + 开发者 DX 之 API 文档门户）。分支 `worktree-feat+m4-4-api-docs-portal`，BASE=main `6339fb6`（含 M4.4 设计 spec + 计划）。
> 实现范式：子代理驱动 + 两阶段审查（规格合规 → 代码质量），每任务 TDD、独立 commit，禁用 `--amend`。

## 交付概要

在 Web Console 新增只读 `/apps/{app_id}/developer` 开发者文档区：
- **数据面（主）**：授权 Check 的 quickstart + 核心概念 + SDK 参考——手写静态内容，代码取自真实 `examples/orderservice` 与公开 SDK 签名。
- **管理面 API 端点参考**：从授权唯一真相 `mgmt.ruleTable` + REST route 注册表 `restgw.allRoutes()` **只读派生**，与实际鉴权面同源、防漂移。

**架构：防漂移单一真相源。** `mgmt`/`restgw` 各加一个**只读导出访问器**（`RuleEntries()` 派生 `ruleTable`、`Routes()` 派生 `allRoutes()`），返回独立文档 struct 不暴露内部 `rpcRule`/`route`；Console `buildAPIReference()` 以 `ruleTable` 为锚 join REST route。全程 html/template BFF、复用 M3.1 设计系统、**无新 JS**、会话只读鉴权、**零触碰授权核心/SDK/数据面**。

## 提交序列（BASE `6339fb6` 之后）

| commit | 层 | 摘要 |
|---|---|---|
| `1d3b6b6` | mgmt+restgw | 只读导出 API 文档访问器（`RuleEntries` 派生 `ruleTable`、`Routes` 派生 `allRoutes`，授权/route 内容零改） |
| `e42b46a` | mgmt+restgw | **测试补齿**：scope 用独立期望表校验（非 `scopeName==scopeName` 恒真式）+ route 逐条比对字段取值；反向验证 FAIL→PASS（质量审查捕获，见下） |
| `09ced4a` | console | 管理面 API 参考组装（锚定 `ruleTable` join restgw route）+ DP-2 零漂移有齿测试（每 admin RPC 必现，反向验证） |
| `1e8c297` | console | **测试补齿**：钉死 `RESTMethod/RESTPath` 具体取值 + 多路由 tie-break（质量审查变异测试捕获，见下） |
| `4eca5c0` | console | `/developer` 文档区（数据面 quickstart+概念+SDK 参考手写取自真实 orderservice + 管理面 API 参考自动派生渲染，会话只读不泄露 secret，无新 JS） |
| `f7fb182` | console | **修复**：app_id 解析失败对齐 `renderGRPCError` 约定（去 Error 级日志噪音/文案重复）+ 补 REST 渲染有齿断言（质量审查捕获） |
| `aa2ab38` | console | `_appnav` 加开发者 tab + 文档区样式（复用设计系统 token，代码块可读 + 端点表窄屏横向滚动，`仅 gRPC` 复用 `.hint` 不新造类，无新 JS） |
| `3c52a2d` | console | **修复**：端点表滚动容器键盘可达（axe `scrollable-region-focusable`，**真实浏览器走查捕获**，见下）+ Go 回归断言 |

## DP-1..7 逐条核验（对照 spec §4）

| DP | 结论 | 依据 |
|---|---|---|
| **DP-1 单一真相源** | ✅ | 管理面参考数据只从 `mgmt.RuleEntries()`（派生 `ruleTable`）+ `restgw.Routes()`（派生 `allRoutes()`）来；`console/apiref.go` 仅依赖两包导出面，无第二处硬编码 RPC 清单/route 表。 |
| **DP-2 零漂移有齿** | ✅ | `apiref_test.go` 断言 `ruleTable` 每条 admin RPC 都出现在 `buildAPIReference()` 且字段相等。**反向验证**：临时漏一条 `CreateRole` → 测试 FAIL（"管理面参考漏了 admin RPC…CreateRole"）；还原 → PASS。join 逻辑另钉死 `UpsertDataPolicy` 的 `RESTMethod="POST"`+具体 path（含多路由稳定排序 tie-break），变异 Method/Pattern 对调或取末条均 FAIL。 |
| **DP-3 quickstart 真实 SDK 符号** | ✅ | `developer.html` 里 `sydom.New`/`Check`/`BatchCheck`/`FilterSQL`/`ReportPermissions`/`Close` 逐一对照 `sdk/go/sydom/{client,permission}.go` 真实签名一致；`127.0.0.1:8090`、`FilterSQL(...,"order",...)` 用法对齐真实 `examples/orderservice`。 |
| **DP-4 只读不泄露 secret** | ✅ | `developer` handler 只 `requireSession`+`pathUint64`+`renderPage`——无写/bump/审计/CSRF 消耗；模板不含任何 secret 概念。Go 断言 `NotContains "app_secret"`；真实浏览器 DOM 全文扫 `rootsecret`/`app_secret`/`master_key`/主密钥字节 → 0 命中。 |
| **DP-5 会话鉴权** | ✅ | `requireSession` 守卫；`TestConsole_DeveloperPage_RequiresSession` 匿名请求断言 303 SeeOther（重定向 /login）。 |
| **DP-6 无新 JS** | ✅ | 全 diff 无新增 `<script>`/.js 文件；`developer.html` 纯静态 html/template，自动转义。 |
| **DP-7 授权/SDK/数据面零触碰** | ✅ | `git diff 6339fb6..HEAD -- casbin/ adminauthz/ kernel/ sidecar/ sdk/` = **0 行**；`authz.go`（`ruleTable`/`rpcRule`）+ `routes.go`（`route`/`allRoutes`）内容 diff = **0 行**。`handler.go` 仅 +1 行 register，`mgmt`/`restgw` 仅新增 `apidoc.go`。 |

## 全量验证

```
gofmt -l internal/        → 空（干净）
go build ./...            → exit 0
go vet ./...              → exit 0（干净）
go test ./internal/controlplane/{mgmt,restgw,console}/ -count=1  → 全 ok（mgmt 132s / restgw 67s / console 154s，testcontainers PG+Redis）
```

**DP-7 零触碰硬核验**：
```
git diff 6339fb6..HEAD -- casbin/ internal/controlplane/adminauthz/ internal/kernel/ internal/sidecar/ sdk/  →  0 行
git diff 6339fb6..HEAD -- internal/controlplane/mgmt/authz.go internal/controlplane/restgw/routes.go          →  0 行
```
授权真相源（casbin / adminauthz / kernel）、数据面（sidecar）、公开 SDK 与 `ruleTable`/route 内容零改动，diff 证明。文档面纯派生。

## ✅ 真实浏览器 axe 走查（2026-07-06 完成）

一次性 build-tag `walkthrough` 脚手架（`zz_walkthrough_scaffold_test.go`，复用 `newConsole` 同款真依赖装配 + `dbtest` testcontainers PG+Redis、root 超管、会话 TTL `time.Hour`、播种 1 app、打印 `/login`+`/developer` URL、阻塞待 SIGTERM）+ 系统 Chrome via **Playwright MCP**（`--prefer-offline @playwright/mcp@0.0.77`）+ **axe-core 4.10.2**（浏览器可达 jsdelivr、`<script src>` 注入）。**修 bug 后重建二进制重启再验**；走查后脚手架文件已删、进程按确切 PID 停、无残留容器，均未提交。

| 页/状态 | axe 4.10.2 违规 | 单 h1 | 关键（真实渲染核实） |
|---|---|---|---|
| `/apps/1/developer`（修复前） | **1（serious）** | ✓「开发者文档」 | `scrollable-region-focusable` on `.table-scroll`——宽端点表溢出成可滚动区、键盘不可达（走查捕获，见下） |
| `/apps/1/developer`（修复后） | **0** | ✓ | 四块 section `region`（`aria-labelledby` 解析出可访问名）；标题层级 H1→H2×4→H3×2 无跳级；appnav「开发者」`aria-current="page"` + 键盘可聚焦；端点表 55 行全渲染真实 REST method+path；DOM 无 secret 泄露 |

**真实浏览器核实明细**（修复后）：四锚点 `quickstart`/`concepts`/`sdk`/`api-reference` 齐；`.table-scroll` 具 `tabindex="0" role="region" aria-label="管理面 API 参考端点表"`；开发者 tab `aria-current="page"`、`tabIndex>=0`；secret 泄露扫描 0 命中；55 条 admin RPC 全部有 REST 映射（真实数据 0 条纯 gRPC，`仅 gRPC` 分支不触发）。

### 走查捕获并修复的真实 a11y bug（`3c52a2d`）

任务 4 给宽端点表加了 `<div class="table-scroll">`（`overflow-x:auto`）以便窄屏横向滚动。**该容器溢出即成可滚动区，但键盘用户无法滚动它**（内无可聚焦子元素、div 本身不可聚焦）——axe-core `scrollable-region-focusable`（serious）。**Go 测试无浏览器覆盖不到**，真实浏览器 axe 走查首次端到端跑该页时捕获。修复：滚动容器加 `tabindex="0" role="region" aria-label`（WAI 可滚动表格惯用式），键盘可聚焦 + 屏幕阅读器命名；修后重建脚手架二进制重启、同页真实浏览器复验 axe **0 违规**。另补 Go 回归断言钉死 `class="table-scroll" tabindex="0" role="region"`，未来去掉即单测 FAIL（无需浏览器）——呼应「走查捕获后加回归测试」教训。

## 审查中捕获并修复的其它真实缺陷（两阶段审查）

- **`e42b46a`（访问器测试补齿）**：质量审查故障注入发现——`mgmt` 仅校验 Scope 属已知集、`restgw` 仅判字段非空，均抓不到「文档面与授权/路由真相源漂移」。**尤其**审查最初建议的 `require.Equal(scopeName(r.scope), e.Scope)` 是**恒真式**（两侧同经 `scopeName`），反向验证已捕获其无齿；改用独立 `wantScope` 期望表 + `restgw` 按 `(fullMethod,pattern)` 逐条比对取值，反向验证 FAIL→PASS 确认有齿。
- **`1e8c297`（join 逻辑补齿）**：质量审查变异测试发现——DP-2 覆盖率有齿但 REST join（本文件唯一新逻辑）无齿：对调 `Method/Pattern` 或多路由改取末条测试仍 PASS。改为精确值断言钉死字段映射 + 稳定排序 tie-break。
- **`f7fb182`（错误处理对齐）**：质量审查发现 app_id 解析失败用了 `renderError`+非 nil err（对纯客户端输入错误打 Error 级日志、文案重复），偏离既有纯渲染页 `renderGRPCError` 约定；对齐既有约定去噪（呼应一致性优先）。同时补 REST 渲染有齿断言。

## 补充安全审视（整体评审）

- **单一真相源**：文档面纯派生自 `ruleTable`+`allRoutes()`，无第二套清单、无漂移（DP-1/DP-2）。
- **无越权/无 secret**：只读页会话鉴权足够（不读 app 数据、不按 app_id 查询）；全路径绝不含凭据（DP-4/DP-5）。
- **零触碰授权/SDK/数据面**：diff 硬证明（DP-7）；文档随授权面自动跟随，改一条 RPC 授权要素文档即更新。
- **无新 JS**：全 diff 无新增 `<script>`/.js（DP-6）。

## 裁决

**READY，真实浏览器 axe 走查已完成。** DP-1..7 逐条满足；三包全量测试 ok / 0 FAIL；授权核心 + SDK + 数据面零触碰经 diff 硬证明；`/apps/1/developer` 真实浏览器 axe-core 4.10.2 **0 违规**（修复走查捕获的 `scrollable-region-focusable`），单 h1 + 四锚点 + 正确标题层级 + 开发者 tab `aria-current` + 键盘可达 + 无 secret 泄露全链路真实浏览器验证。两阶段审查另捕获并修复 3 处测试缺齿/一致性缺陷。M4.4 全部关卡闭合。
