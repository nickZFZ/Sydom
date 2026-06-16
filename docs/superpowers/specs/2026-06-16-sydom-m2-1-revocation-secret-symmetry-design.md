# 司域 (Sydom) M2.1 设计 · 撤权对称 + Secret 硬切换

> 里程碑：**M2 · 授权产品功能纵深（operate / understand）** 的第一个子项目（M2.1）。
> M2 路线图范围拆为 4 子项目：**M2.1 撤权对称 + 生命周期补全（operate）** / M2.2 决策可解释性 / M2.3 审计查询 + 变更历史 / M2.4 List 分页·搜索·排序·过滤。本文仅覆盖 M2.1，且经 brainstorm 收敛为「安全对称缺口优先」。

## 1. 背景与目标

M1（M1.1–M1.5）落地后，控制面有 33 个 AdminService RPC，但**写面存在两处安全对称缺口**（实查 33-RPC surface）：

1. **撤权不对称（system 域）**：app 域已对称（`RevokePermission` / `UnbindUserRole`），但 system 域只能授不能撤——`GrantAdminRole` / `BindOperatorRole` 无逆操作。后果：授出去的管理特权**无法移除**（standing privilege you can't remove）。
2. **Secret 不可轮换**：`CreateApplication` / `CreateOperator` 仅在创建时一次性下发 secret，无轮换/重置入口。后果：app/operator 的 HMAC 凭据一旦泄露**无法轮换**。

**目标**：补齐这两处安全对称——`RevokeAdminGrant` / `UnbindOperatorRole` + `RotateApplicationSecret` / `ResetOperatorSecret`，形成可演示的安全闭环（授→撤、发→轮）。

## 2. 范围与 YAGNI 边界

**纳入（M2.1 必须项）**：
- `RevokeAdminGrant`、`UnbindOperatorRole`（system 域撤权对称）
- `RotateApplicationSecret`、`ResetOperatorSecret`（Secret 硬切换）
- 三面 parity：gRPC + REST + Console（决策 a）

**不纳入（移出 M2.1，留后续切片）**：
- 生命周期补全（Get-single / 改名 / 删除）——`status=disabled` 已提供 operator/application 软删，List 已覆盖查单个，硬删/改名属 YAGNI 或留 M2.4 体验切片。
- explain / 反查 / 审计（M2.2、M2.3）；分页（M2.4）。

**两处已定决策（brainstorm 收敛，记录于此供审查）**：
- **(a) 三面 parity**：按 M1.2/M1.4 范式，4 个新 RPC 同时落 gRPC handler + REST 路由 + Console UI；三面共用同一 `AuthorizeRule`/`ruleTable`，物理不可能策略漂移。
- **(b) Secret 切换语义 = 硬切换**：生成新 secret、立即覆盖 `secret_enc`、旧 secret 即刻失效、新 secret 一次性返回。理由：secret 解析器（`OperatorResolver.ResolveSecret`、`secret.Resolver.ResolveSecret`）**每请求查库、无缓存**，硬切换是单条 `UPDATE`、零 schema 改动、即时生效；轮换瞬间到调用方更新配置前会断（app secret 尤甚），靠 runbook 协调（类比 M1.5「证书轮换 = 滚动重启」）。否决「宽限窗口双 secret」（需 schema + 双匹配 + 更大攻击面）。
- **(c) 撤权幂等语义 = 严格**：撤不存在的 grant/binding → 回滚 + `NotFound`，**非**幂等 no-op。理由：与 ③-1 既有 delete（`DeleteRolePermission`/`DeleteDataPolicy` 校验 `RowsAffected==0` 报错回滚「防版本跳变 + 幽灵 delta」）一致。

## 3. RPC 契约（`api/proto/sydom/admin/v1/admin.proto`）

新增 4 个 RPC + 2 个 response message（请求消息按字段列）：

| RPC | 请求字段 | 返回 | 镜像对偶 |
|---|---|---|---|
| `RevokeAdminGrant` | `role_id int64, domain string, resource string, action string` | `WriteResponse`（复用） | `GrantAdminRole` 的逆 |
| `UnbindOperatorRole` | `operator_id int64, role_id int64, domain string` | `WriteResponse`（复用） | `BindOperatorRole` 的逆 |
| `RotateApplicationSecret` | `app_id int64` | `RotateApplicationSecretResponse { string secret }` | `CreateApplication` 的 secret 段 |
| `ResetOperatorSecret` | `operator_id int64` | `ResetOperatorSecretResponse { string secret }` | `CreateOperator` 的 secret 段 |

- 撤权复用既有 `WriteResponse{ bool changed }`。
- 两个 secret RPC 各自响应仅含 `string secret`（一次性凭据，镜像 `CreateApplicationResponse`/`CreateOperatorResponse`）。
- buf lint：若触发 `RPC_REQUEST_RESPONSE_UNIQUE`/`RPC_REQUEST_STANDARD_NAME`，按既有 except 先例处理（不破坏契约）。

## 4. 组件与数据流

### 4.1 存储层（`internal/controlplane/adminauthz/store.go` + `internal/controlplane/secret`）
- 新增 `DeleteRoleGrant(ctx, q cp.DBTX, roleID int64, domain, resource, action string) error`、`DeleteSubjectRole(ctx, q cp.DBTX, operatorID, roleID int64, domain string) error`——镜像既有 `InsertRoleGrant`/`InsertSubjectRole` 的 `DELETE`，**校验 `RowsAffected==0` → 返可识别错误**（供 handler 映射 NotFound、回滚、不 bump）。
- Secret 硬切换 DAO：`UPDATE application SET app_secret_enc=$1 WHERE id=$2`、`UPDATE admin_operator SET secret_enc=$1 WHERE id=$2`，各校验 `RowsAffected==0 → NotFound`。复用既有 `secret.EncryptSecret`（app）/`crypto.Encrypt`（operator）+ `genSecret`。

### 4.2 Handler（`internal/controlplane/mgmt/admin_ops.go`）
- **撤权**（镜像 `GrantAdminRole`/`BindOperatorRole` 原子模板）：
  `BeginTx → Delete*（0 行→NotFound 回滚）→ BumpPolicyVersion → Commit → WriteResponse{Changed:true}`。
  **必 bump**：撤权立即生效是一致性红线——meta-RBAC enforcer 经 `admin_policy_version` 版本化重载，被撤特权下次 `Enforce` 即 fail-close 拒绝。
- **Secret 硬切换**（镜像 `CreateOperator`/`CreateApplication` 的 secret 生成）：
  `genSecret → encrypt → BeginTx → UPDATE secret_enc（0 行→NotFound）→ Commit → 返回新 secret`。
  **不 bump**：secret 非 casbin 策略；解析器每请求查库，覆盖 `secret_enc` 即时生效，无需 enforcer 重载。

### 4.3 鉴权（`internal/controlplane/mgmt/authz.go` ruleTable）
ruleTable 增量加 4 条（镜像各自授权对偶的 scope；matcher 与既有条目一字未改）：

| RPC | rpcRule `{resource, action, isWrite, scope}` | 镜像 |
|---|---|---|
| `RevokeAdminGrant` | `{"admin", "update", false, scopeSystem}` | `GrantAdminRole` |
| `UnbindOperatorRole` | `{"admin", "update", false, scopeSystem}` | `BindOperatorRole` |
| `ResetOperatorSecret` | `{"admin", "update", false, scopeSystem}` | `CreateOperator`（operator 是 system 域实体） |
| `RotateApplicationSecret` | `{"application", "update", false, scopeApp}` | `SetApplicationStatus`（`isWrite=false` 故不受 status 写拦截，停用 app 也可轮换） |

三拦截器管线（认证 → 鉴权 → status 写拦截）零改；4 个新 RPC 经同一 `AuthorizeRule` 唯一真相源。

### 4.4 三面 parity（REST + Console）
- **REST**（`internal/controlplane/restgw`）：加 4 路由，**path 权威覆写** body 的 `app_id`/`operator_id`（防伪造越权）。
- **Console**（`internal/controlplane/console`）：
  - 撤权：在既有 admin-role grant 列表 / operator 绑定列表加「移除」按钮，走 `doWrite`（会话 → CSRF → 授权 → 调用 → PRG 303）。
  - Secret 轮换：在应用页 / operator 页加「轮换 secret」按钮，新 secret **一次性展示、不 PRG、不日志**（镜像既有 Create 一次性 secret 处理）。

## 5. 一致性与安全不变量（MS-1..MS-6，验收逐条核验）
- **MS-1 撤权即时生效**：撤权写 `BumpPolicyVersion`，enforcer 版本化重载，被撤特权下次 `Enforce` 即拒绝（一致性红线）。
- **MS-2 撤权 fail-close 不静默**：`Delete*` 命中 0 行 → 回滚 + `NotFound`，**绝不 bump 版本**（防幽灵 delta / 版本跳变）。
- **MS-3 Secret 硬切换即时失效**：`UPDATE secret_enc` 后旧 secret 下次认证即 `Unauthenticated`/401（解析器无缓存），新 secret 一次性返回。
- **MS-4 Secret 一次性不泄露**：新 secret 仅 Rotate/Reset 响应返一次，绝不入日志、List 物理不 SELECT/不返、不 PRG。
- **MS-5 一份授权真相三面共用**：gRPC/REST/Console 经同一 `AuthorizeRule`+`ruleTable`；4 新 RPC scope 镜像其授权对偶，物理不可能策略漂移。
- **MS-6 M1.1 租户隔离/matcher 零触碰**：`adminauthz` matcher 与 ruleTable 既有条目一字未改（仅增量加 4 条）；跨租户/跨域撤权 403 安全矩阵守门。

## 6. 错误处理
- 撤不存在的 grant/binding、轮换未知 app/operator → `NotFound`（fail-close，回滚，不 bump）。
- 跨域/跨租户无权 → `PermissionDenied`（经 AuthorizeRule，不泄露存在性）。
- 内部错误 → `Internal`（沿用既有 `%v` 透传债；统一脱敏留 M3，非本切片引入）。

## 7. 测试策略（TDD）
- 每个 RPC 先写失败测试再实现。
- **撤权**：撤权后经**真实 `Enforce`** 验证被撤 operator/role 的特权即刻消失（证 bump 生效）；撤不存在 → `NotFound`。
- **Secret**：硬切换后旧 secret 认证即刻 401、新 secret 通过（经真实 HMAC 认证路径）；轮换未知 id → `NotFound`。
- **安全矩阵**：跨租户 / 跨域撤权 → 403；secret 不泄露（不入日志、List 不返）。
- **三面 parity**：REST 路由状态码（403 无权 / 200 有权）；Console `doWrite` 缺 CSRF→403、带 CSRF→303。
- 兜底：`gofmt -l` / `go vet ./...` / `make proto-check`（无漂移）/ 相关包 `go test` + 全仓 `go test ./...`。

## 8. 范围边界 / 移交
- M2.1 仅这两组安全对称；生命周期补全（get-single/改名/硬删）、explain/反查（M2.2）、审计（M2.3）、分页（M2.4）各走独立 spec→plan→实现周期。
- 硬切换的 app secret 轮换会中断 sidecar 直至其配置更新——属运维协调，runbook 注明（类比 M1.5 证书轮换=滚动重启），不在本切片做宽限窗口。
- 范式延续：子代理驱动 + 两阶段审查 + 整体安全评审；跨包改签名后 `go vet ./...` 全仓兜底。

## 9. 下一步
本 spec 经用户审查批准后，调用 writing-plans 创建 M2.1 实现计划（TDD 任务分解）。
