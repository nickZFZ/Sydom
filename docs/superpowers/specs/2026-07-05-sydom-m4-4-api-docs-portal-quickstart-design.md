# M4.4 API 文档门户 + Quickstart 设计

> 里程碑 M4（技术向建模台 + 开发者 DX）第 4 子项目。BASE=main `6339fb6`（M4.3 后）。
> 范式：设计 → 计划 → 子代理驱动实现（两阶段审查）；TDD；每任务独立 commit；禁 `--amend`。

## 目标

在 Web Console 新增 **`/developer` 开发者文档区**，面向客户的开发/安全团队（建模台受众），降低「首次集成耗时」。**数据面授权 Check 为主**（quickstart + SDK 参考 + 核心概念），**管理面 REST/gRPC API 为辅**（从权威 `ruleTable` + route 注册表自动派生的端点参考，测试断言零漂移）。

## 背景与动机（探索发现）

- **现状无开发者文档面**：仓内只有 `docs/superpowers`（内部设计文档）、根 `README.md`；无 OpenAPI/Swagger；数据面接入靠读 `examples/orderservice` 源码 + SDK 源码自悟。「首次集成耗时」无产品内支撑。
- **数据面接入面（主）**——公开 SDK `sdk/go/`：
  - `sdk/go/sydom`：`sydom.New(sidecarAddr)` → 客户端；`Check`/`BatchCheck`（功能权限）、`FilterSQL`（数据权限参数化 SQL 片段）、`ReportPermissions`（上报权限点目录）。
  - `sdk/go/sydomhttp`：net/http 鉴权中间件（options/resolver/middleware/context）。
  - `sdk/go/sydomgorm`、`sdk/go/sydomsql`：GORM / `database/sql` 的数据权限行过滤集成。
  - 接入模式（`examples/orderservice/main.go` 实证）：跑 Sidecar（回环，pin 本 app 域）→ `sydom.New` → 启动上报权限点目录（fail-soft）→ handler 里 `Check`/`FilterSQL`。gRPC `AuthService`（`api/proto/sydom/auth/v1/auth.proto`）：Check/BatchCheck/FilterSQL/ReportPermissions，域由 Sidecar pin 不在请求体（强隔离）。
- **管理面接入面（辅）**——`AdminService`（`api/proto/sydom/admin/v1/admin.proto`）经 gRPC + 手写 REST 网关（`internal/controlplane/restgw`）双面。**权威真相**：`internal/controlplane/mgmt/authz.go` 的 `ruleTable map[fullMethod]rpcRule{resource, action, isWrite, scope}` 是每条 admin RPC 授权要素的**唯一集中定义**；`restgw` 的 `route{method, pattern, fullMethod, ...}` 注册表把 fullMethod 映射到 REST method+path。二者可在进程内枚举。
- **既有可复用资产**：M3.1 设计系统（token 化零构建 CSS、深色就绪）；Console `registerXxx(mux)` 分区注册范式（`registerPolicyCode`/`registerDataPolicy`）+ `_appnav` partial 的 Tab 判定；`requireSession` 会话鉴权；M4.2/M4.3 真实浏览器 axe 走查脚手架。

## §1 范围与非目标

**范围**：Console `/developer` 区（会话可访问、html/template BFF、复用设计系统、**无新 JS**）含四块——① 数据面 Quickstart（手写 prose + 可复制 Go 代码，取自真实 orderservice）② 核心概念（简）③ SDK 参考 ④ 管理面 API 端点参考（自 `ruleTable`+route 注册表**自动派生**）+ 一个断言零漂移的有齿测试。

**非目标（YAGNI）**：不做 sandbox 测试模式、不做密钥管理 UI（均属 **M4.5**）；不做全字段级 proto 参考（端点清单足够，字段级链到 proto/示例）；不引入 OpenAPI/Swagger 工具链（违背零构建/无新 JS）；不做多语言 SDK 文档（当前仅 Go SDK）；**不改任何授权/数据面/`ruleTable`/SDK 逻辑**（纯只读派生，diff 证明）。

## §2 架构：in-Console `/developer` 区（单一真相源，无新 JS）

- **落位**：新 `registerDeveloper(mux)`（镜像 `registerPolicyCode` 范式），路由 `GET /apps/{app_id}/developer`（可含子页锚点或分页，见 §3）；`_appnav` 加「开发者」入口（Tab 判定加 `developer` 项）；会话鉴权复用 `requireSession`（只读、无 CSRF、无写、无 doWrite）。
- **渲染**：html/template BFF，复用 M3.1 设计系统 token/类，**无新 `<script>`/.js 文件**（若确需交互如「复制代码」按钮，用既有 `interactions.js` 的声明式属性范式，不新增 JS 文件）。深色就绪。
- **防漂移核心**：管理面 API 参考的数据**在进程内从权威 `ruleTable`（`mgmt/authz.go`）+ `restgw` route 注册表派生**——不手写、不复制第二份。为跨包读取 `ruleTable`（当前包内私有 `var ruleTable`），新增一个**只读导出访问器**（如 `mgmt.APIReference() []mgmt.RPCDoc` 或导出 `RuleEntries()`），返回 `{FullMethod, Resource, Action, Scope, IsWrite}` 切片；REST method+path 由 `restgw` 暴露一个只读 `Routes() []restgw.RouteDoc`（`{Method, Pattern, FullMethod}`）。Console 组装二者渲染端点表。**只加只读访问器，不改 `ruleTable`/route 内容与授权逻辑**（diff 证明）。

## §3 内容：四块文档区

> 单页分节（各 `<section>` + 单 h1「开发者文档」+ 段内 h2/h3）或数子页（tab 内联）——以最小实现与 a11y（单 h1）为准；默认单页锚点分节。

1. **Quickstart（数据面，主）**：手写 prose + 可复制 Go 代码块（**取自真实 `examples/orderservice`，非杜撰**）：
   - 步骤：① 跑 Sidecar（pin 到本 app 域，占位展示本 app 的 domain/key 占位符，**绝不含真 secret**）② `go get` SDK ③ `sydom.New(sidecarAddr)` ④ `client.Check(subject, object, action)` ⑤（可选）`FilterSQL` 行过滤 + `sydomhttp` 中间件片段 ⑥ 启动上报权限点目录。
   - app-上下文化：展示本 app 的 domain 占位与「你已上报的权限点」提示（读 app 上下文，只读；若涉及读权限点目录用既有只读 List，不新增写）。
2. **核心概念（简）**：subject/object/action 三元组、功能权限 vs 数据权限、Sidecar 回环架构与强隔离（域由 Sidecar pin）、fail-close——各一小段 prose。
3. **SDK 参考**：`sydom`（Check/BatchCheck/FilterSQL/ReportPermissions 签名 + 用途）、`sydomhttp`（中间件用法）、`sydomgorm`/`sydomsql`（数据过滤集成）——prose + 签名，不逐字段展开。
4. **管理面 API 参考（辅）**：从 `ruleTable`+route 注册表**自动生成的端点清单表**：REST `method + path` / gRPC `fullMethod` / `resource·action·scope` / `isWrite`。字段级细节以链接指向 proto 文件/示例（不内嵌字段表）。按 resource 或 method 分组，稳定排序（防注入式的白名单排序不适用——纯静态渲染）。

## §4 行为契约（DP-1..7，验收标准）

- **DP-1 单一真相源（管理面参考不漂移）**：端点参考的数据全部来自 `ruleTable` + route 注册表的只读派生，无第二份手写清单。
- **DP-2 零漂移有齿**：一个测试断言 **`ruleTable` 每条 RPC 都出现在渲染的管理面参考里**（漏一条即 FAIL）；**反向验证**：临时从渲染源移除一条 → 测试 FAIL。呼应 M2.4/M4.2/M4.3「测试须有齿、须能捕获回归」。
- **DP-3 Quickstart 真实可用**：quickstart 代码取自真实 `examples/orderservice`（编译通过的源），非杜撰；关键 API 调用（`sydom.New`/`Check`/`FilterSQL`）签名与 SDK 现状一致（测试或 go doc 佐证签名不漂移，至少一处断言引用真实 SDK 符号）。
- **DP-4 只读无副作用**：`/developer` 全部 GET 只读——不 bump、不写审计、不 doWrite、无 CSRF 写路径；不泄露 secret（app 凭据只展示占位符/domain，绝不渲染真实 app_secret）。
- **DP-5 会话鉴权 + 租户隔离一致**：`/developer` 复用 `requireSession`；若渲染 app 上下文数据（如权限点目录）经既有只读授权范式（`AuthorizeRule` 或既有 List handler），不另起鉴权逻辑、不越权跨 app。
- **DP-6 无新 JS / 渐进增强**：无新增 `<script>`/.js 文件；页面无 JS 也完全可读（文档是静态内容）；复用设计系统类。
- **DP-7 授权核心/SDK/数据面零触碰**：`casbin/`、`adminauthz/`、`kernel/`、`sidecar/`、`ruleTable` 授权内容、SDK 逻辑**一字未改**（`git diff` 证明）；仅新增只读导出访问器 + Console 文档区 + 模板/样式。

## §5 a11y + 三面 parity

- **a11y**：每页单 h1「开发者文档」+ breadcrumb（建模台 · 开发者）、复用设计系统、键盘可达、真实浏览器 axe-core 4.10.2 **0 违规**（沿用 M4.2/M4.3 走查脚手架范式）。
- **三面 parity**：不涉及——`/developer` 是 Console 专属只读文档区，不新增可写 RPC，不触三面写入口。管理面参考本身即描述三面（gRPC/REST）现状。

## §6 测试策略

- **DP-2 零漂移测试**（关键 artifact）：渲染管理面参考 → 断言 `ruleTable` 每 fullMethod 都在输出；反向验证（移除一条则 FAIL）。
- **DP-3 quickstart 真实性**：断言 quickstart 引用的 SDK 符号真实存在（编译期引用或 go/doc 断言），代码块取自 `examples/orderservice` 的真实片段。
- **Console handler 测试**：`/developer` 页 200、单 h1、含四块锚点、不含 secret（断言页面 body 不含 app_secret 值）；会话鉴权（未登录挡）。
- **真实浏览器 axe 走查**（DP a11y）：`/developer` 页 axe 0 违规、单 h1、breadcrumb、复制代码交互（若有）键盘可达。
- **全量**：`gofmt`/`go vet ./...`/`go test ./...` 0 FAIL；DP-7 零触碰 `git diff` 硬核验。

## §7 任务分解（供 writing-plans 细化）

1. `mgmt`/`restgw` 只读导出访问器（`RuleEntries()`/`Routes()` 返回文档用只读结构 + 单测；`ruleTable`/route 内容零改）。
2. Console 管理面 API 参考渲染 + **DP-2 零漂移有齿测试**（含反向验证）。
3. Console 数据面 Quickstart + 核心概念 + SDK 参考页（手写内容，代码取自 orderservice；DP-3 真实性断言 + 不泄露 secret 断言）。
4. `_appnav` 加「开发者」入口 + 模板/样式（设计系统类，无新 JS）+ 页整合（单 h1/breadcrumb/四块锚点）。
5. 整体核验 DP-1..7 + 真实浏览器 axe 走查 + opus 评审 + FF。

## §8 自检对照（写完本设计后，全新视角）

- **占位符扫描**：无「待定/TODO」；每节含具体机制（访问器形状、防漂移测试、四块内容、落位范式）。§3 的「单页 vs 子页」标注为「以最小实现/a11y 单 h1 为准，默认单页锚点」是刻意的实现自由度，非占位。
- **内部一致性**：数据面为主（quickstart+概念+SDK 参考 3 块）、管理面为辅（1 块自动派生）；防漂移=从 `ruleTable`+route 派生 + 有齿测试（DP-1/2）；零触碰授权/SDK（DP-7）——内部一致。
- **范围检查**：聚焦「文档门户+quickstart」，单实现计划可覆盖（5 任务，近 M4.3）；sandbox/密钥管理明确划归 M4.5。
- **模糊性检查**：落位=in-Console /developer（会话可访问）；管理面参考=端点清单从 route 注册表派生（非全字段级 proto）；quickstart=Go-first 取自真实 orderservice；只读无 secret 泄露——均明确。
