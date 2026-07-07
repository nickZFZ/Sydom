# M4.5 开发者自助闭环（凭据总览 + 数据权限沙箱）— 整体核验 + 真实浏览器 axe 走查记录

> 里程碑：M4.5（技术向建模台 + 开发者 DX 之尾项，M4 收官）。分支 `worktree-feat+m4-5-developer-sandbox`，BASE=main `94a615d`（M4.4 tip）。
> 规格：`docs/superpowers/specs/2026-07-07-sydom-m4-5-developer-sandbox-credentials-design.md`（SD-1..7）。
> 计划：`docs/superpowers/plans/2026-07-07-sydom-m4-5-developer-sandbox-credentials.md`（6 任务 TDD，子代理驱动 + 两阶段审查）。

## 交付概览

Web Console 新增两薄件（都在建模台开发者区，复用既有内核，不造第二套决策）：
- **件① 接入凭据总览**：`/apps/{id}/developer` 加「接入凭据」section，由新增只读 RPC `GetApplication` 读回 `app_id`/`app_key`/`domain`（复用 `ApplicationSummary`，**类型层无 secret 字段**）；轮换凭据入口复用既有 `POST /apps/{id}/rotate-secret`。
- **件② 数据权限沙箱**：专页 `/apps/{id}/data-sandbox`，输入 subject/resource/attrs，看**数据面同一渲染器**（`dataperm.Filter.FilterSQL`）产出的参数化 WHERE + args，由新增只读 RPC `PreviewDataFilter`（`effperm.PreviewFilter` 薄包 `buildEngine`+`dataperm.NewFilter`+`FilterSQL`）支撑。

两条新 RPC 均 `scopeApp` read、三面 parity（gRPC/REST/Console）、fail-close、零副作用。

## 提交序列（BASE=main `94a615d`）

| 提交 | 面 | 摘要 |
|---|---|---|
| `02bf285` | mgmt | 任务1：`GetApplication` 只读 RPC（复用 `ApplicationSummary` 无 secret，scopeApp read） |
| `f0f8554` | mgmt | 任务1 审查修复：全栈 `PermissionDenied` 反泄露回归测试 + 6 字段精确断言 + `errors.Is`/SQL 风格统一 |
| `e41fcc8` | effperm+mgmt | 任务2：`PreviewDataFilter` 只读 RPC（复用 `buildEngine`+`dataperm.FilterSQL` 数据面同源零触碰，反向验证有齿） |
| `163a11c` | mgmt | 任务2 审查修复：测试复用既有 `mustDataPolicy`（去近重复 helper，一致性） |
| `ebaabd6` | restgw | 任务3：REST parity（`GET /v1/applications/{id}` + `POST /v1/apps/{id}/data-filter/preview`，path 权威覆写，同 ruleTable） |
| `8e9d073` | console | 任务4：`/developer` 接入凭据 section（绝不渲染 secret，轮换入口复用既有 POST 表单，无新 JS） |
| `9779ced` | console | 任务5：数据权限沙箱专页（GET 回填复用 PreviewDataFilter，attrs 每行 key=value 服务端解析，`_appnav` 沙箱 tab，无新 JS） |
| `f701be1` | console | 任务6 走查修复：沙箱决策链接独立成段（修真实浏览器 axe 捕获 `link-in-text-block`）+ Go 回归断言 |

## 安全不变量 SD-1..7 逐条核验

| 不变量 | 结论 | 证据 |
|---|---|---|
| **SD-1 secret 绝不泄露** | ✅ | 件① 只经 `ApplicationSummary`（无 secret 字段）；mgmt/REST/Console 三处测试均 `NotContains "secret"`/`"app_secret"`；**真实浏览器 DOM 全文扫描 `/developer` 无 `app_secret`/`rootsecret` 字面**。 |
| **SD-2 单一真相源 / 零触碰数据面 eval** | ✅ | `effperm.PreviewFilter` 复用 `dataperm.Filter.FilterSQL`（数据面同一函数，effperm 现有 `computeViews` 已同款构造 `dataperm.NewFilter(eng,table)`）；`git diff main..HEAD -- internal/sidecar/dataperm/ internal/sidecar/authz/ internal/sidecar/kernel/ internal/controlplane/adminauthz/ casbin/` = **0**。 |
| **SD-3 参数化** | ✅ | 沙箱预览 `sql="dept = ?"`、`args=["shanghai"]`；**真实浏览器验证值 `shanghai` 只在 args 列表、绝不在 `<pre>` SQL 文本内**（`sqlValueNotInText=true`）。 |
| **SD-4 只读 fail-close** | ✅ | 两 RPC `scopeApp` read；`AuthorizeRule` 前置守卫；handler 无写/无 bump/无审计；缺变量 → `InvalidArgument`（报错而非误导性 SQL）；拒绝走 `renderGRPCError` 降级。 |
| **SD-5 无新 JS** | ✅ | 两页纯 html/template 服务端渲染（attrs 服务端 `parseAttrs`）；轮换按钮复用既有 `data-confirm`（挂既有 `interactions.js`，非新增）；全 diff 无新增 `<script>`/`.js`。 |
| **SD-6 授权面最小改动** | ✅ | `authz.go` 恰 +2 条 read ruleTable 项（`GetApplication`→`{application,read,false,scopeApp}`、`PreviewDataFilter`→`{effective_permission,read,false,scopeApp}`）；决策核心零改；三面 parity 走同一 ruleTable。 |
| **SD-7 a11y** | ✅ | 真实浏览器 axe-core 4.10.2：`/developer`（含凭据 section）**0 违规**、`/data-sandbox`（空表单 + 结果态）**各 0 违规**（修复走查捕获的 `link-in-text-block` 后）；单 h1 + breadcrumb + 表单控件 aria-label + `role=status` + 沙箱 tab `aria-current`。 |

## 全量验证

- `make proto-gen` 幂等：生成后 `git status` 干净（gen 已随任务 1/2 提交）。
- `gofmt -l internal/` → 空；`go vet ./...` → 干净。
- `go test ./...` → **0 FAIL**（含 console 169s、mgmt 150s、restgw 81s、effperm 26s，及数据面 `sidecar/dataperm`、决策核心 `sidecar/kernel`/`sidecar/authz`/`adminauthz` 全绿）。

## 真实浏览器 axe 走查（2026-07-07 完成）

一次性 build-tag `walkthrough` 脚手架（`zz_walkthrough_scaffold_test.go`，复用 `newConsole` 同款真依赖装配 + `dbtest` testcontainers PG+Redis、root 超管、会话 TTL `time.Hour`、播种 1 app + 一条 `alice→viewer` + `viewer` 对 `order` 的数据策略 `dept=$user.dept`、httptest 起服务、阻塞待 SIGTERM）+ 系统 Chrome via **Playwright MCP**（`--prefer-offline @0.0.77`）+ **axe-core 4.10.2**（无 CSP header、`<script src>` 从 jsdelivr 注入）。**修 bug 后重建二进制重启再验**；走查后脚手架文件已删、进程按确切 PID 停、`.playwright-mcp` 产物清理、均未提交。

| 页 / 状态 | axe 4.10.2 违规 | 单 h1 | 关键（真实渲染核实） |
|---|---|---|---|
| `/apps/1/developer`（含凭据 section） | **0** | ✅ | `#credentials` 展示 `app_key=AK_order`、`domain=order-system`；DOM 全文无 secret 字面 |
| `/apps/1/data-sandbox`（空表单，**修复前**） | 1（`link-in-text-block`，serious） | ✅ | 见下「走查捕获的 bug」 |
| `/apps/1/data-sandbox`（空表单，**修复后**） | **0** | ✅ | breadcrumb + 三控件均有 `<label for>` + 沙箱 tab `aria-current` + 决策链接独立成段 |
| `/apps/1/data-sandbox`（结果态：alice/order/dept=shanghai） | **0** | ✅ | `渲染结果`；WHERE=`dept = ?`；args=`[shanghai]`；**值不在 SQL 文本**（SD-3）；DOM 无 secret |

### 走查捕获的 a11y bug：`link-in-text-block`（serious）

沙箱页说明段落内嵌「决策解释」链接：`…功能权限「试一试」见 <a href="…/decision">决策解释</a>。`。该链接内嵌于文字块，**仅靠颜色区分、无下划线/边框**，axe-core `link-in-text-block`（WCAG「链接不能仅靠颜色区分」，serious）命中。**Go 测试无浏览器覆盖不到**（渲染 CSS 层面），真实浏览器 axe 首次端到端跑该页时捕获。

**修复**（`f701be1`）：把决策链接移出说明段、独立成一个 `<p>` CTA（`<p><a href="…/decision">功能权限「试一试」→ 决策解释</a></p>`）——链接不再处于文字块内，规则不再适用。修后同页真实浏览器 axe 复验 **0 违规**。另补 Go 回归断言钉死 `<p><a href="/apps/{id}/decision">` 独立成段结构（未来若把链接内联回说明段即单测 FAIL，无需浏览器）——呼应「走查捕获后加回归测试」教训。

## 结论

**READY，M4.5 全部关卡闭合。** SD-1..7 逐条满足（证据如上）；`go test ./...` 0 FAIL；数据面 eval + 决策核心经 diff 硬证零触碰；`authz.go` 恰 +2 条 read 项；两页真实浏览器 axe 0 违规（修复走查捕获的 `link-in-text-block`）；沙箱端到端「输入 → 参数化 WHERE + args（值只进 args）」真实浏览器验证；DOM 无 secret 泄露。**M4「技术向建模台 + 开发者 DX」5 子项目（M4.1..M4.5）全部完结。**
