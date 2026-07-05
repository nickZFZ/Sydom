# M4.3 条件构建器 v2（Condition Builder v2）设计

> 里程碑 M4（技术向建模台 + 开发者 DX）第 3 子项目。BASE=main（M4.2 后 `9ddbb54`）。
> 范式：设计 → 计划 → 子代理驱动实现（两阶段审查）；TDD；每任务独立 commit；禁 `--amend`。

## 目标

把司域数据策略的可视化条件构建器从**单层扁平**重做为**支持任意嵌套的构建器 v2**，并**从源头修复一个潜在正确性 bug**：当前可视化构建器产出的条件与数据面引擎文法不一致，会在评估时被 fail-close 拒绝。

## 背景与动机（探索发现）

- **canonical 文法**在 `internal/sidecar/dataperm/condition.go`（数据面引擎，权威）：
  - 逻辑算子 **AND / OR / NOT**（大写）：AND/OR ≥1 子节点，NOT 恰 1 子节点，可任意嵌套。
  - 叶子算子（大写）：**EQ NE GT GE LT LE IN NOT_IN LIKE NOT_LIKE IS_NULL IS_NOT_NULL BETWEEN**。
  - value 规则：IS_NULL/IS_NOT_NULL 无 value；IN/NOT_IN 非空数组；BETWEEN 2 元数组；其余标量（非数组、非空）。
  - 字段名白名单 `^[A-Za-z_][A-Za-z0-9_]*$`（字段进 SQL 文本，堵注入）。
  - `validate()` fail-close：逻辑节点不得带 field/value，未知算子拒。
- **潜在 bug**：可视化构建器 `internal/controlplane/console/static/datapolicy.js`（JS 开启时的**默认**路径，textarea 隐藏）序列化出**小写**算子（`and`/`or`/`eq`/`ne`/`gt`/`lt`/`in`）+ 引擎**不存在**的 `contains`，且**单层无嵌套**。控制面写入路径 `UpsertDataPolicy` 把 `condition` 当**不透明串直存、从不校验**（`routes_datapolicy.go` upsert handler：`Condition: r.FormValue("condition") // 后端 fail-close`）。全仓**无算子大小写归一化**。⟹ 任何用可视化构建器搭出的条件都会在数据面评估时被 `validate()` 拒（deny）。历史上**从未被测试捕获**——Go 测试只验无 JS 的原始 JSON 路径。
- **能力缺口**：无嵌套、无 NOT、算子不全、field/value 无校验、无实时谓词预览。
- **既有可复用资产**：控制面**已复用** `sidecar/dataperm`（`effperm/effperm.go:17`，M1.3 有效权限「与 Sidecar 同源无第二套决策」）；只读符号谓词渲染器 `condition_predicate.go` 已在控制面（`condNode` 支持递归 AND/OR/NOT 渲染）。

## §1 范围与非目标

**范围**：文法对齐（构建器产出 canonical 大写合法条件）+ 嵌套 AND/OR/NOT + 13 全算子 + 按算子的 value 输入 + field 标识符校验 + 实时服务端符号谓词预览 + 写时 fail-close 校验（复用引擎校验器）。

**非目标（YAGNI）**：不引入字段元数据/schema 子系统（field 保持自由文本 + 校验，不做下拉目录）；不改数据面求值逻辑；不做条件的版本化/历史 diff（已有审计覆盖）；不做拖拽排序（键盘可达即可）。

## §2 架构：单一真相源

条件文法**只有一处定义**——数据面引擎 `dataperm`。写入路径与预览端点**复用**它（正如 effperm 已复用），杜绝第二套文法与漂移。

- `dataperm` 新增**导出** `ValidateCondition(raw string) error`（薄包住现有 `parseCondition`；**不改** `parseCondition`/`validate`/求值任何行为）。这是全系统唯一的条件校验入口。
- 依赖方向沿用既有（控制面 → `sidecar/dataperm`，与 effperm 一致），不搬包、不新建中立包。

## §3 后端

### 3a. `sidecar/dataperm`（仅 +1 导出）
```go
// ValidateCondition 校验不透明条件 JSON 是否符合 canonical 文法（fail-close）；
// 空串视为「无条件」合法（与既有语义一致，见 conditionPredicate 空串处理）。
func ValidateCondition(raw string) error { ... 复用 parseCondition ... }
```
求值逻辑（`parseCondition`/`validate`/leaf 校验/字段白名单）**一字不改**——diff 证明。

### 3b. 写入路径 fail-close 校验
`UpsertDataPolicy`（`policy` manager 或 mgmt handler，覆盖 gRPC/REST/Console 三面唯一写入口）在落库前 `dataperm.ValidateCondition(condition)`：非法 → 返回 `InvalidArgument`（错误信息含具体原因，如「未知算子」「非法字段名」「IN 需非空数组」），**不落库**。空条件放行。
> 效果：从源头堵死「构建器产出引擎拒绝的条件」这一类 bug；错误在**写时**回给用户，而非评估时静默 deny。既有可能已存的非法条件不追溯（评估时仍如旧 fail-close），新写受校验。

### 3c. 预览端点
`POST /apps/{app_id}/data-policies/preview-condition`（Console；鉴权/CSRF 沿用既有写入面机制或只读会话——预览是幂等只读，用会话鉴权即可，不改授权真相）：
- 入：条件 JSON（表单/JSON 体）。
- 处理：`dataperm.ValidateCondition` → 合法则 `conditionPredicate` 渲染符号谓词；非法则取校验错误信息。
- 出：`{predicate: string, error: string}`（HTML 片段或 JSON，供构建器内联展示）。
- 语义：只读、无副作用、不 bump、不写审计。

### 3d. `condition_predicate.go` 补全
补齐 13 个叶子算子（大写）的符号渲染：现有覆盖 AND/OR/NOT + EQ/NE/GT/GE/LT/LE + 透传 IN/BETWEEN；补 NOT_IN / LIKE / NOT_LIKE / IS_NULL / IS_NOT_NULL 的可读呈现（如 `field IS NULL`、`field NOT IN [..]`、`field LIKE "x"`）。`$user.xxx` 符号值保留原样。

## §4 前端 builder v2（`datapolicy.js`，仍是唯一 JS 文件）

- **布局 A 嵌套盒**：递归渲染组节点。每组 = 组合算子选择（AND/OR/NOT）+ 若干叶子行 + 若干子组（缩进边框盒）+「+ 条件」「+ 子组」。NOT 组视觉上限定单子节点。
- **叶子行**：field 文本框 + 算子下拉（13 大写算子）+ value 输入（**按算子自适应**：标量单框 / IN·NOT_IN 多值 chips 或逗号分隔→数组 / BETWEEN 两输入 / IS_NULL·IS_NOT_NULL 隐藏 value）+ 删除。
- **field 校验**：实时校验 `^[A-Za-z_][A-Za-z0-9_]*$`（与引擎一致），非法即行内标红。
- **value**：`$user.xxx` 符号值提示/占位；数组类算子把输入解析为 JSON 数组；标量按字符串/数字尽力。
- **序列化**：产出 canonical **大写** 条件树 JSON（`{op:"AND",children:[{field,op:"EQ",value},{op:"OR",children:[...]}]}`），写回 `#cond-json`。
- **实时预览**：构建器变动防抖（如 300ms）→ 调 §3c 预览端点 → 内联显示符号谓词 + 校验错误。
- **专业模式**：切原始 JSON textarea 手改（沿用现有 toggle）。
- **渐进增强契约不变**：无 JS 基线 = `#cond-json` textarea 可见、canonical 提交路径；有 JS 接管、提交前序列化。**无新增 JS 文件 / 无第二个 JS**。
- **a11y**：嵌套组与所有控件键盘可达、每控件可访问名（aria-label）、单 h1 + breadcrumb 保留、复用 M3.1 设计系统类。

## §5 行为契约（CB-1..8，验收标准）

- **CB-1 单一真相源**：写入 + 预览 + 数据面 eval 用**同一** `dataperm` 校验器；无第二套文法定义。
- **CB-2 写时 fail-close 有齿**：非法条件（未知算子 / 非法字段名 / value 元数不符 / 逻辑节点带 field）→ `UpsertDataPolicy` 拒（`InvalidArgument`）、不落库。**反向验证**：去掉写时校验则该测试 FAIL。
- **CB-3 构建器产出合法（补历史缺口）**：可视化构建器序列化输出被 `dataperm.ValidateCondition` **接受**（含嵌套 + NOT + 各类算子的代表样例）——这是此前从未测过的一环。
- **CB-4 渐进增强不变**：无 JS 基线原始 JSON textarea canonical 提交路径行为不变；唯一 JS 文件仍是 `datapolicy.js`，**无新增 `<script>`/.js 文件**。
- **CB-5 嵌套 & NOT 一致引擎**：任意深度 AND/OR/NOT；NOT 恰 1 子、AND/OR ≥1 子（构建器约束与引擎 `validate` 一致，空/非法组不产出非法 JSON）。
- **CB-6 预览单源**：预览复用 `condition_predicate.go`，**不在 JS 复制谓词逻辑**；防抖；非法条件预览给内联错误而非静默。
- **CB-7 数据面求值零触碰**：`sidecar/dataperm` 仅 +导出 wrapper，`parseCondition`/`validate`/leaf 校验/求值一字未改（`git diff` 证明）；`casbin/`、`adminauthz/`、`kernel/`、authz 决策零触碰。
- **CB-8 a11y**：数据策略构建器页真实浏览器 axe-core 0 违规、单 h1、breadcrumb、嵌套组键盘可达 + 控件 aria-label。

## §6 三面 parity

条件是数据策略的字段，唯一写入口 `UpsertDataPolicy` 覆盖 gRPC/REST/Console 三面 → §3b 写时校验对三面自动生效（单点施加）。预览端点与可视化构建器是 Console 专属渐进增强（REST/gRPC 用原始 JSON，写时校验同样保护）。

## §7 测试策略

- **CB-3 缺口测试**（新）：构造构建器序列化形状的 canonical JSON（嵌套/NOT/IN/BETWEEN/IS_NULL 各样例）→ 断言 `dataperm.ValidateCondition` 接受；并断言 `datapolicy.js` serialize() 产出大写算子（JS 可用轻量 DOM 测试或在 e2e 中验证）。
- **CB-2 写时校验**（testcontainers）：非法条件 → `UpsertDataPolicy` `InvalidArgument` 不落库；反向验证（去校验则 FAIL）。
- **预览端点**：合法→谓词串、非法→错误信息；会话鉴权；只读无副作用。
- **`condition_predicate`**：表驱动覆盖 13 算子 + 嵌套 + NOT 渲染。
- **`dataperm.ValidateCondition`**：复用既有 `condition_test.go` 样例断言导出入口与内部一致。
- **e2e + 真实浏览器 axe 走查**（CB-8）：数据策略页嵌套构建 → 实时预览 → 保存 → 列表谓词，逐页 axe 0 违规（沿用 M4.2 走查脚手架范式，Playwright MCP 已修 `--prefer-offline @playwright/mcp@0.0.77`）。
- 全量：gofmt / `go vet ./...` / `go test ./...` 0 FAIL；关键正确性测试含反向验证（M2.4 教训）。

## §8 任务分解（供 writing-plans 细化）

1. `dataperm.ValidateCondition` 导出 + 单测（求值零改，diff 证明）。
2. `condition_predicate.go` 补全 13 算子渲染 + 表驱动测试。
3. 写入路径 fail-close 校验（`UpsertDataPolicy`）+ 三面覆盖 + 反向验证测试（CB-2）。
4. 预览端点（Console）+ 测试（CB-6）。
5. `datapolicy.js` builder v2：嵌套盒 + 13 算子 + 自适应 value + field 校验 + 序列化大写 + 防抖预览 + 专业模式（CB-3/4/5）。
6. 模板/样式（嵌套盒 M3.1 设计系统类、a11y）。
7. 整体核验 CB-1..8 + 真实浏览器 axe 走查 + opus 评审 + FF。

## §9 自检对照（写完本设计后，全新视角）

- 占位符：无「待定/TODO」；每节含具体机制。
- 一致性：写时校验与 eval 用同一 `dataperm` 校验器（CB-1/7）；构建器序列化大写 ⟺ 引擎大写（CB-3）；预览复用谓词渲染器不复制（CB-6）——内部一致。
- 范围：聚焦条件子系统，单个实现计划可覆盖（7 任务，规模近 M4.2）。
- 模糊性：预览端点鉴权明确为会话只读；写时校验空条件放行明确；field 保持自由文本（非目录）明确。
