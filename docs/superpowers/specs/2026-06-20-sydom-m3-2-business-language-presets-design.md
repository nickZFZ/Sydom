# M3.2a+b 业务概念翻译层 + 模板核心 + 司域官方预设包 — 设计

> **里程碑上下文**：M3（业务向运营台成体系）拆 4 子项目——M3.1 设计系统 + a11y 基座（✅ 已交付）/ **M3.2 业务语言抽象层 + 预设·模板** / M3.3 关系可视化 + 决策模拟器 / M3.4 体验打磨横扫 + onboarding。
>
> M3.2 全貌经 brainstorm 收敛为：**预设/模板既要司域官方预设包、也要租户自有模板**，模板内容端态含**权限点 + 业务角色 + 数据范围预设 + onboarding**。因单一计划无法覆盖，M3.2 拆为 3 子切片，各走自己 spec→plan→impl：
> - **M3.2a+b（本 spec）**：业务概念翻译层 + 模板核心 + 司域官方预设包。
> - M3.2c：租户自有模板 + 数据范围（符号化）预设。
> - onboarding 向导：归 M3.4（roadmap 既定）。

## 1. 目的

让**非技术租户管理员**能一键从司域官方预设包 bootstrap 一个空 app（权限点 + 业务角色），并在运营台始终看到一致的中文业务语言——消除「必须先有技术人在建模台定义权限点，业务名/角色才有意义」的 bootstrap 阻塞。直接服务 M3 验收：真实非技术用户可用性达标（任务完成率 / SUS）。

## 2. 关键决策（brainstorm 收敛）

| 决策点 | 选择 | 理由 |
|---|---|---|
| 模板形态（端态） | **官方预设包 + 租户自有模板**（本 spec 只做官方预设包） | 既解决「从零起步」（官方包）又解决「多 app 复用」（租户模板，留 M3.2c） |
| 模板内容（端态） | 权限点 + 业务角色 + 数据范围 + onboarding（本 spec 只做权限点 + 业务角色） | 数据范围依赖 `$user.属性` 符号化、归 M3.2c；onboarding 归 M3.4 |
| 首片拆分 | **翻译层 + 官方预设包合为一片** | 翻译层是模板渲染地基，合并避免跨片返工 |
| 翻译层存储 | **系统动词词表（代码）+ 业务名存现有 `permission.name`（预设自带），无新表** | 最精简、无 migration；原语 action/resource 是任意字符串，词表给动作统一中文、名靠预设 |
| 预设包存储 | **代码内嵌 `//go:embed presets/*.json`，无新表** | 司域全局内容、随产品版本化、租户不可改；无 migration/无管理面 |
| 预览 | **靠 `ListTemplates` 返回内容在页面渲染**（不单独 dry-run RPC） | 精简到 2 RPC；命中 manual 的跳过明细在应用后摘要展示 |

## 3. 业务概念翻译层

把现 `internal/controlplane/console/routes_ops.go` 中散落的 `permNameMap`/`roleNameMap`/`label`/`roleName` 抽成一个**集中、可测**的翻译单元（`internal/controlplane/console/bizterm.go`，仍 `package console`；纯函数 + 词表，无 I/O、无新表）。

- **系统动作动词词表**（代码常量 `map[string]string`）：`read|list|get→查看`、`create|add→新建`、`update|write|edit→编辑`、`delete|remove→删除`、`export→导出`、`import→导入`、`approve→审批`、`reject→驳回`、`assign→分配`……未在表中的 action 原样返回（不臆造）。
- **能力名解析顺序**（`CapabilityName(name, resource, action) string`）：① `permission.name` 非空 → 用之（显式业务名，预设自带，最优）；② 否则合成「`{resource} · {verb(action)}`」（verb 来自词表，未知 action 用原 action）；③ 不再出现裸 `resource:action` 拼接。
- **角色名解析**（`RoleName(map, code) string`）：保留现语义（map 命中→业务名，否则回退 code，绝不回退 role_id）。
- 统一应用于运营台所有面（现人员能力页 + 新模板库/预览/摘要页）。`routes_ops.go` 改为调用 `bizterm`，行为等价（回归测试守门）。

**边界**：翻译层不 decompose `permission.name`（不从「查看订单」反解出 resource 显示名），不建 per-app 术语表（已否决）。resource 在合成路径下可能仍显示原始串——但预设包均自带 `name`，故此路径仅影响「手工建的无名权限点」，属可接受退化。

## 4. 预设包模型

新目录 `internal/controlplane/presets/`：`//go:embed *.json` + loader（`package presets`，纯内存、随二进制）。

**Schema（JSON）**：
```json
{
  "id": "general-admin",
  "name": "通用后台管理",
  "description": "适合大多数后台：查看/编辑/删除 + 管理员/编辑/只读三角色",
  "version": 1,
  "permissions": [
    {"code":"order.read","resource":"order","action":"read","type":"act","name":"查看订单","description":""}
  ],
  "roles": [
    {"key":"admin","name":"管理员","description":"全部能力","permission_codes":["order.read","order.write"]}
  ]
}
```
- `data_scopes` / `onboarding` 字段**预留**（本片不解析；M3.2c/M3.4 填）——loader 忽略未知字段，向前兼容。
- v1 内置 **2 个预设包**：`general-admin`（通用后台管理）、`ecommerce-ops`（电商运营）。中文业务名齐全。
- loader 启动期校验：包 `id` 唯一、`permission.code` 包内唯一、`role.permission_codes` 引用的 code 均存在；任一违例 → 启动 fail-close（`log.Fatal` / 构造期 error），绝不带损坏内容运行。

## 5. 应用引擎

**2 个新 `AdminService` RPC**（proto `admin.proto` + 生成代码）：

- `ListTemplates(ListTemplatesRequest{app_id}) → ListTemplatesResponse{templates:[Template]}`：返回内置包元数据 + 完整内容（供运营台预览渲染）。内容本身是全局产品资料（与具体 app 无关），但**以 app 为鉴权上下文**（模板库页在 `/ops/apps/{app_id}/templates`）：`scopeApp` 读——对该 app 有读权的租户管理员即可列出（见 §7）。
- `ApplyTemplate(ApplyTemplateRequest{app_id, template_id}) → ApplyTemplateResponse{permissions_created, permissions_skipped, roles_created, roles_skipped}`：原子幂等应用到目标 app。

**应用语义**（`policy.PolicyManager.ApplyTemplate`，单 `runVersionedWrite` 事务）：
1. 逐 permission：复用 `store.UpsertAutoPermission`（`source='auto'`，命中 `source='manual'` 行跳过保留、计 skipped）→ 建立 `code→permission_id` 映射。
2. 逐 role：用**确定性 code** `tpl:<template_id>:<role_key>`（稳定、可幂等 upsert），按 `(app_id, code)` 唯一：不存在→建角色（计 created）、已存在→跳过（计 skipped，不改人工后续编辑）；建角色时按 `permission_codes` 经步骤 1 的映射解析到 `permission_id` 批量授权。
3. 任一步失败 → 整事务回滚（沿用 `runVersionedWrite`）。投影变化照常 bump 版本 + 广播 Delta（新增权限点/授权使有效策略变化）。

**幂等保证**：re-apply 同一包 → 权限点经 auto upsert 不重复、角色经确定性 code 不重复；计数反映「本次实际新建 vs 已存在跳过」。**绝不覆盖人工**：manual 权限点、管理员手工改过的同名角色（同 code 已存在即跳过）均保留。

## 6. UI（运营台，复用 M3.1 设计系统，无新 JS）

运营台（`/ops/` 前缀）加「模板库」入口与页：
- `GET /ops/apps/{app_id}/templates`：卡片列出官方预设包（`name` + `description` + 「含 N 权限点 / M 角色」），每卡片可展开**预览**（经翻译层渲染：权限点业务名清单 + 角色及其能力清单）。预览数据来自 `ListTemplates`。
- `POST /ops/apps/{app_id}/templates/apply`（`doWrite` + CSRF + `ApplyTemplate`）→ PRG → 应用摘要页（created/skipped 计数 + 跳过说明 + 「去人员页分配角色」闭环链接）。
- 纯服务端渲染，沿用 M3.1 组件 class（card/btn/badge/table/empty-state），无新增 JS。

## 7. 后端契约与不变量

**触及**：proto（`admin.proto` +2 RPC + 4 message，buf lint 既有 except 范式）；`mgmt`（2 handler `ListTemplates`/`ApplyTemplate` + `ruleTable` +2 条规则 + `AdminServer` 接 presets loader）；`policy`（`ApplyTemplate` 引擎，复用 `runVersionedWrite`/`UpsertAutoPermission`/角色 DAO）；新 `internal/controlplane/presets` 包（embed + loader + 校验）；`console`（运营台模板库/预览/摘要页 + `bizterm.go` + `routes_ops.go` 改调 bizterm）。**不碰** `internal/sidecar` 与数据面（纯控制面 + 表现层）。

**ruleTable 规则**：
- `ListTemplates`：`scopeApp`、`resource="template"`、`action="read"`、`isWrite=false`——以 app 为鉴权上下文（模板库页 per-app），对该 app 有读权即可列出；`TenantDomainOf` fail-close 拦未知/跨租户 app。响应内容是全局产品资料、无任何租户/app 数据。
- `ApplyTemplate`：`scopeApp`、`resource="template"`、`action="apply"`、`isWrite=true`——租户管理员在自己 app 域有此权即可应用；`TenantDomainOf` fail-close 拦未知/跨租户 app。

**不变量（TP-1..TP-8）**：
- **TP-1 一份授权真相**：三面（gRPC/REST/Console）共用 `AuthorizeRule` + 唯一 `ruleTable`，Console 无自有授权逻辑。
- **TP-2 fail-close**：鉴权/查询失败一律降级、不泄露存在性（跨租户 app → PermissionDenied 不 NotFound）。
- **TP-3 auto 不覆盖 manual**：apply 复用 `UpsertAutoPermission` 既有语义 + 角色按 code 已存即跳过。
- **TP-4 幂等**：re-apply 同包计数稳定、无重复实体。
- **TP-5 原子**：单 `runVersionedWrite` 事务，任一步失败全回滚。
- **TP-6 租户隔离**：`ApplyTemplate` 经 `TenantDomainOf`，跨租户 403；安全矩阵覆盖。
- **TP-7 secret 不泄露**：apply 全程不读/不写 `app_secret_enc`；响应无 secret 字段。
- **TP-8 运营台无原语**：模板库/预览/摘要经 `bizterm` 渲染业务名，缺名退化为「resource · 动词」，不裸 `resource:action`、不漏 code/id。

## 8. 测试策略

- `bizterm` 单测：动词词表命中/未知 action 原样、`CapabilityName` 三级解析、`RoleName` 回退；现 `routes_ops` 行为等价回归。
- `presets` loader 单测：embed 解析、schema 校验（id 唯一 / code 唯一 / role.permission_codes 引用存在）、损坏包构造期 fail-close。
- `policy.ApplyTemplate` 单测（testcontainers PG）：幂等（应用两次计数稳定、无重复）、auto 不覆盖 manual（预置 manual 同 code → 跳过保留）、确定性 role code 不重复、原子回滚（注入失败）、投影 bump。
- `mgmt`：`ApplyTemplate` 跨租户 403、未知 template `NotFound`/`InvalidArgument`、`ListTemplates` 认证后可读。
- `console`：模板库页渲染（业务名 / 无原语 / 卡片）、apply 走 doWrite（CSRF 缺→403、带→PRG）、摘要页计数。
- 全量 `go test ./...` 0 FAIL；`gofmt`/`go vet`/`make proto-check` 干净。

## 9. YAGNI / 范围边界（明确延后）

- 租户自有模板（存/克隆自有 bundle）→ **M3.2c**。
- 数据范围（符号化 `$user.属性`）预设 → **M3.2c**（schema 已留 `data_scopes` 位）。
- onboarding 向导 / in-product 帮助 → **M3.4**（schema 已留 `onboarding` 位）。
- per-app/per-tenant 术语表（翻译层可定制）→ 已否决（系统词表足够）。
- 模板版本升级/迁移、模板市场、第三方/社区模板、模板导入导出 → 不做。
- 单独 dry-run 预览 RPC（带 DB 冲突检查）→ 不做（预览靠 ListTemplates 内容；实际跳过见应用后摘要）。

## 10. 自检记录

- **占位符扫描**：无 TODO/待定；schema 的 `data_scopes`/`onboarding` 预留字段是刻意的向前兼容位（loader 忽略未知字段），非未完成需求。
- **内部一致性**：§4 预设包内容（权限点 + 业务角色）与 §5 应用引擎（逐 permission upsert + 逐 role 确定性 code）、§7 触及面一致；§2 决策表与各节一致；翻译层 §3「无新表/名靠 permission.name」与预设包 §4「自带 name」一致。
- **范围检查**：聚焦单一实现计划可覆盖（翻译层抽取 + presets 包 + 2 RPC + apply 引擎 + 运营台 3 页）；租户模板/数据范围/onboarding 已切出。
- **模糊性检查**：apply 幂等键明确（permission=auto upsert by code、role=确定性 `tpl:<id>:<key>` by (app_id,code)）；`ListTemplates`/`ApplyTemplate` 均 scopeApp（以 app 为鉴权上下文、TenantDomainOf 租户隔离、响应无租户数据泄露）；翻译层退化路径明确（缺名→resource·动词，不裸原语）。
