# SP·M1.2 自助账户最小集 + tenant-scoped 读 — 详细设计

> **里程碑**：M1（多租户基座）子项目 M1.2。前置 M1.1（租户隔离基座，已并入 main `437b28d`）。
> **依据**：`docs/superpowers/specs/2026-06-13-sydom-production-readiness-roadmap.md`（M1 范围）+ M1.1 计划非目标交接。
> **下一步**：本设计经用户审查批准后，对 M1.2 调用 writing-plans 创建 TDD 实现计划。

---

## 1. 目标与锁定决策

**目标**：在 M1.1 的租户隔离鉴权核心之上，补齐「账户层」——让租户能自助注册、邀请同级管理员、并让所有「应用枚举/创建」读写按租户作用域收敛。一句话：把 M1.1 的「鉴权能隔离租户」升级为「人能自助进入并运营自己的租户」。

**brainstorm 锁定的三项载荷决策**：

1. **运营者→租户建模 = `tenant_membership` 关联表**（多对多）。一个运营者可属多个租户（代理/顾问场景）。直接后果：每个 tenant-scoped RPC 必须**显式携带目标 `tenant_id`** 消歧，运营者须先「发现自己属于哪些租户」才能导航 → 引入 `ListMyTenants`。
2. **成员分层本期只做 owner + admin 两档**。owner=自助注册创建者（不可被移除的创始人）；admin=被邀请的同级管理员。两档**授权等价**（都绑 `tenant-admin-<id>@t:<id>` 全权角色），档位差异仅记于账户层（membership.tier），用于「谁是创始人/谁能在 M2 做生命周期」。`member`（租户内只读）需「按-app 细分权限」能力，**延后 M1.3/M2**，schema 预留 tier=3 不签发。
3. **自助注册 = 新增 `RegisterTenant` RPC，三面（gRPC/REST/Console）统一**，纳入集中免鉴权白名单（login 之外第二个免鉴权入口）。邀请 = 已认证 `InviteMember` RPC，返回一次性 secret（无邮件，Beta 不依赖邮件基建）。

**注册滥用面处理**（brainstorm 末确认的张力）：注册是公开免鉴权入口，私有 Beta 下是 spam/DoS 面。本期按「**开放注册 + 唯一名约束 + 限流记 TODO 延后 M5 硬化**」处理；不做注册码门禁（如未来 Beta 需要静态 signup gate，作增量并入）。

---

## 2. 现状事实（已核实源码，设计据此）

| 维度 | 现状 | 出处 |
|---|---|---|
| 身份 | `admin_operator(principal UNIQUE, secret_enc, status)`；登录=principal+secret（gRPC/REST 走 HMAC，Console 表单常量时间比对） | `000013_admin_schema.up.sql`、`console/auth.go` |
| 运营者→租户关联 | **不存在**显式列/表；只能从 `admin_subject_role.domain="t:<id>"` 隐式推 | `000013` |
| 租户表 | `tenant(id, name UNIQUE, status)`，无 owner/联系人/账户概念 | `000001_tenant.up.sql` |
| `CreateApplication` | system 域（仅超管）；收 `tenant_name`、按名 upsert 建租户、再建 app；返回一次性 secret | `mgmt/admin_ops.go:32` |
| `ListApplications` | 列**全部** app 零过滤；system 域 | `mgmt/admin_ops.go:80` |
| 鉴权形态 | 仅两种：`system`（`*` 域）/ app-scoped（域取自请求 `app_id`，M1.1 经 `TenantDomainOf` 补 tdom） | `mgmt/authz.go:62` |
| gRPC 拦截器链 | `auth.UnaryServerInterceptor`(HMAC) → `AuthzUnaryInterceptor` → `StatusWriteUnaryInterceptor` | `mgmt/server.go:121` |
| `EnsureTenantAdmin` | M1.1 落地的 bootstrap（masterKey 路径），建角色+grant+绑定，**未挂任何 RPC/启动路径**，**未写 membership** | `adminauthz/operator.go:128` |

---

## 3. 数据模型

### 3.1 新增 migration `000016_tenant_membership`

```sql
-- up
CREATE TABLE tenant_membership (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id   BIGINT      NOT NULL REFERENCES tenant(id)         ON DELETE CASCADE,
    operator_id BIGINT      NOT NULL REFERENCES admin_operator(id) ON DELETE CASCADE,
    tier        SMALLINT    NOT NULL,        -- 1=owner, 2=admin (3=member 预留，本期不签发)
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_tenant_membership UNIQUE (tenant_id, operator_id)
);
CREATE INDEX idx_tenant_membership_operator ON tenant_membership(operator_id);

-- down
DROP TABLE tenant_membership;
```

- **两层真相，刻意分离**：
  - **账户真相层** = `tenant_membership`（谁在租户、什么档）。
  - **授权真相层** = casbin（`tenant-admin-<id>` 角色 @ `t:<id>` 域的绑定）。
- **锁步不变量（I-1，见 §7）**：owner/admin 的一行 membership ⟺ 一条 `admin_subject_role` 绑定（op→`tenant-admin-<id>`@`t:<id>`）。二者**同一事务增删，任一失败整体回滚**（fail-close）。账户层只用于「作用域解析 + 账户运营」，授权决策仍只走 casbin。
- `tenant` 表本期不动（rename/删除 → M2）。`tier` 用 SMALLINT 常量：`TierOwner=1`、`TierAdmin=2`、`TierMember=3`（预留）。

---

## 4. RPC 面（4 新 + 2 改，三面 full-parity）

proto 文件：`api/proto/sydom/admin/v1/admin.proto`（改后重生成 `gen/`）。

### 4.1 新增 messages / RPC

```proto
service AdminService {
  // ... 既有 27 RPC ...
  rpc RegisterTenant(RegisterTenantRequest) returns (RegisterTenantResponse); // 免鉴权
  rpc ListMyTenants(ListMyTenantsRequest)   returns (ListMyTenantsResponse);  // self
  rpc InviteMember(InviteMemberRequest)     returns (InviteMemberResponse);   // tenant-target
  rpc ListMembers(ListMembersRequest)       returns (ListMembersResponse);    // tenant-target 读
}

message RegisterTenantRequest  { string tenant_name = 1; string owner_principal = 2; }
message RegisterTenantResponse { uint64 tenant_id = 1; string owner_principal = 2; string owner_secret = 3; } // owner_secret 一次性

message ListMyTenantsRequest  {}
message TenantMembershipSummary { uint64 tenant_id = 1; string tenant_name = 2; uint32 tier = 3; }
message ListMyTenantsResponse { repeated TenantMembershipSummary memberships = 1; bool is_operating_plane = 2; }

message InviteMemberRequest  { uint64 tenant_id = 1; string principal = 2; } // 本期固定签发 admin 档，不收 tier
message InviteMemberResponse { uint64 operator_id = 1; string principal = 2; string secret = 3; }            // secret 一次性

message ListMembersRequest  { uint64 tenant_id = 1; }
message MemberSummary       { uint64 operator_id = 1; string principal = 2; uint32 tier = 3; uint32 status = 4; }
message ListMembersResponse { repeated MemberSummary members = 1; }
```

### 4.2 改动 messages

```proto
// CreateApplicationRequest：tenant_name(按名建租户) → tenant_id(目标已存在租户)。
// 租户改由 RegisterTenant 创建，建 app 不再顺带建租户。
message CreateApplicationRequest { uint64 tenant_id = 1; string domain = 2; string name = 3; string app_key = 4; }
// CreateApplicationResponse 不变（app_id + 一次性 app_secret）。

// ListApplicationsRequest：加 tenant_id。=N 列该租户 app；=0 → "*" 域（仅超管可过）列全量。
message ListApplicationsRequest { uint64 tenant_id = 1; }
```

> **破坏性 proto 变更说明**：`CreateApplicationRequest` 字段 1 由 `string tenant_name` 改为 `uint64 tenant_id`（pre-Beta，无外部消费者，可破坏）。所有调用方（Console `createApp`、REST `/applications`、e2e/seeder）随之改。`SeedAppInTenant` 已直插 DB 不受影响。

### 4.3 RPC 作用域汇总

| RPC | 新/改 | scope | resource/action | 备注 |
|---|---|---|---|---|
| `RegisterTenant` | 新 | exempt | —（不进 authz） | 集中白名单；auth+authz 双跳过 |
| `ListMyTenants` | 新 | self | —（仅认证） | handler 按 ctx principal 过滤自有 membership |
| `InviteMember` | 新 | tenant | `member`/`create` | owner/admin 命中 `tenant-admin-<id>` 全权 |
| `ListMembers` | 新 | tenant | `member`/`read` | secret 绝不出查询 |
| `CreateApplication` | 改 | tenant（原 system） | `application`/`create` | 域取自请求 `tenant_id` |
| `ListApplications` | 改 | tenant（原 system） | `application`/`read` | 域取自请求 `tenant_id`（0→`*`） |

---

## 5. 授权作用域模型（并入同一 `AuthorizeRule`/`ruleTable`，零另起逻辑）

### 5.1 `rpcRule` 重构：`system bool` → `scope` 枚举

```go
type ruleScope int
const (
    scopeSystem ruleScope = iota // "*" 域：运营平面（CreateOperator 等既有 system=true）
    scopeApp                     // 域取自请求 app_id → TenantDomainOf（既有 system=false）
    scopeTenant                  // 域取自请求 tenant_id（0→"*"，否则 t:<id>），tdom=dom（新）
    scopeSelf                    // 不 enforce，认证通过即放行（新：ListMyTenants）
)
type rpcRule struct { resource, action string; isWrite bool; scope ruleScope }

// tenantIDGetter：携带目标租户的请求（CreateApplication/ListApplications/InviteMember/ListMembers）。
type tenantIDGetter interface{ GetTenantId() uint64 }
```

`ruleTable` 既有 27 条：`system:true` → `scope:scopeSystem`，`system:false` → `scope:scopeApp`，机械改写、语义等价。`CreateApplication`/`ListApplications` 由 `scopeSystem`/`scopeApp` 改 `scopeTenant`；新增 4 条（`RegisterTenant` **不入 ruleTable**，入白名单；其余 3 条按 §4.3）。

### 5.2 `AuthorizeRule` 域解析（唯一真相源，gRPC/REST/Console 共用）

```go
func AuthorizeRule(ctx, enf, fullMethod, principal, req) (context.Context, error) {
    rule, known := ruleTable[fullMethod]
    if !known { return nil, PermissionDenied("unknown method") }
    switch rule.scope {
    case scopeSelf:
        return cp.WithOperator(ctx, principal), nil      // 认证已由上游保证；自有数据由 handler 过滤
    case scopeSystem:
        domain, tdom = "*", "*"
    case scopeApp:                                        // M1.1 既有路径，不变
        appID := req.(appIDGetter).GetAppId()
        domain = DomainOfAppID(appID)
        tdom   = enf.TenantDomainOf(ctx, appID)          // 失败 → fail-close PermissionDenied
    case scopeTenant:
        tid := req.(tenantIDGetter).GetTenantId()
        if tid == 0 { domain, tdom = "*", "*" }           // 运营平面通配（仅超管 g(sub,*,"*") 命中）
        else        { domain = TenantDomain(int64(tid)); tdom = domain }
    }
    if allow, err := enf.Enforce(ctx, principal, domain, tdom, rule.resource, rule.action); err != nil || !allow {
        return nil, PermissionDenied("permission denied")
    }
    return cp.WithOperator(ctx, principal), nil
}
```

### 5.3 关键：M1.1 matcher **一字不改**即覆盖 tenant scope（已逐项核实）

M1.1 matcher（`adminauthz/enforcer.go`）：
```
m = (g(r.sub,p.sub,r.dom) || g(r.sub,p.sub,r.tdom) || g(r.sub,p.sub,"*"))
 && (p.dom==r.dom || p.dom==r.tdom || p.dom=="*")
 && (p.res==r.res || p.res=="*") && (p.act==r.act || p.act=="*")
```

| 调用 | dom=tdom | 命中路径 | 结果 |
|---|---|---|---|
| `ListApplications(0)` 超管 | `*` | `g(sub,super-admin,"*")` ∧ `p.dom="*"` | allow（列全量） |
| `ListApplications(0)` 租户管理员 | `*` | `g(sub,tenant-admin,"*")`=false（绑定在 `t:<id>` 非 `*`） | **deny**（fail-close） |
| `ListApplications(N)`/`CreateApplication(N)` 本租户 owner/admin | `t:<N>` | `g(sub,tenant-admin-<N>,t:<N>)` ∧ `p.dom=t:<N>` | allow |
| 同上，非成员/不存在租户 N | `t:<N>` | 无任何 `g(sub,*,t:<N>)` 绑定 | **deny**（无存在性泄露） |
| 同上，超管 | `t:<N>` | `g(sub,super-admin,"*")` ∧ `p.dom="*"` | allow |

`tenant_id=0 ⟺ "*" 域` 是「运营平面通配」语义，与既有 super-admin `(*,*,*)` grant 自然对齐，无需新增策略行或改 matcher。

### 5.4 免鉴权白名单（集中，auth+authz 双拦截器共识）

```go
// mgmt 包集中定义，唯一真相。
var UnauthenticatedMethods = map[string]bool{
    "/sydom.admin.v1.AdminService/RegisterTenant": true,
}
```

- **gRPC auth**：新增**附加** `auth.UnaryServerInterceptorExempt(resolver, exempt map[string]bool)`（保留原 `UnaryServerInterceptor(resolver)` 不动，sync 复用不受影响）。exempt 命中 → 跳过 HMAC、ctx 原样传入。
- **gRPC authz**：`AuthzUnaryInterceptor` 在 ruleTable 查找前先查 `UnauthenticatedMethods`，命中 → 直接放行（不 enforce、不注 operator）。
- **REST**：`/register` 路由处理器**不调** `authenticateHTTP`，直接解析 body 调 `srv.RegisterTenant`。
- **Console**：`/register` 公开路由（不 `requireSession`），POST 直调 `srv.RegisterTenant`（in-process 不经拦截器）。

---

## 6. 自助 / 邀请 / 读 流程

### 6.1 RegisterTenant（免鉴权）

一事务（fail-close，任一步失败整体回滚）：
1. `INSERT INTO tenant(name)`（唯一名冲突 → `AlreadyExists`，Beta 可接受的注册期披露，等同「用户名已占用」）。
2. ensureOperator(owner_principal, 生成随机 secret，masterKey 加密)；principal 冲突 → `AlreadyExists`。
3. `INSERT tenant_membership(tenant_id, op_id, TierOwner)`。
4. 建/复用角色 `tenant-admin-<tenant_id>` + grant `(t:<id>,*,*)` + 绑定 op→角色@`t:<id>`（复用 M1.1 `EnsureTenantAdmin` 内核）。
5. `BumpPolicyVersion`（触发 enforcer 重载）。
返回 `{tenant_id, owner_principal, owner_secret}`。**owner_secret 明文仅当场返回**，绝不日志/落盘/入 session（复用 `app_created.html` 的「不 PRG、直渲染一次性密钥」管线）。

> **`EnsureTenantAdmin` 接入实现**：M1.1 遗留的「`EnsureTenantAdmin` 接入 cmd 装配」由 **RegisterTenant 这条 API 路径实现**（API 驱动优于启动期 seed）。`EnsureTenantAdmin` 重构为复用同一内核并**补写 membership(owner)**，使其 bootstrap 路径与 API 路径产出一致的账户层状态（M1.1 既有调用方/测试随之带上 membership 断言）。

### 6.2 InviteMember（tenant-target，owner/admin 可调）

授权：`scopeTenant`, `member/create` → `tenant-admin-<id>` 全权命中。一事务：
1. ensureOperator(principal, 生成 secret)；2. `INSERT tenant_membership(tenant_id, op_id, TierAdmin)`；3. 绑定 op→`tenant-admin-<id>`@`t:<id>`；4. bump。
返回一次性 `secret`，邀请人转交。重复邀请同 principal 同租户 → membership 唯一约束 → `AlreadyExists`。

### 6.3 ListMyTenants（self）

handler 据 ctx principal：`SELECT t.id,t.name,m.tier FROM tenant_membership m JOIN tenant t ON t.id=m.tenant_id JOIN admin_operator o ON o.id=m.operator_id WHERE o.principal=$1`。`is_operating_plane` = 探测 `enf.Enforce(principal,"*","*","application","read")`（超管=true）。Console 据此决定「按租户视图」或「运营平面全局视图」。

### 6.4 ListApplications / ListMembers（tenant-target 读）

授权后按 `tenant_id` 过滤：`ListApplications` → `WHERE tenant_id=$1`（`tenant_id=0` 超管已通过 → 不加 WHERE 列全量）；`ListMembers` → join membership `WHERE tenant_id=$1`，**只 SELECT operator_id/principal/tier/status，secret_enc 绝不出查询**。

---

## 7. 一致性不变量 & 安全（评审锚点）

- **I-1 membership ⟺ casbin 绑定锁步**：owner/admin 的账户行与授权绑定同事务增删，fail-close 回滚。任一存在而另一缺失即不一致缺陷。
- **I-2 跨租户 403 扩到账户层**：A 租户 owner/admin 对 B 租户的 app/member 一律 `t:<B>` enforce 失败 → PermissionDenied；非成员调 `tenant_id=N` 无存在性泄露（与不存在租户同一拒绝路径）。
- **I-3 一份授权真相**：tenant scope 决策只在 `AuthorizeRule` 出，Console/REST **不另判租户**；`self` 数据过滤按 ctx principal，绝不接受客户端传入身份。
- **I-4 secret 全程一次性**：注册/邀请/建 app 的明文 secret 仅当场返回，绝不日志/落盘/入 session/出任何 List 查询。
- **I-5 免鉴权入口最小化**：仅 `RegisterTenant`；白名单集中（§5.4），auth 与 authz 双拦截器查同一真相源；REST/Console 对齐。
- **I-6 注册期披露边界**：tenant_name / owner_principal 唯一冲突回 `AlreadyExists`（等同注册场景「名称已占用」，非授权资源枚举）。限流防爆破延后 M5（记 TODO）。
- **opus 整体安全评审**：实现完成后做整体评审，逐条 PASS I-1..I-6 + M1.1 既有 7 不变量不回归（跨包改签名后 `go vet ./...` 全仓兜底）。

---

## 8. Console / REST 面

### 8.1 Console（`internal/controlplane/console/`）

- 新增 `/register`（公开，GET 表单 + POST 直调 `srv.RegisterTenant` → 渲染一次性 secret，不 PRG）。
- 新增 `/tenants`（GET：`ListMyTenants`，多租户切换；单租户隐式直达；超管见「运营平面」入口）。
- 新增 `/tenants/{id}/members`（GET：`ListMembers` 团队页 + 邀请表单；POST：`InviteMember` → 渲染一次性 secret，不 PRG）。
- `dashboard` 改 tenant 感知：先 `ListMyTenants` 定上下文，再 `ListApplications(tenant_id)`；PermissionDenied 仍降级「按 App ID 直达」无枚举。
- 建 app 表单去掉 free-text `tenant_name`，改为「在当前租户建」（tenant_id 取自上下文；多租户时选择）。
- 既有纪律全沿用：CSRF、降级无枚举、一次性 secret 不 PRG/不日志、会话仅存 principal/csrf。

### 8.2 REST（`internal/controlplane/restgw/`）

- 4 新 RPC 各加路由（`POST /tenants` 注册=免 HMAC、`GET /me/tenants`、`POST /tenants/{id}/members`、`GET /tenants/{id}/members`），REST-HMAC 绑完整请求（已认证路由）。
- `CreateApplication`/`ListApplications` 路由随 proto 字段调整（tenant_id）。
- `/register` 在 §5.4 白名单内，跳 HMAC；其余路由不变。

---

## 9. 范围边界（YAGNI）

**本期做**：`tenant_membership` 表 + migration、4 新 RPC、2 改 RPC、authz `scope` 重构（system/app/tenant/self + exempt 白名单）、`EnsureTenantAdmin` 重构补 membership 并经 RegisterTenant 接入、Console/REST 三面、跨租户账户层安全矩阵测试、opus 整体评审。

**明确不做（→ 后续里程碑）**：
- `member` 只读档（需按-app 细分权限）→ M1.3/M2；schema 已预留 tier=3。
- 移除/停用成员、owner 转移、租户改名/删除（生命周期对称）→ M2。
- 邮件邀请 / 邀请 token 链接 → M5/M6（依赖邮件基建）。
- 注册限流 / signup gate 防滥用 → M5 硬化（本期仅唯一名约束 + TODO）。
- super-admin 全局跨租户运营台打磨（仅保留 `ListApplications(0)` 列全量的最小能力）→ M2/M3。

---

## 10. 假设与未决项

- **proto 破坏性变更**（`CreateApplicationRequest.tenant_name`→`tenant_id`）在 pre-Beta 无外部消费者前提下可接受；若已有外部集成需保留旧字段，则改为新增 `tenant_id` + 弃用 `tenant_name`（保留一版）。**假设无外部消费者**。
- **多租户运营者的「当前租户」上下文**由各 surface 自管（Console 走 URL/选择，REST/gRPC 由客户端每请求显式传 `tenant_id`）；本期不引入服务端「会话级当前租户」状态。
- **is_operating_plane 探测**用 `Enforce(*,*,application,read)` 作 UI 提示（非授权决策；真正 enforce 仍在各 RPC）。
