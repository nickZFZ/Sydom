# M6-sso-1：每租户 OIDC IdP 配置地基（企业 SSO 第一片）

**日期**：2026-07-18
**里程碑**：M6 商业化+合规+GA → 企业 SSO（OIDC，每租户自带 IdP，JIT 开通）
**范围**：单一实现计划可覆盖的第一片=IdP 配置模型 + 管理 RPC + 加密。OIDC 登录流/JWKS 验签/JIT 开通/域路由/Console 配置页为后续片（各独立 spec→plan→实现）。

## 1. 背景与定位

brainstorm 决策链：计费之后用户选「SSO」→ 协议 **OIDC**（两者都要、先 OIDC）→ **每租户自带 IdP**（真企业 SSO）→ **JIT 自动开通** → 第一片=**IdP 配置地基**。

现状：无任何 SSO/OIDC/SAML 代码。Console 登录=`console/auth.go handleLoginPost`（operator principal + secret 常量时间比对 → Redis 会话 → cookie）。**SSO 的干净插入点**：外部 IdP 认证成功后汇聚到同一 `h.sessions.Create(principal)` + cookie 流程，下游会话/RBAC/授权核心不变——SSO 只是多一条前门认证路径。

本片补齐 **每租户 OIDC IdP 配置的存储与管理**，为下一片的 OIDC 登录流铺路。**本片不触认证/登录路径，纯配置 CRUD + 加密**，完全本地可测（无需外部 IdP）。

## 2. 目标 / 非目标

**目标**
- 每租户可配置其 OIDC IdP（issuer / client_id / client_secret / 允许的 email 域 / 启用开关）。
- client_secret 加密存储（复用主密钥 AES-256-GCM），读路径绝不回明文。
- email 域全局唯一（DB 强制），为下一片按域路由登录不歧义。
- 租户 owner 自助配置（scopeTenant），跨租户隔离 fail-close。

**非目标（明确延后到后续片）**
- OIDC 发起（authorization code flow）/ 回调 handler。
- go-oidc discovery / JWKS 验签 / ID token 校验。
- JIT 开通（首次 SSO 登录建 operator + 成员）。
- 按 email 域路由到租户 IdP 的登录逻辑。
- Console SSO 配置页 / 「用 SSO 登录」入口。
- 多 IdP per 租户（本片一租户一 IdP）。
- SAML（协议第二片）。

## 3. 架构决策

**AD-1 一租户一 IdP（`UNIQUE(tenant_id)`）**
本片一租户至多一条 IdP config。多 IdP per 租户延后（真需求少；届时去 UNIQUE 加 selector）。

**AD-2 域独立表 + 全局 `UNIQUE(domain)`**
`tenant_idp_domain` 一租户多域（acme.com/acme.co.uk）；`UNIQUE(domain)` DB 强制「一域→一租户 IdP」，使下一片按 email 域路由无歧义。域存小写（大小写不敏感匹配）。

**AD-3 ConfigureTenantIdp 由租户 owner 自助（scopeTenant）**
配自家 SSO 是租户管理动作；授权走现有 scopeTenant（租户管理员在 t:<id> 域的 `*:*` grant 覆盖）。跨租户配置 fail-close（PermissionDenied，早于 handler）。

**AD-4 client_secret 加密 + 读不泄露**
`crypto.Encrypt(masterKey, secret)` 存 `client_secret_enc BYTEA`（同 app/operator 凭据 AES-256-GCM）。`GetTenantIdp` 绝不回明文/密文 secret，只回元数据 + `configured` 标志——沿用 app secret「不泄露」铁律。

**AD-5 复用 M6-errsem 错误语义**
域冲突（pq 23505）经写路径归一为 `AlreadyExists`；输入非法→InvalidArgument。

## 4. 数据模型（迁移 `000024`，expand-only）

```sql
CREATE TABLE tenant_idp (
    id                 BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id          BIGINT      NOT NULL REFERENCES tenant(id),
    issuer             TEXT        NOT NULL,
    client_id          TEXT        NOT NULL,
    client_secret_enc  BYTEA       NOT NULL,
    enabled            BOOLEAN     NOT NULL DEFAULT false,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_tenant_idp_tenant UNIQUE (tenant_id)
);

CREATE TABLE tenant_idp_domain (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id  BIGINT NOT NULL REFERENCES tenant(id),
    domain     TEXT   NOT NULL,
    CONSTRAINT uq_tenant_idp_domain UNIQUE (domain)
);
```
down：`DROP TABLE tenant_idp_domain; DROP TABLE tenant_idp;`（先删域表——虽无 FK 指向 tenant_idp，仍按依赖直觉先删子）。

## 5. 加密

`client_secret` 经 `crypto.Encrypt(s.masterKey, []byte(secret))`（`internal/crypto` AES-256-GCM，KeySize=32）存 `client_secret_enc`。仅未来 OIDC 流（下一片）内部 `Decrypt` 用于换 token；本片无 Decrypt 调用方（写入即封存）。

## 6. RPC（proto additive）

### 6.1 `ConfigureTenantIdp`
```proto
rpc ConfigureTenantIdp(ConfigureTenantIdpRequest) returns (ConfigureTenantIdpResponse);
message ConfigureTenantIdpRequest {
  uint64 tenant_id = 1;
  string issuer = 2;
  string client_id = 3;
  string client_secret = 4;   // 明文入参，服务端加密存储；GetTenantIdp 绝不回
  repeated string domains = 5; // email 域（服务端 lowercase）
  bool enabled = 6;
}
message ConfigureTenantIdpResponse { uint64 tenant_id = 1; bool enabled = 2; }
```
- ruleTable：`{"sso", "update", false, scopeTenant}`。
- handler：校验 issuer/client_id/client_secret 非空、domains 非空且格式合理（含 `.`，无 `@`）→ 事务：`UpsertTenantIdpTx`（加密 secret upsert tenant_idp + 删旧域插新域）→ commit。
- 错误：域被他租户占用→`AlreadyExists`（pq 23505 归一，M6-errsem）；空必填→`InvalidArgument`。

### 6.2 `GetTenantIdp`
```proto
rpc GetTenantIdp(GetTenantIdpRequest) returns (GetTenantIdpResponse);
message GetTenantIdpRequest { uint64 tenant_id = 1; }
message GetTenantIdpResponse {
  bool configured = 1;         // 是否已配 IdP
  string issuer = 2;
  string client_id = 3;
  repeated string domains = 4;
  bool enabled = 5;
  // 注意：绝不含 client_secret（AD-4）
}
```
- ruleTable：`{"sso", "read", false, scopeTenant}`。
- handler：读 tenant_idp + domains；未配→`configured=false` 其余空；跨租户由 scopeTenant 拦截（PermissionDenied 早于 handler）。

## 7. store 层
`internal/controlplane/store/tenant_idp.go`：
- `TenantIdp` 类型（不含明文 secret：issuer/clientID/domains/enabled/configured）。
- `UpsertTenantIdpTx(ctx, tx, tenantID, issuer, clientID, secretEnc, domains, enabled) error`：
  `INSERT INTO tenant_idp (...) VALUES (...) ON CONFLICT (tenant_id) DO UPDATE SET ...`；
  `DELETE FROM tenant_idp_domain WHERE tenant_id=$1`；逐 domain `INSERT`（域冲突→pq 23505 上抛）。
- `TenantIdpOf(ctx, ex, tenantID) (TenantIdp, bool, error)`：读元数据（**不 select client_secret_enc**）+ 聚合 domains；无行→`configured=false`。

## 8. 不变量
- **INV-1 secret 不泄露**：`GetTenantIdp` 响应无 client_secret 任何形态；`TenantIdpOf` 不查密文列。DB 内为密文（AES-256-GCM）。
- **INV-2 域全局唯一**：`uq_tenant_idp_domain` 保证；跨租户抢同域→冲突 fail-close。
- **INV-3 一租户一 IdP**：`uq_tenant_idp_tenant` + ON CONFLICT upsert 幂等。
- **INV-4 跨租户隔离**：scopeTenant 授权，配置/读他租户→PermissionDenied。
- **INV-5 零触碰授权核心**：casbin/adminauthz 求值/kernel/dataperm/authz.go 求值逻辑不动（ruleTable 加条目=接入面配置）；机器 diff 核。
- **INV-6 additive 兼容**：proto 仅追加，过 proto-breaking。

## 9. 测试计划（TDD，测试须有齿）
1. **迁移**：`RunMigrations` 幂等；tenant_idp/tenant_idp_domain 表存在；`uq_tenant_idp_domain` 拒重复域；down 对称。
2. **store UpsertTenantIdpTx / TenantIdpOf**：upsert 幂等（同租户二次配置覆盖）；domains 替换（旧域清、新域入）；TenantIdpOf 回元数据且**结构无 secret 字段**；域跨租户冲突→pq 唯一违约。
3. **ConfigureTenantIdp RPC**：owner 配置成功（DB 里 client_secret_enc 为密文≠明文）；域被他租户占用→AlreadyExists；空 issuer/client_id/secret/domains→InvalidArgument；跨租户→PermissionDenied（AuthorizeRule）。
4. **GetTenantIdp RPC**：已配→回 issuer/client_id/domains/enabled **且响应序列化不含 secret 明文**；未配→configured=false；跨租户→PermissionDenied。
5. **加密往返**：写入后 `crypto.Decrypt(masterKey, enc)` == 原 secret（仅内部断言，证加密可逆供下一片用）。
6. **零触碰 + 兼容**：机器 diff 核授权核心空；proto additive 过 proto-breaking + proto-check。
7. **变异证有齿**：撤 GetTenantIdp 的 secret 排除（若误加 secret 字段）→ 泄露测试红；撤域 UNIQUE→跨租户抢域测试红。

## 10. 落地顺序（供 writing-plans 细化）
1. 迁移 000024（tenant_idp + tenant_idp_domain + down）。
2. store：tenant_idp.go（TenantIdp 类型 + UpsertTenantIdpTx + TenantIdpOf）。
3. proto：ConfigureTenantIdp + GetTenantIdp + 消息 → buf generate。
4. handler：billing 同款——ConfigureTenantIdp（加密+事务+域冲突映射）+ GetTenantIdp（脱敏）+ ruleTable 两条目 + restgw 路由 + apidoc。
5. 验证：go test ./...、proto-breaking、机器 diff 零触碰、变异。

## 11. 风险 / 权衡
- **R-1 域路由未在本片使用**：域表+唯一约束本片只存不路由；下一片登录流消费。提前建约束避免路由歧义（值得）。
- **R-2 一租户一 IdP 限制**：多 IdP（如同时 Okta+Azure）延后；届时去 UNIQUE(tenant_id) 加 selector 列。
- **R-3 client_secret 轮换**：本片 ConfigureTenantIdp upsert 即覆盖=硬轮换（同 RotateApplicationSecret 语义）；无历史。可接受。
- **R-4 SSO 不改动认证路径**：本片纯配置存储，登录仍走 principal+secret；SSO 真正生效在下一片。文档注明「配置已存但登录流未接」。
