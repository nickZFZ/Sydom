# 司域 AdminService REST 网关（SP2）详细设计

> 接入面大特性 3 子项目之 SP2。前置：SP1 读面（8 只读 List RPC）已并入 main `d8db7d1`。后续：SP3 Web Console（人用 html/template BFF）。

## 1. 目标与范围

把已有的 gRPC `AdminService`（19 写/管理 RPC + SP1 新增 8 读 RPC = 共 27 RPC）映射为**对外程序化 REST/JSON 接口**，折进控制面进程，开新 HTTP 端口。供外部程序直接以 HTTP 管理 app/角色/权限/授权/数据策略，而不仅经 gRPC SDK。

**本子项目只做对外程序化 REST。** 人用 Web Console（服务端 BFF、session、登录页、HTML 渲染）是 SP3，不在此。

### 1.1 已定架构决策（接入面大特性 4 项 + SP2 4 项 AskUserQuestion 拍板）

接入面层面：接入对象=人+程序；读能力=完整读面（SP1 已交付）；部署=折进控制面进程新 HTTP 端口；REST=手写 net/http（非 grpc-gateway codegen）。

SP2 层面：① 派发/鉴权=**直调 `*AdminServer` + 共享授权核心**（非 loopback gRPC、非每 handler 内联）；② URL=**资源式 REST 路径**；③ JSON↔proto=**protojson 官方编解码**；④ 首批范围=**全量 27 RPC**。

## 2. 架构与组件边界

新包 **`internal/controlplane/restgw`**，职责单一：HTTP ↔ gRPC-service 适配。数据流：

```
程序 ──HTTP/JSON──▶ restgw.Handler (net/http ServeMux, Go1.22+ 方法感知路由)
                       1. http.MaxBytesReader 读 body → 缓存 []byte + sha256
                       2. REST-HMAC 认证 → principal（复用 OperatorResolver）
                       3. decode: path/query/body → proto Request（path 权威覆写）
                       4. 共享授权核心 AuthorizeRule + CheckStatusWrite（复用 ruleTable）
                       5. invoke 闭包直调 *mgmt.AdminServer.Method(ctx, req)  ← 零网络跳
                       6. protojson 编码 resp / 错误码 → HTTP status
                       ▼
                  既有 *mgmt.AdminServer（gRPC 与 REST 共用同一实例）
```

**单元边界**：`restgw` 依赖 `mgmt`（`*AdminServer` + 导出的授权核心 + ruleTable 经 FullMethod 间接引用）、`adminauthz.Enforcer`、`auth`（REST-HMAC + OperatorResolver）、`adminv1`（proto 类型）、`database/sql`。它做什么：把一个 HTTP 请求翻成「认证→鉴权→调用 service→编码」。它依赖什么：上述既有组件，全部注入。别人无需看内部即可理解：一张静态路由表 + 一条固定中间件管线。

### 2.1 进程装配

`internal/controlplane/app`（`config.go` + `run.go`）新增第 3 个监听器：

- `config.go`：`Config` 加 `RESTAddr string`（YAML `rest_addr`，如 `:8082`）。空则不起 REST（向后兼容）；非空则校验。
- `run.go`：`Run` 已构造并持有 `db / *adminauthz.Enforcer / OperatorResolver / *mgmt.AdminServer`（mgmt gRPC 装配产物）。REST 仅再包一层 `http.Server{Handler: restgw.NewHandler(...)}` 复用它们。新增一个 goroutine `httpSrv.Serve(restLis)`，`<-ctx.Done()` → `httpSrv.Shutdown(shutdownCtx)` 优雅关闭，纳入既有 `sync.WaitGroup` + cascade-cancel（任一协程退出 defer cancel）。`Run` 签名加注入监听器 `restLis net.Listener`（测试用 `127.0.0.1:0`，main 用配置地址），镜像既有 admin/sync 监听器注入。

**对 mgmt/gRPC 零对外行为改动**：仅把 authz/status 两段判定抽成可复用导出函数，gRPC 拦截器改调它们（语义不变，既有 mgmt 测试守门）。

## 3. 资源式路由表（27 RPC）

每条路由静态登记 `{httpMethod, pattern, grpcFullMethod, decode, invoke}`。`grpcFullMethod` 即 `ruleTable` 键，使授权核心与 gRPC 端逐字节复用同一条 `rpcRule`。路由用 Go 1.22+ `http.ServeMux` 方法感知模式（`"POST /v1/apps/{app_id}/roles"`）+ `r.PathValue(...)`，无第三方路由库。

### 3.1 App 域（`/v1/apps/{app_id}/...`；app_id 路径 → proto `AppId`，授权域 = app_id）

| Method + Path | → RPC | 字段来源 |
|---|---|---|
| `GET    /v1/apps/{app_id}/roles` | ListRoles | path |
| `POST   /v1/apps/{app_id}/roles` | CreateRole | path + body{code,name} |
| `DELETE /v1/apps/{app_id}/roles/{role_id}` | DeleteRole | path |
| `GET    /v1/apps/{app_id}/permissions` | ListPermissions | path |
| `PUT    /v1/apps/{app_id}/permissions/{code}` | UpsertPermission | path(code 须无 `/`) + body{resource,action,ptype,name} |
| `GET    /v1/apps/{app_id}/grants` | ListGrants | path + query `role_id`（可选，缺=0=全部） |
| `POST   /v1/apps/{app_id}/roles/{role_id}/grants` | GrantPermission | path + body{permission_id,eft} |
| `DELETE /v1/apps/{app_id}/roles/{role_id}/grants/{permission_id}` | RevokePermission | path |
| `GET    /v1/apps/{app_id}/role-inheritances` | ListRoleInheritances | path |
| `POST   /v1/apps/{app_id}/roles/{child_role_id}/parents` | AddRoleInheritance | path + body{parent_role_id} |
| `DELETE /v1/apps/{app_id}/roles/{child_role_id}/parents/{parent_role_id}` | RemoveRoleInheritance | path |
| `GET    /v1/apps/{app_id}/user-bindings` | ListUserBindings | path + query `user_id`（可选，缺=""=全部） |
| `POST   /v1/apps/{app_id}/users/{user_id}/roles` | BindUserRole | path + body{role_id} |
| `DELETE /v1/apps/{app_id}/users/{user_id}/roles/{role_id}` | UnbindUserRole | path |
| `GET    /v1/apps/{app_id}/data-policies` | ListDataPolicies | path + query `resource`（可选，缺=""=全部） |
| `POST   /v1/apps/{app_id}/data-policies` | UpsertDataPolicy（id=0 新增） | path + body{subject_type,subject_id,resource,condition,effect} |
| `PUT    /v1/apps/{app_id}/data-policies/{id}` | UpsertDataPolicy（id 来自路径） | path + body{subject_type,subject_id,resource,condition,effect} |
| `DELETE /v1/apps/{app_id}/data-policies/{id}` | DeleteDataPolicy | path |

### 3.2 应用管理（顶层）

| Method + Path | → RPC | 字段来源 |
|---|---|---|
| `GET  /v1/applications` | ListApplications | — |
| `POST /v1/applications` | CreateApplication | body{tenant_name,domain,name,app_key} · **响应含一次性 app_secret** |
| `PUT  /v1/applications/{app_id}/status` | SetApplicationStatus | path + body{status} · 授权域=app_id（system=false） |

### 3.3 管理员 / admin-role 域（system 域 `*`，super-admin 专属）

| Method + Path | → RPC | 字段来源 |
|---|---|---|
| `GET  /v1/operators` | ListOperators | — |
| `POST /v1/operators` | CreateOperator | body{principal} · **响应含一次性 secret** |
| `PUT  /v1/operators/{operator_id}/status` | SetOperatorStatus | path + body{status} |
| `POST /v1/operators/{operator_id}/roles` | BindOperatorRole | path + body{role_id,domain} |
| `GET  /v1/admin-roles` | ListAdminRoles | — |
| `POST /v1/admin-roles` | CreateAdminRole | body{code,name} |
| `POST /v1/admin-roles/{role_id}/grants` | GrantAdminRole | path + body{domain,resource,action} |

**28 条路由 ↔ 27 RPC**（UpsertDataPolicy 占 POST/PUT 两路由，仍 1 RPC / 1 条 ruleTable 规则；其余 26 RPC 各 1 路由）。

## 4. REST-HMAC 认证

签名串（区别于 gRPC 的方法绑定串，绑定到完整 HTTP 请求，防跨端点/改 body 重放）：

```
<principal>\n<unix_ts>\n<HTTP-METHOD>\n<request-target>\n<hex(sha256(body))>
```

- `request-target` = `r.URL.RequestURI()`（path + 查询串，按客户端发出时原样；客户端与服务端须对完全相同的目标串签名/验签）。
- `body` = 原始请求体字节；无 body（GET/DELETE）→ `sha256("")`。
- **头部**（镜像 gRPC metadata 风格）：`X-Sydom-Principal` / `X-Sydom-Timestamp`（unix 秒）/ `X-Sydom-Signature`（小写 hex HMAC-SHA256，64 字符）。

**新增 `internal/auth` 导出符号**（与既有 `Sign/Verify` 并列）：
- `SignREST(secret []byte, principal string, unixTS int64, httpMethod, target, bodySHA256Hex string) string`
- `VerifyREST(secret []byte, principal string, unixTS int64, httpMethod, target, bodySHA256Hex, gotHex string) bool`（常量时间 `hmac.Equal`）
- `ValidPrincipal(s string) bool` = 现有 `validAppID` 逻辑（ASCII 0x21..0x7e、非空、挡控制字符/换行/空格/非 ASCII 同形字）导出供两路复用；`validAppID` 改为内部委托 `ValidPrincipal`。

**认证中间件流程**：① `http.MaxBytesReader`（1 MiB 上限）读全 body → 缓存 + sha256（超限 → 400）；② 缺任一头部 → 401 `"missing auth fields"`；③ `ValidPrincipal` 非法 → 401；④ 解析 ts + `auth.MaxClockSkew`（±5min，复用）越界 → 401；⑤ **复用同一 `OperatorResolver.ResolveSecret(ctx, principal)`**（实现 `auth.SecretResolver`），空密钥 fail-close → 401；⑥ `VerifyREST` 不符 → 401 `"authentication failed"`（通用，防 app/operator 存在性枚举 oracle）。认证成功 → principal 进入后续授权。

**防重放**：沿用 ±5min 时钟窗，无 nonce 缓存（与 gRPC 一致；YAGNI）。

## 5. 共享授权核心（一致性关键）

把 mgmt 现有两拦截器的判定抽成两个**导出纯函数**，gRPC 拦截器与 REST 都调用，`ruleTable` 仍是唯一真相源（保持 mgmt 包内不导出，两路经 FullMethod 间接引用）：

```go
// AuthorizeRule 据 ruleTable[fullMethod] 计算授权域（system→"*"，否则取 req 的 app_id），
// 调 enf.Enforce，成功返回注入 operator 的 ctx；失败返回 gRPC status 错误。
func AuthorizeRule(ctx context.Context, enf *adminauthz.Enforcer, fullMethod, principal string, req any) (context.Context, error)

// CheckStatusWrite 对"具体 app 的业务策略写"（isWrite 规则）校验目标 app 未停用；非写规则直接放行。
func CheckStatusWrite(ctx context.Context, db *sql.DB, fullMethod string, req any) error
```

`AuthzUnaryInterceptor`/`StatusWriteUnaryInterceptor` 重构为这两函数的薄封装（语义零变，既有 mgmt authz/status 测试守门）。REST 中间件按**同序**调用：`AuthorizeRule` → `CheckStatusWrite`（status 必在 authz 之后，否则借 NotFound/FailedPrecondition 差异泄露 app 存在性）。两函数返回 gRPC `status` 错误（codes），REST 统一映射为 HTTP（§7）。

**派发**：每路由一个 `invoke` 闭包 `func(ctx, *mgmt.AdminServer, proto.Message)(proto.Message, error)`，直调对应导出方法（如 `s.CreateRole(ctx, m.(*adminv1.CreateRoleRequest))`），ctx 携 `AuthorizeRule` 注入的 operator（下游 PolicyManager 可见）。27 个显式闭包、零反射、零网络跳。

## 6. JSON 映射（protojson）

**解码**：`protojson.UnmarshalOptions{DiscardUnknown:true}.Unmarshal(body, msg)` 填 body 字段，**再以 path/query 值强制覆写**对应 proto 字段（app_id/role_id/code/operator_id/... 以路径为权威）。这防 body 伪造 app_id 致域混淆。GET/DELETE 无 body → 仅设 path/query。路径里的整型（role_id 等）解析失败 → 400。

**编码**：`protojson.MarshalOptions{EmitDefaultValues:true}.Marshal(resp)`（canonical proto JSON：字段 lowerCamelCase、uint64 编码为 string、默认值也输出以稳定形态）。`Content-Type: application/json`。

> 取舍：canonical protojson 约定（uint64-as-string、lowerCamelCase）是用户选定 protojson 时接受的代价；换来 proto 即契约单一真相源、随 proto 演进零手工同步。`UseProtoNames` 维持 false（canonical）。

## 7. 错误码映射

| gRPC code | HTTP |
|---|---|
| OK | 200 |
| InvalidArgument | 400 |
| Unauthenticated | 401 |
| PermissionDenied | 403 |
| NotFound | 404 |
| AlreadyExists | 409 |
| FailedPrecondition | 409 |
| Unavailable | 503 |
| 其余（Internal/Unknown/...） | 500 |

错误 body：`{"code":"<snake_of_grpc_code>","message":"<安全文案>"}`。未匹配任何路由 → 404；方法不匹配（路径在但动词错）→ ServeMux 返 405。

## 8. 安全铁律

承接「一致性优先于简化」（见 `feedback_consistency_over_simplicity`）：

1. **Internal/Unknown 绝不泄露内部细节**：mgmt 现把 PolicyManager 错误以 `%v` 塞进 Internal message（含内部细节）。对外 REST 映射 500 时 message 一律换通用 `"internal error"`，详情走服务端 `slog`（带 principal/method）。这正是 mgmt observability TODO 的对外兜底边界。
2. **认证/鉴权通用文案**：401 `"authentication failed"` / 403 `"permission denied"` 不区分「不存在」与「无权」，防枚举 oracle，与 gRPC 层一致。
3. **一次性 secret**：CreateApplication 的 app_secret、CreateOperator 的 secret 经 JSON 返回（仅此一次明文）；**绝不落服务端日志**。**生产 TLS 终止是部署职责**——本子项目监听明文 loopback/内网（与既有两 cmd 一致），spec/示例配置注明生产须经 TLS（反代或后续加 `Secure`）。
4. **body 上限**：1 MiB（`http.MaxBytesReader`），防大 body DoS；超限 → 400。
5. **app_id 路径权威**：body 内混入的 app_id（或其它路径字段）一律被路径值覆写。

## 9. 测试策略（TDD）

- **`restgw` 包**（`httptest` + testcontainers PG，真实 DB/Enforcer/AdminServer，复用 `dbtest.SetupSchema` + `adminauthz.EnsureRootOperator`；测试客户端用导出的 `auth.SignREST` 签名）。断言矩阵：
  ① 各类资源 happy path（GET/POST/PUT/DELETE 各打、protojson 往返）；
  ② 认证失败——缺头部 / 坏签名 / 时间偏移越界 / 非法 principal → 401 通用；
  ③ 鉴权——跨 app 域 reader → 403、细粒度资源 → 403、system 端点非超管 → 403（复用 SP1 同款 reader setup）；
  ④ status 写闸——停用 app 上写 → 409；
  ⑤ 一次性 secret——CreateApplication/CreateOperator 响应含非空 secret；
  ⑥ 错误映射——InvalidArgument→400、未知路由→404、body 超限→400、**Internal→500 且 body 不含内部细节**（断言通用文案）；
  ⑦ path 权威——body 伪造 app_id 被路径覆写。
- **`internal/auth` 包**：`SignREST`/`VerifyREST` 纯函数往返 + 篡改任一字段验签失败；`ValidPrincipal` 边界（空/控制字符/非 ASCII/正常）。
- **`internal/controlplane/app` 包**：`Run` 集成测试加一个 REST 监听器（`127.0.0.1:0`），发一个已签名请求（如 `GET /v1/applications`），断言三监听器（admin gRPC / sync gRPC / REST HTTP）并存且 REST 走通认证链。
- **既有 mgmt 测试**：重构后必须全绿（守住「拦截器变薄封装语义不变」）。

## 10. 范围边界（YAGNI / 移交 SP3）

**不做**：分页/游标（List 直返全量，与 SP1 一致）、过滤排序扩展、OpenAPI/Swagger 文档生成、CORS、限流、API-key/OAuth（仅 operator HMAC）、TLS 终止（部署职责）、nonce 防重放（沿用时钟窗）、批量/事务端点、WebSocket/SSE 实时、health/metrics 端点。

**不改**：proto 契约、gRPC 对外行为（仅抽共享授权函数 + 拦截器变薄封装）。

**移交 SP3**：人用 Web Console = html/template 服务端 BFF + operator 凭据持于 session（绝不落盘）+ 登录页 + CSRF + 浏览器只持 session cookie。SP3 复用本 SP2 的共享授权核心与（可选）REST 端点或直调同一 service。

## 11. 文件清单（实现计划据此分解）

- 新建 `internal/auth/signature_rest.go`（SignREST/VerifyREST/signingStringREST）+ 导出 `ValidPrincipal`（改 `internal/auth/interceptor.go` 的 `validAppID` 委托之）。
- 新建 `internal/controlplane/restgw/`：`routes.go`（27 路由静态表 + decode/invoke 闭包）、`auth.go`（REST-HMAC 认证中间件）、`handler.go`（`NewHandler` 装配 ServeMux + 中间件管线 + 编码/错误映射）、`errors.go`（code→HTTP + 安全文案）。
- 改 `internal/controlplane/mgmt/authz.go`：抽 `AuthorizeRule` / `CheckStatusWrite` 导出函数，两拦截器改薄封装。
- 改 `internal/controlplane/app/config.go`（+RESTAddr）、`run.go`（+REST 监听器/goroutine/优雅关闭）。
- 测试：`restgw/*_test.go`、`auth/signature_rest_test.go`、`app/run_test.go`（扩）。

## 12. 完成标准

`gofmt -l` 无输出；`go vet ./...` 干净；`go build ./...` 通过；`go test ./...`（含新 restgw/auth/app 测试 + 既有 mgmt 回归）全绿；`make proto-check` 无漂移（本子项目不改 proto，应天然无漂移）。最终独立整体评审确认 7 条断言矩阵覆盖、5 条安全铁律落地、对 gRPC 对外行为零回归。
