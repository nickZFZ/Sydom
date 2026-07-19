# M6-sso-3：OIDC JIT 自动开通（企业 SSO 第三片）

**日期**：2026-07-19
**里程碑**：M6 商业化+合规+GA → 企业 SSO（OIDC，每租户自带 IdP）
**范围**：租户 owner 开启 `jit_enabled` 后，其 IdP 域下 **email_verified 的全新用户**首登 Console 时**自动开通为零权限成员**并复用既有会话。开关**默认关**时 M6-sso-2 事前严格映射一字不变。**授权求值核心 + `authz.go` 全零改**（JIT 不新增 RPC，开关搭现有 `ConfigureTenantIdp`，开通发生在登录回调内）。

---

## 1. 背景与定位

M6-sso-1（`54ec387`）落地每租户 OIDC IdP 配置；M6-sso-2（`3c78b1f`）落地 OIDC 登录流 + **事前登录严格映射**（email 须匹配既有 active operator 且为签发 IdP 所属租户成员，否则 fail-close）。严格映射的代价：**管理员须逐一预建 operator + 设 email**——企业量级（几十上百员工）不可扩展。

**JIT（Just-In-Time provisioning）** 消除该摩擦：验签通过的 OIDC 用户首登即自动开通。M6-sso-1/2 spec 原已预期 JIT，M6-sso-2 明确「留后续片」——即本片。

**brainstorm 决策链**（本片，均用户决策）：
- ① **门控 = 每租户显式开关 `jit_enabled`，默认关**（保留严格映射为默认；租户 owner 自助开；与 fail-close/向后兼容一致，无人误开）。
- ② **默认权限 = 最小权限**：只建 operator + `tenant_membership(TierMember)` + **零 casbin 授权**（能登录、列入成员，但任何 scopeTenant/App RPC 皆 PermissionDenied，直到管理员显式授权）。
- ③ **既有 email 冲突 = 仅建全新**：JIT 只在 email **完全未知**（无任何 `admin_operator` 行）时开通新账户；email 已属某 operator 但非本租户成员 → **维持 M6-sso-2 fail-close**（不跨租户自动拉入既有账户，防账户接管）。

## 2. 目标 / 非目标

**目标**
1. 迁移 `000026`：`tenant_idp` 加 `jit_enabled BOOLEAN NOT NULL DEFAULT false`。
2. 供应原语（控制面，持 masterKey）：`ProvisionOperatorForLogin(ctx, tenantID, email) → (principal, ok, err)`——仅全新 email 建 operator(principal=email, 随机 secret, status=1) + membership(TierMember) + 审计，**不 bump 策略版本**（零 casbin 绑定）。
3. 回调编排：严格映射未命中 + `idp.JITEnabled` → 尝试 JIT；否则/失败一律**通用 401（无枚举 oracle）**。
4. 开关配置面（additive proto）：`ConfigureTenantIdp` 加 `jit_enabled`（租户 owner 自助）+ `GetTenantIdp` 回显（非 secret）。
5. 安全不变量 fail-close + 授权求值核心/`authz.go` 零触碰 + 有齿测试（mock IdP 端到端）。

**非目标（后续片/延期）**
- JIT 更新既有账户（改档/改邮箱）；跨租户拉入既有账户；默认非零权限（只读 viewer 角色留后续 UX 决策）。
- SCIM/反向目录同步；IdP 群组→角色映射；离职反向 deprovision。
- 承 M6-sso-2：SAML、单租户多 IdP、`sub` 锚、单点登出、operator email/成员的 Console 管理页。

## 3. 架构决策

- **开关不新增 RPC**：`jit_enabled` 搭现有 `ConfigureTenantIdp`（scopeTenant 租户 owner 自助，M6-sso-1 已在 ruleTable）。**故 `mgmt/authz.go` 本片零改**——比 M6-sso-2（+1 ruleTable）更干净。
- **供应发生在登录回调内**（`console/oidc.go handleOIDCCallback`），非独立 RPC。触发前提：OIDC 验签全过（签名+iss+aud+exp+nonce+`email_verified`）+ 租户显式 `jit_enabled`。写入=零权限成员，安全（标准 JIT 模型）。
- **INV-1 secret 留控制面**：供应原语在 `ssologin`（持 db+masterKey）；随机 secret 加密存储、从不返回/入日志/入审计。
- **零策略变更**：JIT 成员**无 casbin 绑定**（`admin_subject_role` 无行）→ enforcer 无需重载 → **不 BumpPolicyVersion**。account-layer I-1 不变量（membership 与 casbin 绑定同事务）在此**平凡满足**（无绑定可锁步）。
- **principal = email**：JIT operator 的 casbin subject/会话 Principal = 其 email（唯一、可溯、与身份锚一致）。`principal`/`email` 列均 UNIQUE，竞态/碰撞 → INSERT 违例 → fail-close。
- **首次签发 TierMember**：`adminauthz.TierMember`（=3，此前「预留不签发」）由 JIT 首次签发。
- **供应写复用既有公有函数**（不改 adminauthz 源）：`store` 新增 operator 插入 + `adminauthz.InsertMembership`/`InsertAdminAudit`（均公有）→ 零触碰核验仍空 diff。

## 4. 数据模型（迁移 `000026`，expand-only）

```sql
-- M6-sso-3：每租户 JIT 开关。默认 false=保留事前登录严格映射（向后兼容）。
ALTER TABLE tenant_idp ADD COLUMN jit_enabled BOOLEAN NOT NULL DEFAULT false;
```
- 默认 false → 存量 IdP 全部保持严格（无行为变更）。
- down：`ALTER TABLE tenant_idp DROP COLUMN jit_enabled;`

## 5. 供应原语（`ssologin` + `store`）

### 5.1 读视图带出开关
- `store.IdPLoginRow` + `ssologin.IdPLogin` 加 `JITEnabled bool`；`store.IdPLoginByDomain`/`IdPLoginByTenant` SELECT 出 `jit_enabled`。
- `store.TenantIdp`（M6-sso-1 读视图）+ `TenantIdpOf` 带 `JITEnabled`；`UpsertTenantIdpTx` 写 `jit_enabled`。（手写 Go 结构统一用 `JITEnabled`；proto 生成字段为 `JitEnabled`/`GetJitEnabled()`，映射层显式转换——同 M6-sso-2 `ClientID` 手写 vs `ClientId` 生成。）

### 5.2 `Resolver.ProvisionOperatorForLogin(ctx, tenantID int64, email string) (principal string, ok bool, err error)`
单事务：
1. `SELECT 1 FROM admin_operator WHERE email=$1` —— **命中 → 返回 `ok=false, err=nil`**（决策 ③：既有 email 不 JIT，caller fail-close）。
2. 生成随机高熵 secret（`crypto/rand`，≥32B）→ `crypto.Encrypt(masterKey)`。
3. `store.InsertJITOperatorTx`：`INSERT admin_operator(principal, secret_enc, email, status) VALUES ($email,$enc,$email,1) RETURNING id`（`principal`/`email` UNIQUE 违例=并发竞态 → `ok=false`）。
4. `adminauthz.InsertMembership(ctx, tx, tenantID, opID, adminauthz.TierMember)`（公有，不改源）。
5. `adminauthz.InsertAdminAudit`：action `jit_provision`、resource `operator`、target=email、detail `{tenant_id, via:"sso_jit"}`——**绝不含 secret**。
6. **不 BumpPolicyVersion**（零 casbin 变更）；`commit`。
- 返回 `principal`（=email）供会话建立。任何 `!ok`/`err` → caller `ssoFail`。

### 5.3 结果性质
- JIT operator 无 `admin_subject_role` 绑定 → 任何 scopeTenant/App `AuthorizeRule` → PermissionDenied（fail-close）；仅 `ListMyTenants`（scopeSelf）可见其租户。
- 随机 secret 从不返回 → 密码登录对其等效禁用（只能 SSO）。
- 出现在 `ListOperators`（admin_operator）与 `ListMembers`（tenant_membership）→ 管理员可见并后续授权/提档。

## 6. 回调编排改动（`console/oidc.go`，唯一登录路径改动）

`handleOIDCCallback` 中严格映射之后：
```go
principal, ok, err := h.operatorMatch.MatchOperatorForLogin(ctx, st.TenantID, email)
if err != nil { h.ssoFail(w, r); return }
if !ok {
    if idp.JITEnabled { // 租户显式开 JIT
        principal, ok, err = h.operatorMatch.ProvisionOperatorForLogin(ctx, st.TenantID, email)
    }
    if err != nil || !ok { h.ssoFail(w, r); return } // JIT 关 / 既有 email / 竞态 → 通用 401
}
// principal 确立 → 既有 sessions.Create + cookie 流不变
```
- `operatorMatcher` 私有接口 additive 加 `ProvisionOperatorForLogin(ctx, tenantID int64, email string) (string, bool, error)`（`ssologin.Resolver` 满足）。
- `idp.JITEnabled` 由已解析的 `ResolveIdPByTenant` 带出（回调按一时态 tenantID 重取，不信回调参数——承 M6-sso-2）。
- **无枚举 oracle**：JIT 关、既有 email、竞态、验签失败——全部同一通用 401，不泄露差异。

## 7. 开关配置面（additive proto，搭现有 RPC）

- `ConfigureTenantIdpRequest` 加 `bool jit_enabled = 7;`；`GetTenantIdpResponse` 加 `bool jit_enabled`（**非 secret，可回显**）。
- `store.UpsertTenantIdpTx` 落 `jit_enabled`；`TenantIdpOf`/`TenantIdp` 读视图回带。
- handler `ConfigureTenantIdp` 透传 `r.JitEnabled` 入 `UpsertTenantIdpTx`；`GetTenantIdp` 回带。
- **`authz.go` 零改**（`ConfigureTenantIdp`/`GetTenantIdp` 已在 ruleTable，scopeTenant）。过 `make proto-breaking`（纯 additive）。

## 8. 不变量（安全，fail-close）

- **门控**：仅 `jit_enabled==true` 才尝试 JIT；默认 false → 严格映射不变。
- **仅全新**：仅 email 无任何 `admin_operator` 行才开通；既有 email（含既有非成员）→ fail-close（跨租户账户接管防护，承 M6-sso-2）。
- **最小权限**：JIT operator 零 casbin 绑定 → scopeTenant/App 皆 PermissionDenied；仅账户层成员可见。
- **前置**：JIT 触发须 OIDC 验签全过 + `email_verified==true`（承 M6-sso-2 §9）。
- **secret**：随机、加密、绝不返回/入日志/入审计。
- **无枚举 oracle**：所有失败路径统一通用 401。
- **零策略变更**：无 casbin 绑定 → 不 bump 版本。
- **零触碰授权求值核心**；机器 diff `casbin/`·`adminauthz/`·`sidecar/kernel/`·`sidecar/dataperm/`·`mgmt/authz.go` 对基线**全空**（本片授权侧一字不改）。

## 9. 测试计划（TDD，测试须有齿，mock IdP）

- **迁移 000026**：`jit_enabled` 列存在、默认 false、down 删列。
- **store**：`UpsertTenantIdpTx`/`TenantIdpOf` roundtrip `jit_enabled`；`IdPLoginByDomain`/`ByTenant` 带出 `JITEnabled`；`InsertJITOperatorTx` 插入（principal=email、status=1）。
- **ssologin `ProvisionOperatorForLogin`**：全新 email→建 operator(principal=email)+membership(TierMember)+返回 principal；二次同 email→`ok=false`；既有非成员 email→`ok=false`。
- **console 端到端（mock IdP）**：
  - JIT 开 + 全新 email → 自动开通 + 会话 cookie；**断言新 principal 对某 scopeTenant RPC 仍 PermissionDenied（零权限）**；断言 operator+membership 行存在、`admin_subject_role` 无该 principal 行。
  - JIT 关 + 全新 email → 401（**回归守卫**：严格映射默认不变）。
  - JIT 开 + 既有非成员 email → 401（跨租户防护）。
  - JIT 开 + 既有成员 email → 严格映射胜（不重复开通；membership 不新增第二条）。
- **mgmt sso**：`ConfigureTenantIdp` 设 `jit_enabled` + `GetTenantIdp` 回显。
- **两变异证有齿**：① 撤 console `idp.JITEnabled` 门（恒尝试开通）→「JIT 关」测试红；② 撤 `ProvisionOperatorForLogin` 的 email 存在检查 →「既有非成员」测试红。
- 全仓 `go test ./...` 全绿 + `make proto-breaking` + 零触碰 diff 核验。

## 10. 落地顺序（供 writing-plans 细化，约 5 任务）

1. 迁移 `000026`（tenant_idp.jit_enabled）+ 迁移测试。
2. store + ssologin：`jit_enabled` 读字段（IdPLoginRow/IdPLogin/TenantIdp）+ `InsertJITOperatorTx` + `ProvisionOperatorForLogin` + 单元测试。
3. 回调编排：`operatorMatcher` 加 `ProvisionOperatorForLogin` + `handleOIDCCallback` JIT 分支 + mock IdP 端到端（4 场景）。
4. 开关配置面：proto additive（ConfigureTenantIdp/GetTenantIdp `jit_enabled`）+ store roundtrip + handler 透传 + mgmt 测试。
5. 全局验证 + 变异 + 零触碰核验。

## 11. 风险 / 权衡

- **JIT=自动开通**：缓解=每租户显式 opt-in（默认关）+ 仅 email_verified + 仅全新 email + 零权限 + 全程审计（`jit_provision`）+ 无枚举 oracle。
- **零权限成员首登「什么都看不到」**：有意——管理员后续 `GrantAdminRole`/组员提档；默认只读 viewer 角色为更大 UX 决策，延期。
- **principal=email 与既有非 email principal 混存**：无碍（principal UNIQUE 兜底）；JIT operator 可溯（email 即 principal + `via:sso_jit` 审计）。
- **切片体量**：小而内聚（1 迁移列 + 1 供应原语 + 1 回调分支 + 1 开关字段），授权侧零改。
