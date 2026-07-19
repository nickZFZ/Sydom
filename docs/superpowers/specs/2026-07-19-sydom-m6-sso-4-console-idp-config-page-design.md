# M6-sso-4：Console IdP 配置页（企业 SSO 第四片，UX 收尾）

**日期**：2026-07-19
**里程碑**：M6 商业化+合规+GA → 企业 SSO（OIDC，每租户自带 IdP）
**范围**：租户 owner 经 **Console UI** 自助配置本租户 OIDC IdP（issuer / client_id / client_secret / email 域 / enabled / jit_enabled），补齐 SSO 可用性闭环——现仅 REST/RPC 可配。消费既有 `GetTenantIdp`（读）+ `ConfigureTenantIdp`（写）。**授权求值核心零触碰**（复用既有 ruleTable `{"sso",…,scopeTenant}`，本片零 ruleTable 改动）。

---

## 1. 背景与定位

企业 SSO 三片已闭环：M6-sso-1（IdP 配置地基 `54ec387`）、M6-sso-2（OIDC 登录流 `3c78b1f`）、M6-sso-3（JIT 自动开通 `9b8696b`）。但 IdP 配置**仅经 REST/RPC**（`PUT/GET /v1/tenants/{id}/idp`）可用——租户 owner 无法经 Console UI 自助配置，与产品「双层 UX（业务向运营台 + 技术向建模台）」定位不符（其余管理能力皆有 Console 页）。本片补齐这一 UX 缺口。

**核心 UX 取舍（brainstorm 用户决策）**：`GetTenantIdp` 绝不回 client_secret（INV-1），故编辑表单无法回填 secret。决策=**client_secret 留空则保持不变**（仅首次配置或想轮换时才填）——避免「切 jit_enabled / 加一个域也须重输 secret」的烂 UX。实现取 **Variant A**（复用 `UpsertTenantIdpTx`，加一个只读密文小函数把旧密文原样回写；**从不解密、绝不出控制面**）。

## 2. 目标 / 非目标

**目标**
1. 后端小加法：`store.TenantIdpSecretEnc`（读原始密文）+ `ConfigureTenantIdp` 放宽——空 client_secret 时复用旧密文（无既有配置→InvalidArgument）。
2. Console 页：`GET /tenants/{id}/idp`（预填表单，secret 永空）+ `POST`（doWrite→ConfigureTenantIdp）。
3. INV-1：页面绝不显示 client_secret；严格 CSP/a11y（照用量页，过 `templates_lint`）。
4. 授权求值核心 + ruleTable 零改。

**非目标（后续片/延期）**
- 删除 IdP（无 `DeleteTenantIdp`，本片不加）；IdP 连通性测试按钮（discovery 探活）；operator/成员 email 的 Console 管理页；租户自助管理成员 email（更大 UX 决策，承 M6-sso-3）。
- 多 IdP 配置 UI（承单租户单 IdP）。

## 3. 架构决策

- **零触碰**：`ConfigureTenantIdp`/`GetTenantIdp` 已在 ruleTable（scopeTenant，M6-sso-1）；本片**不加任何 ruleTable 条目**，不动 `authz.go`/casbin/adminauthz/kernel/dataperm。Console 页经 `doWrite`（POST）+ `AuthorizeRule`（GET 读）唯一鉴权真相源。
- **secret 保留=Variant A**（复用既有写路径，最少活动部件）：
  - `store.TenantIdpSecretEnc(ctx, ex, tenantID) ([]byte, bool, error)` 读原始 `client_secret_enc`（**密文**）。唯一用途=编辑保留时把旧密文原样回写给 `UpsertTenantIdpTx`；**从不解密**（不碰 masterKey），密文不出控制面。
  - `ConfigureTenantIdp`：`client_secret` 空 → 事务内 `TenantIdpSecretEnc` 取旧密文复用（无配置→InvalidArgument「首次配置须提供 client_secret」）；非空 → 照旧 `Encrypt`。`UpsertTenantIdpTx` 签名/SQL **不变**。
- **Console 页照 `routes_usage.go` 范式**：读页经 `AuthorizeRule` + `h.srv.GetTenantIdp`；写经 `doWrite` 管线（session→CSRF→decode→AuthorizeRule→invoke→PRG+flash）。

## 4. 后端（`store` + `mgmt/sso.go`）

### 4.1 `store.TenantIdpSecretEnc`
```go
// TenantIdpSecretEnc 读租户 IdP 的原始加密 client_secret（密文）。无配置→ok=false。
// 仅供 ConfigureTenantIdp 编辑保留时把旧密文原样回写；从不解密、绝不出控制面（INV-1）。
func TenantIdpSecretEnc(ctx context.Context, ex cp.DBTX, tenantID int64) ([]byte, bool, error)
```

### 4.2 `ConfigureTenantIdp` 放宽校验
- 校验改为 `issuer/client_id/domains` 必填（**移除 client_secret 必填**）；域非空校验不变。
- 事务内：`client_secret==""` → `enc, ok := TenantIdpSecretEnc(tx, tenantID)`；`!ok` → `InvalidArgument("client_secret required for first configuration")`；否则 `enc` 复用旧密文。`client_secret!=""` → `enc = Encrypt(masterKey, secret)`。
- `UpsertTenantIdpTx(ctx, tx, …, enc, r.Domains, r.Enabled, r.JitEnabled)` 不变。
- 审计 diff 加 `"secret_rotated": r.ClientSecret != ""`（记本次是否轮换，仍**不含 secret 明文/密文**）。
- **契约**：REST `PUT /v1/tenants/{id}/idp` 同步获得「空 secret=保持」语义（同一 handler，additive 放宽，向后兼容——旧调用方总是带 secret，行为不变）。

## 5. Console 页（`routes_idp.go` + `templates/idp.html`）

### 5.1 `GET /tenants/{tenant_id}/idp` — `h.idpConfig`
照 `usage`：`requireSession` → `pathUint64` → `AuthorizeRule(svc+"GetTenantIdp")` → `h.srv.GetTenantIdp` → `renderPage("idp.html", {Nav:"tenants", TenantID, Configured, Issuer, ClientID, Domains(换行拼接), Enabled, JitEnabled, CSRF})`。**secret 不预填**（响应无此字段）。

### 5.2 `POST /tenants/{tenant_id}/idp` — 经 `doWrite`
- `fullMethod = svc+"ConfigureTenantIdp"`。
- decode：从 form 建 `ConfigureTenantIdpRequest{TenantId:path, Issuer, ClientId, ClientSecret, Domains:textarea 按行 trim 去空, Enabled:checkbox, JitEnabled:checkbox}`。
- invoke：`h.srv.ConfigureTenantIdp`。
- redirectTo：回 `GET /tenants/{id}/idp`（PRG + flash「IdP 配置已保存」）。

### 5.3 `templates/idp.html`
- 表单字段：issuer(text,required)、client_id(text,required)、client_secret(password；已配置时占位「留空=保持不变」不 required，未配置时 required 提示「首次配置须填」)、domains(textarea 一行一域)、enabled(checkbox)、jit_enabled(checkbox + 内联提示「开启后该 IdP 域下**全新**用户首登自动开通为**零权限**成员」)。
- 严格 CSP/a11y：单 h1、label 关联控件、隐藏 `csrf_token`、无内联 style/script（过 `templates_lint_test`）。已配置时显示状态与当前 issuer/域；域冲突/空域等 handler 错误经 `renderGRPCError`→409/400 文案。
- 注册：`handler.go` 加 `h.registerIdP(mux)`；Nav 复用 `"tenants"`（与用量/成员同租户上下文）。

## 6. 不变量（安全）

- **INV-1**：页面/响应**绝不含 client_secret**；`TenantIdpSecretEnc` 读的密文只原样回写、从不解密、不入渲染/日志/审计。
- **授权**：读写皆经 `AuthorizeRule`（scopeTenant）——租户 owner 配自己、跨租户 PermissionDenied(403)、未知租户经既有 handler NotFound/InvalidArgument。写经 `doWrite` 必过 CSRF。
- **零触碰授权求值核心 + ruleTable**：机器 diff `casbin/`·`adminauthz/`·`sidecar/kernel/`·`sidecar/dataperm/`·`mgmt/authz.go` 对基线**全空**。
- **向后兼容**：`ConfigureTenantIdp` 放宽为 additive（空 secret 新语义），既有 REST/RPC 调用方（总带 secret）行为不变。

## 7. 测试计划（TDD，有齿）

- **store**：`TenantIdpSecretEnc`（有配置→返密文+ok；无→ok=false）。
- **mgmt handler**：① 首次配置空 secret → InvalidArgument；② 先配（带 secret）后编辑空 secret 改 jit_enabled → 成功且 **DB client_secret_enc 字节不变**（密文保留）；③ 编辑带新 secret → 密文变化（轮换）；④ 既有域冲突/空域校验不回归。
- **Console 页**：GET 渲染（已配置预填 issuer/域/开关，**无 secret**）；POST 新建（带 secret）→ 落库 + flash；POST 编辑（空 secret 切 jit_enabled）→ 保留密文 + jit 变更；模板 `templates_lint` 过。
- **变异证有齿**：撤「空 secret→复用旧密文」分支（改成恒 `Encrypt(r.ClientSecret)`）→ 保留-密文测试红（空 secret 加密后密文变化，或解密失败）。
- 全仓 `go test ./...` 全绿 + `make proto-breaking`（本片无 proto 改，仍跑）+ 零触碰 diff 核验。

## 8. 落地顺序（供 writing-plans 细化，约 3 任务）

1. 后端：`store.TenantIdpSecretEnc` + `ConfigureTenantIdp` 空-secret-保留 + 测试（含保留-密文有齿）。
2. Console 页：`routes_idp.go`（GET/POST）+ `templates/idp.html` + 注册 + 测试（渲染/往返/模板 lint）。
3. 全局验证 + 变异 + 零触碰核验。

## 9. 风险 / 权衡

- **Variant A 移动密文**：缓解=密文从不解密（不碰 masterKey）、只原样回写、不入渲染/日志/审计；`TenantIdpSecretEnc` 注释明确唯一用途。（备选 Variant B=UPDATE-only 不碰 secret 列，语义更「不碰」但多一个写函数；本片取 A 少活动部件。）
- **无 Delete IdP**：承既有（无 DeleteTenantIdp RPC）；停用经 `enabled=false`（本页可切）。删除留后续片若需。
- **切片体量**：小（1 只读 store 函数 + 1 handler 放宽 + 1 Console 页），授权侧零改。
