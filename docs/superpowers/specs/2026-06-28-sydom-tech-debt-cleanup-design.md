# 司域 技术债清理（一轮）— 设计

> 日期：2026-06-28。BASE = 本地 main `8718aff`（M3 里程碑收官后）。
> 来源：M1–M3 各子项目记录在记忆中的「非阻塞观察项」，控制者已逐项回源核实当前代码真实状态后分级。
> 范式：一份 spec → 一份 plan（分组任务）→ 子代理驱动 + 两阶段审查（先规格后质量）+ 整体安全评审 + FF 并入本地 main（不 push origin）。

## 1. 背景与目标

M1–M3（多租户基座 / 授权功能纵深 / 业务向运营台）三里程碑落地过程中累积了一批**非阻塞**技术债，散记于各子项目记忆。本轮一次性清理，**不引入新功能**，只补齐一致性、脱敏纵深、DB 强制与表现层规范。授权决策核心（`AuthorizeRule` / 唯一 `ruleTable` / M1.1 matcher / casbin `enforcer.go`）**一字不改**。

本轮范围（用户确认纳入后，A1 经回源核实已实现而移除）：A2 关系表复合 FK（全 3 表）、A3 operator 术语统一为「操作员」、B 表现层快修、C 扫尾（仅纳入 datapolicies 删除确认，余项 YAGNI 推迟）。

## 2. A1 · Internal 错误脱敏拦截器 —— 已实现，移除（回源核实）

**结论：本项无需做。** 设计期回源核实发现 `internal/controlplane/mgmt/errorsanitize.go` 已存在 `SanitizeErrorUnaryInterceptor(logger)`，且 `mgmt/server.go:124` 已把它装配为 `ChainUnaryInterceptor` 的**最外层**（位置 0，先于 Authz）：对 `Internal`/`Unknown`（含裸 error 经 `status.Convert` 归 Unknown）→ 回通用文案 "internal error"、原始细节仅进服务端日志；其余 code（NotFound/InvalidArgument/PermissionDenied…API 契约文案）原样透出。即「直连 gRPC」这一路早已与 REST `writeError` / Console `renderGRPCError` 的 500 脱敏铁律对齐。

记忆里「Internal %v 透传〔统一脱敏既有债〕」是**陈旧债项**——后续某里程碑已补齐拦截器；mgmt 那 171 处 `%v` 是写给日志的细节、并非泄漏面。本项从范围移除，仅留此记录（呼应「写论断先回源核实」）。唯一可选微调（DataLoss 也纳入脱敏）属 YAGNI——本代码库从不返回 DataLoss，不做。

## 3. A2 · role/permission 关系表复合 FK（全 3 表）

**现状（回源核实）**：`role`、`permission` 均为 `id BIGINT PK` + `app_id NOT NULL` + `UNIQUE(app_id, code)`。三张关系表按**单列**引用，未强制被引用的 role/permission 属同一 app：
- `role_permission`（000005）：`permission_id→permission(id)`、`role_id→role(id)`。
- `role_inheritance`（000006）：`parent_role_id→role(id)`、`child_role_id→role(id)`。
- `user_role_binding`（000007）：`role_id→role(id)`。

应用层（GrantPermission 等）已在 app 层校验局部性（M1.4：投影域取 rp.app_id、非越权）。本项是 **DB 强制的防御纵深**，补齐「DB 真相源」一致性；同款缺口 3 表一次性补齐（一致性优先）。

**设计**：新迁移（下一可用编号 `000019`，up + down 可逆）：
1. `role` 加 `UNIQUE(app_id, id)`；`permission` 加 `UNIQUE(app_id, id)`（作复合 FK 目标）。
2. up **前置数据校验**：若任一关系表存在「引用的 role/permission 的 app_id ≠ 本行 app_id」的跨 app 行，迁移**失败报错**（fail-close，不静默修复/删除）。
3. `role_permission`：DROP 旧单列 FK `fk_role_permission_role` / `fk_role_permission_permission`，ADD 复合 `(app_id,role_id)→role(app_id,id)`、`(app_id,permission_id)→permission(app_id,id)`。
4. `role_inheritance`：parent/child 两 FK 改复合 `(app_id,parent_role_id)→role(app_id,id)`、`(app_id,child_role_id)→role(app_id,id)`。
5. `user_role_binding`：`(app_id,role_id)→role(app_id,id)`。
6. down：逆序还原为单列 FK + 删除新增 UNIQUE。

**应用层零行为变化**：现有写路径已保证局部性，复合 FK 只是把不变式下沉到 DB。

**TDD**（testcontainers 真 PG）：迁移 up/down 跑通；正常同 app 写入成功；**构造跨 app 引用直接 SQL 插入 → 被复合 FK 拒绝**（有齿）；既有 seeder/测试数据全绿。

## 4. A3 · operator 术语统一为「操作员」

**现状**：4 模板用「算子」、4 模板用「操作员」，分裂。「算子」在中文指数学/函数算子，用于「人」（管理账户）不自然。

**设计**：把所有用户可见中文展示文案中的「算子」改「操作员」（模板 + 任何 system 域页面标题/确认文案/flash 文案）。**代码标识符 `operator`（Go 标识符、RPC 名、列名、URL 路径段）一律不动**，仅中文展示层。全仓 grep 核实改后无「算子」残留（展示文案范畴）。

## 5. B · 表现层快修

- **B4** `ops_role_new.html:12` 权限点勾选项裸回退 `{{.Resource}}:{{.Action}}` → 在 opsRoleNew handler 侧用 `capabilityName`（bizterm，镜像 `opsTemplates` 的 capRow 范式）合成业务名，模板只渲 `.Name`，缺名也不露裸 `resource:action`（TP-8）。
- **B5** `ops_templates.html`：第二个 `<h1>我的模板</h1>` → `<h2>`（页面恰一 h1=「模板库」）；5 处行内 `style="…"` 抽到 `components.css` 语义类（沿用 token，零硬编码色）。
- **B6** `ops_templates.html:41`「删除」+ `ops_tenant_template.html` 同类破坏按钮 `class="btn"` → `class="btn danger"`（与已迁页破坏按钮一致；data-confirm 已在 M3.4a 接入，本项只补视觉危险态）。

行为/路由/renderPage data 键不变；axe 0 违规（h1 层级修复为单 h1）。

## 6. C · 扫尾（YAGNI 三审）

- **纳入**：`DeleteDataPolicy`（routes_datapolicy.go:80 当前走 `doWrite` 未走确认）接入 `requireConfirm`（与 M3.4a 8 个破坏动作一致——删除确属破坏；服务端确认页 + data-confirm）。
- **推迟（YAGNI，明列不做）**：
  - usersWithRole O(N)（GetRoleGraph，Beta 规模无实测瓶颈）。
  - SetFlash 非原子 RMW（M3.4a 已判 Beta 可接受、并发边角）。
  - rotate/reset 双 CSRF（`requireConfirm` 内已 checkCSRF，handler 再查一次为无害冗余、动安全路径无功能收益）。
  - onboardingDone 不校验 template_id（onboardingOf nil-safe → 空 next_steps 优雅降级，无 bug）。

## 7. 不变量（TD-1..8，验收逐条核）

- **TD-1 授权真相零触碰**：`git diff` `internal/controlplane/mgmt/authz.go`（ruleTable）、`adminauthz/`、`casbin/enforcer.go`、M1.1 matcher = 0 行。A2 纯 schema；A3/B/C 表现层与确认门复用。
- **TD-2 A2 防御纵深 + fail-close**：复合 FK 拒绝跨 app 引用（有齿）；up 前置跨 app 数据校验失败即拒迁不静默；down 可逆；应用层行为不变（既有测试全绿）。
- **TD-3 A3 纯展示**：仅中文展示文案改「操作员」；代码标识符/列名/路径不动；全仓无「算子」展示残留。
- **TD-4 B 表现层守恒**：行为/路由/data 键不变；B4 无裸原语（capabilityName）；B5 单 h1 + 零硬编码色；B6 破坏按钮 danger；axe 0。
- **TD-5 C 一致**：DeleteDataPolicy 确认门与 M3.4a 一致；推迟项明列且确无引入风险。
- **TD-6 secret 绝不泄露**：全程无 secret 渲染/日志/响应泄露（A1 出口脱敏既有，不在本轮范围但仍生效）。
- **TD-7 全验证绿**：`go test ./...` 0 FAIL（含 e2e + A2 迁移 testcontainers 实测）、`gofmt -l internal/ api/` 空、`go vet ./...` 净、`make proto-check` 零漂移（proto 未动）。

## 8. 任务分组（交 writing-plans 细化）

1. A2 复合 FK 迁移 000019（up/down + 数据校验 + testcontainers TDD）。
2. A3 术语统一（模板 + system 域文案，grep 核实）。
3. B 表现层快修（B4 handler+模板 / B5 ops_templates h2+CSS / B6 danger）。
4. C datapolicies 删除确认门接入。
5. 整体核验 TD-1..7 + 安全评审 + FF。

> A1 经回源核实已实现，移出任务。任务间基本独立，可并行实现、各自两阶段审查；A2 为最重单任务。
