# 司域 M2.1 实现计划 · 撤权对称 + Secret 硬切换

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 补齐 system 域撤权对偶（`RevokeAdminGrant` / `UnbindOperatorRole`）与 Secret 硬切换（`RotateApplicationSecret` / `ResetOperatorSecret`）四个新 RPC，三面 parity（gRPC + REST + Console），形成「授→撤、发→轮」安全闭环。

**架构：** 镜像既有授权对偶的原子事务模板——撤权走 `BeginTx → Delete*（0 行→NotFound 回滚）→ BumpPolicyVersion → Commit`（必 bump，撤权立即生效）；Secret 硬切换走 `genSecret → encrypt → 单条 UPDATE secret_enc（0 行→NotFound）→ 返回新 secret`（不 bump，解析器每请求查库即时生效）。三面共用唯一真相源 `mgmt.AuthorizeRule` + `ruleTable`，物理不可能策略漂移。

**技术栈：** Go、protobuf/buf（`make proto-gen`/`proto-check`）、casbin v3.10.0、`crypto.Encrypt`（AES-256-GCM）、net/http（REST 网关 + Console BFF）、html/template、PostgreSQL、`dbtest` 集成测试（真实 DB + bufconn gRPC + 真实 HMAC）。

**对应规格：** `docs/superpowers/specs/2026-06-16-sydom-m2-1-revocation-secret-symmetry-design.md`（决策 a/b/c、MS-1..MS-6）。

---

## 文件结构

| 文件 | 职责 | 任务 |
|---|---|---|
| `api/proto/sydom/admin/v1/admin.proto` | 4 RPC + 6 message 契约 | 1 |
| `gen/sydom/admin/v1/*.pb.go`（生成） | `make proto-gen` 产出，入库 | 1 |
| `internal/controlplane/adminauthz/store.go` | `ErrNotFound` 哨兵 + `DeleteRoleGrant` + `DeleteSubjectRole`（镜像 Insert*） | 2 |
| `internal/controlplane/adminauthz/store_test.go` | Delete* 单元测试（直连 DB） | 2 |
| `internal/controlplane/mgmt/admin_ops.go` | 4 个 handler（2 撤权 + 2 secret） | 3, 4 |
| `internal/controlplane/mgmt/authz.go` | ruleTable 增量 4 条 | 3, 4 |
| `internal/controlplane/mgmt/revoke_test.go`（新建） | 撤权集成测试（真实 Enforce 证 bump、NotFound、跨租户 403） | 3 |
| `internal/controlplane/mgmt/secret_rotate_test.go`（新建） | Secret 硬切换集成测试（旧失效/新可用、NotFound、disabled 可轮换） | 4 |
| `internal/controlplane/restgw/routes.go` | REST 4 路由（system 3 + application 1） | 5 |
| `internal/controlplane/restgw/routes_secret_revoke_test.go`（新建） | REST parity 测试 | 5 |
| `internal/controlplane/console/routes_system.go` | Console 撤权/解绑/重置 secret 3 handler + 注册 | 6 |
| `internal/controlplane/console/routes_apps.go` | Console 轮换 app secret handler + 注册 | 6 |
| `internal/controlplane/console/templates/{operators,admin_roles,dashboard}.html` | 加撤权/解绑/轮换 表单按钮 | 6 |
| `internal/controlplane/console/templates/{operator_secret_reset,app_secret_rotated}.html`（新建） | 一次性 secret 展示页 | 6 |
| `internal/controlplane/console/routes_secret_revoke_test.go`（新建） | Console parity 测试（CSRF/authz/一次性 secret 不 PRG） | 6 |

**关键决策（已在源码核实）：**
- **请求消息全部新建专用**（`RevokeAdminGrantRequest` 等），不复用 `GrantAdminRoleRequest`：既有共享请求消息（`UserRoleRequest`/`RoleInheritanceRequest`）名称中性，而 `GrantAdminRoleRequest` 名称非中性，复用读着别扭；新建专用最清晰。`buf.yaml` 已 except `RPC_REQUEST_RESPONSE_UNIQUE` / `RPC_REQUEST_STANDARD_NAME` / `RPC_RESPONSE_STANDARD_NAME`，故新建 + 复用 `WriteResponse` 均无 lint 问题。
- **Secret handler 不开事务**（偏离 spec §4.2 的 `BeginTx`）：硬切换是「encrypt 后单条 UPDATE，不 bump」，单语句本身原子、无第二条语句，无需事务。镜像最近的同胞 `SetApplicationStatus`（单 `ExecContext` + `RowsAffected==0→NotFound`，无 tx）。撤权仍开事务（因含 Delete + Bump 两条语句）。
- **撤权 NotFound 走 admin_ops.go 显式映射**（非 `writeResp`）：`writeResp` 把一切错误压成 `Internal`（既有债，带 TODO）；而 admin_ops.go 的同胞（`SetApplicationStatus`/`SetOperatorStatus`）显式返回 `codes.NotFound`。4 个新 handler 写在 admin_ops.go，遵循 admin_ops.go 范式：显式 `NotFound`。
- **「撤未知 app」的错误码差异**：`RotateApplicationSecret` 是 `scopeApp`，`AuthorizeRule` 对不存在的 app 经 `TenantDomainOf` fail-close 返回 `PermissionDenied`（不泄露存在性），故「轮换未知 app」实际返回 `PermissionDenied` 而非 `NotFound`（handler 内 `RowsAffected==0→NotFound` 是防御深度，对真不存在的 app 不可达）。`ResetOperatorSecret`/`RevokeAdminGrant`/`UnbindOperatorRole` 是 `scopeSystem`，无 per-entity 存在性校验，故「重置/撤未知 id」→ `NotFound`。测试据此分别断言。

---

## 任务 1：Proto 契约（4 RPC + 6 message）

**文件：**
- 修改：`api/proto/sydom/admin/v1/admin.proto`（service 块 line 34 后；message 区 line 190 后）
- 生成：`gen/sydom/admin/v1/*.pb.go`（`make proto-gen`）

- [ ] **步骤 1：在 service AdminService 块加 4 个 RPC**

在 `rpc BindOperatorRole(BindOperatorRoleRequest) returns (WriteResponse);`（约 line 34）之后插入：

```proto
  // —— M2.1 撤权对称 + Secret 硬切换 ——
  rpc RevokeAdminGrant(RevokeAdminGrantRequest) returns (WriteResponse);
  rpc UnbindOperatorRole(UnbindOperatorRoleRequest) returns (WriteResponse);
  rpc RotateApplicationSecret(RotateApplicationSecretRequest) returns (RotateApplicationSecretResponse);
  rpc ResetOperatorSecret(ResetOperatorSecretRequest) returns (ResetOperatorSecretResponse);
```

- [ ] **步骤 2：在 message 区加 6 个 message**

在 `message BindOperatorRoleRequest { ... }`（约 line 190）之后插入：

```proto
// —— M2.1 撤权对称 + Secret 硬切换 ——
message RevokeAdminGrantRequest {
  int64 role_id = 1;
  string domain = 2;   // app_id 字符串或 "*"
  string resource = 3;
  string action = 4;
}
message UnbindOperatorRoleRequest {
  int64 operator_id = 1;
  int64 role_id = 2;
  string domain = 3;
}
message RotateApplicationSecretRequest { uint64 app_id = 1; }
message RotateApplicationSecretResponse { string app_secret = 1; } // 明文仅此一次返回，服务端只存加密
message ResetOperatorSecretRequest { int64 operator_id = 1; }
message ResetOperatorSecretResponse { string secret = 1; } // 明文仅此一次返回
```

- [ ] **步骤 3：生成代码并验证无漂移**

运行：`make proto-gen && make proto-check`
预期：`proto-lint` PASS（buf.yaml 已 except 相关规则）；`proto-gen` 重新生成 `gen/`；`proto-check` 的 `git diff --exit-code gen/` 在 `git add` 后为空（生成代码已入库）。
若 `make proto-tools` 未装：先 `make proto-tools`。

- [ ] **步骤 4：确认全仓编译（Unimplemented 默认兜底）**

运行：`go build ./...`
预期：PASS。`mgmt.AdminServer` 内嵌 `adminv1.UnimplementedAdminServiceServer`，新 RPC 在 handler 落地前由默认实现兜底，**不破坏编译**。

- [ ] **步骤 5：Commit**

```bash
git add api/proto/sydom/admin/v1/admin.proto gen/
git commit -m "feat(proto): M2.1 撤权对称 + Secret 硬切换 4 RPC 契约"
```

---

## 任务 2：adminauthz 存储层 Delete*（TDD）

**文件：**
- 修改：`internal/controlplane/adminauthz/store.go`（加 `errors` import、`ErrNotFound`、两个 Delete 函数）
- 测试：`internal/controlplane/adminauthz/store_test.go`（`package adminauthz_test`）

- [ ] **步骤 1：编写失败的测试**

追加到 `internal/controlplane/adminauthz/store_test.go`：

```go
func TestDeleteRoleGrant_RemovesThenReportsMissing(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	roleID, err := adminauthz.InsertRole(ctx, db, "app9-admin", "App9 管理员")
	require.NoError(t, err)
	require.NoError(t, adminauthz.InsertRoleGrant(ctx, db, roleID, "9", "role", "create"))

	// 命中删除：成功，且行确实消失。
	require.NoError(t, adminauthz.DeleteRoleGrant(ctx, db, roleID, "9", "role", "create"))
	rows, err := adminauthz.LoadPolicyRows(ctx, db)
	require.NoError(t, err)
	require.NotContains(t, rows, []string{"app9-admin", "9", "role", "create"})

	// 再删（已不存在）：ErrNotFound（fail-close，不静默）。
	require.ErrorIs(t, adminauthz.DeleteRoleGrant(ctx, db, roleID, "9", "role", "create"), adminauthz.ErrNotFound)
}

func TestDeleteSubjectRole_RemovesThenReportsMissing(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	opID, err := adminauthz.InsertOperator(ctx, db, "carol", []byte("enc"))
	require.NoError(t, err)
	roleID, err := adminauthz.InsertRole(ctx, db, "app9-admin2", "n")
	require.NoError(t, err)
	require.NoError(t, adminauthz.InsertSubjectRole(ctx, db, opID, roleID, "9"))

	require.NoError(t, adminauthz.DeleteSubjectRole(ctx, db, opID, roleID, "9"))
	gRows, err := adminauthz.LoadGroupingRows(ctx, db)
	require.NoError(t, err)
	require.NotContains(t, gRows, []string{"carol", "app9-admin2", "9"})

	require.ErrorIs(t, adminauthz.DeleteSubjectRole(ctx, db, opID, roleID, "9"), adminauthz.ErrNotFound)
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/controlplane/adminauthz/ -run 'TestDelete(RoleGrant|SubjectRole)' -v`
预期：编译失败 / FAIL —— `undefined: adminauthz.DeleteRoleGrant` 等。

- [ ] **步骤 3：实现 ErrNotFound + 两个 Delete 函数**

在 `internal/controlplane/adminauthz/store.go`，把 import 块从

```go
import (
	"context"
	"fmt"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
)
```

改为加入 `errors`：

```go
import (
	"context"
	"errors"
	"fmt"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
)
```

紧接 import 块之后加哨兵：

```go
// ErrNotFound 表示 Delete* 未命中任何行。fail-close：撤不存在的授权/绑定时，
// 上层据此映射 NotFound、回滚事务、绝不 bump 版本（防幽灵 delta / 版本跳变）。
var ErrNotFound = errors.New("adminauthz: not found")
```

在 `InsertSubjectRole` 之后（约 line 57 后）加两个 Delete：

```go
// DeleteRoleGrant 撤角色一条管理权（casbin p 行），镜像 InsertRoleGrant。
// 命中 0 行 → ErrNotFound（不静默）。
func DeleteRoleGrant(ctx context.Context, q cp.DBTX, roleID int64, domain, resource, action string) error {
	res, err := q.ExecContext(ctx,
		`DELETE FROM admin_role_grant WHERE role_id=$1 AND domain=$2 AND resource=$3 AND action=$4`,
		roleID, domain, resource, action)
	if err != nil {
		return fmt.Errorf("adminauthz: delete role grant: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("adminauthz: delete role grant rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteSubjectRole 解绑操作者与角色（casbin g 行），镜像 InsertSubjectRole。
// 命中 0 行 → ErrNotFound。
func DeleteSubjectRole(ctx context.Context, q cp.DBTX, operatorID, roleID int64, domain string) error {
	res, err := q.ExecContext(ctx,
		`DELETE FROM admin_subject_role WHERE operator_id=$1 AND role_id=$2 AND domain=$3`,
		operatorID, roleID, domain)
	if err != nil {
		return fmt.Errorf("adminauthz: delete subject role: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("adminauthz: delete subject role rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/controlplane/adminauthz/ -run 'TestDelete(RoleGrant|SubjectRole)' -v`
预期：PASS（两个测试）。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/adminauthz/store.go internal/controlplane/adminauthz/store_test.go
git commit -m "feat(adminauthz): Delete{RoleGrant,SubjectRole} + ErrNotFound 哨兵(撤权对称存储层)"
```

---

## 任务 3：撤权 handler + ruleTable（TDD）

**文件：**
- 修改：`internal/controlplane/mgmt/admin_ops.go`（加 `RevokeAdminGrant` + `UnbindOperatorRole`）
- 修改：`internal/controlplane/mgmt/authz.go`（ruleTable 加 2 条）
- 测试：`internal/controlplane/mgmt/revoke_test.go`（新建，`package mgmt_test`）

> 注：`admin_ops.go` 已 import `errors` 与 `adminauthz`，无需新增 import。集成测试经 `dialMgmt` 走完整三拦截器 + 真实 HMAC；ruleTable 必须先加这 2 条，否则 `AuthorizeRule` 会以「unknown method」拒绝。

- [ ] **步骤 1：编写失败的测试**

新建 `internal/controlplane/mgmt/revoke_test.go`：

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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// MS-1 + MS-2：撤管理授权后，被撤特权经真实 Enforce 即刻消失；再撤 → NotFound。
func TestRevokeAdminGrant_PrivilegeGoneAndStrictNotFound(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	dom := func() string { return adminDomainOf(appID) } // 见步骤 3 注：用 strconv 即可
	_ = dom
	require.NoError(t, adminauthz.EnsureRootOperator(context.Background(), db, mk(), "root", []byte("root-secret")))
	root := dialMgmt(t, db, "root", []byte("root-secret"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// root 建操作员 alice、角色 r，授 (domain=appID, role/create) 并绑定。
	domain := domainStr(appID)
	op, err := root.CreateOperator(ctx, &adminv1.CreateOperatorRequest{Principal: "alice"})
	require.NoError(t, err)
	role, err := root.CreateAdminRole(ctx, &adminv1.CreateAdminRoleRequest{Code: "app-admin", Name: "n"})
	require.NoError(t, err)
	_, err = root.GrantAdminRole(ctx, &adminv1.GrantAdminRoleRequest{
		RoleId: role.RoleId, Domain: domain, Resource: "role", Action: "create"})
	require.NoError(t, err)
	_, err = root.BindOperatorRole(ctx, &adminv1.BindOperatorRoleRequest{
		OperatorId: op.OperatorId, RoleId: role.RoleId, Domain: domain})
	require.NoError(t, err)

	// alice 现在能在该 app 建角色（特权生效）。
	alice := dialMgmt(t, db, "alice", []byte(op.Secret))
	_, err = alice.CreateRole(ctx, &adminv1.CreateRoleRequest{AppId: uint64(appID), Code: "c1", Name: "n"})
	require.NoError(t, err)

	// root 撤销该授权。
	_, err = root.RevokeAdminGrant(ctx, &adminv1.RevokeAdminGrantRequest{
		RoleId: role.RoleId, Domain: domain, Resource: "role", Action: "create"})
	require.NoError(t, err)

	// MS-1：bump 已触发 enforcer 重载，alice 同一特权下次即被拒。
	_, err = alice.CreateRole(ctx, &adminv1.CreateRoleRequest{AppId: uint64(appID), Code: "c2", Name: "n"})
	require.Equal(t, codes.PermissionDenied, status.Code(err))

	// MS-2：再撤已不存在的授权 → NotFound（严格，不幂等）。
	_, err = root.RevokeAdminGrant(ctx, &adminv1.RevokeAdminGrantRequest{
		RoleId: role.RoleId, Domain: domain, Resource: "role", Action: "create"})
	require.Equal(t, codes.NotFound, status.Code(err))
}

// MS-1 + MS-2：解绑操作员角色后特权即刻消失；再解绑 → NotFound。
func TestUnbindOperatorRole_PrivilegeGoneAndStrictNotFound(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	require.NoError(t, adminauthz.EnsureRootOperator(context.Background(), db, mk(), "root", []byte("root-secret")))
	root := dialMgmt(t, db, "root", []byte("root-secret"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	domain := domainStr(appID)

	op, err := root.CreateOperator(ctx, &adminv1.CreateOperatorRequest{Principal: "bob"})
	require.NoError(t, err)
	role, err := root.CreateAdminRole(ctx, &adminv1.CreateAdminRoleRequest{Code: "app-admin2", Name: "n"})
	require.NoError(t, err)
	_, err = root.GrantAdminRole(ctx, &adminv1.GrantAdminRoleRequest{
		RoleId: role.RoleId, Domain: domain, Resource: "role", Action: "create"})
	require.NoError(t, err)
	_, err = root.BindOperatorRole(ctx, &adminv1.BindOperatorRoleRequest{
		OperatorId: op.OperatorId, RoleId: role.RoleId, Domain: domain})
	require.NoError(t, err)

	bob := dialMgmt(t, db, "bob", []byte(op.Secret))
	_, err = bob.CreateRole(ctx, &adminv1.CreateRoleRequest{AppId: uint64(appID), Code: "x1", Name: "n"})
	require.NoError(t, err)

	_, err = root.UnbindOperatorRole(ctx, &adminv1.UnbindOperatorRoleRequest{
		OperatorId: op.OperatorId, RoleId: role.RoleId, Domain: domain})
	require.NoError(t, err)

	_, err = bob.CreateRole(ctx, &adminv1.CreateRoleRequest{AppId: uint64(appID), Code: "x2", Name: "n"})
	require.Equal(t, codes.PermissionDenied, status.Code(err))

	_, err = root.UnbindOperatorRole(ctx, &adminv1.UnbindOperatorRoleRequest{
		OperatorId: op.OperatorId, RoleId: role.RoleId, Domain: domain})
	require.Equal(t, codes.NotFound, status.Code(err))
}

// MS-6：非超管操作员无 system 域授权 → 撤权被拒 403（PermissionDenied）。
func TestRevoke_NonSuperAdminDenied(t *testing.T) {
	db := dbtest.SetupSchema(t)
	require.NoError(t, adminauthz.EnsureRootOperator(context.Background(), db, mk(), "root", []byte("root-secret")))
	root := dialMgmt(t, db, "root", []byte("root-secret"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// 建一个无任何 system 授权的操作员 mallory。
	op, err := root.CreateOperator(ctx, &adminv1.CreateOperatorRequest{Principal: "mallory"})
	require.NoError(t, err)
	mallory := dialMgmt(t, db, "mallory", []byte(op.Secret))
	_, err = mallory.RevokeAdminGrant(ctx, &adminv1.RevokeAdminGrantRequest{
		RoleId: 1, Domain: "*", Resource: "admin", Action: "update"})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}
```

测试用到一个小 helper `domainStr(appID int64) string`（把 app_id 转成 casbin domain 字符串）。在本测试文件顶部（import 之后）加：

```go
import "strconv"

func domainStr(appID int64) string { return strconv.FormatInt(appID, 10) }
```

（删除上面草稿里 `dom`/`adminDomainOf` 那两行占位——最终文件只保留 `domainStr`。）

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/controlplane/mgmt/ -run 'TestRevoke|TestUnbind' -v`
预期：FAIL —— `root.RevokeAdminGrant` 等未定义（编译失败）。

- [ ] **步骤 3：ruleTable 加 2 条**

在 `internal/controlplane/mgmt/authz.go` 的 `ruleTable` 中，`"/sydom.admin.v1.AdminService/BindOperatorRole": {"admin", "update", false, scopeSystem},` 之后插入：

```go
	"/sydom.admin.v1.AdminService/RevokeAdminGrant":   {"admin", "update", false, scopeSystem},
	"/sydom.admin.v1.AdminService/UnbindOperatorRole": {"admin", "update", false, scopeSystem},
```

- [ ] **步骤 4：实现两个撤权 handler**

在 `internal/controlplane/mgmt/admin_ops.go` 末尾（`BindOperatorRole` 之后）加：

```go
// RevokeAdminGrant 撤一条管理授权（GrantAdminRole 的逆）。原子事务 + 必 bump（撤权立即生效）。
// 撤不存在的授权 → 回滚 + NotFound（严格，不幂等；防版本跳变 / 幽灵 delta）。
func (s *AdminServer) RevokeAdminGrant(ctx context.Context, r *adminv1.RevokeAdminGrantRequest) (*adminv1.WriteResponse, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin: %v", err)
	}
	defer tx.Rollback()
	if err := adminauthz.DeleteRoleGrant(ctx, tx, r.RoleId, r.Domain, r.Resource, r.Action); err != nil {
		if errors.Is(err, adminauthz.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "grant not found")
		}
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := adminauthz.BumpPolicyVersion(ctx, tx); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "commit: %v", err)
	}
	return &adminv1.WriteResponse{Changed: true}, nil
}

// UnbindOperatorRole 解绑操作员与管理角色（BindOperatorRole 的逆）。原子事务 + 必 bump。
// 解绑不存在的绑定 → 回滚 + NotFound。
func (s *AdminServer) UnbindOperatorRole(ctx context.Context, r *adminv1.UnbindOperatorRoleRequest) (*adminv1.WriteResponse, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin: %v", err)
	}
	defer tx.Rollback()
	if err := adminauthz.DeleteSubjectRole(ctx, tx, r.OperatorId, r.RoleId, r.Domain); err != nil {
		if errors.Is(err, adminauthz.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "binding not found")
		}
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := adminauthz.BumpPolicyVersion(ctx, tx); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "commit: %v", err)
	}
	return &adminv1.WriteResponse{Changed: true}, nil
}
```

- [ ] **步骤 5：运行测试验证通过**

运行：`go test ./internal/controlplane/mgmt/ -run 'TestRevoke|TestUnbind' -v`
预期：PASS（三个测试）。

- [ ] **步骤 6：Commit**

```bash
git add internal/controlplane/mgmt/admin_ops.go internal/controlplane/mgmt/authz.go internal/controlplane/mgmt/revoke_test.go
git commit -m "feat(mgmt): RevokeAdminGrant/UnbindOperatorRole(撤权对称, 必 bump 立即生效, 严格 NotFound)"
```

---

## 任务 4：Secret 硬切换 handler + ruleTable（TDD）

**文件：**
- 修改：`internal/controlplane/mgmt/admin_ops.go`（加 `RotateApplicationSecret` + `ResetOperatorSecret`）
- 修改：`internal/controlplane/mgmt/authz.go`（ruleTable 加 2 条）
- 测试：`internal/controlplane/mgmt/secret_rotate_test.go`（新建，`package mgmt_test`）

> `admin_ops.go` 已 import `crypto`、`status`、`codes`、`adminv1`，无需新增。

- [ ] **步骤 1：编写失败的测试**

新建 `internal/controlplane/mgmt/secret_rotate_test.go`：

```go
package mgmt_test

import (
	"context"
	"testing"
	"time"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/secret"
	"github.com/nickZFZ/Sydom/internal/crypto"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// MS-3 + MS-4（operator 路径，经真实 HMAC）：重置后旧 secret 认证即 401、新 secret 通过；secret 一次性。
func TestResetOperatorSecret_OldFailsNewWorks(t *testing.T) {
	db := dbtest.SetupSchema(t)
	require.NoError(t, adminauthz.EnsureRootOperator(context.Background(), db, mk(), "root", []byte("root-secret")))
	root := dialMgmt(t, db, "root", []byte("root-secret"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	op, err := root.CreateOperator(ctx, &adminv1.CreateOperatorRequest{Principal: "dave"})
	require.NoError(t, err)
	old := op.Secret

	// 旧 secret 可认证（ListMyTenants 是 scopeSelf，认证通过即放行）。
	daveOld := dialMgmt(t, db, "dave", []byte(old))
	_, err = daveOld.ListMyTenants(ctx, &adminv1.ListMyTenantsRequest{})
	require.NoError(t, err)

	// root 重置 dave 的 secret，返回新明文（一次性）。
	rr, err := root.ResetOperatorSecret(ctx, &adminv1.ResetOperatorSecretRequest{OperatorId: op.OperatorId})
	require.NoError(t, err)
	require.NotEmpty(t, rr.Secret)
	require.NotEqual(t, old, rr.Secret)

	// MS-3：旧 secret 客户端下次认证即 Unauthenticated（resolver 每请求查库，无缓存）。
	_, err = daveOld.ListMyTenants(ctx, &adminv1.ListMyTenantsRequest{})
	require.Equal(t, codes.Unauthenticated, status.Code(err))

	// 新 secret 通过。
	daveNew := dialMgmt(t, db, "dave", []byte(rr.Secret))
	_, err = daveNew.ListMyTenants(ctx, &adminv1.ListMyTenantsRequest{})
	require.NoError(t, err)
}

// scopeSystem：重置未知 operator → NotFound。
func TestResetOperatorSecret_UnknownNotFound(t *testing.T) {
	db := dbtest.SetupSchema(t)
	require.NoError(t, adminauthz.EnsureRootOperator(context.Background(), db, mk(), "root", []byte("root-secret")))
	root := dialMgmt(t, db, "root", []byte("root-secret"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := root.ResetOperatorSecret(ctx, &adminv1.ResetOperatorSecretRequest{OperatorId: 999999})
	require.Equal(t, codes.NotFound, status.Code(err))
}

// MS-3 + MS-4（app 路径，经 sidecar 同源 secret.Resolver）：硬切换后库里密文换新，
// resolver 解出新明文、旧明文失效；ApplicationSummary 结构上不含 secret。
func TestRotateApplicationSecret_HardCutover(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	require.NoError(t, adminauthz.EnsureRootOperator(context.Background(), db, mk(), "root", []byte("root-secret")))
	root := dialMgmt(t, db, "root", []byte("root-secret"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 取 SeedApp 的 app_key（resolver 按 app_key 解析，与 sidecar 同源）。
	var appKey string
	var oldEnc []byte
	require.NoError(t, db.QueryRow(`SELECT app_key, app_secret_enc FROM application WHERE id=$1`, appID).Scan(&appKey, &oldEnc))

	rr, err := root.RotateApplicationSecret(ctx, &adminv1.RotateApplicationSecretRequest{AppId: uint64(appID)})
	require.NoError(t, err)
	require.NotEmpty(t, rr.AppSecret)

	// 库里密文已换（硬切换）。
	var newEnc []byte
	require.NoError(t, db.QueryRow(`SELECT app_secret_enc FROM application WHERE id=$1`, appID).Scan(&newEnc))
	require.NotEqual(t, oldEnc, newEnc)

	// sidecar 同源 resolver 解出的就是新明文（MS-3：新 secret 即时生效）。
	res, err := secret.NewResolver(db, mk())
	require.NoError(t, err)
	plain, err := res.ResolveSecret(ctx, appKey)
	require.NoError(t, err)
	require.Equal(t, []byte(rr.AppSecret), plain)

	// MS-4：新密文解出的不是旧明文（结构上 ApplicationSummary 也无 secret 字段，List 物理不返）。
	require.NotEqual(t, oldEnc, newEnc)
	_ = crypto.KeySize // 保留 crypto import（如未用到可删此行与该 import）
}

// scopeApp fail-close：轮换未知 app → PermissionDenied（不泄露存在性，非 NotFound）。
func TestRotateApplicationSecret_UnknownApp_PermissionDenied(t *testing.T) {
	db := dbtest.SetupSchema(t)
	require.NoError(t, adminauthz.EnsureRootOperator(context.Background(), db, mk(), "root", []byte("root-secret")))
	root := dialMgmt(t, db, "root", []byte("root-secret"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := root.RotateApplicationSecret(ctx, &adminv1.RotateApplicationSecretRequest{AppId: 888888})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

// isWrite=false：停用的 app 仍可轮换 secret（不受 status 写拦截）。
func TestRotateApplicationSecret_DisabledAppStillRotates(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	require.NoError(t, adminauthz.EnsureRootOperator(context.Background(), db, mk(), "root", []byte("root-secret")))
	root := dialMgmt(t, db, "root", []byte("root-secret"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := root.SetApplicationStatus(ctx, &adminv1.SetApplicationStatusRequest{AppId: uint64(appID), Status: 2})
	require.NoError(t, err)
	rr, err := root.RotateApplicationSecret(ctx, &adminv1.RotateApplicationSecretRequest{AppId: uint64(appID)})
	require.NoError(t, err)
	require.NotEmpty(t, rr.AppSecret)
}
```

> 注：`TestRotateApplicationSecret_HardCutover` 末尾 `_ = crypto.KeySize` 仅为保住 `crypto` import；实现后若该 import 未被其他断言用到，实现者可删去该行与 `crypto` import（gofmt/vet 会提示）。

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/controlplane/mgmt/ -run 'TestReset|TestRotate' -v`
预期：FAIL —— `root.RotateApplicationSecret` / `root.ResetOperatorSecret` 未定义。

- [ ] **步骤 3：ruleTable 加 2 条**

在 `internal/controlplane/mgmt/authz.go` 的 `ruleTable` 中，紧接任务 3 加的两条之后插入：

```go
	"/sydom.admin.v1.AdminService/ResetOperatorSecret":     {"admin", "update", false, scopeSystem},
	"/sydom.admin.v1.AdminService/RotateApplicationSecret": {"application", "update", false, scopeApp},
```

> `RotateApplicationSecret` 用 `scopeApp` 且 `isWrite=false`：在目标 app 自身域校验（本域管理员可轮换自身 app；跨 app 需 * 域超管），且豁免 status 写闸（停用 app 也可轮换）。

- [ ] **步骤 4：实现两个 secret handler**

在 `internal/controlplane/mgmt/admin_ops.go` 末尾加：

```go
// RotateApplicationSecret 硬切换 app 的 HMAC 凭据：生成新 secret、加密、覆盖 app_secret_enc、
// 旧 secret 即刻失效（resolver 每请求查库，无缓存）。不 bump（secret 非 casbin 策略）。
// 单语句 UPDATE 本身原子，无需事务（镜像 SetApplicationStatus）。新 secret 一次性返回。
func (s *AdminServer) RotateApplicationSecret(ctx context.Context, r *adminv1.RotateApplicationSecretRequest) (*adminv1.RotateApplicationSecretResponse, error) {
	secret, err := genSecret()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "gen secret: %v", err)
	}
	enc, err := crypto.Encrypt(s.masterKey, []byte(secret))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encrypt: %v", err)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE application SET app_secret_enc=$1 WHERE id=$2`, enc, int64(r.AppId))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "rotate app secret: %v", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, status.Error(codes.NotFound, "unknown application")
	}
	return &adminv1.RotateApplicationSecretResponse{AppSecret: secret}, nil
}

// ResetOperatorSecret 硬切换 operator 的 HMAC 凭据：生成新 secret、加密、覆盖 secret_enc、
// 旧 secret 即刻失效。不 bump。单语句 UPDATE 本身原子（镜像 SetOperatorStatus 去掉 bump/tx）。
func (s *AdminServer) ResetOperatorSecret(ctx context.Context, r *adminv1.ResetOperatorSecretRequest) (*adminv1.ResetOperatorSecretResponse, error) {
	secret, err := genSecret()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "gen secret: %v", err)
	}
	enc, err := crypto.Encrypt(s.masterKey, []byte(secret))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encrypt: %v", err)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE admin_operator SET secret_enc=$1 WHERE id=$2`, enc, r.OperatorId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "reset operator secret: %v", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, status.Error(codes.NotFound, "unknown operator")
	}
	return &adminv1.ResetOperatorSecretResponse{Secret: secret}, nil
}
```

- [ ] **步骤 5：运行测试验证通过**

运行：`go test ./internal/controlplane/mgmt/ -run 'TestReset|TestRotate' -v`
预期：PASS（五个测试）。

- [ ] **步骤 6：Commit**

```bash
git add internal/controlplane/mgmt/admin_ops.go internal/controlplane/mgmt/authz.go internal/controlplane/mgmt/secret_rotate_test.go
git commit -m "feat(mgmt): RotateApplicationSecret/ResetOperatorSecret(Secret 硬切换, 不 bump, 即时失效)"
```

---

## 任务 5：REST 网关 4 路由（TDD）

**文件：**
- 修改：`internal/controlplane/restgw/routes.go`（`systemRoutes` 加 3、`applicationRoutes` 加 1、更新 `allRoutes` 注释）
- 测试：`internal/controlplane/restgw/routes_secret_revoke_test.go`（新建）

> REST 撤权用 DELETE + query 传 domain/resource/action（这些值含 `*`/`:`，不宜入 path）；role_id/operator_id 仍走 path 权威覆写。secret 用 POST 子资源路径，无 body。

- [ ] **步骤 1：编写失败的测试**

新建 `internal/controlplane/restgw/routes_secret_revoke_test.go`，镜像 `routes_accounts_test.go` 的测试范式（同一测试 helper：起 REST `Handler` + 真实 REST-HMAC）。最小覆盖：

```go
package restgw_test

// 复用本包既有测试 helper（参见 routes_accounts_test.go：建库、seed root、起 NewHandler、签名请求）。
// 断言要点：
//  1. RotateApplicationSecret：root 对 seed app POST /v1/applications/{app_id}/secret → 200 且响应含 appSecret；
//     无权 principal → 403。
//  2. ResetOperatorSecret：root POST /v1/operators/{operator_id}/secret → 200 且响应含 secret。
//  3. RevokeAdminGrant：先经 gRPC/直调 seed 一条 grant，再 DELETE
//     /v1/admin-roles/{role_id}/grants?domain=..&resource=..&action=.. → 200；重复 → 404。
//  4. UnbindOperatorRole：DELETE /v1/operators/{operator_id}/roles/{role_id}?domain=.. → 200；重复 → 404。
//  5. path 权威：body/query 伪造的 role_id/operator_id/app_id 被 path 覆写（仿 routes_accounts_test 的覆写断言）。
```

> 实现者：照搬 `routes_accounts_test.go` 的 helper 与签名工具，逐条落地上述 5 点为独立 `func TestREST_*`。先让它们编译失败（路由未注册 → 404/PermissionDenied）。

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/controlplane/restgw/ -run 'TestREST_(Rotate|Reset|Revoke|Unbind)' -v`
预期：FAIL（路由不存在 → 未命中 / 错误码不符）。

- [ ] **步骤 3：在 systemRoutes() 末尾加 3 路由**

在 `internal/controlplane/restgw/routes.go` 的 `systemRoutes()` 返回切片中，`GrantAdminRole` 路由之后插入：

```go
		{"DELETE", "/v1/admin-roles/{role_id}/grants", pfx + "RevokeAdminGrant",
			func(r *http.Request, _ []byte) (proto.Message, error) {
				id, err := pathInt64(r, "role_id")
				if err != nil {
					return nil, err
				}
				return &adminv1.RevokeAdminGrantRequest{ // role_id path 权威；其余键走 query（含 "*"）
					RoleId:   id,
					Domain:   r.URL.Query().Get("domain"),
					Resource: r.URL.Query().Get("resource"),
					Action:   r.URL.Query().Get("action"),
				}, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.RevokeAdminGrant(ctx, m.(*adminv1.RevokeAdminGrantRequest))
			}},
		{"DELETE", "/v1/operators/{operator_id}/roles/{role_id}", pfx + "UnbindOperatorRole",
			func(r *http.Request, _ []byte) (proto.Message, error) {
				opID, err := pathInt64(r, "operator_id")
				if err != nil {
					return nil, err
				}
				roleID, err := pathInt64(r, "role_id")
				if err != nil {
					return nil, err
				}
				return &adminv1.UnbindOperatorRoleRequest{ // 两 id path 权威；domain 走 query（含 "*"）
					OperatorId: opID, RoleId: roleID, Domain: r.URL.Query().Get("domain"),
				}, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.UnbindOperatorRole(ctx, m.(*adminv1.UnbindOperatorRoleRequest))
			}},
		{"POST", "/v1/operators/{operator_id}/secret", pfx + "ResetOperatorSecret",
			func(r *http.Request, _ []byte) (proto.Message, error) {
				id, err := pathInt64(r, "operator_id")
				if err != nil {
					return nil, err
				}
				return &adminv1.ResetOperatorSecretRequest{OperatorId: id}, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.ResetOperatorSecret(ctx, m.(*adminv1.ResetOperatorSecretRequest))
			}},
```

- [ ] **步骤 4：在 applicationRoutes() 末尾加 1 路由**

在 `applicationRoutes()` 返回切片中，`SetApplicationStatus` 路由之后插入：

```go
		{"POST", "/v1/applications/{app_id}/secret", pfx + "RotateApplicationSecret",
			func(r *http.Request, _ []byte) (proto.Message, error) {
				id, err := pathUint64(r, "app_id")
				if err != nil {
					return nil, err
				}
				return &adminv1.RotateApplicationSecretRequest{AppId: id}, nil // path 权威
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.RotateApplicationSecret(ctx, m.(*adminv1.RotateApplicationSecretRequest))
			}},
```

- [ ] **步骤 5：更新路由计数注释**

把 `allRoutes()` 上方注释 `// allRoutes 汇总全部 34 路由（app 域 20 + 应用管理 3 + system 域 7 + 账户层 4）。` 改为：

```go
// allRoutes 汇总全部 38 路由（app 域 20 + 应用管理 4 + system 域 10 + 账户层 4）。
```

同步更新 `applicationRoutes`（`§3.2 应用管理 3 路由` → `4 路由`）与 `systemRoutes`（`7 路由` → `10 路由`）的函数注释。

- [ ] **步骤 6：运行测试验证通过**

运行：`go test ./internal/controlplane/restgw/ -v`
预期：PASS（新增 + 既有全绿）。

- [ ] **步骤 7：Commit**

```bash
git add internal/controlplane/restgw/routes.go internal/controlplane/restgw/routes_secret_revoke_test.go
git commit -m "feat(restgw): M2.1 REST parity 4 路由(撤权 DELETE+query / secret POST 子资源, path 权威)"
```

---

## 任务 6：Console UI parity（TDD）

**文件：**
- 修改：`internal/controlplane/console/routes_system.go`（注册 + 3 handler）
- 修改：`internal/controlplane/console/routes_apps.go`（注册 + 1 handler）
- 修改：`internal/controlplane/console/templates/{admin_roles,operators,dashboard}.html`
- 新建：`internal/controlplane/console/templates/{operator_secret_reset,app_secret_rotated}.html`
- 测试：`internal/controlplane/console/routes_secret_revoke_test.go`（新建，`package console`）

> 撤权/解绑走 `doWrite`（PRG）；轮换/重置 secret 走「一次性 secret 专管线」（不 PRG，直接渲染展示页，镜像 `createOperator`/`createApp`）。模板经 `//go:embed templates/*.html` 自动发现，新模板无需登记。

- [ ] **步骤 1：编写失败的测试**

新建 `internal/controlplane/console/routes_secret_revoke_test.go`（镜像 `handler_test.go` 的 `newConsole` / `loginAndCSRF` 范式）：

```go
package console

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 撤权走 doWrite：缺 CSRF → 403。
func TestConsole_RevokeAdminGrant_CSRFMissing_Forbidden(t *testing.T) {
	ts, _, _ := newConsole(t)
	c, _ := loginAndCSRF(t, ts, nil, "root@sydom", "rootsecret")
	resp, err := c.PostForm(ts.URL+"/admin-roles/1/revoke-grant",
		url.Values{"domain": {"*"}, "resource": {"admin"}, "action": {"update"}})
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
}

// 重置 operator secret：带 CSRF → 200 且页面一次性展示新 secret（非 PRG / 非 303）。
func TestConsole_ResetOperatorSecret_ShowsSecretOnce(t *testing.T) {
	ts, store, _ := newConsole(t)
	c, csrf := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	// 先建一个 operator 拿 id（经 Console 建操作员页或直接 srv——此处用 Console POST /operators）。
	_, err := c.PostForm(ts.URL+"/operators", url.Values{"csrf_token": {csrf}, "principal": {"erin"}})
	require.NoError(t, err)
	// 重置（operator_id 取自 listOperators 渲染或库；测试可查库取 id，或断言对已知 root 不可——改用 erin 的 id）。
	// 实现者：从 DB 查 erin 的 operator_id 后 POST /operators/{id}/reset-secret。
	// 断言：200、body 含「仅显示一次」警示文案、含新 secret 的 <code class="secret">、且非 303 重定向。
	_ = strings.Contains
}

// 轮换 app secret：缺 CSRF → 403（doWrite 之外的专管线同样先校验 CSRF）。
func TestConsole_RotateAppSecret_CSRFMissing_Forbidden(t *testing.T) {
	ts, _, _ := newConsole(t)
	c, _ := loginAndCSRF(t, ts, nil, "root@sydom", "rootsecret")
	resp, err := c.PostForm(ts.URL+"/apps/1/rotate-secret", url.Values{})
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusForbidden, resp.StatusCode)
}
```

> 实现者：把第二个测试补全（查库取 `erin` 的 operator_id，POST `/operators/{id}/reset-secret` 带 csrf，断言 200 + 一次性文案 + secret 出现 + 非 303）。再加一个 `unbind-role` 缺 CSRF→403 的对称测试。先确保编译失败（handler 未定义）。

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/controlplane/console/ -run 'TestConsole_(Revoke|Reset|Rotate|Unbind)' -v`
预期：FAIL（路由未注册 → 非预期状态码 / 404）。

- [ ] **步骤 3：注册 4 条 Console 路由**

`internal/controlplane/console/routes_system.go` 的 `registerSystem` 中，`grantAdminRole` 行之后加：

```go
	mux.HandleFunc("POST /admin-roles/{role_id}/revoke-grant", h.revokeAdminGrant)
	mux.HandleFunc("POST /operators/{operator_id}/unbind-role", h.unbindOperatorRole)
	mux.HandleFunc("POST /operators/{operator_id}/reset-secret", h.resetOperatorSecret)
```

`internal/controlplane/console/routes_apps.go` 的 `registerApps` 中，`setAppStatus` 行之后加：

```go
	mux.HandleFunc("POST /apps/{app_id}/rotate-secret", h.rotateAppSecret)
```

- [ ] **步骤 4：实现撤权/解绑 handler（doWrite）**

在 `routes_system.go` 末尾加：

```go
// revokeAdminGrant：撤管理授权走 doWrite。role_id 取自 path（权威），domain/resource/action 取表单。
func (h *Handler) revokeAdminGrant(w http.ResponseWriter, r *http.Request) {
	h.doWrite(w, r, svc+"RevokeAdminGrant",
		func(r *http.Request) (proto.Message, error) {
			roleID, err := pathInt64(r, "role_id")
			if err != nil {
				return nil, err
			}
			return &adminv1.RevokeAdminGrantRequest{
				RoleId:   roleID,
				Domain:   r.FormValue("domain"),
				Resource: r.FormValue("resource"),
				Action:   r.FormValue("action"),
			}, nil
		},
		func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
			return s.RevokeAdminGrant(ctx, m.(*adminv1.RevokeAdminGrantRequest))
		},
		func(*http.Request) string { return "/admin-roles" })
}

// unbindOperatorRole：解绑角色走 doWrite。operator_id 取自 path（权威），role_id/domain 取表单。
func (h *Handler) unbindOperatorRole(w http.ResponseWriter, r *http.Request) {
	h.doWrite(w, r, svc+"UnbindOperatorRole",
		func(r *http.Request) (proto.Message, error) {
			opID, err := pathInt64(r, "operator_id")
			if err != nil {
				return nil, err
			}
			roleID, err := formInt64(r, "role_id")
			if err != nil {
				return nil, err
			}
			return &adminv1.UnbindOperatorRoleRequest{OperatorId: opID, RoleId: roleID, Domain: r.FormValue("domain")}, nil
		},
		func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
			return s.UnbindOperatorRole(ctx, m.(*adminv1.UnbindOperatorRoleRequest))
		},
		func(*http.Request) string { return "/operators" })
}

// resetOperatorSecret：重置 operator 凭据走「一次性 secret」专管线——不经 doWrite，绝不 PRG。
// 新明文 secret 仅此一次返回，必须当场渲染；旧凭据即刻失效。该明文绝不日志、绝不落盘。
func (h *Handler) resetOperatorSecret(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if !h.checkCSRF(r, sess) {
		h.renderError(w, r, codes.PermissionDenied, "CSRF 校验失败", nil)
		return
	}
	const fm = svc + "ResetOperatorSecret"
	opID, err := pathInt64(r, "operator_id")
	if err != nil {
		h.renderGRPCError(w, r, fm, err)
		return
	}
	msg := &adminv1.ResetOperatorSecretRequest{OperatorId: opID}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, fm, principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, fm, err)
		return
	}
	resp, err := h.srv.ResetOperatorSecret(ctx, msg)
	if err != nil {
		h.renderGRPCError(w, r, fm, err)
		return
	}
	h.renderPage(w, r, "operator_secret_reset.html", http.StatusOK, map[string]any{
		"Nav": "system", "OperatorID": opID, "Secret": resp.Secret}) // 一次性展示，绝不日志/落盘
}
```

- [ ] **步骤 5：实现轮换 app secret handler（一次性专管线）**

在 `routes_apps.go` 末尾加：

```go
// rotateAppSecret：轮换 app 凭据走「一次性 secret」专管线——不经 doWrite，绝不 PRG。
// 新 App Secret 仅此一次返回，必须当场渲染；旧 secret 即刻失效（使用旧 secret 的 sidecar
// 将认证失败，直到配置更新）。该明文绝不日志、绝不落盘。RotateApplicationSecret 豁免 status
// 写闸（isWrite=false），故停用 app 也可轮换。
func (h *Handler) rotateAppSecret(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if !h.checkCSRF(r, sess) {
		h.renderError(w, r, codes.PermissionDenied, "CSRF 校验失败", nil)
		return
	}
	const fm = svc + "RotateApplicationSecret"
	appID, err := pathUint64(r, "app_id")
	if err != nil {
		h.renderGRPCError(w, r, fm, err)
		return
	}
	msg := &adminv1.RotateApplicationSecretRequest{AppId: appID}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, fm, principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, fm, err)
		return
	}
	resp, err := h.srv.RotateApplicationSecret(ctx, msg)
	if err != nil {
		h.renderGRPCError(w, r, fm, err)
		return
	}
	h.renderPage(w, r, "app_secret_rotated.html", http.StatusOK, map[string]any{
		"Nav": "apps", "AppID": appID, "AppSecret": resp.AppSecret}) // 一次性展示，绝不日志/落盘
}
```

- [ ] **步骤 6：新建 2 个一次性展示模板**

新建 `internal/controlplane/console/templates/operator_secret_reset.html`：

```html
{{define "title"}}操作员密钥已重置 · 司域 Console{{end}}
{{define "content"}}
<h2>操作员密钥已重置</h2>
<p>Operator ID：<code>{{.OperatorID}}</code></p>
<div class="secret-box">
  <p class="warn">⚠️ 旧密钥已立即失效。请立即保存新密钥，仅显示一次，离开本页后无法再次查看。</p>
  <p><strong>Secret</strong></p>
  <p><code class="secret">{{.Secret}}</code></p>
</div>
<p><a class="btn-primary" href="/operators">返回操作员列表</a></p>
{{end}}
```

新建 `internal/controlplane/console/templates/app_secret_rotated.html`：

```html
{{define "title"}}App Secret 已轮换 · 司域 Console{{end}}
{{define "content"}}
<h2>App Secret 已轮换</h2>
<p>App ID：<code>{{.AppID}}</code></p>
<div class="secret-box">
  <p class="warn">⚠️ 旧 App Secret 已立即失效，使用旧密钥的 Sidecar 将认证失败，直到其配置更新为新密钥。此密钥仅显示这一次。</p>
  <p><strong>App Secret</strong></p>
  <p><code class="secret">{{.AppSecret}}</code></p>
</div>
<p><a class="btn-primary" href="/">返回应用列表</a></p>
{{end}}
```

- [ ] **步骤 7：给既有模板加表单/按钮**

`templates/admin_roles.html`：在「授权」`<form>...</form>`（约 line 16，`<button>授权</button></form>`）之后插入撤权表单：

```html
<form method="post" action="/admin-roles/{{.RoleId}}/revoke-grant" class="inline-form">
<input type="hidden" name="csrf_token" value="{{$.CSRF}}">
<input name="domain" placeholder="domain(app_id 或 *)">
<input name="resource" placeholder="resource">
<input name="action" placeholder="action">
<button>撤销授权</button></form>
```

`templates/operators.html`：在「绑定角色」`<form>...</form>`（约 line 17，`<button>绑定角色</button></form>`）之后插入解绑表单 + 重置密钥按钮：

```html
<form method="post" action="/operators/{{.OperatorId}}/unbind-role" class="inline-form">
<input type="hidden" name="csrf_token" value="{{$.CSRF}}">
<input name="role_id" placeholder="role_id">
<input name="domain" placeholder="domain(app_id 或 *)">
<button>解绑角色</button></form>
<form method="post" action="/operators/{{.OperatorId}}/reset-secret" class="inline-form">
<input type="hidden" name="csrf_token" value="{{$.CSRF}}">
<button>重置密钥</button></form>
```

`templates/dashboard.html`：在每行「切换状态」`<form>...</form>`（约 line 17，`<button class="btn-primary">切换状态</button></form>`）之后、`</td></tr>` 之前插入轮换按钮：

```html
<form method="post" action="/apps/{{.AppId}}/rotate-secret" class="inline-form">
<input type="hidden" name="csrf_token" value="{{$.CSRF}}">
<button>轮换密钥</button></form>
```

- [ ] **步骤 8：运行测试验证通过**

运行：`go test ./internal/controlplane/console/ -v`
预期：PASS（新增 + 既有全绿）。

- [ ] **步骤 9：Commit**

```bash
git add internal/controlplane/console/
git commit -m "feat(console): M2.1 撤权/解绑/轮换 UI parity(doWrite + 一次性 secret 专管线, 新展示页)"
```

---

## 任务 7：全量验证 + 整体安全评审（MS-1..MS-6）

**文件：** 无新增（仅验证 + 必要 fmt 修整）

- [ ] **步骤 1：格式 + 静态检查 + proto 漂移**

运行：
```bash
gofmt -l internal/ api/ && echo "gofmt clean"
go vet ./...
make proto-check
```
预期：`gofmt -l` 无输出；`go vet` 干净；`proto-check` 的 `git diff --exit-code gen/` 为空。

- [ ] **步骤 2：全仓测试**

运行：`go test ./...`
预期：0 FAIL（含 mgmt / adminauthz / restgw / console / e2e）。

- [ ] **步骤 3：MS-6 租户隔离/matcher 零触碰核验**

运行：`git diff <M2.1 起点 SHA>..HEAD -- internal/controlplane/adminauthz/enforcer.go | wc -l`
（起点 SHA = 任务 1 commit 的父提交；可用 `git log --oneline` 定位。）
预期：`0`（M1.1 matcher 一字未改）。再人工确认 `authz.go` 的 ruleTable 改动**仅是新增 4 条**、既有条目 + AuthorizeRule + CheckStatusWrite 逻辑未动。

- [ ] **步骤 4：逐条核验 MS-1..MS-6**

对照 spec §5，逐条确认有测试支撑：
- MS-1 撤权即时生效 ← `TestRevokeAdminGrant_PrivilegeGoneAndStrictNotFound` / `TestUnbindOperatorRole_*` 的 PermissionDenied 断言。
- MS-2 撤权 fail-close 不静默 ← 同上的 NotFound 断言 + 任务 2 `ErrNotFound` 单测。
- MS-3 Secret 硬切换即时失效 ← `TestResetOperatorSecret_OldFailsNewWorks`（Unauthenticated）+ `TestRotateApplicationSecret_HardCutover`（resolver 解出新明文）。
- MS-4 Secret 一次性不泄露 ← Rotate/Reset 响应返一次；`ApplicationSummary`/`OperatorSummary` 结构无 secret 字段；Console 一次性展示页非 PRG。
- MS-5 一份授权真相三面共用 ← 三面均经 `mgmt.AuthorizeRule` + 同一 `ruleTable`（REST/Console 测试的 403/200 行为一致）。
- MS-6 租户隔离/matcher 零触碰 ← 步骤 3 的 diff=0 + 跨租户/非超管 403 测试。

- [ ] **步骤 5：整体安全评审（opus）**

调用一次 opus 整体安全评审（项目里程碑范式：末尾单次综合评审，而非逐任务双评审），聚焦：撤权必 bump、secret 不入日志/不被 List 返回、path 权威覆写防伪造、DELETE+query 不经 body 泄露、一次性 secret 不 PRG/不日志、fail-close 错误码不泄露存在性。修掉评审提出的 Blocker/Major；Minor 视情况。

- [ ] **步骤 6：Commit（若有 fmt/评审修整）**

```bash
git add -A
git commit -m "chore(m2.1): 全量验证 + 整体安全评审收尾(MS-1..MS-6 PASS)"
```

---

## 自检（writing-plans）

**1. 规格覆盖度**（对照 spec 各节）：
- §2 纳入项：4 RPC（任务 1/3/4）+ 三面 parity（任务 3/4 gRPC、5 REST、6 Console）✓
- §3 RPC 契约：任务 1 全覆盖（含 buf except 已在 buf.yaml，无需新增 except）✓
- §4.1 存储层：撤权 Delete*（任务 2）；secret UPDATE（任务 4 inline，偏离 spec 的「DAO 函数」——理由见文件结构决策，镜像 SetApplicationStatus inline）✓
- §4.2 Handler：撤权 tx+bump（任务 3）、secret 不 bump（任务 4，偏离 spec 的 BeginTx → 改单语句无 tx，理由已记）✓
- §4.3 鉴权 ruleTable 4 条（任务 3 加 2 + 任务 4 加 2）✓
- §4.4 三面 parity REST+Console（任务 5/6）✓
- §5 MS-1..MS-6：任务 7 步骤 4 逐条映射到测试 ✓
- §6 错误处理：撤未知→NotFound（scopeSystem 路径）、轮换未知 app→PermissionDenied（scopeApp fail-close，已在决策与任务 4 测试澄清 spec §6 的细微不精确）✓
- §7 测试策略：真实 Enforce 证 bump、真实 HMAC 证旧 secret 失效、安全矩阵 403、三面 parity ✓
- §8 范围边界：仅这两组对称；硬切换中断靠 runbook（Console 页已含警示文案）✓

**2. 占位符扫描**：测试草稿中 `TestConsole_ResetOperatorSecret_ShowsSecretOnce` 与 REST 测试列了「实现者补全」说明而非完整代码——这是因其依赖本包既有测试 helper（`newConsole`/`loginAndCSRF`/REST 签名工具），属「照搬既有范式」而非凭空 TODO；已给出精确断言要点与可运行骨架。其余所有新生产代码（proto、store、handler、route、template）均为完整可粘贴代码，无 TODO/占位。

**3. 类型一致性**：`RevokeAdminGrantRequest{RoleId,Domain,Resource,Action}` / `UnbindOperatorRoleRequest{OperatorId,RoleId,Domain}` / `RotateApplicationSecretRequest{AppId}` + `RotateApplicationSecretResponse{AppSecret}` / `ResetOperatorSecretRequest{OperatorId}` + `ResetOperatorSecretResponse{Secret}` 在 proto（任务 1）、handler（任务 3/4）、REST（任务 5）、Console（任务 6）、测试用法全程一致；`adminauthz.ErrNotFound` / `DeleteRoleGrant` / `DeleteSubjectRole` 签名在任务 2 定义、任务 3 调用一致；`genSecret`/`crypto.Encrypt(s.masterKey,…)` 与既有 `CreateApplication`/`CreateOperator` 一致。

---

## 执行交接

**计划已完成并保存到 `docs/superpowers/plans/2026-06-17-sydom-m2-1-revocation-secret-symmetry.md`。两种执行方式：**

**1. 子代理驱动（推荐）** — 每个任务调度一个新的子代理，任务间进行审查，快速迭代。本项目里程碑范式：逐任务由控制者独立验证（git show + 重跑测试 + diff），末尾单次 opus 整体安全评审。

**2. 内联执行** — 在当前会话中使用 executing-plans 执行任务，批量执行并设有检查点。

**选哪种方式？**
