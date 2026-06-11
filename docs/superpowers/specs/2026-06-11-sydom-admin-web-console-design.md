# SP3 司域 Admin Web Console 详细设计

**日期**：2026-06-11
**子项目**：接入面（access plane）大特性第 3 子项目（SP1 读面 → SP2 REST 网关 → **SP3 Web Console**）
**状态**：设计已确认，待写实现计划

---

## 1. 概述与目标

给**人类运维者**一个浏览器控制台，管理司域的应用 / 角色 / 权限点 / 授权 / 角色继承 / 用户绑定 / 数据策略，以及 system 域的 operator 与管理角色。

Console 是**服务端 BFF**（`html/template`），**折进控制面进程**作为第 4 个监听器（可选 `console_addr`，空则不起，与 SP2 REST 同款向后兼容）。它**复用 SP2 已导出的鉴权尾链**（`mgmt.AuthorizeRule` / `mgmt.CheckStatusWrite` / 直调 `*AdminServer`），只把前门从 REST-HMAC 换成 **session cookie**。

**核心目标**：人面可视化管理 + 与 gRPC/REST 三方零策略漂移 + fail-close 全贯穿 + operator 凭据绝不落盘、绝不入会话。

## 2. 范围

**本子项目内（full parity，覆盖全部 27 个 AdminService RPC）**：
- 9 个读：ListApplications + ListRoles/ListPermissions/ListGrants/ListRoleInheritances/ListUserBindings/ListDataPolicies（6 app 域）+ ListOperators/ListAdminRoles（2 system 域）。
- 18 个写：CreateApplication/SetApplicationStatus（2 应用管理）；CreateRole/DeleteRole/UpsertPermission/GrantPermission/RevokePermission/AddRoleInheritance/RemoveRoleInheritance/BindUserRole/UnbindUserRole/UpsertDataPolicy/DeleteDataPolicy（11 app 业务写）；CreateOperator/SetOperatorStatus/CreateAdminRole/GrantAdminRole/BindOperatorRole（5 system 写）。
- 登录 / 登出 / 会话（Redis）/ CSRF / 友好错误页。
- 数据策略 condition 编辑：默认可视化构建器 + 「专业模式」原始 JSON。

**非目标（YAGNI / 后续）**：
- TLS（属部署层，反代/loopback 终结；与 SP2 同口径）。
- 分页 / 高级检索 / 排序 / 批量操作（首版用简单筛选 + 全量列出，沿用 List RPC 现状）。
- 多语言 i18n、主题切换、富前端框架、构建步骤（除数据策略页一小段 vanilla JS 外，全站零 JS）。
- 审计日志查看页、操作历史回放（控制面 audit 表已存，UI 留后续）。
- 控制面多副本会话亲和性以外的 HA 细节（Redis 会话已天然支持多副本）。

## 3. 决策记录

| # | 决策 | 取值 | 理由 |
|---|---|---|---|
| D1 | 能力范围 | **完整读 + 全部写（full parity）** | 用户拍板：每个写 RPC 都要有浏览器表单。 |
| D2 | BFF↔后端 | **进程内直调 + 会话只存 principal** | 复用 SP2 同一 AdminServer 实例与导出鉴权函数；secret 登录验毕即弃，**绝不入会话**，安全面最小。 |
| D3 | 会话存储 | **Redis 支撑** | 复用控制面现有 `rdb`；跨控制面重启/多副本存活，无需 sticky session；会话状态仅 `{principal,csrf,...}`，**无 secret**。 |
| D4 | 测试深度 | **httptest 骨干 + 聚焦 Playwright 走查** | TDD hermetic 主层 + 真浏览器端到端证据（照 Demo 先例）。 |
| D5 | 信息架构 | **2 区导航 + 应用工作台 + 4 种统一页型** | 修正「页面图杂乱」：按运维者心智组织，不按 RPC 平铺。 |
| D6 | 应用工作台组织 | **B · 左侧二级导航**（全局「应用/系统」切换提到顶栏，左栏只放当前应用上下文，始终两列） | 资源多/名字长更耐扩展，层级显式。 |
| D7 | 数据策略 condition 编辑 | **可视化构建器（默认）+ 专业模式原始 JSON** | 原始 JSON 为 canonical / 无 JS 基线（始终服务端渲染、fail-close 校验），构建器为渐进增强。 |

## 4. 架构

新包 `internal/controlplane/console`，镜像 `restgw` 形态。

```
Run(ctx, cfg, adminLis, syncLis, restLis, consoleLis, logger)
  ├─ adminSrv := mgmt.NewAdminServer(...)        // 同一实例
  ├─ enforcer := adminauthz.NewEnforcer(db)      // 同一实例
  ├─ operatorResolver := adminauthz.NewOperatorResolver(...) // 登录验证用
  ├─ rdb := redis.NewClient(...)                 // 同一实例
  ├─ grpc admin / sync / relay / dispatch / (rest)  // 既有
  └─ console（若 consoleLis != nil）：
        sessions := console.NewRedisStore(rdb, cfg.ConsoleSessionTTL)
        consoleSrv := &http.Server{Handler: console.NewHandler(
            adminSrv, operatorResolver, enforcer, db, sessions, logger)}
        优雅 Shutdown(5s)
```

`ruleTable` 仍是**唯一鉴权真相源**（不导出），Console 经 `fullMethod` 字符串间接引用，与 gRPC/REST 三方零漂移。

## 5. 认证 / 会话 / CSRF

**登录**（`GET /login` 渲染表单，`POST /login` 验证）：
1. 表单提交 `principal` + `secret`。
2. `secret, err := operatorResolver.ResolveSecret(ctx, principal)`——`OperatorResolver` 对**未知 / 停用 / 解密失败一律 fail-close error**（已核实 `operator.go:31`），故天然拒绝停用 operator。
3. `subtle.ConstantTimeCompare([]byte(输入secret), secret)`——secret **当密码用**（人面控制台唯一合理 UX）。
4. 任一失败 → **通用「凭据无效」**（不区分未知/停用/密码错，杜绝枚举 oracle）。
5. 验过 → 生成会话；**输入的 secret 立即丢弃，绝不入会话**。

**会话存储（Redis）**：
- key `console:sess:<id>`，`<id>` = `crypto/rand` 32 字节 base64url（256-bit）。
- value JSON `{principal, csrf, created_at, last_seen}`——**绝不含 secret**。
- 空闲 TTL（默认 30 min），每次认证请求 `EXPIRE` 续期。
- `NewRedisStore(rdb, ttl)`：`Create(ctx, principal) (id string, csrf string, err)` / `Get(ctx, id) (Session, err)` / `Delete(ctx, id)` / 续期。

**Cookie**：`sydom_console_session`，值仅 session ID，属性 **HttpOnly + SameSite=Strict + Secure + Path=/**（`Secure` 默认开，dev 可经配置放开以走 loopback http）。

**CSRF**（同步器令牌）：会话内存 `csrf` 随机串；每个改状态表单嵌隐藏字段 `csrf_token`；所有 POST 端 `ConstantTimeCompare` 校验，不符 → 403。SameSite=Strict 是第一道，CSRF token 是纵深第二道。

**登出**（`POST /logout`）：删 Redis 会话 + 过期 cookie。

**TLS**：属部署层（反代/loopback），本子项目不做（与 SP2 YAGNI 一致）。

## 6. 信息架构与页面

### 6.1 整体骨架（App Shell）
- **顶栏**：品牌 + 全局区切换「应用 / 系统」+ 当前 operator + 登出。
- **左栏（上下文）**：在应用区时显示当前应用上下文（app 名 + 状态 + 7 个资源链接）；在系统区时显示 Operators / 管理角色。始终两列，不挤。
- **内容区**：当前页（列表 / 工作台 / 表单 / 认证）。

### 6.2 四种统一页型
1. **列表页**：标题 + 主操作按钮 + 简单筛选 + 数据表格 + 行内操作。
2. **工作台详情页**：面包屑 + 头部（名+状态徽章+操作）+ 左侧二级导航 + 选中资源内容。
3. **表单页 / 内联区**：建 / 改。
4. **认证页**：登录（居中卡片）、错误 / 403（居中信息）。

### 6.3 路由表（27 RPC 全覆盖 + 认证 + 表单页）

| 区 | 方法 路径 | RPC / 用途 | 页型 |
|---|---|---|---|
| 认证 | `GET /login` | 登录表单 | 认证 |
| | `POST /login` | 验证 + 建会话 | — |
| | `POST /logout` | 销会话 | — |
| 应用 | `GET /` | ListApplications（**优雅降级**，见 6.4） | 列表 |
| | `GET /apps/new` · `POST /apps` | CreateApplication | 表单 |
| | `GET /apps/{appId}` | 应用总览（默认重定向到 …/roles） | 工作台 |
| | `POST /apps/{appId}/status` | SetApplicationStatus | — |
| 角色 | `GET /apps/{appId}/roles` | ListRoles | 工作台 |
| | `POST /apps/{appId}/roles` | CreateRole | — |
| | `POST /apps/{appId}/roles/{roleId}/delete` | DeleteRole | — |
| 权限点 | `GET /apps/{appId}/permissions` | ListPermissions | 工作台 |
| | `POST /apps/{appId}/permissions` | UpsertPermission | — |
| 授权 | `GET /apps/{appId}/grants` | ListGrants | 工作台 |
| | `POST /apps/{appId}/grants` | GrantPermission | — |
| | `POST /apps/{appId}/grants/revoke` | RevokePermission | — |
| 继承 | `GET /apps/{appId}/inheritances` | ListRoleInheritances | 工作台 |
| | `POST /apps/{appId}/inheritances` | AddRoleInheritance | — |
| | `POST /apps/{appId}/inheritances/remove` | RemoveRoleInheritance | — |
| 用户 | `GET /apps/{appId}/bindings` | ListUserBindings | 工作台 |
| | `POST /apps/{appId}/bindings` | BindUserRole | — |
| | `POST /apps/{appId}/bindings/unbind` | UnbindUserRole | — |
| 数据策略 | `GET /apps/{appId}/data-policies` | ListDataPolicies | 工作台 |
| | `POST /apps/{appId}/data-policies` | UpsertDataPolicy | — |
| | `POST /apps/{appId}/data-policies/delete` | DeleteDataPolicy | — |
| Operator | `GET /operators` | ListOperators | 列表 |
| | `GET /operators/new` · `POST /operators` | CreateOperator | 表单 |
| | `POST /operators/{principal}/status` | SetOperatorStatus | — |
| 管理角色 | `GET /admin-roles` | ListAdminRoles | 列表 |
| | `POST /admin-roles` | CreateAdminRole | — |
| | `POST /admin-roles/grant` | GrantAdminRole | — |
| | `POST /operators/{principal}/roles` | BindOperatorRole | — |

**path 权威覆写**：`{appId}` 等路径参数权威，覆写任何表单同名字段（沿用 SP2 防越权铁律）。表单页的下拉（选角色/权限填授权等）由对应域 List 读填充（读也受 authz）。

### 6.4 仪表盘优雅降级（无枚举）
`ListApplications` 是 system 域（`*`），仅超管能列全部应用；普通 per-app operator 会被 `AuthorizeRule` 拒。仪表盘据此降级：尝试 `ListApplications`，成功→渲染应用列表；返 `PermissionDenied`→渲染「按 app ID 直达管理」输入框——**不泄露哪些 app 存在**（无枚举 oracle）。

### 6.5 导航不预过滤
系统区菜单一律显示，访问时由 `AuthorizeRule` 把关，无权→**友好 403**（不按超管身份隐藏菜单——enforce-on-access 为唯一权威闸，与 Demo 「bob 见按钮、点了得 403」一致）。

### 6.6 数据策略 condition 编辑（D7）
- 表单始终提交单个 `condition` 字段（JSON 字符串）。
- **专业模式 / 无 JS 基线**：服务端始终渲染原始 JSON 文本框，完全可用；提交即送后端 `dataperm` 校验，非法 → fail-close 报错回显。
- **可视化构建器**：仅此页一小段**自包含 vanilla JS**（无框架、无构建、无网络），做 AND/OR + 字段/算子/值逐行编辑 ↔ JSON 互转 + 模式切换显隐。关 JS 自动落到专业模式。两模式产出同一 `condition` JSON → 服务端解码路径完全一致。

## 7. 请求管线

**写动作（POST）**：取 session→principal（无则 302 `/login`）→ **校验 CSRF** → 解析表单建 proto（path 权威覆写）→ `mgmt.AuthorizeRule(ctx, enf, fullMethod, principal, msg)` → `mgmt.CheckStatusWrite(ctx, db, fullMethod, msg)`（status 闸，**必在授权后**）→ `rt.invoke(ctx, adminSrv, msg)` → 成功 **302 重定向（PRG）+ flash**；失败回渲表单带错误。

**读页面（GET）**：取 session→principal → 建 List 请求 → `mgmt.AuthorizeRule` → 直调 `ListXxx` → `html/template` 渲染。

与 SP2 REST 的 7 步管线同核，仅前门「读 body→REST-HMAC 认证」换成「会话查 Redis→principal + CSRF」。

## 8. 渲染与静态资源

`html/template`（**自动转义 → 默认抗 XSS**，仓内 `examples/orderservice` 已有先例）。`embed.FS` 内嵌 `templates/*.html`（base 布局 + 每资源页）与 `static/app.css`（一份极简 CSS）+ `static/datapolicy.js`（唯一 JS）。纯服务端表单 + 整页刷新 + PRG 重定向。

## 9. 安全不变量（fail-close 闭环）

1. **会话永不含 secret**——D2 根本红利；secret 仅登录瞬间在内存，验毕即弃。
2. **同一鉴权核心 + 同一运行时实例**——Console/REST/gRPC 共用 `AuthorizeRule`/`CheckStatusWrite`/`ruleTable`，物理上不可能策略漂移。
3. **管线顺序认证→授权→status 闸**——status 闸必在授权后（防借 NotFound/FailedPrecondition 泄露 app 存在性）。
4. **path 权威覆写**表单 app_id；跨 app/越权一律 `AuthorizeRule` 拒 → 友好 403。
5. **登录无枚举 oracle**（通用「凭据无效」）+ **仪表盘无枚举**（降级不泄露 app 存在性）。
6. **CSRF 全 POST 覆盖** + **会话存储无 secret** + body 上限。
7. **错误脱敏**——`Internal/Unknown`→500 通用文案、细节仅进 slog（沿用 restgw `errors.go` 口径）。

## 10. 错误处理

复用 SP2 `restgw/errors.go` 的 code→HTTP 映射思路，但渲染**友好 HTML 错误页**而非 JSON：`PermissionDenied`→403 页；`Unauthenticated`/无会话→302 `/login`；`NotFound`/`FailedPrecondition`→对应友好提示；`InvalidArgument`→回渲表单带字段错误；`Internal`/`Unknown`→500 通用页 + 细节仅进 slog。

## 11. 测试策略

**httptest 骨干**（testcontainers PG+Redis，TDD）：
- 辅助：`login(t, principal, secret)`（POST /login 捕获会话 cookie）、`csrfOf(html)`（从渲染表单抽 token）、`get/post` 带 cookie+csrf。
- 逐域驱动「读 + 至少一个写」；断言渲染 HTML 含预期行 + **后端 DB 实效**。
- 安全矩阵：无会话→302；跨 app/越权→403；登录错密码→**通用失败**（不泄露存在性）；登出后会话失效；**CSRF 不符/缺失→403**；仪表盘对受限 operator 降级无枚举；数据策略非法 condition→fail-close 报错。

**Playwright 走查**（`WALKTHROUGH.md` + 截图，照 Demo 先例）：超管登录 → 看应用 → 进 app → 建角色 → 授权 → 绑用户 → 数据策略（可视化建一条 + 专业模式查 JSON）→ 换受限 operator 撞 403 → 登出。聚焦，不穷举。

## 12. 包结构与接线

```
internal/controlplane/console/
  handler.go      NewHandler + mux 注册（方法感知路由）
  session.go      Redis 会话存储 Create/Get/Delete/续期
  auth.go         登录/登出 handler + requireSession 中间件 + CSRF 生成/校验
  routes.go       路由表（pattern→fullMethod→form-decode→invoke→render）
  render.go       模板渲染 + 错误页 + flash/PRG
  errors.go       code→HTTP + Internal 脱敏（复用 restgw 口径）
  forms.go        表单 → proto 解码（path 权威覆写 + condition 透传）
  templates/*.html  base 布局 + 每资源页
  static/app.css
  static/datapolicy.js   唯一 JS（构建器↔JSON）
```

**接线变更**：
- `app/run.go`：加可选 console 监听器（与 REST 同款 launch + 优雅 Shutdown，`errCh` 容量 +1）；`Run` 签名加 `consoleLis net.Listener` 参数。
- `app/config.go`：加 `ConsoleAddr`（`console_addr`，非必填）+ 可选 `ConsoleSessionTTL`（`console_session_ttl`，默认 30m）+ 可选 `ConsoleCookieInsecure`（dev）；复用 `RedisAddr`。
- `app/Main`：仅当 `cfg.ConsoleAddr != ""` 才建 `consoleLis`。

> ⚠️ **跨包回归预警**：`Run` 签名加 `consoleLis` → 必触发 `test/e2e/e2e_test.go` 旧调用断裂（SP2 任务 8 同款，`go build` 不编译测试文件、漏到 `go vet ./...` 才暴露）。实现计划须显式纳入：改签名后 **`go vet ./...` 全仓兜底** + e2e 传 `nil consoleLis`。

## 13. 配置（新增）

```yaml
console_addr: ":8082"          # 空则不起 Console（向后兼容）
console_session_ttl: 30m       # 可选，默认 30m
console_cookie_insecure: false # 可选，dev 走 loopback http 时置 true
# redis_addr 复用既有
```

## 14. 移交 / 后续

- 分页 / 排序 / 高级检索（List RPC 增强后再补 UI）。
- 审计日志查看页（控制面 audit 表已存）。
- i18n / 主题 / 富前端。
- TLS / 反代部署样例（属部署层）。
- 其它语言/移动端管理端。
