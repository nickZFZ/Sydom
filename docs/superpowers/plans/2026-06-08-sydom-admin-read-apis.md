# 司域接入面 SP1：AdminService 读面 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 给控制面 gRPC `AdminService` 加 8 个只读 `List` RPC（6 个 app-域 + 2 个 system-域），让上层 REST 网关 / Web 管理台能查现状。

**架构：** 读 RPC 直查 DB（镜像现有 `ListApplications`），**不**经 `PolicyManager`、**不**写 outbox、**不** bump version。鉴权复用同一元-RBAC `Enforcer` + `ruleTable`（每读 RPC 加一条 `action:"read"`）。新增实现集中在 `internal/controlplane/mgmt/admin_reads.go`，与写路径物理分离。

**技术栈：** Go 1.26 / gRPC / buf v1.34（protoc-gen-go + protoc-gen-go-grpc）/ PostgreSQL（lib/pq）/ testify + testcontainers(PG) + bufconn。

**安全红线（贯穿全程，测试钉死）：** ① `ListOperators` 永不返回 secret（SQL 根本不 SELECT `secret_enc`）；② app-域读强制 `WHERE app_id=$1` + 鉴权域=请求 app_id，跨域 `PermissionDenied`；③ 读不受 status 写拦截（`isWrite:false`）。

**规格依据：** `docs/superpowers/specs/2026-06-08-sydom-admin-read-apis-design.md`

---

## 文件结构

| 文件 | 职责 | 动作 |
|---|---|---|
| `api/proto/sydom/admin/v1/admin.proto` | 8 个 List RPC + Request/Response/Summary 消息 | 修改 |
| `gen/sydom/admin/v1/admin.pb.go` / `admin_grpc.pb.go` | buf 重新生成 | 重新生成 |
| `internal/controlplane/mgmt/admin_reads.go` | 8 个读方法：直查 DB → 填 Summary | 创建 |
| `internal/controlplane/mgmt/authz.go` | `ruleTable` +8 条 read 规则 | 修改（`authz.go:29-49`） |
| `internal/controlplane/mgmt/admin_reads_test.go` | 读正确性/空集/secret 不泄露/跨域隔离 | 创建 |
| `internal/controlplane/mgmt/proto_smoke_test.go` | 补引一个新读消息 | 修改 |

每个任务产出一个独立、可测的变更。任务顺序：proto → app-域读 → system-域读 → 安全隔离测试 → 验证门。

---

## 任务 1：proto 契约 + 重新生成

**文件：**
- 修改：`api/proto/sydom/admin/v1/admin.proto`
- 重新生成：`gen/sydom/admin/v1/*`
- 测试：`internal/controlplane/mgmt/proto_smoke_test.go`

- [ ] **步骤 1：编写失败的测试（引用尚不存在的生成类型）**

在 `proto_smoke_test.go` 的 `TestProtoGenerated` 函数体追加一行：

```go
	_ = &adminv1.ListRolesRequest{}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go build ./internal/controlplane/mgmt/...`
预期：FAIL，报 `undefined: adminv1.ListRolesRequest`（生成代码尚无此类型）。

- [ ] **步骤 3：编辑 proto —— 加 8 个 RPC**

在 `admin.proto` 的 `service AdminService { ... }` 内、最后一个 RPC（`BindOperatorRole`）之后、闭合 `}` 之前插入：

```proto

  // —— 读面（SP1：只读 List，REST/Console 展示现状用）——
  rpc ListRoles(ListRolesRequest) returns (ListRolesResponse);
  rpc ListPermissions(ListPermissionsRequest) returns (ListPermissionsResponse);
  rpc ListGrants(ListGrantsRequest) returns (ListGrantsResponse);
  rpc ListRoleInheritances(ListRoleInheritancesRequest) returns (ListRoleInheritancesResponse);
  rpc ListUserBindings(ListUserBindingsRequest) returns (ListUserBindingsResponse);
  rpc ListDataPolicies(ListDataPoliciesRequest) returns (ListDataPoliciesResponse);
  rpc ListOperators(ListOperatorsRequest) returns (ListOperatorsResponse);
  rpc ListAdminRoles(ListAdminRolesRequest) returns (ListAdminRolesResponse);
```

- [ ] **步骤 4：编辑 proto —— 加消息定义**

在 `admin.proto` 文件**末尾**（最后一个 message `BindOperatorRoleRequest` 之后）追加：

```proto

// —— SP1 读面消息（app-域 6 个）——
message RoleSummary {
  int64 role_id = 1;
  string code = 2;
  string name = 3;
  string description = 4;
}
message ListRolesRequest { uint64 app_id = 1; }
message ListRolesResponse { repeated RoleSummary roles = 1; }

message PermissionSummary {
  int64 permission_id = 1;
  string code = 2;
  string resource = 3;
  string action = 4;
  string ptype = 5; // 对应 DB 列 permission.type
  string name = 6;
  string source = 7; // manual|auto（SDK D 语义）
}
message ListPermissionsRequest { uint64 app_id = 1; }
message ListPermissionsResponse { repeated PermissionSummary permissions = 1; }

message GrantSummary {
  int64 grant_id = 1;
  int64 role_id = 2;
  int64 permission_id = 3;
  string eft = 4; // allow|deny
}
message ListGrantsRequest {
  uint64 app_id = 1;
  int64 role_id = 2; // 0 = 全部
}
message ListGrantsResponse { repeated GrantSummary grants = 1; }

message RoleInheritanceSummary {
  int64 inheritance_id = 1;
  int64 parent_role_id = 2;
  int64 child_role_id = 3;
}
message ListRoleInheritancesRequest { uint64 app_id = 1; }
message ListRoleInheritancesResponse { repeated RoleInheritanceSummary inheritances = 1; }

message UserBindingSummary {
  int64 binding_id = 1;
  string user_id = 2;
  int64 role_id = 3;
}
message ListUserBindingsRequest {
  uint64 app_id = 1;
  string user_id = 2; // "" = 全部
}
message ListUserBindingsResponse { repeated UserBindingSummary bindings = 1; }

message DataPolicySummary {
  int64 data_policy_id = 1;
  string subject_type = 2;
  string subject_id = 3;
  string resource = 4;
  string condition = 5; // 原始 JSON 串
  string effect = 6;    // allow|deny
  uint64 version = 7;
}
message ListDataPoliciesRequest {
  uint64 app_id = 1;
  string resource = 2; // "" = 全部
}
message ListDataPoliciesResponse { repeated DataPolicySummary data_policies = 1; }

// —— SP1 读面消息（system-域 2 个）——
message OperatorSummary { // 永不含 secret
  int64 operator_id = 1;
  string principal = 2;
  uint32 status = 3;
}
message ListOperatorsRequest {}
message ListOperatorsResponse { repeated OperatorSummary operators = 1; }

message AdminRoleSummary {
  int64 role_id = 1;
  string code = 2;
  string name = 3;
}
message ListAdminRolesRequest {}
message ListAdminRolesResponse { repeated AdminRoleSummary roles = 1; }
```

- [ ] **步骤 5：重新生成**

运行：`make proto-gen`
预期：buf lint 通过 + 生成 `gen/sydom/admin/v1/*` 更新。
若报 `protoc-gen-go: program not found`，先跑 `make proto-tools` 再重跑 `make proto-gen`。

- [ ] **步骤 6：验证生成物一致 + 编译通过**

运行：`make proto-check && go build ./internal/controlplane/mgmt/...`
预期：`git diff --exit-code gen/` 退出 0（生成物已落库），build 通过（`ListRolesRequest` 已存在）。

- [ ] **步骤 7：Commit**

```bash
git add api/proto/sydom/admin/v1/admin.proto gen/sydom/admin/v1/ internal/controlplane/mgmt/proto_smoke_test.go
git commit -m "feat(admin): SP1 读面 proto 契约（8 个 List RPC + 消息）

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 2：app-域 6 个读实现 + ruleTable + 读正确性测试

**文件：**
- 创建：`internal/controlplane/mgmt/admin_reads.go`
- 修改：`internal/controlplane/mgmt/authz.go`（`ruleTable`，约 `authz.go:48` 之后插入）
- 测试：`internal/controlplane/mgmt/admin_reads_test.go`

- [ ] **步骤 1：编写失败的测试（读正确性 round-trip + 空集 + 可选过滤）**

创建 `admin_reads_test.go`：

```go
package mgmt_test

import (
	"context"
	"testing"
	"time"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestAdminReads_AppDomain_RoundTrip(t *testing.T) {
	db := dbtest.SetupSchema(t)
	require.NoError(t, adminauthz.EnsureRootOperator(context.Background(), db, mk(), "root", []byte("root-secret")))
	cli := dialMgmt(t, db, "root", []byte("root-secret"))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	app, err := cli.CreateApplication(ctx, &adminv1.CreateApplicationRequest{
		TenantName: "t", Domain: "d", Name: "n", AppKey: "k-roundtrip"})
	require.NoError(t, err)
	appID := app.AppId

	mgrRole, err := cli.CreateRole(ctx, &adminv1.CreateRoleRequest{AppId: appID, Code: "manager", Name: "经理"})
	require.NoError(t, err)
	clerkRole, err := cli.CreateRole(ctx, &adminv1.CreateRoleRequest{AppId: appID, Code: "clerk", Name: "店员"})
	require.NoError(t, err)
	perm, err := cli.UpsertPermission(ctx, &adminv1.UpsertPermissionRequest{
		AppId: appID, Code: "order:read", Resource: "order", Action: "read", Ptype: "api", Name: "读订单"})
	require.NoError(t, err)
	_, err = cli.GrantPermission(ctx, &adminv1.GrantPermissionRequest{
		AppId: appID, RoleId: mgrRole.RoleId, PermissionId: perm.PermissionId, Eft: "allow"})
	require.NoError(t, err)
	_, err = cli.AddRoleInheritance(ctx, &adminv1.RoleInheritanceRequest{
		AppId: appID, ChildRoleId: clerkRole.RoleId, ParentRoleId: mgrRole.RoleId})
	require.NoError(t, err)
	_, err = cli.BindUserRole(ctx, &adminv1.UserRoleRequest{AppId: appID, UserId: "alice", RoleId: mgrRole.RoleId})
	require.NoError(t, err)
	_, err = cli.UpsertDataPolicy(ctx, &adminv1.UpsertDataPolicyRequest{
		AppId: appID, Id: 0, SubjectType: "role", SubjectId: "manager", Resource: "order",
		Condition: `{"field":"dept","op":"EQ","value":"x"}`, Effect: "allow"})
	require.NoError(t, err)

	roles, err := cli.ListRoles(ctx, &adminv1.ListRolesRequest{AppId: appID})
	require.NoError(t, err)
	require.Len(t, roles.Roles, 2)

	perms, err := cli.ListPermissions(ctx, &adminv1.ListPermissionsRequest{AppId: appID})
	require.NoError(t, err)
	require.Len(t, perms.Permissions, 1)
	require.Equal(t, "order:read", perms.Permissions[0].Code)
	require.Equal(t, "manual", perms.Permissions[0].Source) // 写面默认 source

	grants, err := cli.ListGrants(ctx, &adminv1.ListGrantsRequest{AppId: appID})
	require.NoError(t, err)
	require.Len(t, grants.Grants, 1)
	require.Equal(t, "allow", grants.Grants[0].Eft)
	g2, err := cli.ListGrants(ctx, &adminv1.ListGrantsRequest{AppId: appID, RoleId: clerkRole.RoleId})
	require.NoError(t, err)
	require.Empty(t, g2.Grants) // 过滤：clerk 无授权

	inh, err := cli.ListRoleInheritances(ctx, &adminv1.ListRoleInheritancesRequest{AppId: appID})
	require.NoError(t, err)
	require.Len(t, inh.Inheritances, 1)
	require.Equal(t, mgrRole.RoleId, inh.Inheritances[0].ParentRoleId)
	require.Equal(t, clerkRole.RoleId, inh.Inheritances[0].ChildRoleId)

	binds, err := cli.ListUserBindings(ctx, &adminv1.ListUserBindingsRequest{AppId: appID})
	require.NoError(t, err)
	require.Len(t, binds.Bindings, 1)
	require.Equal(t, "alice", binds.Bindings[0].UserId)
	b2, err := cli.ListUserBindings(ctx, &adminv1.ListUserBindingsRequest{AppId: appID, UserId: "nobody"})
	require.NoError(t, err)
	require.Empty(t, b2.Bindings) // 过滤：无此用户

	dps, err := cli.ListDataPolicies(ctx, &adminv1.ListDataPoliciesRequest{AppId: appID})
	require.NoError(t, err)
	require.Len(t, dps.DataPolicies, 1)
	require.Equal(t, "order", dps.DataPolicies[0].Resource)
	require.Equal(t, "allow", dps.DataPolicies[0].Effect)
	require.Greater(t, dps.DataPolicies[0].Version, uint64(0))
	require.JSONEq(t, `{"field":"dept","op":"EQ","value":"x"}`, dps.DataPolicies[0].Condition)
	dpEmpty, err := cli.ListDataPolicies(ctx, &adminv1.ListDataPoliciesRequest{AppId: appID, Resource: "other"})
	require.NoError(t, err)
	require.Empty(t, dpEmpty.DataPolicies) // 过滤：无此资源

	// 空集：新 app 无任何角色 → 空切片、nil error（非 NotFound）
	app2, err := cli.CreateApplication(ctx, &adminv1.CreateApplicationRequest{
		TenantName: "t2", Domain: "d2", Name: "n2", AppKey: "k-empty"})
	require.NoError(t, err)
	empty, err := cli.ListRoles(ctx, &adminv1.ListRolesRequest{AppId: app2.AppId})
	require.NoError(t, err)
	require.Empty(t, empty.Roles)
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/controlplane/mgmt/ -run TestAdminReads_AppDomain_RoundTrip -count=1`
预期：FAIL —— 编译期 `cli.ListRoles undefined`（`AdminServiceClient` 接口已有方法，但服务端未实现 ⇒ 实际运行返回 `Unimplemented`）。先确保至少能编译失败或运行得到 `codes.Unimplemented`。

- [ ] **步骤 3：创建 admin_reads.go（6 个 app-域读方法）**

```go
package mgmt

import (
	"context"
	"database/sql"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *AdminServer) ListRoles(ctx context.Context, r *adminv1.ListRolesRequest) (*adminv1.ListRolesResponse, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, code, name, COALESCE(description,'') FROM role WHERE app_id=$1 ORDER BY id`, int64(r.AppId))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list roles: %v", err)
	}
	defer rows.Close()
	out := &adminv1.ListRolesResponse{}
	for rows.Next() {
		var x adminv1.RoleSummary
		if err := rows.Scan(&x.RoleId, &x.Code, &x.Name, &x.Description); err != nil {
			return nil, status.Errorf(codes.Internal, "scan role: %v", err)
		}
		out.Roles = append(out.Roles, &x)
	}
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "rows role: %v", err)
	}
	return out, nil
}

func (s *AdminServer) ListPermissions(ctx context.Context, r *adminv1.ListPermissionsRequest) (*adminv1.ListPermissionsResponse, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, code, resource, action, type, name, source FROM permission WHERE app_id=$1 ORDER BY id`, int64(r.AppId))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list permissions: %v", err)
	}
	defer rows.Close()
	out := &adminv1.ListPermissionsResponse{}
	for rows.Next() {
		var x adminv1.PermissionSummary
		if err := rows.Scan(&x.PermissionId, &x.Code, &x.Resource, &x.Action, &x.Ptype, &x.Name, &x.Source); err != nil {
			return nil, status.Errorf(codes.Internal, "scan permission: %v", err)
		}
		out.Permissions = append(out.Permissions, &x)
	}
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "rows permission: %v", err)
	}
	return out, nil
}

func (s *AdminServer) ListGrants(ctx context.Context, r *adminv1.ListGrantsRequest) (*adminv1.ListGrantsResponse, error) {
	var rows *sql.Rows
	var err error
	if r.RoleId == 0 {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, role_id, permission_id, eft FROM role_permission WHERE app_id=$1 ORDER BY id`, int64(r.AppId))
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, role_id, permission_id, eft FROM role_permission WHERE app_id=$1 AND role_id=$2 ORDER BY id`, int64(r.AppId), r.RoleId)
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list grants: %v", err)
	}
	defer rows.Close()
	out := &adminv1.ListGrantsResponse{}
	for rows.Next() {
		var x adminv1.GrantSummary
		if err := rows.Scan(&x.GrantId, &x.RoleId, &x.PermissionId, &x.Eft); err != nil {
			return nil, status.Errorf(codes.Internal, "scan grant: %v", err)
		}
		out.Grants = append(out.Grants, &x)
	}
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "rows grant: %v", err)
	}
	return out, nil
}

func (s *AdminServer) ListRoleInheritances(ctx context.Context, r *adminv1.ListRoleInheritancesRequest) (*adminv1.ListRoleInheritancesResponse, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, parent_role_id, child_role_id FROM role_inheritance WHERE app_id=$1 ORDER BY id`, int64(r.AppId))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list inheritances: %v", err)
	}
	defer rows.Close()
	out := &adminv1.ListRoleInheritancesResponse{}
	for rows.Next() {
		var x adminv1.RoleInheritanceSummary
		if err := rows.Scan(&x.InheritanceId, &x.ParentRoleId, &x.ChildRoleId); err != nil {
			return nil, status.Errorf(codes.Internal, "scan inheritance: %v", err)
		}
		out.Inheritances = append(out.Inheritances, &x)
	}
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "rows inheritance: %v", err)
	}
	return out, nil
}

func (s *AdminServer) ListUserBindings(ctx context.Context, r *adminv1.ListUserBindingsRequest) (*adminv1.ListUserBindingsResponse, error) {
	var rows *sql.Rows
	var err error
	if r.UserId == "" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, user_id, role_id FROM user_role_binding WHERE app_id=$1 ORDER BY id`, int64(r.AppId))
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, user_id, role_id FROM user_role_binding WHERE app_id=$1 AND user_id=$2 ORDER BY id`, int64(r.AppId), r.UserId)
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list user bindings: %v", err)
	}
	defer rows.Close()
	out := &adminv1.ListUserBindingsResponse{}
	for rows.Next() {
		var x adminv1.UserBindingSummary
		if err := rows.Scan(&x.BindingId, &x.UserId, &x.RoleId); err != nil {
			return nil, status.Errorf(codes.Internal, "scan binding: %v", err)
		}
		out.Bindings = append(out.Bindings, &x)
	}
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "rows binding: %v", err)
	}
	return out, nil
}

func (s *AdminServer) ListDataPolicies(ctx context.Context, r *adminv1.ListDataPoliciesRequest) (*adminv1.ListDataPoliciesResponse, error) {
	var rows *sql.Rows
	var err error
	if r.Resource == "" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, subject_type, subject_id, resource, condition::text, effect, version FROM data_policy WHERE app_id=$1 ORDER BY id`, int64(r.AppId))
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, subject_type, subject_id, resource, condition::text, effect, version FROM data_policy WHERE app_id=$1 AND resource=$2 ORDER BY id`, int64(r.AppId), r.Resource)
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list data policies: %v", err)
	}
	defer rows.Close()
	out := &adminv1.ListDataPoliciesResponse{}
	for rows.Next() {
		var x adminv1.DataPolicySummary
		var ver int64
		if err := rows.Scan(&x.DataPolicyId, &x.SubjectType, &x.SubjectId, &x.Resource, &x.Condition, &x.Effect, &ver); err != nil {
			return nil, status.Errorf(codes.Internal, "scan data policy: %v", err)
		}
		x.Version = uint64(ver)
		out.DataPolicies = append(out.DataPolicies, &x)
	}
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "rows data policy: %v", err)
	}
	return out, nil
}
```

- [ ] **步骤 4：扩 ruleTable（authz.go）—— 加 6 条 app-域 read 规则**

在 `internal/controlplane/mgmt/authz.go` 的 `ruleTable` map 内、`"/sydom.admin.v1.AdminService/BindOperatorRole": ...` 行之后插入：

```go
	"/sydom.admin.v1.AdminService/ListRoles":            {"role", "read", false, false},
	"/sydom.admin.v1.AdminService/ListPermissions":      {"permission", "read", false, false},
	"/sydom.admin.v1.AdminService/ListGrants":           {"grant", "read", false, false},
	"/sydom.admin.v1.AdminService/ListRoleInheritances": {"inheritance", "read", false, false},
	"/sydom.admin.v1.AdminService/ListUserBindings":     {"binding", "read", false, false},
	"/sydom.admin.v1.AdminService/ListDataPolicies":     {"data_policy", "read", false, false},
```

- [ ] **步骤 5：运行测试验证通过**

运行：`go test ./internal/controlplane/mgmt/ -run TestAdminReads_AppDomain_RoundTrip -count=1`
预期：PASS。

- [ ] **步骤 6：Commit**

```bash
git add internal/controlplane/mgmt/admin_reads.go internal/controlplane/mgmt/authz.go internal/controlplane/mgmt/admin_reads_test.go
git commit -m "feat(admin): SP1 app-域 6 个读 RPC + ruleTable read 规则

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 3：system-域 2 个读实现 + ruleTable + secret 不泄露测试

**文件：**
- 修改：`internal/controlplane/mgmt/admin_reads.go`（追加 2 方法）
- 修改：`internal/controlplane/mgmt/authz.go`（`ruleTable` +2 条）
- 测试：`internal/controlplane/mgmt/admin_reads_test.go`（追加）

- [ ] **步骤 1：编写失败的测试（operators/admin-roles + secret 不泄露）**

在 `admin_reads_test.go` 追加：

```go
func TestAdminReads_SystemDomain_NoSecret(t *testing.T) {
	db := dbtest.SetupSchema(t)
	require.NoError(t, adminauthz.EnsureRootOperator(context.Background(), db, mk(), "root", []byte("root-secret")))
	cli := dialMgmt(t, db, "root", []byte("root-secret"))
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	_, err := cli.CreateOperator(ctx, &adminv1.CreateOperatorRequest{Principal: "op-2"})
	require.NoError(t, err)

	ops, err := cli.ListOperators(ctx, &adminv1.ListOperatorsRequest{})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(ops.Operators), 2) // root + op-2
	var principals []string
	for _, o := range ops.Operators {
		require.NotZero(t, o.OperatorId)
		require.NotEmpty(t, o.Principal)
		require.Contains(t, []uint32{1, 2}, o.Status)
		principals = append(principals, o.Principal)
	}
	require.Contains(t, principals, "root")
	require.Contains(t, principals, "op-2")
	// secret 不泄露由 OperatorSummary 结构保证（无 secret 字段）；此处再断言 principal/status 正确即可。

	roles, err := cli.ListAdminRoles(ctx, &adminv1.ListAdminRolesRequest{})
	require.NoError(t, err)
	var roleCodes []string
	for _, r := range roles.Roles {
		roleCodes = append(roleCodes, r.Code)
	}
	require.Contains(t, roleCodes, "super-admin") // 000013 内置
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/controlplane/mgmt/ -run TestAdminReads_SystemDomain_NoSecret -count=1`
预期：FAIL —— `ListOperators` 服务端未实现，返回 `codes.Unimplemented`。

- [ ] **步骤 3：追加 admin_reads.go（2 个 system-域读方法）**

在 `admin_reads.go` 末尾追加：

```go
func (s *AdminServer) ListOperators(ctx context.Context, _ *adminv1.ListOperatorsRequest) (*adminv1.ListOperatorsResponse, error) {
	// 只 SELECT id/principal/status —— secret_enc 绝不出查询，物理保证不泄露。
	rows, err := s.db.QueryContext(ctx, `SELECT id, principal, status FROM admin_operator ORDER BY id`)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list operators: %v", err)
	}
	defer rows.Close()
	out := &adminv1.ListOperatorsResponse{}
	for rows.Next() {
		var x adminv1.OperatorSummary
		var st int16
		if err := rows.Scan(&x.OperatorId, &x.Principal, &st); err != nil {
			return nil, status.Errorf(codes.Internal, "scan operator: %v", err)
		}
		x.Status = uint32(st)
		out.Operators = append(out.Operators, &x)
	}
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "rows operator: %v", err)
	}
	return out, nil
}

func (s *AdminServer) ListAdminRoles(ctx context.Context, _ *adminv1.ListAdminRolesRequest) (*adminv1.ListAdminRolesResponse, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, code, name FROM admin_role ORDER BY id`)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list admin roles: %v", err)
	}
	defer rows.Close()
	out := &adminv1.ListAdminRolesResponse{}
	for rows.Next() {
		var x adminv1.AdminRoleSummary
		if err := rows.Scan(&x.RoleId, &x.Code, &x.Name); err != nil {
			return nil, status.Errorf(codes.Internal, "scan admin role: %v", err)
		}
		out.Roles = append(out.Roles, &x)
	}
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "rows admin role: %v", err)
	}
	return out, nil
}
```

- [ ] **步骤 4：扩 ruleTable（authz.go）—— 加 2 条 system-域 read 规则**

在 `ruleTable` 中、任务 2 插入的 6 条之后追加：

```go
	"/sydom.admin.v1.AdminService/ListOperators":  {"admin", "read", false, true},
	"/sydom.admin.v1.AdminService/ListAdminRoles": {"admin", "read", false, true},
```

- [ ] **步骤 5：运行测试验证通过**

运行：`go test ./internal/controlplane/mgmt/ -run TestAdminReads_SystemDomain_NoSecret -count=1`
预期：PASS。

- [ ] **步骤 6：Commit**

```bash
git add internal/controlplane/mgmt/admin_reads.go internal/controlplane/mgmt/authz.go internal/controlplane/mgmt/admin_reads_test.go
git commit -m "feat(admin): SP1 system-域 2 个读 RPC（operators/admin-roles，secret 不泄露）

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 4：跨 app 域隔离 + 细粒度资源鉴权 测试

**文件：**
- 测试：`internal/controlplane/mgmt/admin_reads_test.go`（追加）

本任务无新生产代码，是钉死安全红线 ② 的不变量测试（隔离/鉴权全靠现有拦截器链 + 任务 2/3 的 ruleTable）。

- [ ] **步骤 1：编写隔离测试**

在 `admin_reads_test.go` 追加（顶部 import 需含 `"google.golang.org/grpc/codes"` 与 `"google.golang.org/grpc/status"` 与 `"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"`）：

```go
func TestAdminReads_CrossAppDomainDenied(t *testing.T) {
	db := dbtest.SetupSchema(t)
	require.NoError(t, adminauthz.EnsureRootOperator(context.Background(), db, mk(), "root", []byte("root-secret")))
	root := dialMgmt(t, db, "root", []byte("root-secret"))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	appA, err := root.CreateApplication(ctx, &adminv1.CreateApplicationRequest{
		TenantName: "ta", Domain: "da", Name: "na", AppKey: "k-a"})
	require.NoError(t, err)
	appB, err := root.CreateApplication(ctx, &adminv1.CreateApplicationRequest{
		TenantName: "tb", Domain: "db", Name: "nb", AppKey: "k-b"})
	require.NoError(t, err)

	// reader：仅在域 A 有 role/read（细粒度）
	op, err := root.CreateOperator(ctx, &adminv1.CreateOperatorRequest{Principal: "reader"})
	require.NoError(t, err)
	ar, err := root.CreateAdminRole(ctx, &adminv1.CreateAdminRoleRequest{Code: "reader-role", Name: "只读"})
	require.NoError(t, err)
	domainA := mgmt.DomainOfAppID(int64(appA.AppId))
	_, err = root.GrantAdminRole(ctx, &adminv1.GrantAdminRoleRequest{
		RoleId: ar.RoleId, Domain: domainA, Resource: "role", Action: "read"})
	require.NoError(t, err)
	_, err = root.BindOperatorRole(ctx, &adminv1.BindOperatorRoleRequest{
		OperatorId: op.OperatorId, RoleId: ar.RoleId, Domain: domainA})
	require.NoError(t, err)

	// 绑定/授权完成后再拨号 reader（dialMgmt 此刻构造的 enforcer 已含上述策略）
	reader := dialMgmt(t, db, "reader", []byte(op.Secret))

	// 域 A 的 role：放行
	_, err = reader.ListRoles(ctx, &adminv1.ListRolesRequest{AppId: appA.AppId})
	require.NoError(t, err)
	// 域 B 的 role：跨域拒绝
	_, err = reader.ListRoles(ctx, &adminv1.ListRolesRequest{AppId: appB.AppId})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	// 域 A 的 permission（reader 只有 role/read）：细粒度资源拒绝
	_, err = reader.ListPermissions(ctx, &adminv1.ListPermissionsRequest{AppId: appA.AppId})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	// system 域 ListOperators（reader 无 admin/read）：拒绝
	_, err = reader.ListOperators(ctx, &adminv1.ListOperatorsRequest{})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}
```

- [ ] **步骤 2：运行测试验证通过**

运行：`go test ./internal/controlplane/mgmt/ -run TestAdminReads_CrossAppDomainDenied -count=1`
预期：PASS（放行 1 处、拒绝 3 处均符合）。
若 `ListRoles(appA)` 意外被拒：确认 reader 在 dialMgmt 之前已完成 GrantAdminRole+BindOperatorRole（enforcer 快照在构造时加载）。

- [ ] **步骤 3：Commit**

```bash
git add internal/controlplane/mgmt/admin_reads_test.go
git commit -m "test(admin): SP1 读面跨 app 域隔离 + 细粒度资源鉴权

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 5：全量验证门 + 收尾

**文件：** 无新增；仅跑验证、必要时修 gofmt/vet。

- [ ] **步骤 1：格式 + 静态检查**

运行：
```bash
gofmt -l internal/controlplane/mgmt/
go vet ./internal/controlplane/mgmt/...
```
预期：`gofmt -l` 无输出；`go vet` 无报错。有 gofmt 差异则 `gofmt -w` 后重跑。

- [ ] **步骤 2：proto 漂移检测**

运行：`make proto-check`
预期：退出 0（生成物与 proto 同步且已入库）。

- [ ] **步骤 3：全量构建**

运行：`go build ./...`
预期：通过。

- [ ] **步骤 4：稳定性测试（跑两遍）**

运行：`go test ./internal/controlplane/mgmt/ -count=2`
预期：全 PASS（含读正确性/system 域/跨域隔离三组新测试 + 既有写面测试）。

- [ ] **步骤 5：（可选）全仓回归**

运行：`go test ./... -count=1`
预期：全 PASS（确认未破坏其他包）。

- [ ] **步骤 6：收尾 commit（若步骤 1 有格式修订）**

```bash
git add -A
git commit -m "chore(admin): SP1 读面验证门通过（gofmt/vet/proto-check/build/test）

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 自检结论

- **规格覆盖度：** 8 个 RPC（任务 1 契约 / 任务 2 app-域 6 / 任务 3 system-域 2）✓；ruleTable read 规则（任务 2+3）✓；安全红线 ①secret 不泄露（任务 3）②跨域隔离（任务 4）③读不受 status 写拦截（`isWrite:false`，任务 2/3 规则即体现）✓；读正确性含 source/condition/effect/version + 可选过滤 + 空集（任务 2）✓；验证门（任务 5）✓。
- **类型一致性：** `ListRolesRequest/Response`、`RoleSummary.RoleId/Code/Name/Description`、`PermissionSummary.Ptype/Source`、`GrantSummary.GrantId/Eft`、`DataPolicySummary.Condition/Effect/Version`、`OperatorSummary.OperatorId/Principal/Status`、`AdminRoleSummary.Code` 在 proto（任务 1）与实现/测试（任务 2-4）中逐一对齐。`mgmt.DomainOfAppID`、`dialMgmt`、`mk`、`dbtest.*`、`adminauthz.EnsureRootOperator` 均为既有符号。
- **占位符：** 无 TODO/待定；每步含完整代码或精确命令 + 预期输出。
