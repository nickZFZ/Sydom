# 司域 M4.2 · 批量操作（Bulk Operations）— 设计

> 生产就绪路线图第 4 里程碑 **M4（技术向建模台 + 开发者 DX）** 的第 2 个子项目。M4 拆 5 子项目（M4.1 策略即代码+导入导出 ✅ / **M4.2 批量操作** / M4.3 条件构建器增强 / M4.4 API 文档门户+quickstart / M4.5 开发者 sandbox+密钥管理 UI）；本 spec 仅覆盖 **M4.2**。BASE = 本地/远端 main `f84d452`（M1–M3 + 技术债清理 + M4.1 均已落地）。

## 1. 背景与目标

差距分析（`2026-06-13-sydom-production-readiness-roadmap.md` §B/§C）列出两条相关缺口：「批量 / 导入导出 / 策略即代码（IaC）：无」与「列表能力：批量选择、内联编辑」。M4.1 已交付 import/export/IaC——对**整个 app 模型**做声明式期望态原子收敛。M4.2 补齐**另一半**：在列表页**勾选具体行 → 一个动作批量作用**（select-and-act），这是与 IaC 期望态互补、且心智完全不同的targeted 增量。

**目标**：让技术向（建模台）与业务向（运营台）用户在列表页多选同类实体，一次原子地批量移除，而不必逐行点击或手写 YAML。

**心智模型（已与用户确认）**：
- **勾选即操作**：用户显式选中列表里的确切行，批量作用一个同构动作。
- **source-agnostic**：批量作用于所选的确切行，无论其 `source` 是 manual/auto/iac——**这是 M4.2 与 M4.1 的语义分界**（M4.1 收敛只碰 `source='iac'` 子集）。
- **全原子 + 幂等即 no-op**：整批一个事务、一次版本 bump、一次广播；任一项真失败整批回滚；「已处于目标态」视为 no-op 跳过不算失败。

**验收**：在 Console 列表页勾选 N 行 → 二次确认「将移除 N 项」→ 确认 → 数据面一次收敛；跨租户批量被 fail-close 拦截；原子回滚（一坏项 → 整批无一落库、版本未 bump）有齿。

## 2. 范围与边界

**做（in scope）**——5 个 app 域「移除族」批量变体（勾选即操作、全原子、source-agnostic）：

| Batch RPC | 单数兄弟 | 语义 |
|---|---|---|
| `BatchUnbindUserRole` | `UnbindUserRole` | 批量解绑用户↔角色 |
| `BatchRevokePermission` | `RevokePermission` | 批量撤销角色↔权限授权 |
| `BatchRemoveRoleInheritance` | `RemoveRoleInheritance` | 批量移除角色继承边 |
| `BatchDeleteRole` | `DeleteRole` | 批量删除角色（**级联**其授权/继承/绑定，见 §4） |
| `BatchDeleteDataPolicy` | `DeleteDataPolicy` | 批量删除数据策略 |

- 三面 parity：gRPC + REST + Console。
- Console 多选 UX：5 个列表页行首复选框 + 批量移除按钮 + 二次确认（复用 M3.4a `requireConfirm`）。

**不做（out of scope，明列）**：
- **关系批量装配 / fan-out**（一个用户↔多角色、一个权限↔多角色的批量**建立**）：本子项目选定「勾选即操作」为主干，装配 fan-out 属另一心智，deferred（YAGNI，无明确需求再做）。故**不含批量 Bind/Grant/AddInheritance/Create**。
- **system 域批量**（`UnbindOperatorRole` / `RevokeAdminGrant` 等 `scopeSystem` 操作）：跨授权域、更高风险；本子项目只管 app 域，保持授权同构（对齐 M4.1 app 域边界）。
- **内联编辑**（列表行原地编辑单实体）：与批量正交，deferred。
- **混合异构批次**（一个请求混不同 op 类型）：每个 batch RPC 严格同构，保证一次 `AuthorizeRule` 覆盖全批。
- **M4.1 式 dry-run diff RPC**：勾选即操作用户已见选中行，改用二次确认即可，不引 dry-run 服务端预览。
- 跨 app / 跨租户批量：每个 batch 目标恒为 path 权威的单一 app。

## 3. API 契约（proto）

```proto
rpc BatchUnbindUserRole       (BatchUnbindUserRoleRequest)       returns (BatchWriteResponse);
rpc BatchRevokePermission     (BatchRevokePermissionRequest)     returns (BatchWriteResponse);
rpc BatchRemoveRoleInheritance(BatchRemoveRoleInheritanceRequest) returns (BatchWriteResponse);
rpc BatchDeleteRole           (BatchDeleteRoleRequest)           returns (BatchWriteResponse);
rpc BatchDeleteDataPolicy     (BatchDeleteDataPolicyRequest)     returns (BatchWriteResponse);

message UserRoleRef    { string user_id = 1; int64 role_id = 2; }
message GrantRef       { int64 role_id = 1; int64 permission_id = 2; }
message InheritanceRef { int64 child_role_id = 1; int64 parent_role_id = 2; }

message BatchUnbindUserRoleRequest        { uint64 app_id = 1; repeated UserRoleRef    items = 2; }
message BatchRevokePermissionRequest      { uint64 app_id = 1; repeated GrantRef       items = 2; }
message BatchRemoveRoleInheritanceRequest { uint64 app_id = 1; repeated InheritanceRef items = 2; }
message BatchDeleteRoleRequest            { uint64 app_id = 1; repeated int64 role_ids = 2; }
message BatchDeleteDataPolicyRequest      { uint64 app_id = 1; repeated int64 data_policy_ids = 2; }

message BatchWriteResponse {
  uint64 version   = 1;  // bump 后新版本（全 no-op 时为当前值）
  uint32 requested = 2;  // 请求项数
  uint32 applied   = 3;  // 实际改变项数（RowsAffected 累加；requested−applied = no-op 跳过数）
  bool   changed   = 4;  // applied>0 → 已 bump + 广播
}
```

- `app_id` 恒 **path/域权威**（REST/Console 从路径取；gRPC 从 request，经 `AuthorizeRule` scopeApp 校验）。
- **空 `items` → `InvalidArgument`**（空批次是客户端错误，不做无意义写）。
- **每批上限 1000 项 → 超限 `InvalidArgument`**（防超大 tx）。
- `applied`/`requested` 双计数让前端显示「移除 N 项（M 项已不存在，跳过）」。
- `BatchWriteResponse` 为新增专用 message（不复用单数 `WriteResponse`，因需 `requested`/`applied` 双计数）。

## 4. 原子与 fail-close 语义（BC 不变量）

每个 batch = **一个 `PolicyManager.runVersionedWrite`**（复用既有引擎，与 M4.1 `ImportAppPolicy`、M1.4 `CreateBusinessRole` 同范式）：`mutate` 闭包循环逐项 `store.DeleteXxx(… WHERE app_id=? AND …)`，累加 `RowsAffected` 为 `applied` → 一次 reproject + `projection.Diff` → **一次 version bump + 一次 outbox 广播**。

- **BC-1 全原子**：任一项 `store` 报错（DB 错等）→ 返回 error → 整批 `tx.Rollback`（`runVersionedWrite` 的 `defer tx.Rollback()`）；无部分提交、无部分 bump。
- **BC-2 幂等即 no-op**：`RowsAffected=0`（本 app 内无匹配行 = 已达目标态）→ 跳过、不计 `applied`、不算失败；**不泄露该 id 是否存在于他 app**（`WHERE app_id=?` 天然 app 局部，源无关删除本就该在本 app 域内幂等）。全批 no-op（`applied=0` 且无 casbin diff）→ 走 `runVersionedWrite` 既有 no-op 分支：提交业务态、**不 bump、不广播**、`changed=false`、`version=` 当前值。
- **BC-3 无冲突类（回源核实）**：5 个移除全是纯 `DELETE`，**无 FK 拒绝 / 无 `FailedPrecondition`**。特别地 `store.DeleteRole` **级联删除**（显式 `DELETE role_permission → role_inheritance → user_role_binding → role`），故 `BatchDeleteRole` 一并移除所选角色的授权/继承/绑定——与单数 `DeleteRole` 一致，靠 Console 二次确认「将一并移除其授权与绑定」告知。错误码只有：DB 错→`Internal`、跨租户→`PermissionDenied`、空/超限→`InvalidArgument`。
- **BC-4 租户隔离 fail-close**：`scopeApp` + app_id path 权威 + `TenantDomainOf` fail-close；跨租户/未知 app → `PermissionDenied` 拦在 handler 前，不泄露存在性（与 M4.1 一致）。body 内即便带 app_id 也被 path 覆写（REST/Console）。
- **BC-5 status 闸**：`isWrite=true` → `CheckStatusWrite`；停用 app 拒绝批量写。
- **BC-6 source-blind**：批量移除作用于所选确切行，无论 `source` manual/auto/iac（与 M4.1 只碰 iac 的分界）。有齿：一批混删 iac + manual 行都删。
- **BC-7 授权核心零触碰**：`casbin/enforcer.go` / `internal/controlplane/adminauthz/` / `internal/sidecar/` / `internal/kernel/` git diff = **0 行**；`internal/controlplane/mgmt/authz.go` 仅 **+5** ruleTable 行（全复用既有 `resource`/`action`，无新鉴权原语）；M1.1 tenant matcher 一字未改。
- **BC-8 dedup**：批次内重复项天然 no-op（第二次 `RowsAffected=0`），不显式去重、靠幂等吸收。

**审计（M2.3）**：每个 batch 落**一条** app 域审计（`action='batch_delete_role'` 等，`entity_id` = 该批 id 集合的紧凑表示），与 `runVersionedWrite` 既有审计钩子一致；不逐项 N 条（批是一个原子写单元）。

## 5. 三面 parity

**gRPC**：`internal/controlplane/mgmt/server.go` +5 薄 handler（各调 `s.mgr.BatchXxx`，错误 → `Internal`，空/超限 → `InvalidArgument`）。`ruleTable` **+5，全复用单数兄弟规则**：

| Batch RPC | ruleTable 规则 |
|---|---|
| `BatchUnbindUserRole` | `{"binding","delete",true,scopeApp}` |
| `BatchRevokePermission` | `{"grant","delete",true,scopeApp}` |
| `BatchRemoveRoleInheritance` | `{"inheritance","delete",true,scopeApp}` |
| `BatchDeleteRole` | `{"role","delete",true,scopeApp}` |
| `BatchDeleteDataPolicy` | `{"data_policy","delete",true,scopeApp}` |

**REST**：`internal/controlplane/restgw/routes.go` +5 路由 `POST /v1/apps/{app_id}/<resource>/batch-delete`（`bindings`/`grants`/`role-inheritances`/`roles`/`data-policies` 对齐既有资源段命名），app_id **path 权威**，body = `{items:[…]}` JSON（`role_ids`/`data_policy_ids` 为裸数组），REST-HMAC 绑全请求，复用 `AuthorizeRule` + `CheckStatusWrite`。app 域路由计数相应 +5。

**Console**：`internal/controlplane/console/` 5 个列表页（user bindings / grants / role inheritances / roles / data policies）行首加 `<input type=checkbox name=ids value=…>`，整表包进 `<form method=post action=".../batch-delete">` + 底部「批量移除选中」按钮。5 个批量 handler 走 `doWrite` 全闸 + **`requireConfirm` 二次确认**（服务端确认页显「将移除 N 项」，roles 页额外「将一并移除其授权与绑定」）→ confirmed=1 → `BatchXxx` → PRG + flash「已移除 N 项」。

## 6. 产品体验 · 无新 JS 基线

- **纯 HTML 基线**：行复选框在 `<form>` 内，勾选 → 提交 → 服务端收 `ids`。无 JS 也完全可用（对齐 M3.4b「零构建无新 JS」、M3.4a「有无 JS 都可用」）。
- **不加新 JS**：全选联动 / 实时计数 / 无选禁用留 YAGNI（基线不需要）。
- **二次确认**：复用 M3.4a `requireConfirm` 服务端确认页（回显选中 `ids` 隐藏件、`html/template` 转义、`Action=r.URL.Path` 服务端权威、CSRF 用 `sess.CSRF`）。
- **a11y**：复选框 `<label>` 关联 / 单 h1 + breadcrumb / 复用 M3.1 设计系统组件类，真浏览器 axe 走查 0 违规。

## 7. 测试策略（TDD）

- **BC-1 原子回滚有齿**（testcontainers）：一批注入一个会 DB 报错的项 → 整批无一落库 + version 未 bump（反向验证：去掉原子性则测试 FAIL）。
- **BC-2 no-op 幂等有齿**：全不存在项 → `changed=false`、version 不 bump、`applied=0`；部分存在 → `applied` = 存在数。
- **BC-4 跨租户矩阵**：跨 app id / 未知 app → `PermissionDenied`；path 权威覆写 body app_id 有齿。
- **BC-6 source-blind 有齿**：一批混删 `source='iac'` + `source='manual'` 行，皆删。
- **级联有齿**：`BatchDeleteRole` 后所删角色的 grants/inheritances/bindings 皆无。
- **边界**：空 `items` / 超 1000 项 → `InvalidArgument`。
- **三面 parity**：gRPC + REST（HMAC）+ Console（会话/CSRF/二次确认/PRG）各有测试；Console 确认门有齿（缺 confirmed → 渲确认页不落库）。

## 8. 任务分解（子代理驱动 + 两阶段审查）

1. **proto**：5 RPC + 3 Ref message + 5 Request + `BatchWriteResponse`；`make proto-check` 零漂移。
2. **policy Manager 5 batch 方法**（纯逻辑，各一 `runVersionedWrite`，mutate 循环累加 applied；复用既有 `store.DeleteXxx`）+ 单测（原子/no-op/source-blind/级联/边界）。
3. **mgmt 5 handler + ruleTable +5 + 错误码映射 + 跨租户矩阵**。
4. **REST 5 路由**（path 权威，body items）。
5. **Console 5 列表页多选 + 5 批量 handler + requireConfirm 二次确认 + flash**（无新 JS）。
6. **整体核验 BC-1..8 + 真浏览器 axe 走查 + opus 整体安全评审 + FF**（`git -C <main> merge --ff-only`，核实 main==feature tip，push origin 与否问用户）。

## 9. 跨里程碑一致性约束（carry-forward）

- **一份授权真相**：5 batch RPC 三面全经导出的 `AuthorizeRule` / `CheckStatusWrite` / 唯一 `ruleTable`；无第二套判定。
- **fail-close 全贯穿**：跨租户、原子回滚、no-op 均宁拒绝/无副作用，绝不放行/部分提交。
- **DB 真相源**：批量写以 DB 为准，一次 version bump 驱动缓存/数据面全量收敛。
- **回源核实**：BC-3「级联非冲突」已读 `store.DeleteRole` 源码核实（非臆测）；实现前对任何 casbin/存储行为论断续读源码核实。
- **范式延续**：子代理驱动 + 两阶段审查（规格→质量）+ opus 整体安全评审；跨包改签名后 `go vet ./...` 全仓兜底。

相关：[[feedback-consistency-over-simplicity]]、[[feedback-verify-casbin-before-asserting]]；上游 M4.1 `docs/superpowers/specs/2026-06-28-sydom-m4-1-policy-as-code-design.md`。
