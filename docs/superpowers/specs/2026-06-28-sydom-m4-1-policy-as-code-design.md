# 司域 M4.1 · 策略即代码（Policy-as-Code）+ 导入导出 — 设计

> 生产就绪路线图第 4 里程碑 **M4（技术向建模台 + 开发者 DX）** 的第 1 个子项目。M4 经范围评估拆为 5 子项目（M4.1 策略即代码+导入导出 / M4.2 批量操作 / M4.3 条件构建器增强 / M4.4 API 文档门户+quickstart / M4.5 开发者 sandbox+密钥管理 UI）；本 spec 仅覆盖 **M4.1**。BASE = 本地/远端 main `85354f0`（M1–M3 + 技术债清理一轮均已落地）。

## 1. 背景与目标

把**一个 app 的授权模型**在「声明式文本文件 ⇄ 系统状态」之间双向同步，让技术向用户能把授权模型纳入版本控制、跨环境复现、以代码方式审查变更：

- **export**：复用已验证的 `tenanttemplate.Capture`，把 app 模型导出为 YAML 或 JSON 文本。
- **import**：文件 = 该 app **IaC 托管子集**的**期望状态**，import **收敛**（建 / 改 / 删）到该状态，带 **dry-run diff 预览 + 显式确认**。

**心智模型（已与用户确认）**：真 IaC（声明式期望状态收敛，含删除），但删除的**治理域限定在「文件托管」子集**——经来源标记区分，手工（`source=manual`）与其他来源实体永不被 import 触碰。

**验收**：export→编辑→import 往返幂等；真实演示「文件改一行 → dry-run 看 diff → 确认 → 数据面收敛」闭环。

## 2. 范围与边界

**做（in scope）**：
- app 域授权模型的 export / import：权限点（permission）、业务角色（role，含已授权限码与角色数据范围 data_scope）、顶层数据策略（data_policy）。
- 真 IaC 收敛：CREATE / UPDATE / DELETE / ADOPT，限 `source='iac'` 子集。
- dry-run diff 预览（默认首响）+ 显式确认后原子 apply。
- 三面 parity：gRPC + REST + Console。
- YAML 与 JSON 双格式（export 可选，import 自动识别）。

**不做（out of scope，明列）**：
- **用户角色绑定（user_role_binding）与凭据**：永不进文件、import 永不读/写（与 `Capture` 一致——绑定是运营态不是「代码」；凭据涉密）。
- **admin / system 域**（管理员角色、运营台账户、租户成员）：本子项目只管 app 域授权模型。
- 批量 UI（M4.2）、条件构建器增强（M4.3）、API 文档（M4.4）、sandbox（M4.5）。
- 跨 app / 跨租户的模型迁移（import 目标恒为 path 权威的单一 app；export 单 app）。

## 3. 文件模型与格式

复用现有 `tenanttemplate.Bundle` 结构作为文件 schema 内核，外加一层文件信封：

```yaml
apiVersion: sydom.policy/v1          # 信封版本，import 校验
app:                                  # app 标识（仅信息性；import 目标恒取 path 权威 app_id，文件 app 块不决定写哪个 app）
  key: orders-prod                    # app_key（信息性，便于人辨认）
permissions:
  - code: order:read                  # 稳定身份键
    resource: order
    action: read
    type: app
    name: 查看订单
    description: ""
    source: iac                       # 来源注记（export 输出；import 用于采纳判定，见 §4）
roles:
  - key: viewer                       # 稳定身份键（= role.code 去 IaC 命名空间后的人读 key）
    name: 查看员
    description: ""
    permission_codes: [order:read]    # 引用 permissions[].code
    data_scopes:
      - resource: order
        effect: allow
        condition: {field: tenant_id, op: EQ, value: "$user.tenant_id"}  # 符号谓词，原样透传
data_policies:                        # 顶层（非角色绑定的）数据策略
  - subject_type: role
    subject_id: viewer
    resource: order
    effect: allow
    condition: {op: ALL}
```

**稳定身份（往返不漂移）**：权限点以 `code` 匹配、角色以人读 `key` 匹配（IaC 角色的 DB `code` 用确定性命名空间 `iac:<key>`，与手工 code 及模板 `tpl:` 隔离不撞）、数据策略以 `(subject_type, subject_id, resource)` 匹配。

**格式**：export `format` 参数选 `yaml`|`json`。import **按内容首个非空白字符自动识别**（`{` 或 `[` → JSON via stdlib `encoding/json`；否则 YAML via `gopkg.in/yaml.v3`——**本子项目唯一新增依赖**）。两条解析路径产出**同一内部期望态模型**，后续算法对格式无感。`condition` 字段在两格式下都解析为等价 JSON 结构后以 `json.RawMessage` 规范化透传（符号谓词 `$user.` 保留）。

## 4. 治理域与来源标记

**迁移（新增）**：给 `role`、`data_policy` 各加 `source VARCHAR(8) NOT NULL DEFAULT 'manual'`（对齐 `permission` 既有 `source`；既有行默认 `manual`，向后兼容）。`source` 值域 `manual` / `auto` / `iac`。

**收敛治理域**：import 的建/改/删**只作用于 `source='iac'` 子集**，外加文件引用到的待采纳实体。`source IN ('manual','auto')` 的实体 import **永不删除/修改**。

**采纳流程（adoption，manual → iac）**：
- 首次 export 捕获 app **全部来源**实体（`Capture` 现状即如此），输出为起始文件，便于「export 现状 → 提交 git → 此后以文件管理」。
- import 时，文件声明、库中存在且当前 `source='manual'` 的实体 → 归类 **ADOPT**（`manual`→`iac`），在 dry-run diff 中**显式标注「将纳入 IaC 托管」**；用户确认 apply 后该实体 `source` 翻为 `iac`，自此纳入收敛治理域（后续从文件移除 → 被删）。
- `source='auto'`（如权限点上报）实体：文件即便声明也**不采纳、不改其 source**（auto 是上报真相，不被 IaC 夺管）；其授权/归属仍可被 iac 角色引用，但其自身生命周期不由文件管理。（此为保守缺省，避免与 D 切片上报机制冲突。）

## 5. export 设计

`ExportAppPolicy(app_id, format)` →
1. `AuthorizeRule`（scopeApp，`policy/export` read）→ 鉴权 + 租户域 fail-close。
2. 复用 `Capture(ctx, db, appID)` 取 permissions + roles（含授权码 + data_scopes）；另查顶层 data_policies。
3. 各实体附 `source` 注记，套文件信封，按 `format` 序列化（YAML/JSON）。
4. 返回文本内容。**纯读、不下发、绝不读凭据列**（`Capture` 既有保证）。

Console 提供「导出下载」（`Content-Disposition` 附文件名 `<app_key>.policy.yaml|json`）。

## 6. import 收敛算法

新 `internal/controlplane/iac` 包封装解析 + diff；写经 `PolicyManager` 的 `runVersionedWrite`（原子 + bump + 广播）。

1. **解析**：自动识别 YAML/JSON → 内部期望态模型。
2. **校验**（fail-close，任一失败整笔拒绝、零写入）：信封 `apiVersion` 已知；权限点 `code` / 角色 `key` 文件内唯一；`role.permission_codes` 全部在文件声明的 permissions 内（否则拒，呼应 `ApplyTemplate` 既有 fail-close）；`condition` JSON 合法；`effect ∈ {allow,deny}`；身份键不含命名空间保留字符（`:`）。
3. **读现状**：app 的 `source='iac'` 子集 + 文件引用到的实体当前状态。
4. **算 diff**（逐实体类型）：
   - **CREATE**：文件有 / 库无 → 新建为 `source='iac'`。
   - **ADOPT**：文件有 / 库为 `manual` → `manual`→`iac`（显式标注）。
   - **UPDATE**：`source='iac'` 且字段 / 授权码集 / data_scopes 与文件有别 → 改至文件态。
   - **DELETE**：`source='iac'` 且文件未声明 → 删除。
   - `source='auto'` 命中：不采纳、不改（§4）。
5. **dry-run 预览**（`dry_run=true`）：返回结构化 diff（分类计数 + 逐条），**只读事务、不 bump、不广播、零副作用**。import 的默认首响。
6. **apply**（`dry_run=false` + 显式确认）：单 `runVersionedWrite` 事务内按 **FK 安全序** 执行——
   - 删除序：先卸角色授权（role_permission）/继承（role_inheritance）→ 删 data_scope/data_policy → 删角色 → 删权限点（无 role 再引用后）。
   - 新建/更新序：先 upsert 权限点 → 解析 code→id → upsert 角色 + 授权 + data_scope → upsert 顶层 data_policy。
   - 任一步失败 → **整事务回滚**（一致性优先，绝不半收敛）；投影变化照常 bump 版本 + 广播 outbox（Sidecar 收敛）。
7. **乐观版本守护**：plan 读到的 app 版本随响应回传；apply 时若 app 版本已漂移（期间有别的写），检出并提示「状态已变，请重看 diff」而非盲目套用旧预览。
8. **删除安全**：删一个仍有 `user_role_binding` 的 iac 角色 → **fail-close 拒绝该删除项**，在 diff 预览标为「冲突：需先解绑用户」（绑定永不被 import 触碰，呼应 §2 边界）；整笔 import 在有未解决冲突时拒绝 apply（不部分应用）。

## 7. 三面 surface（parity，复用唯一 `AuthorizeRule` + `ruleTable`）

- **gRPC**（新 2 RPC + message）：
  - `ExportAppPolicy(app_id, format) → {content}`：scopeApp，`policy/export` read。
  - `ImportAppPolicy(app_id, content, dry_run) → {diff, applied, version}`：scopeApp，`policy/import` write，受 `CheckStatusWrite`（停用 app 拒 import）；`dry_run=true` 仅返回 diff。
- **REST**（path 权威）：
  - `GET /v1/apps/{app_id}/policy/export?format=yaml|json`
  - `POST /v1/apps/{app_id}/policy/import?dry_run=true|false`（body = 文件内容）
- **Console**（建模台加「策略即代码」页）：export 下载按钮 + import textarea → 提交（`dry_run`）→ **服务端渲染 diff 预览页**（分类列出 create/adopt/update/delete/冲突）→ 确认 → apply（PRG）。镜像既有 `requireConfirm` / `doWrite` 安全管线（会话 → CSRF → AuthorizeRule → CheckStatusWrite）；**无新 JS**（textarea + 服务端渲染 diff），复用 M3.1 设计系统。

`ruleTable` 加 2 条：`ExportAppPolicy`=scopeApp `policy/export` read；`ImportAppPolicy`=scopeApp `policy/import` update（isWrite=true）。

## 8. 不变量（PC-1..8，验收逐条核）

- **PC-1 一份授权真相零触碰**：`internal/controlplane/mgmt/authz.go`（ruleTable 仅 +2 条）、`adminauthz/`、`casbin/enforcer.go`、M1.1 matcher 的 git diff = **0 行**（matcher/adminauthz/enforcer 完全不碰）；新 RPC 全经唯一 `AuthorizeRule` + `ruleTable`，无第二套授权。
- **PC-2 export 不泄露 secret**：`Capture` 不读凭据列；export 内容、响应、Console 页面均无 secret。
- **PC-3 收敛只动 iac 子集**：`manual` / `auto` 实体永不被 import 删除或修改（**有齿**：import 文件省略某 manual 权限点 → 该权限点仍在）。
- **PC-4 dry-run 零副作用**：`dry_run=true` 走只读事务，不 bump、不广播、不写库（**有齿**：plan 后断言 DB 全等 + 版本未变）。
- **PC-5 原子 + fail-close**：解析/校验失败 → 整笔拒绝、零写入；apply 单 `runVersionedWrite` 事务，中途失败整事务回滚（**有齿**：注入中途失败断言无残留）。
- **PC-6 删除安全**：有 `user_role_binding` 的 iac 角色拒删、绑定不被触碰（**有齿**：带绑定角色的删除项被拒、绑定仍在）。
- **PC-7 租户隔离**：`app_id` 经 `AuthorizeRule` / `TenantDomainOf` fail-close，跨租户 → PermissionDenied / NotFound，无存在性枚举（**有齿**：跨租户 app import/export 用例）。
- **PC-8 数据面保真 + parity**：apply 后投影 bump + 广播（Sidecar 收敛，与既有写同源）；`internal/sidecar/` git diff = **0 行**（复用既有 sync）；gRPC / REST / Console 三面行为 parity。

## 9. 测试策略（TDD + testcontainers）

- **往返幂等**：export → 同内容 import → 空 diff（无 create/update/delete）。
- **收敛各分支有齿**：CREATE / UPDATE / DELETE / ADOPT 各一用例，断言收敛后状态 == 文件期望态。
- **PC-3 有齿**：import 省略某 manual 实体 → 不删它。
- **PC-4 有齿**：dry-run 后 DB 全等 + 版本未变。
- **PC-5 有齿**：解析失败 / 校验失败零写；apply 中途失败原子回滚。
- **PC-6**：删除带绑定的 iac 角色 fail-close。
- **格式**：YAML 与 JSON 同语义文件解析到同一模型；自动识别正确（`{` → JSON、`-`/键 → YAML）。
- **安全**：export 无 secret；跨租户 import/export 隔离；停用 app import 被 `CheckStatusWrite` 拒。
- **数据面**：apply 后 version bump + outbox 有记录（与既有 GrantPermission 等同源断言）。

## 10. 任务分组（交 writing-plans 细化）

1. 迁移：`role` / `data_policy` 加 `source` 列（up/down，testcontainers 校验默认 manual 向后兼容）。
2. `internal/controlplane/iac` 包：文件信封 + YAML/JSON 解析（自动识别）+ 序列化 + 校验（fail-close），TDD。
3. 收敛引擎：diff 计算（CREATE/ADOPT/UPDATE/DELETE）+ 经 `runVersionedWrite` 原子 apply（FK 安全序、删除安全、版本守护），TDD。
4. export：`Capture` + data_policies + 序列化；mgmt handler + ruleTable + RPC。
5. import：mgmt handler（dry_run / apply）+ ruleTable + RPC + 跨租户矩阵。
6. proto：2 RPC + message；REST 2 路由（path 权威）。
7. Console「策略即代码」页：export 下载 + import textarea + 服务端 diff 预览 + 确认 apply（doWrite/requireConfirm 复用，无新 JS）。
8. 整体核验 PC-1..8 + 安全评审 + 真实浏览器走查（diff 预览页 axe）+ FF。

## 11. 假设与未决项

- **唯一新依赖 `gopkg.in/yaml.v3`**：双格式由用户拍板「两者都支持」引入；若后续倾向零额外依赖，可降级为「仅 JSON」（去依赖、去自动识别），属可回退决策。
- **`source='auto'` 不被 IaC 夺管**为保守缺省（避免与 D 切片权限点上报冲突）；若实践中希望 auto 也可声明式管理，留后续子项目再议。
- **乐观版本守护**的 UX 细节（漂移时是硬拒还是展示新 diff 让用户重确认）留 plan/实现期定，缺省硬拒重看（fail-close）。
- **大文件 / 大模型**的分页或流式不在本轮（app 模型规模有限，单次全量可接受）。

## 自检记录

- **占位符扫描**：无 TODO / 待定；每节为具体设计决策。
- **内部一致性**：§3 稳定身份（code/key/subject-resource）与 §6 diff 匹配口径一致；§4 来源标记（manual/auto/iac）与 §6 收敛治理域、§8 PC-3 一致；§7 surface 与 §10 任务分组一致。
- **范围检查**：聚焦单 app 域授权模型的 export/import，可单 plan 覆盖；批量/条件构建器/API 文档/sandbox 已明列 out-of-scope（各为后续 M4 子项目）。
- **模糊性检查**：import 默认首响为 dry-run、apply 需显式确认（§6）；删除治理域限 iac 子集、删除安全 fail-close（§4/§6/§8）——均已明确单一口径。
