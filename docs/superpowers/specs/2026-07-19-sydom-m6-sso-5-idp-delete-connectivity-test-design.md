# M6-sso-5：IdP 删除 + 连通性测试（企业 SSO 第五片，管理收尾）

**日期**：2026-07-19
**里程碑**：M6 商业化+合规+GA → 企业 SSO（OIDC，每租户自带 IdP）
**范围**：补齐 Console IdP 管理——**删除 IdP**（`DeleteTenantIdp` RPC + 二次确认 Console 动作）+ **连通性测试**（复用 `oidc.Discover`/`FetchJWKS` 探活已保存的 issuer，SSRF-安全）。**授权求值核心零触碰**；`authz.go` 仅 **+1 ruleTable** `{"sso","delete",…,scopeTenant}`（非求值逻辑，同 M6-sso-2 加法；连通性测试**零新增 ruleTable**，复用 GetTenantIdp 授权）。

---

## 1. 背景与定位

企业 SSO 四片闭环（IdP 配置地基 `54ec387` / 登录流 `3c78b1f` / JIT `9b8696b` / Console 配置页 `8320fb5`）。Console IdP 页现支持**创建/编辑**，但缺**删除**与**探活**：租户 owner 无法经 UI 移除 IdP，也无法在启用前验证 issuer 是否可达。本片补齐这两项，使 IdP 管理 CRUD + 运维完整。

**brainstorm 决策链（本片，均用户决策）**：
- ① 删除门控=**二次确认 + 锁死警示，不阻断**（照 DeleteDataPolicy 二次确认范式；管理员决定，不因存在 SSO-only operator 而拦）。
- ② 连通性测试目标=**仅探已保存的 issuer**（与登录流实际 fetch 同一 URL，**不新增 SSRF 面**）；不探表单里未保存的任意 URL（那会让已认证 operator 探内网=新 SSRF 面）。想测新 issuer=先保存再测。

## 2. 目标 / 非目标

**目标**
1. `DeleteTenantIdp` proto additive + `store.DeleteTenantIdpTx` + handler（scopeTenant，删 `tenant_idp`+`tenant_idp_domain`，审计，无配置→NotFound）+ ruleTable `{"sso","delete",false,scopeTenant}` + REST `DELETE /v1/tenants/{id}/idp`。
2. Console 删除动作（`POST /tenants/{id}/idp/delete`，**二次确认** + 锁死警示）。
3. Console 连通性测试（`POST /tenants/{id}/idp/test`，**零新增 RPC/ruleTable**，复用 GetTenantIdp 授权，探已保存 issuer 的 `Discover`+`FetchJWKS`）。
4. 授权求值核心零触碰；`authz.go` 仅 +1 ruleTable（delete）。

**非目标（后续片/延期）**
- 删除 IdP 时级联清理 JIT-provisioned operator/membership（**有意不做**：operator/成员持久，仅失去 SSO 登录；管理员可重加 IdP 或给其发密码）。
- 连通性测试做完整授权码流/token 交换（仅 discovery+JWKS 探活，无浏览器、无 secret）。
- 承前：SAML、单租户多 IdP、sub 锚、单点登出、JIT 默认角色、operator/成员 email 管理页。

## 3. 架构决策

- **删除=标准 additive RPC**：`DeleteTenantIdp` 走 proto+store+handler+ruleTable+REST 全套，与 ConfigureTenantIdp 同域（scopeTenant 自助）。`authz.go` +1 ruleTable `{"sso","delete",false,scopeTenant}`（**非求值逻辑**；超管 `*` 通配 grant + 租户 owner `(t:<id>,*,*)` 均覆盖新 action，无需 seed）。
- **连通性测试=Console-only，零新增 RPC/ruleTable**：不引入服务端 RPC；Console 动作复用 `AuthorizeRule(GetTenantIdp)`（scopeTenant 读授权）后，直接用 Handler 持有的 `h.oidcHTTP` 调 `oidc.Discover`/`FetchJWKS`。**SSRF-安全**：只探 `GetTenantIdp` 返回的**已保存 issuer**——与登录流 fetch 同一 URL，不新增出站面；绝不探表单未保存的任意 URL。
- **删除破坏性→二次确认**：`POST /tenants/{id}/idp/delete` 经 `requireConfirm`（CSRF + confirm 令牌），照 `SetApplicationStatus`(停用)/`DeleteDataPolicy` 范式。**不级联删 operator/membership**（仅删 `tenant_idp`+`tenant_idp_domain`）。
- **零触碰**：casbin/adminauthz/kernel/dataperm 机器 diff 空；`authz.go` 仅 +1 ruleTable 行。

## 4. 删除 IdP（`store` + `mgmt/sso.go` + proto + REST）

### 4.1 proto additive
- `rpc DeleteTenantIdp(DeleteTenantIdpRequest) returns (WriteResponse);`
- `message DeleteTenantIdpRequest { uint64 tenant_id = 1; }`
- 过 `make proto-breaking`。

### 4.2 `store.DeleteTenantIdpTx(ctx, tx cp.DBTX, tenantID int64) (bool, error)`
事务内：`DELETE FROM tenant_idp_domain WHERE tenant_id=$1`（`tenant_idp_domain.tenant_id` 引用 `tenant` 非 `tenant_idp`，不级联，须显式先删域）；`DELETE FROM tenant_idp WHERE tenant_id=$1`，返回后者 `RowsAffected() > 0`（是否真有配置被删）。

### 4.3 handler `DeleteTenantIdp`
scopeTenant；`tx` → `DeleteTenantIdpTx` → 命中 0（无配置）→ `NotFound "no idp configured"`；审计 `delete_idp`（resource `tenant_idp`，target=tenant_id，**不含 secret**）→ commit → `WriteResponse{}`. REST `DELETE /v1/tenants/{id}/idp`（restgw routes_accounts.go，照 GET/PUT idp 同款 path 提取）。

### 4.4 Console 删除动作
`POST /tenants/{id}/idp/delete` → **先 `h.requireConfirm(w, r, svc+"DeleteTenantIdp")`**（未确认→渲染确认页，含锁死警示）→ `doWrite(svc+"DeleteTenantIdp")`（decode `DeleteTenantIdpRequest{TenantId:path}`，invoke `DeleteTenantIdp`，redirect 回 `/tenants/{id}/idp`）。idp 页删除按钮 + 确认页文案：**「删除后该域 SSO 登录停用；仅 SSO 登录的 operator（含 JIT 开通、无密码）将无法登录。已开通的成员账户保留。」**

## 5. 连通性测试（Console-only）

`POST /tenants/{id}/idp/test`（idp 页「测试连接」按钮，POST+csrf）：
1. `requireSession` → `checkCSRF`（POST 触发出站请求须防 CSRF-触发-SSRF）。
2. `AuthorizeRule(svc+"GetTenantIdp")`（scopeTenant 读）→ `h.srv.GetTenantIdp` 取已保存配置。
3. `!Configured` → flash/error「请先配置 IdP」。
4. `oidc.Discover(h.oidcHTTP, resp.Issuer)`（校 issuer 字段、解析端点）+ `oidc.FetchJWKS(h.oidcHTTP, pc.JWKSURI)`。
5. 成功 → flash「连通正常：discovery 与 JWKS 端点可达」；失败 → error（issuer 不可达 / issuer 字段不符 / JWKS 无法解析），**通用错误文案不回显内网细节**。
6. 回 `/tenants/{id}/idp`（PRG）。

**不新增 RPC/ruleTable**；`h.oidcHTTP`（sso-2 装配，10s 超时）复用。

## 6. 不变量（安全）

- **零触碰授权求值核心**；`authz.go` 仅 +1 ruleTable（delete）；连通性测试零 ruleTable。机器 diff `casbin/`·`adminauthz/`·`sidecar/kernel/`·`sidecar/dataperm/` 空。
- **SSRF-安全**：连通性测试只探已保存 issuer（=登录流同一出站 URL），绝不探表单未保存 URL；失败文案不泄露内网/存在性细节。
- **INV-1**：删除审计、测试结果均不含 secret；测试不解密、不碰 client_secret。
- **删除破坏性→二次确认**（CSRF + confirm 令牌）；**不级联删 operator/membership**（仅删 tenant_idp+域）。
- **授权**：删除/测试均经 `AuthorizeRule`（scopeTenant）——租户 owner 管自己、跨租户 PermissionDenied(403)、无配置删→NotFound。

## 7. 测试计划（TDD，有齿，mock IdP）

- **store**：`DeleteTenantIdpTx`（删配置+域→后续 TenantIdpOf Configured=false + 域行为 0；无配置删→命中 false）。
- **mgmt handler**：`DeleteTenantIdp`（配置后删→Configured=false；无配置→NotFound；跨租户→PermissionDenied 经 AuthorizeRule）。
- **Console**：删除走二次确认（未确认→确认页含警示、确认→删除+回页 idp 未配置）；连通性测试（**mock IdP**：discovery+jwks 可达→flash 正常；issuer 不可达→错误；未配置→提示先配）。
- **REST**：`DELETE /v1/tenants/{id}/idp`（删→200+WriteResponse JSON、无配置→404〔restgw errors.go NotFound 映射〕）。
- **两变异证有齿**：① 撤 `DeleteTenantIdpTx` 的 `DELETE tenant_idp_domain`（只删 tenant_idp）→ 残留域测试红（删后域行未清）；② 撤连通性测试 `!Configured` 先拦 → 未配置探空 issuer 测试红（应提示先配而非探空）。
- 全仓 `go test ./...` 全绿 + `make proto-breaking` + 零触碰 diff 核验（authz.go 仅 +1 行）。

## 8. 落地顺序（供 writing-plans 细化，约 3 任务）

1. 删除后端：proto additive + `store.DeleteTenantIdpTx` + handler + ruleTable +1 + REST + 测试。
2. Console：删除动作（二次确认+警示）+ 连通性测试动作 + idp 页按钮 + 测试（mock IdP）。
3. 全局验证 + 变异 + 零触碰核验（authz.go 仅 +1 行）。

## 9. 风险 / 权衡

- **删除锁死 SSO-only operator**：缓解=二次确认 + 明确警示；不级联删账户（可重加 IdP / 发密码恢复）。不阻断=尊重管理员决定（与 fail-close 无冲突：删除是显式主动破坏，非隐式）。
- **连通性测试 SSRF**：缓解=只探已保存 issuer（=登录流同一 URL，无新增面）+ POST+CSRF + 通用失败文案 + `h.oidcHTTP` 超时。
- **切片体量**：小（1 additive RPC + 1 store 删函数 + 2 Console 动作），授权侧仅 +1 ruleTable。
