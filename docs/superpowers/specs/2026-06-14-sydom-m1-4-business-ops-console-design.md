# 司域 (Sydom) M1.4 — 薄运营台：业务语言旅程 设计

> 类型：单功能 spec（M1 多租户基座第 4 子项目）。
> 日期：2026-06-14
> 前置：M1.1 租户隔离基座 `437b28d` / M1.2 自助账户 `48ecae8` / M1.3 有效权限视图 `588ae54` 均已并入 main。
> 路线图定位：`2026-06-13-sydom-production-readiness-roadmap.md` §4 M1 «一条业务语言薄运营台旅程»；§2C «业务语言抽象层» 的最薄首切片（完整抽象层 / 模板 / 关系图 / 决策模拟器归 M3）。

## 1. 背景与目标

现有 Console 是**技术向建模台**：导航＝角色 / 权限点 / 授权 / 继承 / 用户 / 数据策略 / 有效权限，模型是 casbin 原语（先建角色→建权限点→把权限授给角色→把用户绑到角色，分步）。非技术业务人员不这样思考——他们想的是「让 Alice 当销售经理，看她能做什么」。

**目标（精准命中 M1 验收）**：让**非技术业务管理员无辅助完成「把 Alice 设为销售经理并看她能做什么」**。交付一个**独立的业务向运营台**（双层 UX 的薄种子），业务语言、隐藏技术原语，底层**复用全部既有 RPC + `AuthorizeRule`（一份授权真相、零旁路）**。

**本轮 brainstorm 锁定的范围决策**：
| 维度 | 决策 |
|---|---|
| 旅程边界 | 业务管理员可：**分配/移除既有业务角色** + **看「能做什么」** + **新建业务角色（命名 + 勾选能力）**。权限点 / 数据策略 / 继承的**定义仍归技术向建模台**。 |
| 数据策略呈现 | 业务视图用**一句业务简记**（如「仅限本人区域的订单」）呈现行级范围，**绝不**露原始谓词。 |
| 形态 | **独立「运营台」业务区**（新 URL 前缀 `/ops/`），与技术建模台两套界面、两种语言，同一 operator 登录 / 会话 / 鉴权核心 / 数据。 |

## 2. 范围

**纳入**：
- 独立运营台业务区（`/ops/apps/{app_id}/...`，Console BFF 内）：**人员**页 + **业务角色**页。
- 业务语言映射层（纯展示翻译）：role.name / permission.name / data_policy.description。
- 新 RPC `CreateBusinessRole`（原子复合：建角色 + 批量授权），三面 parity。
- 接通既有 `data_policy.description` 列全链（**无迁移**）+ 建模台数据策略表单加「业务说明」输入。

**不纳入（M1.4↔M3 边界）**：
- 权限点定义（resource/action 建模）、数据策略条件构建、角色继承——仍归技术建模台。
- 审批 / 申请工作流、模板 / 预设、关系图 / 决策模拟器、设计系统 / 多语 / a11y——M3。
- 人员目录：司域不持客户用户目录，「人」即 app 域内 subject 字符串（user_id）。
- 编辑既有角色能力的原子化复合 RPC——M1.4 编辑走既有逐条 `GrantPermission`/`RevokePermission`（各自已原子+版本化；仅「建角色」需原子复合以避免无能力的空角色）。

## 3. 方案选型与决策

**形态（主架构决策）**：

| | 方案 | 与路线图 | 裁决 |
|---|---|---|---|
| **A** | 独立运营台业务区（`/ops/` 前缀 + 业务导航；技术页保留为建模台；复用同一 enforcer/AdminServer/会话） | 高——正是「两套界面、两种语言」薄种子 | ✅ **采用** |
| B | 业务页混入现有 Console 导航 | 低——两语混杂违背双层 UX | ✗ |
| C | 完整 运营台/建模台 模式切换骨架 | 过重 | ✗ M3 |

**「建业务角色」原子性**：新增 `CreateBusinessRole` RPC，PolicyManager 在**单个 `runVersionedWrite` 事务**内建角色 + 批量授权（原子、单次版本 bump、一次广播）。否决 BFF 串多次 `CreateRole`+N×`GrantPermission`（非原子 → 可能「角色建了授权没跟上」的部分态，违背一致性红线）。

**数据策略业务简记**：`data_policy.description VARCHAR(512)` 列**已存在**（migration 000008），但 `cp.DataPolicy`/store/proto/mgmt 全链未接通。M1.4 **接通既有列**（无迁移），建模台填、运营台显示。

## 4. 运营台架构

```
/ops/apps/{app_id}/people                 人员列表 + 录入
/ops/apps/{app_id}/people/view?user_id=   某人：业务角色(增删) + 能做什么(能力+数据简记)
/ops/apps/{app_id}/roles                   业务角色列表
/ops/apps/{app_id}/roles/new               新建业务角色(名称 + 能力勾选)  [GET 表单 / POST 建]
/ops/apps/{app_id}/roles/view?role_id=     某角色能力(勾选编辑=逐条 Grant/Revoke)
```

- Console `handler.go` 注册 `h.registerOps(mux)`（与既有 `registerRBAC` 等并列）。
- **复用既有 BFF 基建**：`requireSession` / `doWrite`（CSRF→授权→status 闸→直调→PRG）/ `renderPage` / `renderGRPCError` / `mgmt.AuthorizeRule` / `h.srv`（同一 `*AdminServer` 实例）/ `h.enf`。运营台**无自有授权逻辑**。
- 运营台独立 layout/nav（业务语言、不含技术原语链接）；模板新建于 `templates/ops_*.html`。
- 底层调用全是既有 + 新 `CreateBusinessRole` RPC：`ListUserBindings`（人员/某人角色）、`ListRoles`（角色名）、`ListPermissions`（能力名+勾选源）、`BindUserRole`/`UnbindUserRole`（分配）、`GrantPermission`/`RevokePermission`（编辑角色能力）、`GetEffectivePermissions`（能做什么）、`ListDataPolicies`（数据简记，读 description）、`CreateBusinessRole`（建角色）。

## 5. 业务语言映射层（纯展示翻译，不碰决策）

| 技术原语 | 业务呈现 | 来源 |
|---|---|---|
| role.code / role_id | 角色名「销售经理」 | `role.name`（无 name 回退 code） |
| 有效权限 `(resource, action)` | 能力「查看订单 / 导出订单」 | join `ListPermissions` 的 `name`（按 resource+action 匹配；无 name 回退 `resource:action`） |
| data_policy 谓词 | 业务简记「仅限本人区域的订单」 | `data_policy.description`（无描述回退「受限范围（详见建模台）」，**绝不露谓词**） |

- 映射在 Console handler 层组装（取 ListPermissions 建 `(resource,action)→name` map，对 `GetEffectivePermissions` 结果翻译）。`(resource,action)` 视为权限点业务键。
- 运营台**只读不写谓词**：数据策略的定义 / 谓词 / description 的**编写**全在建模台。

## 6. 新 RPC `CreateBusinessRole`（原子复合）

`api/proto/sydom/admin/v1/admin.proto`：
```proto
rpc CreateBusinessRole(CreateBusinessRoleRequest) returns (CreateBusinessRoleResponse);

message CreateBusinessRoleRequest {
  uint64 app_id = 1;
  string name = 2;                   // 业务名称（销售经理）；业务管理员不填/不见 code
  repeated int64 permission_ids = 3; // 勾选的能力（权限点 id）
}
message CreateBusinessRoleResponse {
  int64 role_id = 1;
  uint64 version = 2;
  bool changed = 3;
}
```
- `PolicyManager.CreateBusinessRole(appID, name, permIDs)`：单个 `runVersionedWrite` 事务内——`store.InsertRole(appID, code, name)` 得 roleID，再对每个 permID `store.InsertRolePermission(appID, roleID, permID, "allow")`（复用 GrantPermission 的 store 写）。原子、一次 bump、一次广播。
- **code 系统生成**：`code = slug(name) + "-" + 短随机`（业务管理员永不见/不填）；唯一性靠 `uq_role_app_code`，冲突极罕见→事务回滚重试一次或返回 `Internal`（不暴露 code 概念）。
- 空 `name` → `InvalidArgument`；`permission_ids` 可空（建无能力的空角色合法，业务管理员后续编辑添加）。
- ruleTable：`CreateBusinessRole: {"role", "create", true, scopeApp}`（与 CreateRole 同 resource/域/写拦截）。
- 三面 parity：gRPC handler + REST 路由 + 运营台「新建业务角色」表单调用。

## 7. 接通 `data_policy.description` 全链（无迁移）

既有列 `description VARCHAR(512)` 当前 NULL。接通点：
1. `cp.DataPolicy` +`Description string`（`internal/controlplane/types.go`）。
2. `store`：`UpsertDataPolicy` INSERT/UPDATE 写 description；`ReadAppDataPolicies` 返回 `cp.DataPolicy.Description`（该函数同时供 Sidecar 同步快照与 M1.3 effperm，二者均不消费 description，无影响）；mgmt `ListDataPolicies` 读 description。
3. proto：`UpsertDataPolicyRequest` +`string description = 8`；`DataPolicySummary` +`string description = 8`（regen）。
4. mgmt `UpsertDataPolicy` 透传 description；`ListDataPolicies` 回带。
5. **建模台**数据策略表单（`datapolicies.html` + handler）加「业务说明」可选输入。
6. **运营台**人员「能做什么」按 resource 聚合数据策略，显示 description。

> 注（定案）：description 是**纯元数据**，不进 casbin 投影、不影响 enforce/数据求值。**明确不加入 `sync.v1.DataPolicy`**——`translate.DataPoliciesToProto` 不映射 description，Sidecar 完全不感知（避免无谓快照 churn）。description 仅活在控制面读路径（`admin.v1` ListDataPolicies + 运营台）。OP-4 守门：Sidecar/effperm 求值行为零变。

## 8. 鉴权 / 一份真相 / 租户隔离（M1.1 matcher 一字不改）

- 运营台所有读写经导出的 `mgmt.AuthorizeRule` + 唯一 `ruleTable`；`/ops/` 路由复用同一 `h.enf`/`h.srv`。跨租户 403、app 存在性 fail-close 由既有 `scopeApp`+`TenantDomainOf` 自然兜住。
- 受限 operator 的运营台同样降级无枚举（越权 app → AuthorizeRule 先拦、不渲染资源）。
- `CreateBusinessRole`=scopeApp 写、受 status 写拦截（停用 app 不可建角色，与 CreateRole 一致）。

## 9. 三面 parity vs Console-only

- **新 RPC `CreateBusinessRole`**：gRPC + REST 路由 + Console（运营台调用）三面 parity（与 M1.2/M1.3 纪律一致）。
- **`data_policy.description` 接通**：proto/store/mgmt（gRPC）+ REST（既有 UpsertDataPolicy/ListDataPolicies 路由自动带新字段）+ 建模台表单。
- **运营台页面**：Console-only（业务 UI，无对应「RPC」概念）；其底层每个动作都打到既有/新 RPC。

## 10. 错误处理 / fail-close

- `CreateBusinessRole` 事务任一步失败 → 整体回滚、返回错误（绝无部分态：要么角色+全部授权都在，要么都不在）。
- 业务语言映射缺失（permission 无 name / data_policy 无 description）→ 回退展示（`resource:action` / 「受限范围」），**非错误**，绝不露原语或谓词。
- 运营台读失败 / 越权 → `renderGRPCError` 降级，不枚举。
- 错误细节回传沿用既有模式（Internal+%v，统一脱敏待可观测性，与 M1.1/M1.2/M1.3 同非阻塞 TODO）。

## 11. 不变量（验收逐条核验，file:line 证据）

- **OP-1 一份授权真相**：运营台经 `AuthorizeRule`/`ruleTable`，无自有授权逻辑；M1.1 matcher 未改（`adminauthz/` diff 0）。
- **OP-2 建角色原子**：`CreateBusinessRole` 单事务，部分失败全回滚，无「空角色 / 半授权」态。
- **OP-3 业务语言不漏原语**：运营台**绝不**渲染 role_id/code/ptype/eft/原始谓词；缺映射时回退业务话术。
- **OP-4 数据求值零影响**：`data_policy.description` 纯元数据，不进投影、不改 enforce/数据过滤；M1.3 effperm + Sidecar 行为不变。
- **OP-5 租户隔离零旁路**：运营台跨租户 403、降级无枚举，与三面同源。
- **OP-6 secret 不泄露**：运营台不触 secret_enc。
- **OP-7 CSRF**：运营台所有写动作走 `doWrite`（CSRF 强制）。

## 12. 测试策略

- `CreateBusinessRole`：建角色+授权原子（dbtest 断言 role + N grants 同事务落库、单次 version bump）；空 name→InvalidArgument；空 perm 列表→合法空角色；冲突 code 处理；跨租户 403 矩阵；停用 app 写拦截。
- 业务语言映射：`(resource,action)→name` 翻译、缺 name 回退；data_policy description 显示、缺描述回退「受限范围」、**断言谓词绝不出现在运营台 body**。
- `data_policy.description` 全链：Upsert 写入→ListDataPolicies 读回；建模台表单往返；effperm/Sidecar 求值不受影响（既有测试全绿守门）。
- 运营台页面（Console）：人员旅程 200 渲染、分配/移除、建业务角色闭环、降级无枚举、CSRF、secret 不泄露。
- 全链回归：`go vet ./...` + `go test ./...` 全绿；`make proto-check` 无漂移。

## 13. 子项目任务分解（交 writing-plans 细化）

1. proto：`CreateBusinessRole` RPC+消息 + `UpsertDataPolicyRequest`/`DataPolicySummary` 加 description；regen。
2. `data_policy.description` 全链接通（cp.DataPolicy/store 读写/mgmt 透传）+ 单测。
3. `PolicyManager.CreateBusinessRole` 原子复合（runVersionedWrite 建角色+批量授权）+ dbtest。
4. mgmt `CreateBusinessRole` handler + ruleTable + 跨租户矩阵。
5. REST 路由 `CreateBusinessRole`（+ description 经既有路由自动带）+ 测。
6. 建模台数据策略表单加「业务说明」输入 + 测。
7. 运营台业务区：handler（registerOps + 人员/业务角色页 + 业务语言映射）+ 模板 + 导航 + 测（含降级无枚举/谓词不外露）。
8. 整体验证 + opus 安全评审（OP-1..OP-7 逐条 file:line）。

## 14. 假设与未决

- **假设**：`(resource, action)` 是权限点业务键（join ListPermissions 取 name）；若同 (resource,action) 多权限点 name 冲突，取其一并在实现注明（M1.4 不解决多义，留 M3 建模台规范）。
- **假设**：业务角色 name 不要求 DB 唯一（沿用现 schema，仅 code 唯一）；运营台建角色前展示既有角色列表，重名由管理员目视避免（软约束，不加迁移）。
- **未决（非阻塞）**：运营台是否需独立顶部「运营台/建模台」切换入口——M1.4 仅以 URL 前缀 + 独立 nav 区分，显式切换 UI 留 M3。
