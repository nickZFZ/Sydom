# M4.3 条件构建器 v2（Condition Builder v2）整体核验记录

> 里程碑：M4.3（技术向建模台 + 开发者 DX 之条件构建器增强）。分支 `worktree-feat+m4-3-condition-builder-v2`，BASE=main `160fdc5`（含 M4.3 设计 spec；上一里程碑 M4.2 `9ddbb54`）。
> 实现范式：子代理驱动 + 两阶段审查（规格合规 → 代码质量），每任务 TDD、独立 commit，禁用 `--amend`。

## 交付概要

把数据策略可视化条件构建器从**单层扁平**重做为**支持任意嵌套 AND/OR/NOT + 13 全算子的 v2**，并**从源头修复潜在正确性 bug**：旧构建器序列化出小写算子（`and`/`eq`）+ 引擎不存在的 `contains`、单层无嵌套 → 数据面 eval 时被 `validate()` fail-close 拒。

**架构：单一真相源。** 条件文法只在数据面引擎 `dataperm` 定义一处；导出 `dataperm.ValidateCondition`（薄包 `parseCondition`，求值零改），控制面写入路径（mgmt handler 三面单点）+ 服务端谓词预览端点复用它（正如 effperm 已复用 dataperm），杜绝第二套文法。前端 `datapolicy.js`（仍是唯一 JS 文件，渐进增强）序列化 canonical **大写** JSON，防抖调预览端点显示符号谓词。

## 提交序列（BASE `160fdc5` 之后）

| commit | 层 | 摘要 |
|---|---|---|
| `5c90a65` | dataperm | 导出 `ValidateCondition`（薄包 parseCondition，条件文法单一真相源入口，求值零改） |
| `535cde5` | dataperm | ValidateCondition 单测清理（删死 import + gofmt + 对齐 `require.ErrorIs` 哨兵校验，质量审查收敛） |
| `3b91c9e` | console | 符号谓词渲染补全 13 叶子算子（IS_NULL/IS_NOT_NULL/LIKE/NOT_LIKE/NOT_IN，大写对齐引擎） |
| `fa9e543` | mgmt | UpsertDataPolicy 写时 fail-close 校验（复用 ValidateCondition，三面单点，非法即 InvalidArgument 不落库，反向验证有齿） |
| `a90a62f` | console | 条件预览端点（服务端复用 dataperm 校验 + conditionPredicate 渲染，幂等只读会话鉴权，单源不复制逻辑） |
| `85fe863` | console | **修复**：预览端点鉴权失败也返回 JSON（抽 `lookupSession` 不写响应重构，401/403 JSON 契约一致，测试锁 Content-Type+状态码——呼应 fetch 消费，质量审查捕获） |
| `3e75c6f` | console | 条件构建器 v2（嵌套盒 AND/OR/NOT + 13 大写算子 + 按算子自适应 value + field 校验 + 大写序列化修文法 bug + 防抖服务端预览，仍唯一 JS 文件渐进增强） |
| `8db861b` | console | **修复**：构建器 `parseScalar` 保真（布尔 round-trip 不腐化成字符串 + 仅严格可逆才转数字防雪花 ID/hex/前导 0 静默改值 + `$var` 先 trim，质量审查捕获，呼应权限一致性） |
| `f12d812` | console | **修复**：构建器模式移除 `cond-json` 的 `required`（`display:none` 不豁免约束校验致提交被浏览器拦截、序列化监听器永不执行；随模式切换 required，服务端 fail-close 仍是真闸）——**真实浏览器走查捕获** |

## CB-1..8 逐条核验（对照 spec §5）

| CB | 结论 | 依据 |
|---|---|---|
| **CB-1 单一真相源** | ✅ | 写入（mgmt handler）+ 预览（console 端点）+ 数据面 eval 全部走同一 `dataperm.ValidateCondition`（`condition.go:96`，纯委托 `parseCondition`）；无第二套文法定义。谓词渲染唯一入口 `conditionPredicate`，预览端点复用不复制。 |
| **CB-2 写时 fail-close 有齿** | ✅ | `UpsertDataPolicy`（`server.go`）effect 校验后、落库前 `ValidateCondition`：非法（未知算子/非法字段名/IN 非数组/空串）→ `InvalidArgument` 不落库（`TestAdminService_UpsertDataPolicy_InvalidCondition_Rejected` 表驱动 + Len 0）。**反向验证**：临时删校验行 → 测试 FAIL（非法被接受落库 0x3→0x0）；恢复 → PASS。子代理独立复现属实。 |
| **CB-3 构建器产出合法（补历史缺口）** | ✅ | Go 契约锚 `TestBuilderV2SerializedShapesAccepted` 断言 5 类 canonical 大写形状（嵌套 AND/OR/NOT + IN + BETWEEN + IS_NULL + NOT_LIKE）被 `ValidateCondition` 接受。**真实浏览器端到端**：构建器搭嵌套条件 → 保存 → 落库为 canonical 大写 JSON（见下端到端）→ 写时校验接受、无 fail-close。此前小写/`contains`/单层从未测过。 |
| **CB-4 渐进增强不变** | ✅ | 无 JS 基线 `#cond-json` textarea 可见、`name="condition"` `required` canonical 提交；有 JS 接管、提交前序列化写回。唯一 JS 仍是 `datapolicy.js`（+ 既有 `interactions.js`），**无新增 `<script>`/.js 文件**（`static/` 目录核实）。 |
| **CB-5 嵌套 & NOT 一致引擎** | ✅ | serialize 只从大写常量表 `LOGICAL_OPS`/`LEAF_OPS` 取 op（物理上不可能产小写/`contains`）；AND/OR≥1 子、NOT 恰 1 子（浏览器实测：NOT 组加满 1 子即隐藏 +按钮）；空组/空行序列化返 null 跳过（预览实测空子组不产非法 JSON）。 |
| **CB-6 预览单源** | ✅ | 预览端点复用 `conditionPredicate`（`condition_predicate.go`）+ `ValidateCondition`，**JS 无自建谓词逻辑**；防抖 ~300ms（`PREVIEW_DEBOUNCE_MS`）；非法条件预览内联给 error 而非静默；谓词用 `textContent` 显示（防 XSS）。 |
| **CB-7 数据面求值零触碰** | ✅ | `git diff 160fdc5..HEAD -- casbin/ adminauthz/ kernel/` = **0 行**；`dataperm/condition.go` 仅 +`ValidateCondition`（8 行，零删除，`parseCondition`/`validate`/`validateLeaf`/字段白名单一字未改）；sidecar 仅 dataperm 触碰。authz 决策零触碰。 |
| **CB-8 a11y** | ✅ | 真实浏览器 axe-core 4.10.2 三态各 0 违规（下）；单 h1「数据策略」+ breadcrumb「建模台 · 数据策略」；每 select/输入/按钮有 aria-label；嵌套组均原生 `<select>/<input>/<button>` 天然键盘可达。 |

## 全量验证

```
gofmt -l internal/        → 空（干净）
go vet ./...              → 干净（exit 0）
go build ./...            → 干净
go test ./...             → exit 0；35 ok，0 FAIL，9 no test files（含 test/e2e、sidecar/dataperm、mgmt、console）
```

**CB-7 零触碰硬核验**：
```
git diff 160fdc5..HEAD -- casbin/ internal/controlplane/adminauthz/ internal/kernel/  →  0 行
git diff 160fdc5..HEAD -- internal/sidecar/dataperm/condition.go  →  仅 +ValidateCondition（0 删除）
git diff 160fdc5..HEAD -- internal/sidecar/  →  仅 dataperm（condition.go +9、condition_validate_test.go +34）
```
授权真相源（casbin / adminauthz / kernel）与数据面求值（parseCondition/validate/leaf/字段白名单）零改动，diff 证明。

## ✅ 真实浏览器 axe 走查 + 端到端（2026-07-05 完成）

一次性 build-tag `walkthrough` 脚手架（`zz_walkthrough_scaffold_test.go`，复用 `newConsole` 装配 + `dbtest` testcontainers PG+Redis、root 超管、会话 TTL `time.Hour`、播种 1 app + 1 条既有数据策略、URL 写文件、阻塞待 SIGTERM）+ 系统 Chrome via **Playwright MCP**（`--prefer-offline @playwright/mcp@0.0.77`，见 M4.2 Reference）+ **axe-core 4.10.2**（浏览器可达 jsdelivr、`<script src>` 注入；console 无 CSP，注入无阻）。脚手架静态资源 `go:embed`，**修 bug 后重建二进制重启**再验；走查后脚手架文件已删、进程按确切 PID（672474）停、无残留容器，均未提交。

| 页/状态 | axe 4.10.2 违规 | 单 h1 | breadcrumb | 关键（真实渲染核实） |
|---|---|---|---|---|
| 数据策略（构建器可见态） | **0** | ✓「数据策略」 | ✓ | JS 接管：`#builder` 显示、`#cond-json` textarea 隐藏；op 下拉 13 大写算子；AND/OR/NOT 组合 select + +条件/+子组 全带 aria-label |
| 数据策略（满嵌套构建态） | **0** | ✓ | ✓ | 三层嵌套 AND[叶子, OR[IN 叶子, NOT[叶子]]] 全控件 aria-label + 预览容器 `role=status aria-live=polite` |
| 数据策略（列表渲染态，含嵌套策略） | **0** | ✓ | ✓ | 保存后列表渲染嵌套条件 JSON + 既有 subject 判别 aria-label |

**端到端流程（真实浏览器，修复后真实代码重跑）**：
1. 无 JS→有 JS 接管：构建器出现、textarea 隐藏、`#cond-json` `required` 随构建器模式移除（专业模式恢复）。
2. 搭建三层嵌套：根 `AND`{ `dept EQ $user.dept`, `OR`{ `status IN [pending, approved]`, `NOT`{ `archived EQ true` } } }。
3. **实时防抖预览**（服务端 conditionPredicate 渲染）内联显示：`(dept = $user.dept AND (status IN [pending, approved] OR NOT archived = true))`——嵌套 + IN 数组 + NOT + 布尔 `true`（非 `'true'` 字符串）全正确。
4. value 按算子自适应：切 IN → value 输入变「值列表（逗号分隔）」；NOT 组约束恰 1 子项（加满隐藏 +按钮）。
5. **保存 → PRG 回列表 → 落库为 canonical 大写嵌套 JSON**：`{"op":"AND","children":[{"op":"EQ","field":"dept","value":"$user.dept"},{"op":"OR","children":[{"op":"IN","field":"status","value":["pending","approved"]},{"op":"NOT","children":[{"op":"EQ","field":"archived","value":true}]}]}]}`——**写时校验接受、无 fail-close**，端到端证明文法 bug 已修复（含布尔 `true` 保真落库）。
6. 专业模式 toggle 往返：builder(`required=false`,隐藏) ↔ pro(`required=true`,可见,textarea 回填序列化)。

### 走查捕获并修复的真实 bug（`f12d812`）

`#cond-json` textarea 是 `required` 且 JS 以 `display:none` 隐藏。**CSS `display:none` 并不豁免约束校验**（常见误解；实测 `willValidate===true`）——用户在构建器模式点保存，浏览器在 submit 事件**之前**做原生校验，发现空 required 即拦截提交（`An invalid form control with name='condition' is not focusable`），写序列化的 submit 监听器**永不执行**，构建器搭的条件永远存不进去。**此 bug 自构建器诞生即潜伏**（Go 测试只覆盖无 JS 原始 JSON 路径，此前无任何走查端到端测过构建器**保存**）；M4.3 真实浏览器走查首次端到端测构建器保存，捕获。修复：`required` 随模式切换（构建器移除、专业恢复、无 JS 保留），服务端 `ValidateCondition` fail-close 仍是真闸。修复后真实代码重跑走查确认端到端可用。

## 审查中捕获并修复的其它真实缺陷（子代理两阶段审查）

- **`85fe863`（预览端点 JSON 契约）**：首个 fetch 消费的 JSON 端点鉴权失败却返回 HTML（session 过期 302→HTML 被 fetch 静默跟随、CSRF 失败 HTML 错误页）→ `resp.json()` SyntaxError。改为全分支 JSON（401/403），抽 `lookupSession` 不写响应原语，测试锁 Content-Type + 状态码。
- **`8db861b`（parseScalar 保真）**：布尔 `true` 经构建器 round-trip 被腐化成字符串 `"true"`（翻转 SQL 语义，触权限一致性红线）；雪花 ID/hex/前导 0/科学计数被 `Number()` 静默改值。改为布尔保真 + 仅 `/^-?\d+(\.\d+)?$/ && String(Number(t))===t` 才转数字。

## 补充安全审视（opus 整体评审）

- **单一真相源**：写入/预览/eval 同一 `dataperm` 校验器，无文法漂移（CB-1）。
- **字段白名单堵注入**：`^[A-Za-z_][A-Za-z0-9_]*$` 仍是唯一防线，构建器 field 正则与引擎逐字符一致；非法 field 视觉标红但仍序列化提交，服务端写时校验兜底 fail-close（与整体 fail-close 一致）。
- **预览端点无越权**：不读任何 app 数据、不按 app_id 查询、不泄露 app 存在性；session+CSRF 足够，无 AuthorizeRule 安全。
- **无 secret 泄露**：预览/构建器/列表路径绝不含凭据。
- **无新 JS**：全 diff 无新增 `<script>`/.js；`fetch` 只打本域预览端点。

## 裁决

**READY，真实浏览器 axe 走查已完成。** CB-1..8 逐条满足；全量套件 35 ok / 0 FAIL；授权核心 + 数据面求值零触碰经 diff 硬证明；数据策略页三态真实浏览器 axe-core 4.10.2 各 **0 违规**；构建器搭嵌套 AND/OR/NOT → 实时预览 → 保存 → 落库 canonical 大写（含布尔保真）→ 写时校验接受全链路真实浏览器验证，端到端修复文法 bug。走查另捕获并修复一个自构建器诞生潜伏的提交拦截 bug（`required`+`display:none`）。M4.3 全部关卡闭合。
