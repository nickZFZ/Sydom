# M3.2c-2 租户自有模板 — 设计

> **里程碑上下文**：M3（业务向运营台成体系）拆 4 子项目——M3.1 设计系统 + a11y 基座（✅）/ **M3.2 业务语言抽象层 + 预设·模板** / M3.3 关系可视化 + 决策模拟器 / M3.4 体验打磨 + onboarding。
>
> M3.2 拆 3 子切片：M3.2a+b 官方预设包（✅ `12b5c8f`，REST parity `6ec204f`）/ M3.2c-1 数据范围（符号化）预设（✅ FF `ad63068`）/ **M3.2c-2 租户自有模板（本 spec）**。
> - onboarding 向导：归 M3.4（roadmap 既定，不在 M3.2c）。

## 1. 目的

让租户把一个**已配好的 app** 的「整套授权模型」（权限点 + 业务角色 + 角色→权限授权 + 角色数据范围）一键**存为租户私有、可复用的模板**，再 apply/克隆到本租户其他 app，加速多 app 标准化配置。

与官方预设包的区别：官方包是 `//go:embed` 内嵌 JSON、随二进制版本化、**全局只读不可改**；租户模板存于 DB、**租户私有、可删**。两者共用同一套 `data_scopes` 模型与同一 `policy.ApplyTemplate` apply 引擎。

**纯控制面写 + 数据面同步**：捕获是控制面读；apply 复用既有 `ApplyTemplate`，照常产生 `Delta` 下发数据面（与 M3.2c-1 同源），不新增数据面逻辑。

## 2. 关键决策（brainstorm 收敛）

| 决策点 | 选择 | 理由 |
|---|---|---|
| 模板来源 | **从现有 app 捕获快照** | 复用全部既有读取设施（store 读 / List* 口径），最贴合「存/克隆自有 bundle」本意，价值最高（配一次、跨 app 复用） |
| 捕获范围 | **全部授权模型**：权限点(auto+manual) + 全业务角色 + 角色→权限授权 + 角色 `data_scopes`；**排除** `user_role_binding` 与 user 主体 `data_policy` | 模板是「结构」不是「谁被分配」；排除 per-user 分配与凭据 |
| 存储 | **单 JSONB blob**（镜像 `presets.Template` 的 `{permissions, roles}` 形状）+ 新表 `tenant_template` | 整体 apply、不查询 bundle 内部、与 `ApplyTemplate` 消费形状零阻抗、单迁移无子表 |
| apply 引擎 | **复用同一 `policy.ApplyTemplate`**，绝不写第二套 | 与官方包 apply 口径完全一致（auto 不覆盖 manual / 确定性 code 幂等 / data_scope 仅新建角色种入 / 原子 / 广播）——一致性优先 |
| 角色 code 命名空间 | apply 租户模板传 `templateID = "tt-<dbid>"` → 确定性 code `tpl:tt-<dbid>:<key>` | 数字 id 短、无 `:`、不与官方 preset id（如 `general-admin`）撞码；复用 `ApplyTemplate` 既有 `tpl:<id>:<key>` 机制无需改引擎 |
| 捕获角色的 bundle key | 从角色 `code` 派生**安全 key**（去 `:`、租户内/bundle 内唯一），保留原 `name`/`description` | app 内既有角色 code 可能含 `:`（如官方模板派生的 `tpl:...`），而 `ApplyTemplate` 拒 key 含 `:`；派生安全 key 使 apply 期形成合法确定性 code |
| 隔离 | `tenant_template.tenant_id` 闸；save/list/get/delete/apply 全 tenant-scoped；跨租户→PermissionDenied/NotFound | 镜像 M1.x 租户隔离；复用 `AuthorizeRule` + 唯一 `ruleTable`；**M1.1 matcher 一字不碰** |
| 重名 save | **同租户名唯一，重名→`AlreadyExists`** | 重存用 delete+save；覆盖语义（update）YAGNI |
| 预览口径 | **符号谓词，绝不枚举**（复用 M3.2c-1 `conditionPredicate`） | 系统不存用户属性/不持客户数据；忠实上限是渲染谓词非真实行 |

## 3. 数据模型 — 新表 `tenant_template`（migration `000018`）

```sql
CREATE TABLE tenant_template (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id     BIGINT       NOT NULL,
    name          VARCHAR(128) NOT NULL,
    description   VARCHAR(512),
    bundle        JSONB        NOT NULL,   -- 见下方 bundle 形状
    source_app_id BIGINT,                  -- 溯源：捕获自哪个 app（可空，仅审计/展示）
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT fk_tenant_template_tenant FOREIGN KEY (tenant_id) REFERENCES tenant(id),
    CONSTRAINT uq_tenant_template_name   UNIQUE (tenant_id, name)
);
CREATE INDEX idx_tenant_template_tenant ON tenant_template (tenant_id);
```

`bundle` JSON 镜像官方 preset 形状（转换层零阻抗）：

```json
{
  "permissions": [
    {"code": "order.read", "resource": "order", "action": "read", "type": "act", "name": "查看订单", "description": ""}
  ],
  "roles": [
    {
      "key": "customer-service", "name": "客服", "description": "",
      "permission_codes": ["order.read"],
      "data_scopes": [
        {"resource": "order", "effect": "allow", "condition": {"field": "department", "op": "EQ", "value": "$user.department"}}
      ]
    }
  ]
}
```

> 注：`source_app_id` 不设 FK 到 application（源 app 可能被删，模板应仍可用），仅作展示溯源。

## 4. 捕获逻辑（`SaveAppAsTemplate`）

handler 在源 app（须属调用者租户）上读取并序列化为 bundle：

1. **permissions**：读 app 全部权限点（auto + manual），逐条映射 `code/resource/action/type/name/description`。
2. **roles**：读 app 全部业务角色，逐角色：
   - 生成**安全 bundle key**：从角色 `code` 派生（去 `:`、必要时附序号保证 bundle 内唯一），保留原 `name`/`description`。
   - `permission_codes`：该角色现 `role_permission` 授权对应的权限 `code` 列表。
   - `data_scopes`：该角色 `data_policy`（`subject_type='role'`、`subject_id=角色 code`）的 `{resource, effect, condition}`，condition 原样透传（JSON）。
3. **排除**：`user_role_binding`（per-user 分配）、user 主体 `data_policy`、`app_secret*`/任何凭据。
4. 单事务写 `tenant_template`（name 唯一冲突→`AlreadyExists`）。捕获是只读 app + 写 tenant_template，**不 bump app 版本、不下发 Delta**（不改源 app）。

## 5. apply 逻辑（`ApplyTenantTemplate`）

mgmt handler：
1. 按 `template_id` 取 `tenant_template`（tenant-scoped；不存在/跨租户→fail-close）。
2. 校验模板 `tenant_id == 目标 app 的 tenant_id`（跨租户 apply→PermissionDenied/NotFound，不泄露存在性）。
3. 解析 bundle → `perms []cp.PermissionPoint` + `roles []policy.TemplateRole`（含 `DataScopes`）。
4. 调**同一** `policy.ApplyTemplate(targetAppID, "tt-<id>", perms, roles)`，返回计数（perms_upserted/skipped、roles_created/skipped、data_scopes_created）。

**复用即一致**：auto 不覆盖 manual、确定性 code `tpl:tt-<id>:<key>` 幂等（再 apply 角色已存在→跳过、不重种 data_scope）、原子回滚、产生 Delta bump+广播——全部来自既有引擎，无第二套 apply 逻辑。

## 6. proto + 三面（gRPC + REST + Console）

### proto（AdminService 新增 5 RPC + message）
- `SaveAppAsTemplate(SaveAppAsTemplateRequest{app_id, name, description}) → (TenantTemplateRef{id, name})`
- `ListTenantTemplates(ListTenantTemplatesRequest{tenant_id, ListPage}) → (…{templates[], total})`（复用 M2.4 `ListPage` 分页/搜索/排序）
- `GetTenantTemplate(GetTenantTemplateRequest{template_id}) → (TenantTemplate{id, name, description, source_app_id, permissions[], roles[ with data_scopes ]})`
- `ApplyTenantTemplate(ApplyTenantTemplateRequest{app_id, template_id}) → (ApplyTemplateResponse 复用)`
- `DeleteTenantTemplate(DeleteTenantTemplateRequest{template_id}) → (google.protobuf.Empty 或既有空响应范式)`

bundle 内 role/permission/data_scope 复用 M3.2a+b/M3.2c-1 既有 proto message（`TemplatePermission`/`TemplateRole`/`TemplateDataScope`）。

### ruleTable scope（鉴权口径）
| RPC | scope | resource/action | 说明 |
|---|---|---|---|
| SaveAppAsTemplate | scopeApp（源 app） | `template`/`create`（**isWrite=false**） | 须对源 app 有权；快照只读 app、不改其状态，故不受 status 写闸（**停用 app 亦可快照**） |
| ListTenantTemplates | scopeTenant | `template`/`read` | 仅见本租户模板 |
| GetTenantTemplate | scopeTenant | `template`/`read` | per-entity 经 tenant_id 闸 |
| ApplyTenantTemplate | scopeApp（目标 app） | `template`/`apply`（isWrite=true） | + 模板同租户校验、受 status 闸 |
| DeleteTenantTemplate | scopeTenant | `template`/`delete` | tenant_id 闸 |

> 既有通配 grant 是否自动覆盖新 `template` 资源的 tenant 域 action：实现期核实（参照 M2.3「新资源 audit 被既有通配 grant 自动覆盖」），不足则补 seeder。

### REST parity
- `POST /v1/apps/{app_id}/template-captures`（SaveAppAsTemplate，name/description 走 body 或 query，app_id path 权威）
- `GET /v1/tenants/{tenant_id}/templates`（List，ListPage query）
- `GET /v1/tenant-templates/{template_id}`（Get）
- `POST /v1/apps/{app_id}/tenant-templates/{template_id}/apply`（Apply，path 权威，镜像官方 ApplyTemplate 路由范式）
- `DELETE /v1/tenant-templates/{template_id}`（Delete）

路由形态与计数实现期定稿，**path 权威覆写**沿既有范式。

### Console
- 模板库页加「我的模板（租户自有）」区：列表（分页）+ 预览（perms/roles + 数据范围**符号谓词**，复用 `conditionPredicate`）+ 「应用到本应用」+ 删除。
- app/建模台页加「存为模板」入口（填名+说明→SaveAppAsTemplate）。
- 复用 M3.1 设计系统、**无新 JS**；写动作走 `doWrite`（会话→CSRF→AuthorizeRule→[status 闸]→调用）；apply 非 PRG（幂等，镜像官方模板页）。

## 7. 关键不变量（TT-1..8，贯穿全程）

- **TT-1 一份授权真相**：复用 `AuthorizeRule` + 唯一 `ruleTable`，无第二套判定；`adminauthz/` 与 `mgmt/authz.go` 既有行不改、**M1.1 matcher 一字未改**（仅 ruleTable 追加 template 资源的 tenant/app 域条目）。
- **TT-2 租户隔离**：`tenant_template.tenant_id` 闸；list/get/delete `WHERE tenant_id` 与 scope 锁步；apply 校验模板 tenant==目标 app tenant；save 只能快照本租户 app；跨租户一律 fail-close（PermissionDenied/NotFound，不泄露存在性）。
- **TT-3 apply 一致**：复用 `policy.ApplyTemplate`，无第二套 apply 逻辑（auto 不覆盖 manual / 幂等确定性 code / data_scope 仅新建角色种入 / 原子 / 广播）。
- **TT-4 捕获保真**：快照含 perms(auto+manual) + 全业务角色 + 授权 + 角色 data_scopes；**排除** user_role_binding、user 主体 data_policy、凭据。
- **TT-5 符号口径**：预览数据范围复用 `conditionPredicate` 符号谓词，绝不枚举真实行。
- **TT-6 secret 不泄露**：bundle 与所有响应绝不含 `app_secret*`/任何凭据。
- **TT-7 sidecar 零漂移**：纯控制面；apply 经既有 `ApplyTemplate` 产 Delta 下发（与 M3.2c-1 同源），sidecar/dataperm 代码 diff=0。
- **TT-8 fail-close**：未知 template→NotFound（不泄露存在性）；bundle 解析失败/非法→拒绝；捕获/apply 任一步失败整事务回滚。

## 8. 测试策略

- **migration**：`tenant_template` 建表 + 唯一约束（同租户重名拒）。
- **store**：写 tenant_template / 读（tenant-scoped、分页）/ 删；捕获读取 app 全模型（perms+roles+grants+data_scopes）。
- **capture**：`SaveAppAsTemplate` 对一个配好的 app（含 data_scope）→ bundle 含全模型、排除 user 绑定/凭据；安全 key 去 `:` 唯一；重名→AlreadyExists。
- **apply**：`ApplyTenantTemplate` 应用到空 app → perms/roles/data_scopes 落地、产生 Delta；re-apply 幂等（角色跳过、不重种）；跨租户→fail-close。
- **mgmt + 三面**：List 分页/Get 预览含符号谓词（NotContains 真实枚举）/ 跨租户 403 / secret 不泄露。
- **整体**：TT-1..8 逐条核验（adminauthz/matcher/sidecar diff=0、secret 链路、无新 JS）；gofmt/vet/proto-check 干净、`go test ./...` 0 FAIL；opus 整体评审。

## 9. YAGNI / 范围边界（明确延后/不做）

- **模板版本化 / diff / 历史**：不做——存即当前快照，重存用 delete+save。
- **apply 前 dry-run / 差异预览**：不做——预览=只读渲染 bundle，apply 幂等可重试。
- **覆盖语义（UpdateTenantTemplate）**：不做——重名拒、用 delete+save。
- **克隆官方预设到租户可改副本**：不做（来源已定为「从 app 捕获」）。
- **从零模板构建器 UI**：不做。
- **跨租户共享 / 模板市场**：不做。
- **捕获选择性角色（勾选）**：不做——本切片捕获全模型；选择性留后续若有需要。
- **onboarding 向导**：M3.4。

## 10. 自检记录

- **占位符扫描**：无 TODO/待定需求。§4「安全 bundle key 从 code 派生（去 `:`、唯一）」与 §6 REST「路由形态与计数实现期定稿」「seeder 通配 grant 覆盖实现期核实」是实现期的明确执行细节/核实点（非未完成需求），对齐既有 spec 范式（如 M3.2c-1 condition 渲染器「抽共享 vs 内联」实现期二选一、M2.3 新资源 grant 覆盖核实）。
- **内部一致性**：bundle 形状（§3）↔ 捕获（§4）↔ apply（§5）↔ proto（§6）字段一致（permissions[] + roles[ key/name/description/permission_codes/data_scopes ]）；角色 code 命名空间 `tt-<id>`（§2）↔ apply 传参 `policy.ApplyTemplate(appID, "tt-<id>", …)`（§5）一致；幂等口径（§2 决策 / §5 / §7 TT-3）三处一致；租户隔离（§2 / §7 TT-2 / §8）一致；apply 引擎复用（§2 / §5 / §7 TT-3）一致。
- **范围检查**：聚焦单一计划可覆盖——1 迁移（tenant_template）+ store（写/读分页/删 + 捕获读全模型）+ 捕获 handler + apply 复用既有引擎 + 5 RPC + 三面 + Console「我的模板」区；规模与 M3.2a+b（8 任务）相当，无需进一步拆分（onboarding 已切至 M3.4）。
- **模糊性检查**：「捕获=全授权模型排除 user 绑定/凭据」「apply 复用同一 ApplyTemplate 无第二套」「重名 save 拒绝（AlreadyExists）非覆盖」「SaveAppAsTemplate isWrite=false 停用 app 亦可快照」「跨租户 fail-close 不泄露存在性」均已明确单写。
