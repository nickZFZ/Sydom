# 企业 SSO（OIDC）配置指南

司域支持**每租户自带 OIDC 身份提供方（IdP）**：租户成员用企业账号（Okta / Entra ID / Google Workspace / Keycloak 等任意 OIDC 提供方）登录司域 Console，无需单独的司域密码。

本指南面向**租户 owner**（配置 IdP）与**平台超级管理员**（管理 operator）。运维/安全内幕见文末「安全设计」；契约细节见 [API 版本化](api-versioning.md)。

---

## 1. 工作原理（一图胜千言）

```
用户输入企业邮箱  →  按邮箱域路由到该租户的 IdP  →  OIDC 授权码流（PKCE）
      →  IdP 认证 + 回调  →  司域验签 ID Token  →  身份映射（严格 / JIT）
      →  建立司域会话（复用既有 cookie 会话，与密码登录同一套）
```

- **域路由**：邮箱 `@` 后的域全局唯一地绑定到某租户的 IdP（一域一租户）。
- **身份映射**：验签通过后，把 IdP 返回的 `email` 映射到一个司域 operator（详见 §4）。
- **零改授权**：SSO 只是多一条**前门认证**路径；映射成功后走的会话、RBAC、授权判定与密码登录**完全一致**。

司域的 OIDC RP（依赖方）为**纯标准库手写**，仅接受 `RS256`/`ES256` 签名，逐条校验 `iss`/`aud`/`exp`/`nonce`/`email_verified`（见 §7）。

---

## 2. 前提条件

1. **一个 OIDC 提供方**，你能在其中注册一个「应用 / 客户端」并拿到 `issuer`、`client_id`、`client_secret`。
2. **在 IdP 注册回调地址（redirect_uri）**：
   ```
   {司域 Console 基址}/auth/oidc/callback
   ```
   基址由部署配置 `SYDOM_CONSOLE_BASE_URL`（YAML `console_base_url`）提供，**不从请求 Host 派生**（防伪造）。例：Console 部署在 `https://console.example.com`，则 redirect_uri 为 `https://console.example.com/auth/oidc/callback`。
   > 未设置 `SYDOM_CONSOLE_BASE_URL` 时 SSO 发起会 fail-close（明确失败），密码登录不受影响。
3. IdP 须在 ID Token 中下发 `email` 与 `email_verified`（授权请求 scope 为 `openid email`）。生产 issuer 须为 `https://`。

---

## 3. 配置 IdP

### 3.1 经 Console（推荐，租户 owner 自助）

1. 登录 Console → **我的租户** → 目标租户的 **企业 SSO** 链接（`/tenants/{租户ID}/idp`）。
2. 填写：
   | 字段 | 说明 |
   |---|---|
   | 发行方 Issuer | IdP 的 `issuer`（司域据此拉 `/.well-known/openid-configuration`） |
   | Client ID | IdP 注册的客户端 ID |
   | Client Secret | 客户端密钥（**加密存储，页面绝不回显**；编辑时留空=保持不变，填新值=轮换） |
   | Email 域 | 一行一个（如 `acme.com`）。这些域的邮箱将路由到本 IdP；**全局唯一**，被他租户占用会报冲突 |
   | 启用 SSO 登录 | 勾选后该域邮箱可经此 IdP 登录 |
   | 启用 JIT 自动开通 | 见 §4.2 |
3. 保存后可点 **测试连接** 探活（校验 issuer 可达、discovery 与 JWKS 端点正常）。

### 3.2 经 REST（自动化 / IaC）

| 方法 | 路径 | 作用 |
|---|---|---|
| `PUT` | `/v1/tenants/{tenant_id}/idp` | 创建/更新（body：`issuer`/`client_id`/`client_secret`/`domains`/`enabled`/`jit_enabled`；空 `client_secret` 且已存在=保持旧密钥） |
| `GET` | `/v1/tenants/{tenant_id}/idp` | 读回元数据（**绝不含 client_secret**） |
| `DELETE` | `/v1/tenants/{tenant_id}/idp` | 删除配置（无配置→404） |

授权为 `scopeTenant`：租户 owner 管自己的 IdP，跨租户被拒（403）。

---

## 4. 身份开通模型：谁能登录

验签通过后，司域把 IdP 的 `email`（须 `email_verified=true`）映射到 operator。有两种模式，**默认严格，JIT 需显式开启**。

### 4.1 事前登录严格映射（默认）

登录成功要求该 email 对应一个 operator，且：**账号 active** + **是签发 IdP 所属租户的有效成员** + **email 已登记**。任一不满足 → 通用失败（不区分原因，无枚举泄露）。

**如何预登记一个可 SSO 的 operator：**
1. 租户 owner **邀请成员**（Console 成员页 / `POST /v1/tenants/{id}/members`）：为该 principal 建 operator（若不存在）并作为**租户管理员**加入租户。
2. 平台**超级管理员**为该 operator **设置 email**（`SetOperatorEmail`，见 §6.4）——email 管理为平台级（`scopeSystem`）。
3. 之后该人用其企业邮箱即可 SSO 登录，获得**租户管理员**权限。

> 严格映射适合小团队 / 高管控：管理员精确掌控谁有权。代价是每人需预登记（含一步平台管理员设 email）。规模化用 JIT。

### 4.2 JIT 自动开通（Just-In-Time，需每租户显式开启）

在 IdP 配置勾选 **启用 JIT 自动开通**（`jit_enabled`）后：该 IdP 域下 `email_verified` 的**全新**邮箱（司域中尚无任何同邮箱 operator）首登时**自动开通**为一个**零权限成员**——能登录、列入租户成员，但**任何管理操作都被拒**，直到管理员显式授权（授予角色 / 提档）。

- **仅全新邮箱**：若该邮箱已属某个既有 operator（哪怕不是本租户成员），维持严格 fail-close（不跨租户自动接管既有账号）。
- **默认零权限**：JIT 成员无任何 casbin 授权；管理员随后按需授权。
- **开关默认关**：不开则行为完全等同 §4.1 严格映射。

> JIT 适合规模化自助：员工用企业邮箱即可进来（零权限），管理员事后按需提权。

---

## 5. 用户登录体验

1. 用户访问 Console 登录页，在**企业邮箱（SSO）**表单输入邮箱，提交。
2. 浏览器被重定向到其企业 IdP 完成认证（可能含 MFA）。
3. IdP 回调司域；司域验签、映射、建立会话，重定向到 Console 首页。
4. 之后与普通登录无异（同一套会话 cookie：HttpOnly / Secure / SameSite=Strict）。

登录页保留 **Principal / Secret** 密码表单作为回退（供 root 等非 SSO 账号）。

---

## 6. 运维

### 6.1 轮换 client_secret
Console IdP 页编辑，在 **Client Secret** 填入新值保存（留空=保持不变）。REST 同理（`PUT` 带新 `client_secret`）。

### 6.2 临时停用
取消勾选 **启用 SSO 登录**（`enabled=false`）保存——该域 SSO 登录立即被拒，配置保留。

### 6.3 删除 IdP
Console IdP 页 **删除 SSO 配置**（**二次确认**）。删除会移除该租户的 IdP 与其域绑定。
> ⚠️ 删除后该域 SSO 登录停用；**仅靠 SSO 登录的 operator（含 JIT 开通、无密码）将无法登录**。已开通的成员账户本身保留（可重加 IdP 或由平台管理员发密码恢复）。

### 6.4 设置 operator 的 email（供严格映射）
平台超级管理员操作（`scopeSystem`）：
- `POST /v1/operators`（建 operator 时带 `email`），或
- `PUT /v1/operators/{principal}/email`（`SetOperatorEmail`；空 email=清除；email 全局唯一，冲突→AlreadyExists；无该 operator→NotFound）。
> 目前 operator email 仅经 gRPC/REST 设置（暂无 Console 页）。

### 6.5 连通性测试
Console IdP 页 **测试连接**：探活**已保存的** issuer（discovery + JWKS）。出于 SSRF 安全，只探已保存 issuer（与登录流实际访问的同一地址），不探表单里未保存的任意 URL——想测新 issuer 请先保存。

---

## 7. 安全设计（为什么可信）

- **验签**：仅接受 `RS256`/`ES256`；显式拒绝 `none` 与 `HS*`（防 alg 混淆/none 绕过）；`kid` 须命中 JWKS 且密钥类型与 alg 族匹配；签名须通过。
- **声明校验**：`iss` == 配置 issuer；`aud` ∋ client_id；`exp` 未过（≤60s 时钟偏移）；`nonce` == 发起时一时态 nonce；discovery 响应的 `issuer` 字段须与请求 issuer 相等（防 mix-up）。
- **email**：`email_verified` 须为 true，否则拒。
- **CSRF + 一次性 state**：`state` 一次性消费（Redis GETDEL），未知/过期/重放一律拒。
- **fail-close 无枚举 oracle**：域未配、映射失败、验签失败、IdP 停用……所有失败路径返回同一通用错误，不泄露差异。
- **secret 全程留控制面**（INV-1）：client_secret 加密存储（AES-256-GCM），仅在进程内用于 token 交换；绝不入 Redis / 日志 / 审计 / 任何 API 响应；Console 页绝不回显。
- **redirect_uri 不可伪造**：取自部署配置 `SYDOM_CONSOLE_BASE_URL`，不从请求 Host 派生。
- **开放重定向防护**：登录后仅跳本站相对路径。

授权求值核心（casbin 判定）在整个 SSO 特性中**零改动**——SSO 只新增前门认证，不触碰权限判定。

---

## 8. 故障排查

登录失败一律显示通用错误（无枚举 oracle），故排查从配置与日志入手：

| 现象 | 排查 |
|---|---|
| 点「用企业账号登录」即失败 | ① 邮箱域是否已在 IdP 配置且**启用**？② `SYDOM_CONSOLE_BASE_URL` 是否已设？③ issuer 是否可达（用**测试连接**）？ |
| IdP 端报 redirect_uri 不匹配 | IdP 注册的回调须**逐字**等于 `{SYDOM_CONSOLE_BASE_URL}/auth/oidc/callback` |
| IdP 认证成功但回司域后失败 | ① `email_verified` 是否 true？② 严格模式下该 email 是否已登记为本租户成员（§4.1）？③ 未开 JIT 而邮箱全新 → 被拒（改开 JIT 或预登记） |
| 「测试连接」失败 | issuer 不可达 / discovery 的 issuer 字段不符 / JWKS 无法解析——核对 issuer 拼写与 IdP 可用性 |
| 域冲突（保存报被占用） | email 域全局唯一，已被他租户 IdP 占用 |
| 删除后有人无法登录 | 见 §6.3：SSO-only / JIT operator 依赖该 IdP；重加 IdP 或发密码 |

---

## 相关

- [API 版本化 + 向后兼容](api-versioning.md) — IdP / operator 相关 REST 契约与演进
- [GA 安全评审](security-review-ga.md) — 信任边界与凭据吊销语义
