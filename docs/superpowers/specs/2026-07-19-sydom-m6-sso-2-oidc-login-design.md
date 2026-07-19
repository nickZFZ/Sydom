# M6-sso-2：OIDC 登录流（企业 SSO 第二片）

**日期**：2026-07-19
**里程碑**：M6 商业化+合规+GA → 企业 SSO（OIDC，每租户自带 IdP）
**范围**：租户 operator 经其租户 OIDC IdP 登录 Sydom **Console**——发起→授权码流→ID Token 验签→身份映射→复用既有会话。**事前登录严格映射**（无 JIT）。OIDC RP **纯 stdlib 手写**（无新依赖）。**授权求值核心零触碰**。SAML/JIT/端用户登录/单租户多 IdP 为后续片。

---

## 1. 背景与定位

M6-sso-1（`54ec387`）已落地**每租户 OIDC IdP 配置地基**：`tenant_idp`（issuer/client_id/client_secret_enc/enabled，一租户一 IdP）+ `tenant_idp_domain`（email 域→租户，全局唯一，为按域路由铺路）。

现状登录（`console/auth.go handleLoginPost`）：operator 提交 `principal`+`secret`（密码等价），`ResolveSecret` 常量时间比对 → `sessions.Create(principal)`（Redis）→ cookie。**principal 即 casbin subject**，下游 `AuthorizeRule`/RBAC/授权核心据此判定。

**SSO 的干净插入点**：外部 IdP 认证成功后**汇聚到同一 `sessions.Create(principal)` + cookie 流程**——SSO 只是多一条前门认证路径，会话/RBAC/授权求值核心完全不变。

**brainstorm 决策链**（本片，均用户决策）：① 范围=**租户 operator 登录 Console**（非端用户 login-as-a-service）；② 开通模型=**事前登录严格**（OIDC email 须匹配既有 operator，否则 fail-close 拒绝；SSO 只替代密码，管理员掌控谁有权）；③ 身份链接=**`admin_operator` 加 email 列**，用 `email_verified` 的 email 匹配，**租户作用域**（须为签发 IdP 所属租户的有效成员）；④ 验签实现=**纯 stdlib 手写 OIDC RP**（无新依赖，build hermetic，契合既有 hand-rolled crypto〔AES-GCM/HMAC〕模式，二段审查+变异严格验证）。

> M6-sso-1 spec 曾预期「JIT 自动开通」；本 brainstorm 明确改为**事前登录严格**（一致性/安全优先、最小片）。JIT 留后续片。

**依赖可用性**：Go module proxy 冷启动偶发 EOF（可重试成功但 CI 未接线）；`go-oidc`/`x/oauth2`/jose 均未在 go.sum/模块缓存。stdlib（`crypto/rsa`·`ecdsa`·`sha256`·`x509`·`encoding/json`·`base64`·`math/big`·`net/http`）齐备 → 手写 RP 零 fetch 风险。

## 2. 目标 / 非目标

**目标**
1. `internal/oidc/` 独立单元：stdlib 手写 OIDC RP（discovery / auth-code URL / token 交换 / JWKS+ID Token 验签），可注入 `*http.Client` 与 `now` 时钟，完全离线可测。
2. Console 登录编排：email 先行发起 → 域路由到租户 IdP → 授权码流 → 回调验签 → 严格身份映射 → 复用 `sessions.Create`。
3. operator email 开通（本片必备，否则无人可 SSO）：`CreateOperator` 加 additive `email` + `SetOperatorEmail` RPC。
4. 迁移 `000025`：`admin_operator` 加 `email`（nullable + UNIQUE）。
5. 安全不变量 fail-close + 授权求值核心零触碰 + 有齿测试（mock IdP 端到端）。

**非目标（后续片/延期）**
- JIT 自动开通；SAML；端用户/下游应用登录（Sydom 作 IdP broker）；单租户多 IdP。
- RP-initiated / back-channel 单点登出（本片仅本地会话登出=既有 `handleLogout`）；refresh token / 长活 IdP 会话（只需 ID Token 建立一次身份，之后走自有会话）。
- operator email 的 Console 管理页（RPC+REST 够用，UI 页留后续）。

## 3. 架构决策

- **位置**：登录流全在 **Console BFF**（`internal/controlplane/console/`），因其独占 login/session。新增 `oidc.go`（编排）+ 路由。RP 原语独立到 `internal/oidc/`（无 Console 耦合）。
- **零触碰**：认证成功后调用**既有** `h.sessions.Create(principal)`+cookie，`Principal`=映射到的 operator。`AuthorizeRule`/casbin/adminauthz/kernel/dataperm/`authz.go` **一字不改**（除 ruleTable 为 `SetOperatorEmail` 加 1 配置条目=非求值逻辑）。机器 diff 核验。
- **secret 处理留在制御面（INV-1）**：Console 经 **narrow resolver 接口**取「域→IdP 登录配置（含解密后的 client_secret 明文）」，解密用 masterKey 发生在控制面实现里（`mgmt`/`adminauthz`）。明文 secret 仅在进程内用于 token 交换，**绝不入 Redis/日志/审计/任何 API 响应**（与 app secret 进程内做 HMAC 同理）。
- **隔离单元与接口**（brainstorm「小单元+清晰边界」）：
  - `internal/oidc`：无状态纯函数，输入 `*http.Client`/`now`/JWKS，输出 claims 或 error。
  - `idpLoginResolver`（Console 依赖的窄接口，两法）：`ResolveIdPByDomain(ctx, domain) (IdPLogin, bool, error)`（发起用）+ `ResolveIdPByTenant(ctx, tenantID) (IdPLogin, bool, error)`（回调用）；`IdPLogin{TenantID, Issuer, ClientID, ClientSecret, Enabled}`；生产实现读 `tenant_idp_domain`→`tenant_idp`+masterKey 解密 client_secret。
  - `operatorMatcher`（窄接口）：`MatchOperatorForLogin(ctx, tenantID, email) (principal string, ok bool, err error)`；生产实现查 `admin_operator`（email + status=1）∩ `tenant_membership`（tenantID 有效成员）。
- **redirect_uri**：`{consoleBaseURL}/auth/oidc/callback`，绝对 URL 须与 IdP 注册一致。`consoleBaseURL` 由 `deploycfg` 配置（**不从请求 Host 派生**——可伪造）。租户 IdP 未配 base URL 而尝试 SSO → 明确 fail-close 错误。

## 4. 数据模型（迁移 `000025`，expand-only）

```sql
-- M6-sso-2：operator 关联 email，供 OIDC 登录严格映射（email_verified 匹配）。
ALTER TABLE admin_operator ADD COLUMN email VARCHAR(320) UNIQUE;  -- nullable；全局唯一（一 email→一 operator）；非 SSO operator 为 NULL
```
- `VARCHAR(320)`=RFC 邮件地址上限。Postgres UNIQUE 允许多个 NULL，故存量 operator 不受影响。
- email **小写化存储**（域大小写不敏感；匹配时验签 email 也小写化）。约束不变、不加索引（UNIQUE 已建索引）。
- down：`ALTER TABLE admin_operator DROP COLUMN email;`

## 5. OIDC RP（`internal/oidc/`，纯 stdlib）

### 5.1 类型与函数
- `type ProviderConfig struct { Issuer, AuthorizationEndpoint, TokenEndpoint, JWKSURI string }`
- `Discover(ctx, hc *http.Client, issuer string) (ProviderConfig, error)`：GET `issuer + "/.well-known/openid-configuration"`；**校验响应 `issuer` 字段 == 请求 issuer**（防 mix-up）；解析三端点。
- `AuthCodeURL(p ProviderConfig, clientID, redirectURI, state, nonce, codeChallenge string) string`：`response_type=code`、`scope=openid email`、`client_id`、`redirect_uri`、`state`、`nonce`、`code_challenge`、`code_challenge_method=S256`。
- `Exchange(ctx, hc, p, clientID, clientSecret, redirectURI, code, codeVerifier string) (rawIDToken string, err error)`：POST `token_endpoint`（`application/x-www-form-urlencoded`：`grant_type=authorization_code`+code+redirect_uri+code_verifier）；客户端认证=**client_secret_basic**（`Authorization: Basic base64(clientID:secret)`）；解析 JSON 取 `id_token`。
- `type JWKS`（kid→公钥映射）；`ParseJWKS([]byte)(JWKS,error)`（RSA：`n`/`e` base64url→`big.Int`→`rsa.PublicKey`；EC：`crv`=P-256、`x`/`y`→`ecdsa.PublicKey`）；`FetchJWKS(ctx, hc, jwksURI)(JWKS,error)`。
- `type VerifyParams struct { Issuer, ClientID, Nonce string }`
- `type IDTokenClaims struct { Iss, Sub, Email string; EmailVerified bool; Exp, Iat int64; Aud []string; Nonce string }`（`aud` 自定义 Unmarshal 兼容 string 与 []string）。
- `VerifyIDToken(rawIDToken string, keys JWKS, p VerifyParams, now time.Time) (IDTokenClaims, error)`。

### 5.2 验签算法（security-critical，逐条固定）
1. 拆 `header.payload.signature`（base64url，无 padding）。
2. 解 header：`alg` **仅接受 `RS256`/`ES256`**；显式**拒绝 `none` 与 `HS*`**（防 alg 混淆/none 绕过）；取 `kid`。
3. 按 `kid` 从 JWKS 选公钥，且 **kty 须与 alg 族匹配**（RS256↔RSA、ES256↔EC/P-256）；kid 未知 → 错误（调用方可刷新 JWKS 重试一次）。
4. 验签名于 `header.payload` 原文：RS256=`rsa.VerifyPKCS1v15(SHA256)`；ES256=`ecdsa.Verify`（r,s 各 32 字节）。
5. 校验声明：`iss==p.Issuer`、`p.ClientID ∈ aud`、`exp > now`（-leeway≤60s）、`iat ≤ now`（+leeway）、`nonce==p.Nonce`。
6. 返回 claims（`email`/`email_verified`/`sub`）。

### 5.3 可测性
`VerifyIDToken` 注入 `keys`+`now`；HTTP 函数注入 `*http.Client`。测试用测试 RSA 密钥签 ID Token、构造含该公钥的 JWKS，断言正/负路径（见 §9）。

## 6. 登录流（Console 编排 `oidc.go` + 路由）

### 6.1 发起 `handleSSOStart`（`POST /login/sso`，form `email`）
1. `domain = lower(after '@')`；无 '@' → 通用登录错误。
2. `idpLoginResolver.ResolveIdPByDomain(domain)` → 未找到 / `!Enabled` → 通用错误（**无枚举 oracle**：不区分「域未配」与「其他失败」）。
3. `oidc.Discover(issuer)`。
4. 生成 `state`、`nonce`、PKCE `verifier`+`challenge=S256(verifier)`（均 32B CSPRNG，复用 `randToken` 族）。
5. **Redis 一时态**（键 `console:oidcstate:<state>`，TTL 10min，回调时 GETDEL 一次性）：`{nonce, verifier, tenantID, returnTo}`——**不含 secret/issuer 之外任何敏感项**（回调按 tenantID 重取 IdP 配置）。
6. 302 到 `oidc.AuthCodeURL(...)`（redirect_uri=`consoleBaseURL+"/auth/oidc/callback"`）。

### 6.2 回调 `handleOIDCCallback`（`GET /auth/oidc/callback`，query `code`/`state`/`error`）
1. `error` 非空 → 通用失败。
2. `state` 缺 / GETDEL 未命中（未知/过期/重放）→ 通用失败（**state=CSRF 防护 + 一次性**）。
3. 按一时态 `tenantID` **重取** IdP 配置（`ResolveIdPByTenant` 或复用 resolver）；`!Enabled`（期间被停用）→ 失败。
4. `oidc.Exchange(code, verifier)` → rawIDToken。
5. `oidc.FetchJWKS`（可短缓存）+ `oidc.VerifyIDToken(raw, jwks, {issuer, clientID, nonce}, now)` → claims；kid 未知则刷新 JWKS 重试一次。
6. **`claims.EmailVerified` 须 true**，否则失败。
7. `operatorMatcher.MatchOperatorForLogin(tenantID, lower(claims.Email))` → `principal, ok`；`!ok` → **通用失败（fail-close）**。
8. `sessions.Create(principal)` → set cookie（同 `handleLoginPost`：HttpOnly/Secure/SameSite=Strict）→ 302 到 `returnTo`（默认 `/`；仅允许本站相对路径，防开放重定向）。

### 6.3 登录页
`login.html` 加 email 先行 SSO 表单（`POST /login/sso`），保留既有 principal/secret 表单为**回退**（root 等非 SSO operator）。

## 7. operator email 开通（本片必备）

- **proto additive**：`CreateOperatorRequest` 加 `string email = <下一个可用 tag>;`（可选，空=NULL）；新增 `rpc SetOperatorEmail(SetOperatorEmailRequest) returns (WriteResponse)`（`SetOperatorEmailRequest{principal, email}`，tag 从 1 起）。
- **handler**：`CreateOperator` 落 email（小写化，空→NULL）；`SetOperatorEmail` 更新（`UPDATE admin_operator SET email=$2 WHERE principal=$1`）；email 与他 operator 冲突→`AlreadyExists`；无该 operator→`NotFound`；空 email 允许（清除）。
- **ruleTable**：`SetOperatorEmail` → `{"admin","update",false,scopeSystem}`（与 CreateOperator 的 scopeSystem 一致；operator 管理=平台超管）。REST 路由 additive。
- 过 `proto-breaking`（纯 additive）。

## 8. 装配 / 配置

- `console.NewHandler` 增注入 `idpLoginResolver` + `operatorMatcher` + `oidcHTTPClient *http.Client` + `consoleBaseURL string`（`run.go` 装配传入；生产实现在 mgmt/adminauthz，持 db+masterKey）。
- `deploycfg` 加 `console_base_url`（`SYDOM_CONSOLE_BASE_URL`）；SSO 路由被调用而缺该值 → 明确 fail-close 错误（生产）。
- `oidcHTTPClient` 带合理超时（connect/read）+ 禁跟随到非 https（生产 issuer 须 https）。

## 9. 不变量（安全，fail-close）

- **验签**：仅 RS256/ES256；拒 none/HS*；kid 须命中且 kty 匹配；签名须过。
- **声明**：iss==配置 issuer；aud∋client_id；exp 未过（leeway≤60s）；nonce==一时态 nonce。
- **email**：`email_verified==true`；否则拒。
- **映射**：operator 须存在（email 匹配）、`status==1`、且为**签发 IdP 所属租户的有效成员**（跨租户冒充防护）。任一不满足 → **通用失败（无枚举 oracle）**。
- **state**：一次性（GETDEL）+ CSRF；未知/过期/重放→拒。
- **IdP disabled**（发起或回调时）→ 拒。
- **secret**：绝不入 Redis/日志/审计/API 响应；仅进程内 token 交换用。
- **开放重定向**：`returnTo` 仅本站相对路径。
- **零触碰授权求值核心**；机器 diff 核验 `casbin/`·`adminauthz/`·`kernel/`·`dataperm`·`authz.go`（仅 ruleTable +1）。

## 10. 测试计划（TDD，测试须有齿，mock IdP）

- **`internal/oidc` 单元**：测试 RSA 密钥签 ID Token + 构造 JWKS。正路径过；**负路径逐条**：签名改一字节→拒、`aud` 错→拒、`iss` 错→拒、`nonce` 错→拒、`exp` 已过→拒、`alg=none`→拒、`alg=HS256`→拒、kid 未知→拒、`email_verified=false`（在编排层断言）。`Discover`/`Exchange`/`FetchJWKS` 用 `httptest` mock。
- **Console 编排**：`httptest` **mock IdP**（serve discovery+JWKS+token endpoint 签真 token）端到端：域→发起→回调→会话建立。断言 state 一次性（重放第二次→拒）、operator 映射（active 成员→会话；非成员/停用/未知 email/跨租户 email→拒）、IdP disabled→拒、缺 consoleBaseURL→fail-close。
- **operator email**：CreateOperator 带 email、SetOperatorEmail、email 冲突→AlreadyExists、无 operator→NotFound。
- **变异实验证有齿**（示例）：撤 alg 白名单（接受 none）→ none-alg 测试转绿即失守（应保持红）；撤租户成员校验→跨租户 email 测试红；撤 `email_verified` 校验→未验证 email 测试红。
- 全仓 `go test ./...` 全绿 + `make proto-breaking` + 零触碰 diff 核验。

## 11. 落地顺序（供 writing-plans 细化）

1. 迁移 `000025`（admin_operator.email）+ 迁移测试。
2. `internal/oidc` RP（discovery/authURL/exchange/JWKS/verify）+ 单元测试（含全部负路径变异）。
3. operator email：proto additive（CreateOperator email + SetOperatorEmail）+ handler + ruleTable + REST + 测试。
4. Console 编排：`idpLoginResolver`/`operatorMatcher` 实现 + 一时态 store + `handleSSOStart`/`handleOIDCCallback` + 登录页 + 装配（NewHandler/run.go/deploycfg）+ mock IdP 端到端测试。
5. 全局验证 + 变异 + 零触碰核验。

## 12. 风险 / 权衡

- **手写 JWT 验签风险**：缓解=alg 白名单+拒 none/HS+kty 绑定+iss/aud/exp/nonce 全校验+逐条负路径变异测试+二段审查（规格+质量）。契合项目既有 hand-rolled crypto（AES-GCM/HMAC，M6-security 审为强项）。
- **email 作为身份锚**：邮箱可变；本片取「事前登录严格+email_verified+租户成员」权衡；`sub` 锚（更稳）留后续片若需。
- **operator email 管理**：本片 RPC/REST 够用，Console 页留后续；租户自助管理成员 email（当前 operator 管理=scopeSystem）为更大 UX 决策，延期。
- **单租户单 IdP**：承 M6-sso-1；多 IdP 延期。
- **切片体量**：本片含 crypto RP + 登录编排 + operator email，较大但内聚（皆服务「operator 经 OIDC 登录」）；writing-plans 拆 5 任务覆盖。
