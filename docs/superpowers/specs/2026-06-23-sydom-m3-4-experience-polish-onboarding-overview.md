# M3.4 体验打磨横扫 + onboarding — 总览拆解设计

> **里程碑上下文**：M3（业务向运营台成体系）拆 4 子项目——M3.1 设计系统 + a11y 基座（✅ `d2cb5da`）/ M3.2 业务语言抽象层 + 预设·模板（✅ `12b5c8f`/`ad63068`/M3.2c-2）/ M3.3 关系可视化 + 决策模拟器（✅ `32ab2b0`）/ **M3.4 体验打磨横扫 + onboarding**（本文档）。
>
> **本文档是 M3.4 的「总览拆解 spec」**，不是单一功能实现 spec。它把 M3.4 这个体验打磨横扫拆为 3 个有序子项目，定义各自范围、依赖、验收与贯穿不变量。每个子项目随后各走自己的 spec → plan → 子代理执行 小循环。

## 1. 背景与目标

M3 的验收口径是「真实非技术用户可用性达标（任务完成率 / SUS）」。M3.1–M3.3 已铺好设计系统地基、业务语言/模板、关系可视化与模拟器。M3.4 是**收口横扫**：把体验从「功能齐备」推到「非技术用户顺手」。

M3.4 不是单一功能，而是多条松耦合的打磨流：① 交互反馈（toast / 模态二次确认 / 批量）② 表现层一致化横扫（剩余 ~25 页迁到设计系统 + breadcrumb 文案）③ 首次使用引导（onboarding 向导，解析 presets 预留的 `onboarding` 字段）。这些塞进一个 spec 会过载，故拆为 3 个有序子项目。

## 2. 关键决策

- **JS 口径 = 受控渐进增强**：M3.4 允许引入**少量、无依赖、纯原生**的渐进增强 JS（toast 自消失、`dialog.showModal()` 二次确认、批量多选），但严格守「有无 JS 都可用」基线——无 JS 时回退到服务端 flash + 二次确认页 + 单条提交。与既有 `datapolicy.js`（有无 JS 基线）范式一致。**不引入任何前端框架/构建步骤/打包器**。
- **拆 3 子项目，顺序 a → b → c**：基元先行 → 横扫时一次性采纳 → onboarding 收口。理由：交互基元是 b/c 都消费的共享件，先做避免横扫页面被触达两次（一次迁设计系统、一次接确认）。
- **后端零触碰**：M3.4 是纯表现层 + 控制面复用。onboarding 仅复用既有 RPC（ApplyTemplate 等）与唯一 AuthorizeRule，不新增鉴权、不碰 adminauthz/enforcer/sidecar/数据面。

## 3. 子项目拆解

| 子项目 | 范围 | 依赖 | 形态 |
|---|---|---|---|
| **M3.4a 交互打磨基元** | 渐进增强 JS 基元：toast 自消失（接既有静态 flash 壳）、`dialog.showModal()` 破坏性动作二次确认、批量多选。统一接入高频破坏性流（删角色 / 撤权 / 轮换 secret）。无 JS 回退完整。新增 `console/static/interactions.js`（仅渐进增强）。 | M3.1 设计系统（`.toast`/`.dialog` 静态壳已在） | 表现层 + 一处 JS；后端零触碰 |
| **M3.4b 页面迁移横扫 + breadcrumb** | 把剩余 ~25 页迁到 M3.1 设计系统 token/组件；**同一遍**接入 M3.4a 确认基元（避免两次触达每页）；补 breadcrumb 逐页文案。 | M3.4a（采纳确认基元） | 纯表现层；后端零触碰 |
| **M3.4c Onboarding 向导** | 新租户/新 app 首次引导旅程；解析 presets 预留 `onboarding` 字段；空 app → 可用 app 的分步引导，复用既有 ApplyTemplate / AuthorizeRule。落在已一致化（a/b）的地基上。 | M3.1 设计系统、M3.4a 基元、M3.2 预设·ApplyTemplate | 控制面复用 + 表现层；后端零触碰（仅复用 RPC） |

### 3.1 M3.4a 交互打磨基元（要点，详见其子 spec）
- toast：服务端已有 flash 静态壳 → JS 仅加「N 秒自消失 + 可手动关」；无 JS 时 flash 静态显示（已可用）。
- 二次确认：破坏性动作（DELETE/撤权/轮换）当前裸 `doWrite` 提交 → 加 `dialog.showModal()` 确认层（焦点陷阱 / ESC 关 / aria-labelledby）；无 JS 时回退到既有「二次确认页（GET 确认 → POST 执行）」或保留直提交。**仍走 CSRF / doWrite / AuthorizeRule / status 闸不变**。
- 批量：多选 checkbox + 单表单批量提交既有 RPC（无新批量 RPC——逐条调既有写 RPC，原子性按既有单条语义）。
- a11y：dialog 焦点管理、aria、键盘可达；axe-core 0 违规。

### 3.2 M3.4b 页面迁移横扫 + breadcrumb（要点）
- 清点剩余未迁页（M3.1 只迁了 5 旗舰页，其余 ~25 页仍靠 app.css 兜底）→ 逐页换 M3.1 组件类、补 breadcrumb 文案、顺手接 M3.4a 确认基元。
- 不改内容/行为/路由/data 键，只换表现；axe-core 对迁后页 0 违规、对比度 ≥ AA。

### 3.3 M3.4c Onboarding 向导（要点）
- presets schema 已留 `onboarding` 字段（loader 当前忽略）→ M3.4c 定义其结构并解析（步骤 / 文案 / 推荐预设包）。
- 向导：选行业预设包 → 一键 ApplyTemplate bootstrap → 引导建首个业务角色/分配 → 完成。全程业务语言、无原语（TP-8）、复用既有写 RPC + AuthorizeRule。
- 幂等 / fail-close / secret 一次性（若涉及 app 凭据展示，沿用既有一次性 secret 管线）。

## 4. 贯穿不变量（每子项目都守，各子 spec 落为编号验收）

- **EX-1 渐进增强基线**：每个新交互**有无 JS 都可用**；无 JS 路径是服务端渲染的完整功能，JS 仅增强。
- **EX-2 一份授权真相**：三面（gRPC/REST/Console）共用唯一 `AuthorizeRule` + ruleTable，无第二套授权；M3.4 不新增鉴权规则（onboarding 复用既有 RPC 的既有 ruleTable 条目）。
- **EX-3 后端零触碰**：`git diff -- internal/controlplane/adminauthz/ casbin/enforcer.go internal/sidecar/` = 0 行；M1.1 matcher 一字未改；数据面不动。
- **EX-4 a11y 基线**：迁后页 / 新交互 axe-core 0 违规；dialog 焦点陷阱 + ESC + aria；对比度 ≥ AA 4.5:1；键盘全可达。
- **EX-5 写动作安全**：所有写仍走 CSRF + doWrite + AuthorizeRule + status 闸；二次确认是 UX 增强非授权替代；批量是逐条既有写 RPC 不旁路。
- **EX-6 secret 不泄露**：secret 绝不入页面/会话/日志；一次性凭据沿用既有专管线（不 PRG/不日志）。
- **EX-7 运营台无原语（TP-8）**：业务语言一致；能力名经 bizterm/permNameMap，数据范围经 conditionPredicate 符号谓词；缺名合成「resource · 动词」绝不裸 `resource:action`。
- **EX-8 零构建**：不引入前端框架/打包器/构建步；JS 是 `//go:embed` 的无依赖原生脚本；CSS 仍 token 化分层。

## 5. 顺序与依赖

```
M3.4a 交互基元 ──┬──▶ M3.4b 页面横扫（采纳确认基元）
                 └──▶ M3.4c Onboarding（用基元 + 设计系统 + 预设）
M3.4b、M3.4c 均依赖 M3.4a；M3.4c 另依赖 M3.2 预设/ApplyTemplate（已交付）。
推荐执行序：a → b → c。
```

## 6. 验收（M3.4 里程碑级）

- 3 子项目 a/b/c 各自 spec → plan → 子代理执行 全绿并 FF 并入本地 main。
- EX-1..8 在每子项目逐条 PASS；最终 opus 整体评审 READY。
- 全 Console 页一致迁到设计系统；破坏性动作有二次确认（渐进增强）；新租户可经 onboarding 从空 app 走到可用。
- `go test ./...` 0 FAIL；gofmt/vet/proto-check 干净（onboarding 若动 proto 则 proto-check 零意外漂移）。

## 7. 不做（YAGNI / 推后）

- **i18n / 多语言**：中文单语产品暂不需；schema 不预埋翻译层。留更后里程碑按需。
- **完整移动端**（hamburger / 完整响应式重构）：M3.1 已桌面优先 + 窄屏堆叠，管理台 Beta 够用。
- **既有 `Internal %v` 透传脱敏债**：单独 chore 统一处理，不混入体验流。
- **批量写专用 RPC**：M3.4a 批量是逐条调既有写 RPC，不新增批量 RPC（YAGNI；如未来需原子批量再评）。
- **前端框架 / SPA 化**：违 EX-8 零构建，明确不做。

## 8. 后续

本总览 spec 通过后，**下一步设计 M3.4a**（交互打磨基元）的子 spec，再 plan，再子代理执行。a/b/c 依次推进。

相关：[[feedback-consistency-over-simplicity]]、[[feedback-verify-casbin-before-asserting]]、[[project-detailed-design-progress]]
