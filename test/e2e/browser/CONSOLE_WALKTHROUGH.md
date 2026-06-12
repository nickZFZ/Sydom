# 司域 Admin Console · 人用走查（Human Walkthrough）

本文用截图记录一次**真人在浏览器里**对司域（Sydom）**管理控制台**（SP3 · 服务端 BFF）的完整走查，
逐屏印证控制台的四件事：**可视化的全资源管理**、**一次性凭据**、**降级无枚举**、**enforce-on-access 的跨域/系统域闸**。

> 控制台是控制面进程的**第 4 个监听器**（与 gRPC AdminService / PolicySync / REST 网关并列），
> 用 `html/template` 服务端渲染，进程内**直调**同一个 `AdminServer`，与 REST/gRPC 共用同一套
> `AuthorizeRule` / `CheckStatusWrite` / `ruleTable`——**没有第二份鉴权真相**。
> 浏览器只跑一段**渐进增强**的 JS（数据策略可视化构建器），其余页面零 JS 也能用。

## 如何复现

控制台默认**不**随 demo compose 起；本走查用「compose 起 PG+Redis，本机 `go run` 起控制面（开 console）」：

```bash
# 1) 起 PG + Redis（Redis 映射到 16379 避让本机已占用的 6379）并迁移
cd deploy
REDIS_HOST_PORT=16379 docker compose --env-file .env.demo up -d postgres redis
REDIS_HOST_PORT=16379 docker compose --env-file .env.demo up migrate   # 一次性，跑完退出

# 2) 写一份开 console 的本地配置（loopback http，故 console_cookie_insecure: true）
cat > /tmp/console.yaml <<'YAML'
database_dsn: "postgres://sydom:sydom@localhost:5432/sydom?sslmode=disable"
redis_addr: "localhost:16379"
admin_addr: ":8081"
sync_addr: ":8082"
console_addr: ":8083"
console_cookie_insecure: true
root_principal: "root@sydom"
YAML

# 3) 起控制面（启动即 EnsureRootOperator 播种 root@sydom / demo-root-secret）
SYDOM_MASTER_KEY=KioqKioqKioqKioqKioqKioqKioqKioqKioqKioqKio= \
SYDOM_ROOT_SECRET=demo-root-secret \
  go run ./cmd/sydom-controlplane -config /tmp/console.yaml

# 浏览器打开 http://localhost:8083 ，用 root@sydom / demo-root-secret 登录
```

> ⚠️ `.env.demo` 与上面的密钥都是 **demo 占位值**（master key 解码为 32 个 `0x2a`），生产务必另行注入。
> `console_cookie_insecure: true` 仅因本走查走明文 http loopback；生产默认 `false`，cookie 带 `Secure`。

---

## 走查记录

### ① 登录页 —— 司域只做授权，认证交给上游

![login](screens/console-01-login.png)

未登录访问任意页一律 `303 → /login`。登录表单只要 **Principal + Secret**（operator 的 HMAC 凭据）。
登录失败统一回「凭据无效」——**无枚举 oracle**，不区分「无此 principal」与「密钥错」。
会话存 Redis，**只含 principal / csrf / 创建时间，绝不含 secret**。

> 旁注：`GET /favicon.ico` 为 **404**（无该路由），非 bug。

### ② root 登录后 —— 应用区（超管看全量）

![dashboard-root](screens/console-02-dashboard-root.png)

root 绑定 `super-admin`（域 `*`），对系统域 `ListApplications` 有权，故仪表盘渲染**完整应用列表**
（此刻为空，下一步新建）。顶栏两个入口：**应用** / **系统**，右侧 **登出**。

### ③ 新建应用 —— 一次性 App Secret（不 PRG、不再次展示）

![app-created](screens/console-03-app-created-secret.png)

填租户/Domain/应用名/App Key → 提交，落地**「应用已创建」页**（注意 URL 停在 `POST` 的 `/apps`，
**刻意不 PRG**）：明文 **App Secret 仅此一屏**，配「⚠️ 此密钥仅显示这一次，离开本页后无法再次查看」。
secret **不写会话、不进日志、不可二次查看**——离开即不可逆。

### ④ 应用工作台 —— 角色 / 权限点 / 授权 / 继承 / 用户 / 数据策略

![workbench-roles](screens/console-04-workbench-roles.png)

左栏二级导航把一个应用的六类资源收拢到 `App #1` 下。每页都是「内联表单 + 列表」同一范式：
表单建一条（PRG `303` 回列表），列表每行可删。本走查依次建了 `manager` 角色、`order:delete`
权限点、把权限授予角色、绑定用户 `alice`。表单里的 `app_id` 一律以 **URL path 为权威**，
不信任表单字段（跨应用越权无从构造）。

### ⑤ 数据策略 · 可视化构建器（渐进增强）

![datapolicy-builder](screens/console-05-datapolicy-builder.png)

数据策略的 `condition` 是结构化 JSON。控制台提供**可视化构建器**：选「组合 AND/OR」、逐条加
「字段 / 运算符（eq/ne/gt/lt/in/contains）/ 值」。这是全项目**唯一**的 JS，且**纯前端、零网络**——
JS 没加载时退化为一个**原始 JSON 文本域基线**，照样能提交。

### ⑥ 数据策略 · 专业模式（看构建器吐出的原始 JSON）

![datapolicy-json](screens/console-06-datapolicy-json.png)

点「专业模式（原始 JSON）」即把构建器序列化进文本域：
`{"op":"and","children":[{"field":"dept","op":"eq","value":"$user.department"}]}`。
保存后控制台**原样透传** `condition`（不在前端预解析/改写），由控制面以 `$5::jsonb` 落库做 canonical 化，
列表回显规范 JSON。可视化与原始 JSON **双向可切**，高级用户始终能手写。

### ⑦ 系统区 · 新建 operator —— 一次性 Secret（同应用 secret 范式）

![operator-created](screens/console-07-operator-created-secret.png)

系统区管 operator 与 admin-role（域 `*`）。新建 operator 同样落地**一次性 Secret 页**
（不 PRG、不日志、不落盘）。随后建 admin-role `app1-ops`、给它授**应用 1 域**（domain=`1`）的
`role:read`、把 operator `ops@order` **绑定**到该角色——**全程经控制台写接口**造出一个
「**仅 app #1 域有权**」的受限身份。

### ⑧ 系统区 · operator 列表

![operators-list](screens/console-08-operators-list.png)

root（超管）在系统区看到全量 operator：`root@sydom` 与刚建的 `ops@order`，每行可切状态 / 绑定角色。
下面切到该受限身份登录，逐屏看权限边界。

### ⑨ 受限 operator 仪表盘 —— **降级无枚举**

![dashboard-degraded](screens/console-09-dashboard-degraded.png)

用 `ops@order` 登录后访问 `/`：它对系统域 `ListApplications` **无权**，仪表盘**降级**为
「你没有列出全部应用的权限。直接输入要管理的 App ID：」+ 一个 App ID 直达框——
**不渲染任何应用列表**。对比第②屏 root 的全量视图：降级页**不泄露应用是否存在、有多少、叫什么**。
（它仍可凭已知的 App ID 直达 `/apps/1/...` 管理自己有权的应用。）

### ⑩ 受限 operator 撞系统区 —— **403，不降级**

![system-403](screens/console-10-system-403.png)

同一受限身份点「系统」（`/operators`，系统域 `*`）→ **PermissionDenied**。
注意与仪表盘的**区别**：仪表盘**降级**（业务首页要可用），系统区**直接 403 不降级**
（`renderGRPCError` → `PermissionDenied` 映射 HTTP **403**）——enforce-on-access、导航不预过滤：
能不能进，进了才由服务端判，判拒就 403。

---

## 命令行佐证（与截图同一台运行实例）

截图之外，用 `curl` 对同一实例逐条核对了**状态码级**的安全行为：

| 场景 | 请求 | 实测 |
|---|---|---|
| 受限身份首页降级 | `GET /`（ops@order 会话） | `200`（降级页，无应用列表） |
| 受限身份撞系统域 | `GET /operators`（ops@order 会话） | **`403`** |
| 写动作缺 CSRF | `POST /apps/1/roles`（root 会话，无 `csrf_token`） | **`403`**（拒绝） |
| 写动作带 CSRF | `POST /apps/1/roles`（root 会话，带 `csrf_token`） | `303`（成功 PRG） |
| 登出 | `POST /logout`（带会话 + cookie） | `303 → /login` |
| **登出后旧 cookie** | `GET /`（复用登出前的 cookie） | **`303 → /login`**（会话已在 Redis 销毁） |

最后一行是关键：登出**服务端删除 Redis 会话**，旧 cookie 立即失效——会话失效是服务端权威，不靠前端清 cookie。

---

## 一句话总结

- **可视化管理**：一个 root 身份，从建应用 → 角色/权限/授权/绑定 → 数据策略，六类资源全程点点点（④⑤⑥）。
- **一次性凭据**：应用与 operator 的 secret 各只显示一屏，不 PRG、不日志、不落盘（③⑦）。
- **降级无枚举**：无权列全量时，仪表盘退化为直达框而非应用列表，不泄露存在性（⑨ vs ②）。
- **enforce-on-access**：导航不预过滤；跨域/系统域由服务端判，判拒即 `403`，系统区不降级（⑩）。
- **会话与 CSRF**：写动作必带 CSRF（缺即 `403`）；登出销毁服务端会话，旧 cookie 立即失效（命令行佐证）。

> 备注（留给收尾整体评审）：`POST /logout` **刻意不校验 CSRF**——它只销毁请求者自己的会话，
> 配合 `SameSite=Strict` 的会话 cookie，CSRF 登出对攻击者无收益，且让「登出」始终可用（安全阀不应被 CSRF 反噬）。
> 所有**状态变更类**写动作（`doWrite` 管线）一律强制 CSRF。此豁免是否保留，交由整体评审显式裁定。
