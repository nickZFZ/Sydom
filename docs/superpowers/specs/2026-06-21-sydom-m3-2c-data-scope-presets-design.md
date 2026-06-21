# M3.2c-1 数据范围（符号化）预设 — 设计

> **里程碑上下文**：M3（业务向运营台成体系）拆 4 子项目——M3.1 设计系统 + a11y 基座（✅）/ **M3.2 业务语言抽象层 + 预设·模板** / M3.3 关系可视化 + 决策模拟器 / M3.4 体验打磨 + onboarding。
>
> M3.2 拆 3 子切片：M3.2a+b 官方预设包（权限点 + 业务角色，✅ 已交付，FF `12b5c8f`；REST parity 补齐 `6ec204f`）。**M3.2c 拆为两个正交切片**（brainstorm 收敛）：
> - **M3.2c-1：数据范围（符号化）预设**（本 spec）——扩展既有模板模型，让模板角色携带符号化数据范围、apply 时种入 `data_policy`。
> - M3.2c-2：租户自有模板（存/克隆自有 bundle）——后续独立 spec→plan。
> - onboarding 向导：归 M3.4（roadmap 既定，不在 M3.2c）。

## 1. 目的

让司域官方预设包除「权限点 + 业务角色」外，还能携带**符号化数据范围**（如「仅本人创建」「本部门」），应用模板时连同种入 `data_policy`。租户应用后在建模台按自己的数据 schema 编辑适配。建机制（端到端）+ 官方包加 1-2 个通用示意数据范围。**纯控制面写 + 数据面同步**，复用既有 `data_policy` / dataperm 符号谓词基础设施。

## 2. 关键决策（brainstorm 收敛）

| 决策点 | 选择 | 理由 |
|---|---|---|
| M3.2c 拆分顺序 | **数据范围预设先于租户自有模板** | 数据范围是对刚建成的官方包系统的小幅扩展、低风险，且为租户模板打基础（租户模板将复用同一 data_scopes 模型） |
| 官方包是否含数据范围 | **机制 + 官方包加 1-2 通用示意** | app 无关包用 app 特定字段只能示意；既建机制又有可见默认，租户编辑适配 |
| 再 apply 幂等口径 | **数据范围只在角色「新建」时种入**（角色已存在→跳过，不碰数据范围） | 镜像既有「角色新建才授权」口径，天然幂等、无需给 `data_policy` 加唯一约束/迁移，契合「模板=种子，不动人工后续编辑」 |
| 预览口径 | **符号谓词，绝不枚举** | 复用 M1.3 `FilterSymbolic` 口径：渲染「`owner_id = $user.id`」非真实行；系统不存用户属性/不持客户数据 |
| condition 处理 | **原样透传，绝不预解析/校验** | 与既有 `UpsertDataPolicy` 一致，合法性 fail-close 留给 sidecar（条件树解析在数据面） |
| 数据范围绑定主体 | **subject_type=role、subject_id=确定性 role code** `tpl:<id>:<key>` | 数据范围随角色生效（绑该角色的用户均受约束），与 dataperm 的 role 匹配（`roleSet[subjectID]`）对齐 |

## 3. 数据模型

预设包 `Role` 用既有预留字段 `data_scopes`（M3.2a+b loader 已忽略未知字段，向前兼容）：

```json
{
  "key": "editor", "name": "编辑", "permission_codes": ["content.write"],
  "data_scopes": [
    {"resource": "content", "effect": "allow",
     "condition": {"field": "owner_id", "op": "EQ", "value": "$user.id"}}
  ]
}
```

- `DataScope{Resource string, Effect string, Condition json.RawMessage}`——condition 是既有条件树（leaf `{"field","op","value"}` / logical `{"op":"AND/OR/NOT","children":[]}`），`$user.xxx` 符号保留，**字段名/值原样**。
- `Effect` 空串按 `allow`（对齐 DB 默认与 `cp.EffectAllow`）。
- **官方包 2 个通用示意**：
  - `general-admin` 的 `editor` 角色 + content「仅本人创建」：`{"field":"owner_id","op":"EQ","value":"$user.id"}`。
  - `ecommerce-ops` 的 `customer-service` 角色 + order「本部门」：`{"field":"department","op":"EQ","value":"$user.department"}`。
  - 字段名（owner_id/department）是**示意性**的；租户应用后在建模台编辑适配自己 schema。
- **loader 校验扩展**：data_scopes 的 `resource` 非空；condition 非空且为合法 JSON（仅校验可解析为 JSON，不校验条件树语义——透传口径）；`effect` ∈ {空, allow, deny}。违例 panic（fail-close，与既有 loader 一致）。

## 4. 应用引擎扩展（`policy.ApplyTemplate`）

在**同一** `runVersionedWrite` 事务内，对每个**新建的**模板角色（`created==true`）：
1. 既有：按 `permission_codes` 授权（不变）。
2. **新增**：按 `data_scopes` 逐条调 `store.UpsertDataPolicy(ctx, tx, appID, cp.DataPolicy{SubjectType:"role", SubjectID:code, Resource:ds.Resource, Condition:ds.Condition, Effect:ds.Effect}, version)`（id=0 即插入）。
3. 角色**已存在**（`created==false`，skipped）→ 既不重授权、**也不碰数据范围**（不动人工后续编辑）。

**关键差异 vs M3.2a+b**：官方包此前是纯元数据（权限点/角色不入 sync）；**数据范围预设真实产生 `data_policy` 行 → `DataPolicyChange` → bump 版本 → 广播到 sidecar**（影响数据面）。`mutate` 闭包须返回 `[]cp.DataPolicyChange`（既有 `runVersionedWrite` 已支持该返回，翻译并下发）。

`ApplyTemplateResult` 加 `DataScopesCreated int`；任一步 error 整事务回滚（TP-5，原子覆盖角色+授权+数据范围）。

## 5. 后端契约（proto）

- `TemplateRole` 加 `repeated TemplateDataScope data_scopes = 5;`；新 message `TemplateDataScope{string resource=1; string effect=2; string condition=3;}`（condition 为 JSON 串）。
- `ApplyTemplateResponse` 加 `uint32 data_scopes_created = 5;`。
- `ruleTable` **不变**（ApplyTemplate 仍 template/apply scopeApp isWrite=true；ListTemplates template/read）。三面（gRPC/REST/Console）共用既有 `AuthorizeRule`+`CheckStatusWrite`。
- mgmt `toProtoTemplate` 映射 data_scopes；ApplyTemplate handler 透传 condition（不预解析）。

## 6. UI（运营台，复用 M3.1 设计系统，无新 JS）

- **模板库预览**（`ops_templates.html`）：每角色除能力列表外，加「数据范围」节，渲染为**符号谓词**——复用 dataperm `FilterSymbolic` 风格把 condition 渲染成人类可读谓词（如「`owner_id = $user.id`」），`$user.xxx` 保留符号。绝不枚举真实行、不解析属性（DSC 符号口径）。控制面侧需一个轻量 condition→谓词渲染（可抽 dataperm 渲染逻辑为共享纯函数，或控制面内联一个最小渲染器——实现期定，二者均不触碰数据面求值）。
- **apply 摘要**（`ops_template_applied.html`）：加「数据范围：新建 {{.DataScopesCreated}}」。
- 建模台数据策略页（既有）已可编辑——租户适配字段的入口已在，无需新建。

## 7. 不变量（DSC-1..DSC-7，贯穿全程）

- **DSC-1 一份授权真相**：ruleTable/AuthorizeRule 不变；三面共用。
- **DSC-2 符号口径忠实**：预览=符号谓词（`$user.xxx` 保留），绝不枚举真实行/不解析用户属性（系统不持客户数据）。
- **DSC-3 condition 原样透传**：控制面绝不预解析/校验条件树语义（仅校验可解析 JSON），合法性 fail-close 留 sidecar。
- **DSC-4 幂等**：数据范围只在角色新建时种入，再 apply 不重复、不覆盖人工编辑（DataScopesCreated=0）。
- **DSC-5 原子**：data_policy 与角色/授权同 `runVersionedWrite`，任一步失败整笔回滚。
- **DSC-6 数据面同步保真**：apply 产生 `DataPolicyChange` → bump → 广播；sidecar 收到与控制面一致的 data_policy。
- **DSC-7 租户隔离 + secret 不泄露 + M1.1 matcher 一字未改**：scopeApp + TenantDomainOf fail-close（跨租户 403）；响应无 secret；`adminauthz` diff=0。

## 8. 测试策略

- **presets**：loader 解析 data_scopes（含 2 官方示意）；校验拒空 resource / 非法 JSON condition / 非法 effect（fail-close 有齿）。
- **policy.ApplyTemplate**：新建角色种入 data_policy（查 DB 验 subject_type=role/subject_id=role code/condition 透传）；re-apply 幂等（DataScopesCreated=0、无重复 data_policy 行）；返回 DataPolicyChange 非空（数据面同步）；原子回滚（数据范围步失败整笔回滚）。
- **mgmt + 三面**：ListTemplates 返回 data_scopes；ApplyTemplate 计数含 data_scopes_created；跨租户 403。
- **Console/REST**：模板库预览含符号谓词（NotContains 真实值枚举）；apply 摘要含数据范围计数。
- **整体**：TP-1..8 + DSC-1..7 逐条；gofmt/vet/proto-check 干净、go test ./... 0 FAIL；opus 整体评审。

## 9. YAGNI / 范围边界（明确延后）

- **租户自有模板**（存/克隆自有 bundle）→ **M3.2c-2**（独立 spec）。
- **onboarding 向导** → **M3.4**（schema 仍留 `onboarding` 位）。
- **data_policy 去重键/再同步**：不做——seed-on-new-role 口径已给幂等，无需迁移。
- **condition 可视化构建器**：建模台既有，模板预览只读渲染符号谓词，不在模板库做编辑。
- **数据范围绑 user 主体**：模板只绑 role；user 级数据策略走既有建模台手工配置。

## 10. 自检记录

- **占位符扫描**：无 TODO/待定；condition 渲染器「抽共享 vs 控制面内联最小渲染」是实现期的明确二选一（均不触数据面），非未完成需求。
- **内部一致性**：data_scopes 模型（§3）↔ proto（§5）↔ apply（§4）↔ 预览（§6）字段一致；幂等口径（§2 决策 / §4 step 3 / §7 DSC-4）三处一致。
- **范围检查**：聚焦单一计划可覆盖（presets data_scopes + loader 校验 + apply 扩展 + proto 3 改 + 三面 + 运营台预览）；租户模板已切出。
- **模糊性检查**：「再 apply 不种数据范围」「condition 透传不预解析」「预览符号不枚举」均已明确单写。
