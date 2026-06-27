# M3.4c Onboarding 向导 — 设计

> **里程碑上下文**：M3.4（体验打磨横扫 + onboarding）拆 3 子项目——M3.4a 交互打磨基元（✅ `8920e49`）/ M3.4b 页面迁移横扫 + breadcrumb（✅ `60a4bd6`）/ **M3.4c Onboarding 向导**（本文档，M3.4 收官）。
>
> **总览 spec**：`docs/superpowers/specs/2026-06-23-sydom-m3-4-experience-polish-onboarding-overview.md`（贯穿不变量 EX-1..8、§3.3 M3.4c 要点）。本文档是 M3.4c 子项目实现 spec，随后走 plan → 子代理执行。

## 1. 背景与目标

M3.1 铺设计系统、M3.2 给业务语言/官方预设包 + `ApplyTemplate`、M3.3 给关系可视化/模拟器、M3.4a/b 给交互基元 + 全页一致化。但**新租户/新 app 首次使用仍是「空白起点」**：建好 app 后是一张白纸（无权限点、无业务角色），非技术租户管理员不知从何下手。`presets` 的内嵌 JSON 当前可携带 `onboarding` 字段但 loader 忽略它（M3.2c 预留 M3.4）。

**目标**：给新 app 一条**首次引导旅程**，把「空 app」一步步带到「真正可用（有业务角色 + 有人被分配）」。全程业务语言、无原语，**复用既有 `ApplyTemplate` / 分配 RPC + 唯一 `AuthorizeRule`**，**后端零触碰**（不新增 RPC/迁移/DB 列；presets schema 是内嵌 JSON 非 DB）。落在 M3.4a/b 已一致化的设计系统地基上，有无 JS 都可用。

## 2. 关键决策

- **完整旅程，4 步固定在代码**：① 选官方预设包 → ② 一键 `ApplyTemplate` bootstrap（种权限点 + 业务角色 + 数据范围）→ ③ 引导把首个用户分配到某业务角色（**可跳过**）→ ④ 完成（指向运营台后续）。步骤结构写死在代码，**不由 schema 驱动异构分步**（YAGNI + 守「有无 JS 都可用」的服务端多步渲染简单性）。
- **入口 = 派生横幅 + 专用路由，零持久化**：「是否需要引导」**派生自 app 是否为空**（无业务角色），不新增「已完成」标志列（守后端零触碰）。app 为空时运营台 app 区 + 仪表盘顶部显示「开始引导」横幅；bootstrap 后 app 非空 → 横幅自然消失。运营台导航另常驻一个「引导」入口（随时可重进，幂等）。
- **onboarding schema 最小（策展 + 文案）**：`presets.Template` 加可选 `onboarding` 字段——`recommended`（选包步骤置顶/标推荐）、`intro`（每包一句业务语言简介）、`next_steps`（完成页「接下来你可以…」文案清单）。未知字段既有 json 忽略、缺省 fail-soft（无 onboarding 内容即按无策展渲染）。schema 只做策展 + 文案，不承载步骤逻辑。
- **后端零触碰、零新鉴权**：向导路由复用既有 RPC（`ApplyTemplate`、分配角色）与既有 ruleTable 条目，**不新增 AuthorizeRule 规则**、不碰 adminauthz/enforcer/sidecar/proto/数据面/迁移。
- **纯表现层 + 控制面复用**：向导是 Console 既有 BFF 内的新路由组 + 模板 + presets schema 解析；唯一可能的 `.go` 改动在 console handler（新路由）与 presets（schema 字段 + loader 宽松解析）——均非 adminauthz/enforcer/sidecar。

## 3. 范围

| 在范围 | 不在范围（§9） |
|---|---|
| presets `onboarding` schema 定义 + loader 解析（宽松、fail-soft）+ 2 官方包补 onboarding 内容 | schema 驱动异构分步；i18n |
| 向导 4 步路由 + 模板（选包/apply/assign/done）+ 空 app 横幅 | 持久化 onboarding 进度/「已完成」标志 |
| 复用 `ApplyTemplate`（bootstrap）+ 分配 RPC（assign）+ 唯一 AuthorizeRule | 新批量/新 RPC；新鉴权规则 |
| 结构性 TDD + 行为回归 + axe 走查 | 注册后强制重定向劫持（用横幅不劫持）|

## 4. onboarding schema（presets）

`presets.Template` 加可选字段：
```jsonc
"onboarding": {
  "recommended": true,
  "intro": "适合需要分级运营与数据隔离的团队",
  "next_steps": ["在「人员」分配更多成员", "在「业务角色」微调能力", "在「模板库」按需追加"]
}
```
- `Onboarding *Onboarding` 指针（缺省 nil = 无策展，向导仍可列该包但不置顶/无 intro）。
- loader 校验**宽松**：onboarding 字段全可选；`next_steps` 任意条数（含 0）；无内容不报错（fail-soft，不破坏 M3.2 既有 fail-close 严格校验的权限点/角色部分）。
- 2 个官方包（通用后台 / 电商运营）补 onboarding 内容；新增内容为业务语言中文。

## 5. 路由与 4 步（全部 scopeApp，path 权威，复用既有 ruleTable）

| 步 | 路由 | 复用的写/读 | 说明 |
|---|---|---|---|
| 横幅 | 注入既有 ops 页 + 仪表盘（无独立路由）| ListRoles（读，判空）| app 无业务角色 → 渲「开始引导」横幅；非空不渲 |
| ① 选包 | `GET /ops/apps/{app_id}/onboarding` | presets.All（内嵌读）| 列官方包，recommended 置顶 + intro 文案；选一个进步骤② |
| ② Bootstrap | `POST /ops/apps/{app_id}/onboarding/apply` | `ApplyTemplate`（既有 RPC，幂等）| CSRF + AuthorizeRule + status 闸；成功 flash/toast → 重定向步骤③ |
| ③ 分配 | `GET/POST /ops/apps/{app_id}/onboarding/assign` | ListRoles（读）+ 分配角色 RPC（既有 opsAssignRole）| 输入首个用户标识 + 选刚建业务角色 → 绑定；**可跳过**（链接直达步骤④）|
| ④ 完成 | `GET /ops/apps/{app_id}/onboarding/done` | —（读 presets next_steps）| 完成卡片 + next_steps 指向运营台（人员/业务角色/模板库）|

- 横幅/入口位置：运营台 ops 区（人员/业务角色/模板库页顶）+ 仪表盘；运营台导航常驻「引导」链接。
- 未知 template → InvalidArgument 不泄露；跨租户 app → AuthorizeRule fail-close NotFound/PermissionDenied（复用既有 scopeApp `TenantDomainOf`）。

## 6. 状态模型（零持久化）

- 「需要引导」= app 为空（ListRoles 无业务角色）。bootstrap 后非空 → 横幅消失。
- 向导步骤进度仅靠路由/POST 前进，不跨会话持久化、不入会话/DB。
- 幂等：重进向导 / 重复 apply 安全（`ApplyTemplate` 既有幂等 + auto 不覆盖 manual + 确定性 code）；assign 重复绑定按既有单条语义。

## 7. 验收不变量 OB-1..7（落 EX-1..8）

- **OB-1 渐进增强基线（EX-1）**：向导每步无 JS 时服务端渲染完整可走通（GET 渲染 + POST 前进）；有 JS 仅复用 M3.4a 既有 toast/确认基元，**不新增 JS 文件**。
- **OB-2 一份授权真相（EX-2）**：复用唯一 `AuthorizeRule` + ruleTable，**零新增鉴权规则**；onboarding 写路由映射既有 RPC（ApplyTemplate/分配）的既有 ruleTable 条目；`git diff` adminauthz `authz.go` 无新规则行。
- **OB-3 后端零触碰（EX-3）**：`git diff <BASE>..HEAD -- internal/controlplane/adminauthz/ casbin/enforcer.go internal/sidecar/ api/proto gen/` = **0 行**；M1.1 matcher 一字未改；**无迁移 / 无新 DB 列 / 无新 RPC**（schema 是内嵌 JSON）。允许的 `.go` 改动仅限 console handler（新路由）+ presets（schema 字段 + loader 宽松解析）。
- **OB-4 a11y（EX-4）**：向导各页 axe-core 0 违规；单一 `<h1>` + breadcrumb；对比度 ≥ AA 4.5:1；键盘全可达。
- **OB-5 写动作安全（EX-5）**：apply/assign 仍走 CSRF + AuthorizeRule + status 闸；向导是 UX 编排非授权替代；幂等 fail-close（未知 template/跨租户不泄露存在性）。
- **OB-6 业务语言无原语（EX-7/TP-8）**：全程业务名（bizterm/permNameMap），数据范围经 conditionPredicate 符号谓词；不渲 role_id/code/谓词；缺名合成「resource · 动词」绝不裸 `resource:action`。
- **OB-7 secret 不泄露（EX-6）**：向导不展示/不产生 app 凭据（bootstrap 仅种授权模型，不碰 secret）；不入会话/日志/页面。

## 8. 测试策略

- **结构性 TDD**：向导 4 步各 200 + 单 h1 + breadcrumb；空 app 横幅出现、bootstrap 后（app 非空）横幅消失。
- **行为**：apply 幂等（重复 apply 不重复种、auto 不覆盖 manual）；assign 走既有授权绑定成功；跨租户 app onboarding 路由 fail-close（NotFound/PermissionDenied 不泄露）；未知 template → InvalidArgument。
- **presets loader**：onboarding 字段宽松解析（有/无/部分字段均不破坏既有严格校验）；2 官方包 onboarding 内容被正确加载。
- **回归网**：既有 Console / presets / policy 测试全绿。
- **末任务整体**：OB-1..7 逐条核验 + 全量 `go test ./...` 0 FAIL + gofmt/vet/proto-check 干净 + 真实浏览器 axe 走查向导各页 0 违规 + opus 整体评审 READY + FF 合并本地 main（不 push origin）。

## 9. 不做（YAGNI / 推后）

- **持久化 onboarding 进度 / 「已完成」标志**：违后端零触碰（无新 DB 列），用派生空态横幅替代。
- **schema 驱动异构分步**：4 步固定在代码，schema 只做策展 + 文案。
- **注册后强制重定向劫持**：用横幅引导，不劫持 register/create-app 落地。
- **新批量 / 新 RPC / 新鉴权规则**：复用既有写 RPC + 唯一 AuthorizeRule。
- **i18n / 多语言**：中文单语，照 umbrella §7。

相关：[[feedback-consistency-over-simplicity]]、[[feedback-verify-casbin-before-asserting]]、[[project-detailed-design-progress]]
