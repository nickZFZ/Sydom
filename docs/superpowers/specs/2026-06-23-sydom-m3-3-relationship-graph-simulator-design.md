# M3.3 角色全景 + 决策模拟器 — 设计

> **里程碑上下文**：M3（业务向运营台成体系）拆 4 子项目——M3.1 设计系统 + a11y 基座（✅）/ M3.2 业务语言抽象层 + 预设·模板（✅ a+b/c-1/c-2 全交付）/ **M3.3 关系可视化 + 决策模拟器** / M3.4 体验打磨横扫 + onboarding。本 spec 只覆盖 M3.3。

## 1. 目标

让运营者在运营台「**看见一个角色的全貌**」并「**模拟一次尚未提交的授权改动**」：

- **角色全景**：选一个业务角色，一屏看清它的绑定用户、能力（含继承来源）、继承的父角色、数据范围（符号谓词）。
- **决策模拟器（反事实预览）**：在角色全景页发起「假如……会怎样」——假如把某用户绑到本角色 / 假如给本角色加某能力——展示受影响用户**有效权限的 diff**（RBAC + 数据范围符号），**不落库**。

两者均为纯控制面 + 表现层，复用既有求值栈与授权真相，不碰数据面 / Sidecar / M1.1 matcher。

## 2. 范围

**在范围内：**
- 2 个新只读 RPC：`GetRoleGraph`、`SimulateRoleChange`，三面 parity（gRPC + REST + Console）。
- effperm 包新增导出函数 `Simulate`（反事实求值），复用既有 `buildEngine`。
- Console 角色全景页（**分区面板**形态）+ 角色全景内的「假如……」模拟入口 + diff 摘要页。
- ruleTable +2 条。

**不在范围内（YAGNI）：** 全应用关系图 / SVG 节点图 / 邻接矩阵表现；批量决策矩阵；「假如给本角色加一条数据范围」hypothetical；children/被继承链；保存 / 导出 / 历史模拟；i18n、深色切换控件、移动端。

## 3. 关键既有依赖（复用，不重写）

- `effperm.buildEngine(ctx, tx, appID)` → 从 `store.ReadAppRules` + `store.ReadAppDataPolicies` 物化瞬态 `kernel.Engine` + `dataperm.Table`（与 Sidecar 快照同源）。`effperm.Compute` 算「某 user 有效权限」（`Result{Roles, Permissions []Perm{Resource,Action}, DataViews []DataView{Resource,Match,Predicate}}`）。
- `mgmt.AuthorizeRule` + 唯一 ruleTable（`rpcRule{resource, action, isWrite, scope}`，键=gRPC FullMethod）；三面共用。
- `console.conditionPredicate` 符号谓词渲染器；`console.capabilityName`（bizterm 业务名兜底）。
- 既有 proto 可复用消息：`EffectivePermission{resource, action}`、`DataPolicyPreview{resource, match, predicate}`。
- M3.1 设计系统（零构建分层 CSS、a11y 基线、`workspace/appnav/card/list-plain/hint/badge/empty-state` 等类）。

## 4. RPC 设计

服务：`sydom.admin.v1.AdminService`。两个 RPC 均 `scopeApp`、`isWrite=false`、读不过 status 闸。

### 4.1 GetRoleGraph（结构聚合，不求值）

```proto
message GetRoleGraphRequest { uint64 app_id = 1; int64 role_id = 2; }

message RoleGraphCapability {
  string resource = 1;
  string action   = 2;
  string name     = 3;   // bizterm 业务名（缺名合成，绝不裸 resource:action）
  string source   = 4;   // "direct" 或来源父角色显示名
}
message RoleGraphParent { int64 id = 1; string code = 2; string name = 3; }

message GetRoleGraphResponse {
  int64  role_id      = 1;
  string role_code    = 2;
  string role_name    = 3;
  repeated string bound_users          = 4;   // user_role_binding 按 role 过滤
  repeated RoleGraphCapability capabilities = 5; // 直接 + 继承（source 标注）
  repeated RoleGraphParent parents     = 6;   // role_inheritance（向上）
  repeated DataPolicyPreview data_scopes = 7; // 本角色直接的 data_policy，符号谓词
}
```

handler 聚合既有 store 读：`role`、`role_permission`(+permission join，含经父角色继承的授权)、`user_role_binding`(role 过滤)、`role_inheritance`(parents)、role 主体 `data_policy`。能力的「继承来源」由角色继承闭包解析：本角色直接授权标 `direct`，否则标贡献该能力的父角色名。`data_scopes` 仅本角色**直接**持有的 data_policy（继承来的数据范围经 `parents` 链可达、v1 不在此展开），复用 `conditionPredicate` 口径渲为 `DataPolicyPreview`。

### 4.2 SimulateRoleChange（反事实求值）

```proto
enum RoleChangeType { ROLE_CHANGE_UNSPECIFIED = 0; BIND_USER = 1; ADD_CAPABILITY = 2; }

message SimulateRoleChangeRequest {
  uint64 app_id   = 1;
  int64  role_id  = 2;
  RoleChangeType change_type = 3;
  string user_id  = 4;   // BIND_USER 用
  string resource = 5;   // ADD_CAPABILITY 用
  string action   = 6;   // ADD_CAPABILITY 用
}

message SubjectDiff {
  string user_id = 1;
  repeated EffectivePermission added_permissions   = 2;
  repeated EffectivePermission removed_permissions = 3;
  repeated DataPolicyPreview   added_data_previews   = 4;  // 符号
  repeated DataPolicyPreview   removed_data_previews = 5;  // 符号
}
message SimulateRoleChangeResponse { repeated SubjectDiff subjects = 1; }
```

**算法（effperm.Simulate，复用 buildEngine 同一求值栈）：**
1. `buildEngine` 读真实 rules + dps，得 baseline 引擎。
2. 确定受影响 subject 集：
   - `BIND_USER` → `{user_id}` 一项。
   - `ADD_CAPABILITY` → 所有 `user_role_binding` 中的去重用户里、隐式角色闭包（`GetImplicitRolesForUser`）含本角色 code 者。
3. 构造**一条合成规则**注入 rules 的副本（不改 DB、不改 baseline 切片）：
   - `BIND_USER` → g 行 `(user_id, roleCode, domain)`。
   - `ADD_CAPABILITY` → p 行 `(roleCode, resource, action, "allow")`。
4. 用修改后的 rules（+原 dps）重建瞬态引擎，得 hypothetical 引擎。
5. 对每个 subject：baseline `Compute` vs hypothetical `Compute`，**双向 diff**：
   - `added/removed_permissions` = 有效 RBAC 允许集对称差（忠实 deny 反转，不假设单调）。
   - `added/removed_data_previews` = DataViews（符号）对称差（match/predicate 维度）。
6. 只回 diff 非空的 subject。**全程不持久化、不 bump、不广播；任一步失败 fail-close 返 error。**

> deny 反转说明：系统支持 deny effect，加一条绑定/能力可能令用户落入某角色级 deny 而失去原本经他途获得的 allow——故必须算 removed，不能假设新增是单调的。

## 5. 表现层

### 5.1 Console 角色全景页（分区面板）

路由 `GET /ops/apps/{app_id}/roles/{role_id}/graph`。形态=**分区面板**（头部 + 四分区：绑定用户 / 能力 / 继承 / 数据范围），复用 M3.1 设计系统、bizterm 业务名、`conditionPredicate` 符号谓词、**无新 JS**。从既有 `/ops/apps/{app_id}/roles` 角色列表每行可达。

模拟入口：相关分区旁「假如……」表单——
- 「+ 绑定用户」：填 user_id → `GET /ops/apps/{app_id}/roles/{role_id}/simulate?change_type=bind_user&user_id=…`
- 「+ 加能力」：选 resource/action（来自本 app 权限点）→ `…?change_type=add_capability&resource=…&action=…`

→ 渲染 **diff 摘要页**（受影响用户、新增/失去的能力、新增/失去的数据范围符号谓词）。模拟为**读语义**（无副作用），走 GET、不过 status 闸、无 CSRF——与既有 `decision.html` 一致。鉴权 scope=read。

### 5.2 REST

- `GET /v1/apps/{app_id}/roles/{role_id}/graph` → GetRoleGraph
- `GET /v1/apps/{app_id}/roles/{role_id}/simulation?change_type=…&user_id=…|resource=…&action=…` → SimulateRoleChange

path 权威覆写 app_id + role_id。复用既有 restgw `route` 范式、`AuthorizeRule`、`pathUint64`。

### 5.3 ruleTable +2

```
"/sydom.admin.v1.AdminService/GetRoleGraph":       {"role", "read", false, scopeApp}
"/sydom.admin.v1.AdminService/SimulateRoleChange": {"effective_permission", "read", false, scopeApp}
```

`GetRoleGraph` 归 `role/read`（结构读，同 ListRoles 域）；`SimulateRoleChange` 归 `effective_permission/read`（求值类，同 GetEffectivePermissions / ExplainDecision）。

## 6. 不变量 RG-1..8

- **RG-1 一份授权真相**：三面共用 `AuthorizeRule` + 唯一 ruleTable（+2）；`git diff -- adminauthz/ casbin/enforcer.go` = 0，M1.1 matcher 一字未改。
- **RG-2 模拟零副作用**：`SimulateRoleChange` 绝不写库/不 bump/不广播；测试断言模拟前后 DB `app_version` 与 policy 行数不变。
- **RG-3 一份决策真相**：模拟复用 `effperm.buildEngine` 同求值栈，diff = 同栈跑两次（baseline vs hypothetical），无第二套决策逻辑。
- **RG-4 deny 覆盖忠实**：双向 diff（added + removed），不假设单调。
- **RG-5 符号口径**：数据范围 diff 全符号（`$user.xxx` 保留），绝不枚举真实行/值。
- **RG-6 租户隔离**：`scopeApp` + `TenantDomainOf` fail-close；跨租户 / 未知 app → 不泄露存在性。
- **RG-7 Sidecar 零漂移 + secret 不泄露**：`git diff -- internal/sidecar/` = 0；响应/页面无凭据。
- **RG-8 fail-close**：未知 role/user → 空 / NotFound；求值任一步失败 → error 不静默。

## 7. 测试策略（TDD）

- `effperm.Simulate` 单测（testcontainers PG）：BIND_USER diff 正确、ADD_CAPABILITY 影响用户集正确、**deny 反转有齿**（构造一条 role 级 deny 使新增绑定致 removed 非空，删反向 diff 逻辑即 FAIL）、**零副作用断言**（模拟后 app_version / policy 行数不变）、数据范围符号 diff。
- mgmt：`GetRoleGraph`（聚合正确、继承来源标注、符号数据范围、跨租户 fail-close）、`SimulateRoleChange`（两 change_type、未知 role/user fail-close）。
- REST：两路由 path 权威 + 鉴权放行 + 反序列化。
- Console：角色全景页（四分区渲染、bizterm 业务名、符号谓词、无新 JS）+ 模拟 diff 页（含 `$user.`、NotContains 真实枚举值/secret）。

## 8. 任务拆分（预估，详见后续 plan）

1. proto 2 RPC + message + regen。
2. `effperm.Simulate`（反事实求值核心 + 单测）。
3. mgmt `GetRoleGraph` handler + ruleTable +1。
4. mgmt `SimulateRoleChange` handler + ruleTable +1。
5. REST 2 路由。
6. Console 角色全景页 + 模拟 diff 页（无新 JS）。
7. 整体验证 RG-1..8 + opus 评审 + FF 合并。

## 9. YAGNI 明示排除

全应用关系图、SVG/矩阵表现、批量决策矩阵、「加数据范围」hypothetical、children/被继承链、保存/导出/历史模拟、模拟结果持久化、i18n/深色切换控件/移动端。
