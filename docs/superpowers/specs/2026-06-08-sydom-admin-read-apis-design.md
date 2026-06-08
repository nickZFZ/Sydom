# 司域 接入面 SP1：AdminService 读面 详细设计

> 子项目：接入面（REST 网关 + Web Console）第一子模块（SP1）。前置：控制面 ③-3 管理 API（`AdminService` 写面 + 元-RBAC + 三拦截器）已实现并入 main。
> 上层背景：接入面整体 = SP1 读面 → SP2 REST 网关（手写 net/http）→ SP3 Web 管理台（html/template BFF）。三者各自独立 规格→计划→实现 周期；本文件只覆盖 **SP1**。
> 范围边界：**仅扩 gRPC `AdminService` 只读 List RPC + DB 直查 + 元-RBAC 读规则 + 测试**。不产出 REST/HTTP、不产出 UI、不新增二进制、不动 DB schema。
> 日期：2026-06-08

---

## 1. 目标与范围

`AdminService` 现状几乎全是写 RPC，唯一的读是 `ListApplications`（`internal/controlplane/mgmt/admin_ops.go:80`）。SP2 的 REST 网关与 SP3 的 Web 管理台要让人「先看后改」，都依赖一个**只读查询面**来展示现状（有哪些角色/权限/授权/绑定/数据策略）。SP1 就补这个读面——**gRPC 优先**，与既有 `AdminService` 同进程同契约，REST 暴露留给 SP2。

读 RPC 不变更任何状态：**不经 `PolicyManager`、不写 outbox、不 bump version、不发广播**，直接查 DB（与 `ListApplications` 完全同模式）。新增实现集中在一个文件 `internal/controlplane/mgmt/admin_reads.go`，与写路径 `admin_ops.go` 物理分离。

**非目标（SP1 不做）：** REST/HTTP 暴露（SP2）、Web UI（SP3）、分页/游标、单条 `Get*`、`admin_role_grant`/`admin_subject_role` 明细读（SP3 若需再补）、字段级过滤以外的搜索、DB schema 变更。

---

## 2. 关键决策

1. **读直查 DB，不下沉 PolicyManager。** `PolicyManager` 是写编排（返回 `*cp.Delta`、落 outbox、bump version）；读无副作用，套这层只增耦合无收益。沿用 `ListApplications` 既有先例：读查询写在 `mgmt` 层直接 `s.db.QueryContext`。
2. **鉴权复用同一元-RBAC `Enforcer` + `ruleTable`（`authz.go:29`），不另起炉灶。** 每个读 RPC 在 `ruleTable` 加一条 `action:"read"`。这是「一致性优先」铁律的直接体现：管理面只有一个授权真相源。
3. **app-域读强制 `app_id` 隔离。** 前 6 个读是 app-域（`system:false`），鉴权域 = 请求 `app_id`，且 SQL 一律 `WHERE app_id=$1`。非超管 operator 越域读 → `PermissionDenied`（鉴权拦截器在查 DB 前就挡住）。
4. **operator 凭据绝不出现在任何响应。** `ListOperators` 的 SQL **根本不 SELECT `secret_enc`**，返回消息无 secret 字段。这是 fail-close 的物理保证，而非靠调用方自觉。
5. **读不受 status 写拦截。** 读 RPC 在 `ruleTable` 标 `isWrite:false`；`StatusWriteUnaryInterceptor` 对其放行——停用的 app 仍可被查看（看 ≠ 改）。
6. **命名遵循 buf 标准 `List<Plural>`。** 新 RPC 全部是 `List<Plural>Request/Response`，符合 buf DEFAULT lint，**无需新增 lint 例外**（不同于写面共享消息的既有例外）。

---

## 3. 组件结构（新增 / 改动）

```
api/proto/sydom/admin/v1/admin.proto   + 8 个 List RPC + 对应 Request/Response/Summary 消息
gen/sydom/admin/v1/                     buf generate 重新生成（admin.pb.go / admin_grpc.pb.go）
internal/controlplane/mgmt/
  admin_reads.go    （新）8 个读方法：直查 DB → 组 Summary。镜像 ListApplications 风格
  authz.go          （改）ruleTable 增 8 条 read 规则
  admin_reads_test.go（新）读正确性 + 隔离/鉴权 + secret 不泄露 + 空集，testcontainers PG
```

**职责边界：** `admin_reads.go` 只做「按域查 DB → 填 proto Summary」，无事务、无 Delta、无广播。`ListApplications` 留在 `admin_ops.go` 不动（历史位置；本期不为对齐而搬迁，避免无关 diff）。

---

## 4. proto 契约（新增）

加在 `service AdminService` 末尾的「读面」分组；消息加在文件相应处。

```proto
service AdminService {
  // ... 既有 19 个 RPC 不变 ...

  // —— 读面（SP1：只读 List，REST/Console 展示现状用）——
  rpc ListRoles(ListRolesRequest) returns (ListRolesResponse);
  rpc ListPermissions(ListPermissionsRequest) returns (ListPermissionsResponse);
  rpc ListGrants(ListGrantsRequest) returns (ListGrantsResponse);
  rpc ListRoleInheritances(ListRoleInheritancesRequest) returns (ListRoleInheritancesResponse);
  rpc ListUserBindings(ListUserBindingsRequest) returns (ListUserBindingsResponse);
  rpc ListDataPolicies(ListDataPoliciesRequest) returns (ListDataPoliciesResponse);
  rpc ListOperators(ListOperatorsRequest) returns (ListOperatorsResponse);
  rpc ListAdminRoles(ListAdminRolesRequest) returns (ListAdminRolesResponse);
}

// —— app-域：6 个 ——
message RoleSummary {
  int64 role_id = 1; string code = 2; string name = 3; string description = 4;
}
message ListRolesRequest  { uint64 app_id = 1; }
message ListRolesResponse { repeated RoleSummary roles = 1; }

message PermissionSummary {
  int64 permission_id = 1; string code = 2; string resource = 3; string action = 4;
  string ptype = 5; string name = 6; string source = 7; // source: manual|auto（SDK D 语义）
}
message ListPermissionsRequest  { uint64 app_id = 1; }
message ListPermissionsResponse { repeated PermissionSummary permissions = 1; }

message GrantSummary {
  int64 grant_id = 1; int64 role_id = 2; int64 permission_id = 3; string eft = 4; // allow|deny
}
message ListGrantsRequest  { uint64 app_id = 1; int64 role_id = 2; } // role_id=0 表示全部
message ListGrantsResponse { repeated GrantSummary grants = 1; }

message RoleInheritanceSummary {
  int64 inheritance_id = 1; int64 parent_role_id = 2; int64 child_role_id = 3;
}
message ListRoleInheritancesRequest  { uint64 app_id = 1; }
message ListRoleInheritancesResponse { repeated RoleInheritanceSummary inheritances = 1; }

message UserBindingSummary {
  int64 binding_id = 1; string user_id = 2; int64 role_id = 3;
}
message ListUserBindingsRequest  { uint64 app_id = 1; string user_id = 2; } // user_id="" 表示全部
message ListUserBindingsResponse { repeated UserBindingSummary bindings = 1; }

message DataPolicySummary {
  int64 data_policy_id = 1; string subject_type = 2; string subject_id = 3;
  string resource = 4; string condition = 5; // 原始 JSON 串，不解析
  string effect = 6; uint64 version = 7;
}
message ListDataPoliciesRequest  { uint64 app_id = 1; string resource = 2; } // resource="" 表示全部
message ListDataPoliciesResponse { repeated DataPolicySummary data_policies = 1; }

// —— system-域：2 个（"*" 域，超管/admin 资源）——
message OperatorSummary { // 永不含 secret
  int64 operator_id = 1; string principal = 2; uint32 status = 3;
}
message ListOperatorsRequest  {}
message ListOperatorsResponse { repeated OperatorSummary operators = 1; }

message AdminRoleSummary {
  int64 role_id = 1; string code = 2; string name = 3;
}
message ListAdminRolesRequest  {}
message ListAdminRolesResponse { repeated AdminRoleSummary roles = 1; }
```

> 字段名注记：`PermissionSummary.ptype` 对应 DB 列 `permission.type`（proto 字段名沿用写面 `UpsertPermissionRequest.ptype` 的既有命名，避免与 proto3 习惯冲突）。

---

## 5. 鉴权：ruleTable 新增 8 条（`authz.go`）

`rpcRule{resource, action, isWrite, system}`：

```go
"/sydom.admin.v1.AdminService/ListRoles":             {"role",         "read", false, false},
"/sydom.admin.v1.AdminService/ListPermissions":       {"permission",   "read", false, false},
"/sydom.admin.v1.AdminService/ListGrants":            {"grant",        "read", false, false},
"/sydom.admin.v1.AdminService/ListRoleInheritances":  {"inheritance",  "read", false, false},
"/sydom.admin.v1.AdminService/ListUserBindings":      {"binding",      "read", false, false},
"/sydom.admin.v1.AdminService/ListDataPolicies":      {"data_policy",  "read", false, false},
"/sydom.admin.v1.AdminService/ListOperators":         {"admin",        "read", false, true},
"/sydom.admin.v1.AdminService/ListAdminRoles":        {"admin",        "read", false, true},
```

- `resource` 与写面同词表（`role`/`permission`/`grant`/`inheritance`/`binding`/`data_policy`/`admin`），`action:"read"` 新增。super-admin 在 `*` 域持 `*/*/*` 通配，自动覆盖全部读。
- app-域读 `system:false`：鉴权域取请求 `app_id`（`AuthzUnaryInterceptor` 已有逻辑，要求 req 实现 `GetAppId()`——SP1 的 app-域 Request 均含 `app_id`，满足）。
- system-域读 `system:true`：域 "*"，不取 app_id。
- 全部 `isWrite:false`：`StatusWriteUnaryInterceptor` 放行。

> 元-RBAC 授予粒度：现有 super-admin（`*/*/*`）天然可读全部；若要给「只读 operator」，运维用现有 `GrantAdminRole(role, domain, resource, "read")` 即可，无需 SP1 额外建模。

---

## 6. 实现要点（`admin_reads.go`，镜像 `ListApplications`）

每个方法统一骨架：

```go
func (s *AdminServer) ListRoles(ctx context.Context, r *adminv1.ListRolesRequest) (*adminv1.ListRolesResponse, error) {
    rows, err := s.db.QueryContext(ctx,
        `SELECT id, code, name, COALESCE(description,'') FROM role WHERE app_id=$1 ORDER BY id`,
        int64(r.AppId))
    if err != nil { return nil, status.Errorf(codes.Internal, "list roles: %v", err) }
    defer rows.Close()
    out := &adminv1.ListRolesResponse{}
    for rows.Next() {
        var x adminv1.RoleSummary
        if err := rows.Scan(&x.RoleId, &x.Code, &x.Name, &x.Description); err != nil {
            return nil, status.Errorf(codes.Internal, "scan: %v", err)
        }
        out.Roles = append(out.Roles, &x)
    }
    if err := rows.Err(); err != nil { return nil, status.Errorf(codes.Internal, "rows: %v", err) }
    return out, nil
}
```

逐方法的 SQL（均 `ORDER BY id`；可空列 `description` 用 `COALESCE(...,'')`）：

| 方法 | SQL（核心） |
|---|---|
| ListRoles | `SELECT id, code, name, COALESCE(description,'') FROM role WHERE app_id=$1` |
| ListPermissions | `SELECT id, code, resource, action, type, name, source FROM permission WHERE app_id=$1` |
| ListGrants | `SELECT id, role_id, permission_id, eft FROM role_permission WHERE app_id=$1 [AND role_id=$2]` |
| ListRoleInheritances | `SELECT id, parent_role_id, child_role_id FROM role_inheritance WHERE app_id=$1` |
| ListUserBindings | `SELECT id, user_id, role_id FROM user_role_binding WHERE app_id=$1 [AND user_id=$2]` |
| ListDataPolicies | `SELECT id, subject_type, subject_id, resource, condition::text, effect, version FROM data_policy WHERE app_id=$1 [AND resource=$2]` |
| ListOperators | `SELECT id, principal, status FROM admin_operator ORDER BY id` ——**无 secret_enc** |
| ListAdminRoles | `SELECT id, code, name FROM admin_role ORDER BY id` |

- 可选过滤（`[AND ...]`）：filter 字段为 0/"" 时不加该条件。实现上用「`role_id=0` 走无过滤分支 / 非 0 走带 `$2` 分支」的二选一 SQL，避免拼字符串（防注入、可读）。
- `condition::text`：JSONB 转文本原样回传，不在控制面解析条件树。
- `out.Roles` 等切片：空结果返回 `&Response{}`（空切片，非 nil error）。

---

## 7. 安全红线（HARD，测试须钉死）

1. **operator secret 绝不出现**：`ListOperators` 响应消息无 secret 字段；SQL 不 SELECT `secret_enc`。测试断言响应里查不到任何凭据数据。
2. **app_id 跨域隔离**：app-域读全部 `WHERE app_id=$1`，且鉴权域 = 请求 app_id。测试：app-scoped operator（仅在域 A 有 read 权）调 `ListRoles(app=B)` → `PermissionDenied`；调 `ListRoles(app=A)` → 仅 A 的角色。
3. **fail-close 一致**：鉴权失败/未知 method/缺身份 → 现有拦截器已 `Unauthenticated`/`PermissionDenied`，读 RPC 不引入任何绕过路径（读方法本身不含鉴权逻辑，全靠拦截器链）。

---

## 8. 测试计划（TDD，先红后绿）

复用 `endtoend_test.go`/`server_test.go` 的 testcontainers PG + bufconn 装配（真实拦截器链 + 真实 DB），新增 `admin_reads_test.go`：

1. **读正确性（super-admin 身份）**：先用写 RPC seed（CreateApplication/CreateRole/UpsertPermission/GrantPermission/AddRoleInheritance/BindUserRole/UpsertDataPolicy/CreateOperator/CreateAdminRole），再逐个 List 断言：
   - 行数、关键字段逐一比对；
   - **permission.source** = "manual"（写面默认）；
   - **data_policy**：condition JSON 串 round-trip 一致、effect 正确、version>0；
   - 可选过滤：`ListGrants(role_id=X)` 只回该角色、`ListDataPolicies(resource="order")` 只回该资源、`ListUserBindings(user_id="alice")` 只回该用户。
2. **隔离/鉴权**：建一个只在域 A 有 `read` 的 operator（CreateOperator + CreateAdminRole + GrantAdminRole(domain=A,resource=role,action=read) + BindOperatorRole），
   - `ListRoles(app=A)` 成功；`ListRoles(app=B)` → `codes.PermissionDenied`；
   - 同一 operator 调 `ListOperators`（system 域，无 admin/read 授予）→ `codes.PermissionDenied`。
3. **secret 不泄露**：`ListOperators`（super-admin）返回 ≥1 条，断言每条仅 {operator_id, principal, status}，无任何 secret/enc 字段（编译期即保证——消息无该字段；运行期再断言 principal 命中、status 正确）。
4. **空集**：新建 app 未加任何角色 → `ListRoles` 返回空切片、nil error（非 NotFound）。
5. **proto smoke**：`proto_smoke_test.go` 当前只引用若干消息类型、无方法计数断言；顺手补引一个新读消息（如 `&adminv1.ListRolesRequest{}`）确认生成物可用即可，无计数需维护。

**验证门（声称完成前必跑）：** `gofmt -l`、`go vet ./...`、`buf generate` 后 `git diff --exit-code gen/`（生成物已提交且一致）、`go build ./...`、`go test ./internal/controlplane/mgmt/... -count=1`（稳定性再 `-count=2`）。

---

## 9. 交付物清单

- `api/proto/sydom/admin/v1/admin.proto`：+8 RPC、+对应消息。
- `gen/sydom/admin/v1/*`：buf 重新生成并提交。
- `internal/controlplane/mgmt/admin_reads.go`：8 个读方法。
- `internal/controlplane/mgmt/authz.go`：ruleTable +8 条。
- `internal/controlplane/mgmt/admin_reads_test.go`：上述测试。
- （按需）`proto_smoke_test.go` 补引一个新读消息（无计数需维护）。

SP1 完成即并入 main；SP2（REST 网关）在其上独立头脑风暴。
