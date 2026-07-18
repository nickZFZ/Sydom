# 司域(Sydom) GA 安全评审 — 信任边界审计

> 本文是 GA 前置的**信任边界安全评审**，属路线图 M6「合规/渗透测试」范围。
> 方法：源码级审查（非动态渗透），逐条附 `file:line` 证据。
> 结论摘要：**未发现可利用漏洞**；下列为已验证的强项、若干加固建议，以及一处需运维明确的**设计语义澄清**（应用停用 ≠ 运行时吊销）。
>
> 评审基线提交：`d18f134`。本文纯文档，未改动任何代码（授权核心零触碰）。

## 1. 评审范围与方法

覆盖的信任边界：

| 边界 | 面 | 关键文件 |
|---|---|---|
| 敏感字段静态加密 | AppSecret / Operator secret 入库 | `internal/crypto/aesgcm.go` |
| 服务间认证（数据面） | app → Sidecar/policysync 的 HMAC | `internal/auth/{signature,interceptor,credentials}.go`、`internal/controlplane/secret/resolver.go` |
| 管理面认证（gRPC/REST） | operator HMAC | `internal/auth/interceptor.go`、`internal/controlplane/restgw/auth.go`、`internal/controlplane/adminauthz/operator.go` |
| 管理面授权（多租户隔离） | 域 scope 求值、fail-close | `internal/controlplane/mgmt/authz.go` |
| 人面会话（Console BFF） | 登录、会话、CSRF、Cookie | `internal/controlplane/console/{auth,session,handler}.go` |
| 输出/错误边界 | 错误脱敏、XSS、SQL 注入、开放重定向 | `internal/controlplane/mgmt/errorsanitize.go`、console 模板层、`listpage.go` |

方法：读实际代码，逐行核对控制是否到位；对每条论断回源核实（遵循「casbin/行为论断先回源」铁律）。

## 2. 已验证的强项（无需动作）

均附源码证据，供 GA 审计留档。

**静态加密**
- AppSecret / operator secret 以 **AES-256-GCM** 加密入库，随机 nonce（`crypto/rand`），主密钥外部注入、**绝不入库**，构造时深拷贝防调用方擦除（`aesgcm.go:24`、`secret/resolver.go:30`、`operator.go:26`）。
- 主密钥长度非法即 fail-close 报错（`aesgcm.go:53`、`NewResolver`/`NewOperatorResolver`）。

**认证**
- HMAC-SHA256 签名，**常量时间比对**（`hmac.Equal`，`signature.go:43`），杜绝时序侧信道。
- 签名串绑定 `app_id \n timestamp \n method`；REST 额外绑定 **body sha256 + RequestURI**（`restgw/auth.go:40`），防跨方法/跨 body 重放。
- **±5 分钟时钟窗**限制重放窗口（`interceptor.go:35`、`restgw/auth.go:32`）。
- **枚举 Oracle 已闭合**：「app/operator 不存在」与「签名不符」统一回 `authentication failed`，细节只进服务端日志（`interceptor.go:39`、`restgw/auth.go:36`）。
- **空密钥 fail-close**：`len(secret)==0` 一律拒绝——空密钥的 HMAC 人人可算（`interceptor.go:41`）。
- `app_id`/principal 来自不可信 metadata，先过字符集校验（`validAppID`/`ValidPrincipal`），拒控制字符/换行防签名串分隔符歧义（`interceptor.go:28`）。

**授权（多租户隔离）**
- 单一真相源 `AuthorizeRule`，gRPC/REST/Console 共用；未知 method → `PermissionDenied`（默认拒，`authz.go:113`）。
- 域 scope 解析 fail-close：app 不存在/查询失败一律 `permission denied`，不泄露存在性差异（`authz.go:133`）。
- 内部错误（DB/策略加载）与合法拒绝区分记日志，但对外统一 `permission denied`（`authz.go:151`）。
- `CheckStatusWrite` 装配契约明确：**必在 `AuthorizeRule` 之后**，否则借 NotFound/FailedPrecondition 差异泄露 app 存在性（`authz.go:163`、`StatusWriteUnaryInterceptor` 注释）。
- **停用 operator 即刻锁死**：`OperatorResolver.ResolveSecret` 对 `status != 1` fail-close（`operator.go:43`）。

**人面会话**
- 会话 ID / CSRF token 均 **32 字节 CSPRNG** base64url（`session.go:40`）。
- Cookie：`HttpOnly` + `Secure`（可配）+ `SameSite=Strict`（`console/auth.go:53`）。
- 登录：secret 常量时间比对，失败统一「凭据无效」+401，无枚举 Oracle（`auth.go:44`）。
- **每个写动作过 CSRF**（常量时间比对），管线铁律 会话→CSRF→授权→status 闸→PRG（`handler.go:50`、`checkCSRF` `auth.go:102`）。

**输出/错误边界**
- 错误脱敏：`Internal`/`Unknown`（含裸 error）统一回 `internal error`，细节只进日志——防约束名/SQL/secret 上下文经直连 gRPC 外泄（`errorsanitize.go`）。
- **无 SQL 注入**：查询全参数化（`$1`…）；`ORDER BY` 经 `resolveOrder` 白名单映射（`listpage.go:12`），拒绝任意列名。
- **无 XSS**：Console 全走 `html/template` 自动转义，全库 **零** `template.HTML/JS/URL/CSS` 逃逸。
- **无开放重定向**：唯一动态重定向 `doWrite → redirectTo(r)` 是各路由开发者提供的闭包，用路由模式 `PathValue` + `url.QueryEscape` 拼**服务端相对路径**，绝不接受原始用户 URL（`handler.go:85`）。

## 3. 发现（按严重度）

### F-1 [低 / 设计语义]　应用「停用」是管理面冻结，非运行时吊销

**现象**：`secret.Resolver.ResolveSecret`（policysync 数据面，`secret/resolver.go:36`）按 `app_key` 取密文解密，**不校验 `application.status`**；而 operator 侧 `OperatorResolver` 对 `status != 1` fail-close（`operator.go:43`）。两者不对称。

**后果**：`SetApplicationStatus` 停用一个应用后——
- 管理面业务策略写被 `StatusWriteUnaryInterceptor`/`CheckStatusWrite` 拦（符合设计，`authz.go:161`）；
- 但**该应用的 sidecar 仍能通过 HMAC 认证 policysync、继续拉取策略快照**（认证不看 status）。

即：停用 **不是** 凭据/运行时吊销开关。真正的运行时吊销路径是 **`RotateApplicationSecret`**——它生成新 secret、重加密 `UPDATE application SET app_secret_enc=$1`（`admin_ops.go:419`），旧 HMAC 即刻失效。

**这可能是有意设计**（停用=管理冻结，轮换=凭据吊销），但对安全事件响应有直接影响，须在 GA 运维手册讲清：

> **应用凭据泄露时的吊销动作是 `RotateApplicationSecret`（轮换密钥），不是停用。** 停用只冻结管理面改动，不切断已泄露凭据的运行时访问。

**可选加固**（需用户 greenlight，因触授权/认证路径，违「零触碰授权核心」须显式批准）：在 policysync 的 `secret.Resolver.ResolveSecret` 也校验 `application.status`，使停用兼具运行时锁出（与 operator 侧对称）。取舍：会让「停用」语义从纯管理冻结变为运行时闸，需确认产品意图。

### F-2 [低 / 已知取舍]　HMAC 重放窗口 5 分钟，无 nonce 缓存

**现象**：认证只校验时间戳落在 ±5min（`MaxClockSkew`），无服务端 nonce/已用签名缓存。窗口内**同一签名可重放**。

**缓解现状**：签名绑定 method+timestamp（REST 再绑 body+URI），且绝大多数管理写具幂等性；数据面 Check 为只读。故实际风险面是「窗口内重放一次某个改动型调用」。

**GA 建议**：文档标注此为已知取舍；若要闭合，可加基于 Redis `SETNX <sig> EX 300` 的一次性 nonce 校验（低成本，代价是认证路径引入 Redis 依赖与一次写）。**非阻断**。

### F-3 [信息]　Console 登录无速率限制 / 失败锁定

Operator secret 是高熵随机 token（非用户自选口令），在线暴破不可行；常量时间比对 + 统一错误已挡枚举。**当前非真实漏洞**。GA 纵深防御可选：N 次失败后临时锁定 + 结构化告警。

### F-4 [信息]　会话滑动 TTL，无绝对寿命上限

`RedisStore.Get` 每次访问续期空闲 TTL（`session.go:85`），活跃会话不会因绝对时长到期。管理 BFF 常见做法。GA 可选加固：以已存的 `Session.CreatedAt` 校验绝对上限（如 12h）强制重登。低优先级。

## 4. 建议的后续动作

| 项 | 类型 | 需用户决策？ |
|---|---|---|
| 在运维手册写明 F-1「轮换即吊销、停用非吊销」 | 纯文档 | 否（可自主） |
| F-1 让 policysync 校验 app status（运行时对称锁出） | 触认证路径 | **是**（违零触碰授权核心，须 greenlight + 确认产品语义） |
| F-2 nonce 缓存闭合重放窗口 | 触认证路径 | 是 |
| F-3 登录速率限制 / F-4 会话绝对寿命 | 纵深防御 | 是（优先级低） |
| 动态渗透测试（本文只做源码审查） | 外部/工具 | 是（供应商/工具选型） |

## 5. 结论

司域的信任边界在 M5.2 安全硬化后已达**强**：静态加密、常量时间认证、fail-close 授权、多租户隔离、CSRF/安全 Cookie、错误脱敏、无 SQL 注入/XSS/开放重定向——逐条经源码核实。本轮源码审查**未发现可利用漏洞**。唯一需即时处置的是 **F-1 的语义澄清**（纯文档即可闭合运维认知缺口）；其余为可选纵深防御，均非 GA 阻断项。

动态渗透测试建议作为独立 GA 工序（需工具/供应商选型），本文的源码审查是其前置基线。
