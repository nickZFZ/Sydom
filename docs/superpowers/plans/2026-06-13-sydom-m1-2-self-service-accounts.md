# M1.2 自助账户最小集 + tenant-scoped 读 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 在 M1.1 租户隔离鉴权核心之上补齐「账户层」——租户可自助注册、邀请同级管理员，所有「应用枚举/创建」按租户作用域收敛；运营者经 `tenant_membership` 多对多归属租户。

**架构：** 新增 `tenant_membership` 账户真相层（与 casbin 授权真相层锁步）；鉴权 `rpcRule.system bool` 重构为 `scope` 枚举（system/app/tenant/self）+ 集中免鉴权白名单（exempt）；4 新 RPC（RegisterTenant/ListMyTenants/InviteMember/ListMembers）+ 2 改 RPC（CreateApplication/ListApplications 改用 tenant_id），三面（gRPC/REST/Console）full-parity。M1.1 的 casbin matcher **一字不改**即覆盖 tenant scope。

**技术栈：** Go、PostgreSQL（golang-migrate）、casbin v3.10.0、buf/protobuf、net/http（REST 手写 + Console html/template）、testcontainers-go。

**依据规格：** `docs/superpowers/specs/2026-06-13-sydom-m1-2-self-service-accounts-design.md`

**前置：** M1.1 已并入 main `437b28d`（5 元 Enforce、`TenantDomain`/`TenantDomainOf`、matcher 含租户域析取、`EnsureTenantAdmin`）。本计划在 main 之上的隔离 worktree 中实现。

---

## 文件结构

**创建：**
- `db/migrations/000016_tenant_membership.up.sql` / `.down.sql` — 账户层表
- `internal/db/membership_schema_test.go` — 迁移/约束测试
- `internal/controlplane/adminauthz/membership.go` — tier 常量、`InsertMembership`、`TenantsOfOperator`、`IsOperatingPlane`、`BindTenantAdminTx`、`EnsureOperator`
- `internal/controlplane/adminauthz/membership_test.go`
- `internal/controlplane/mgmt/accounts.go` — `RegisterTenant`/`ListMyTenants`/`InviteMember`/`ListMembers` 四个 AdminServer 方法
- `internal/controlplane/mgmt/accounts_test.go`
- `internal/controlplane/mgmt/account_isolation_test.go` — 跨租户账户层安全矩阵（退风险验收）
- `internal/controlplane/restgw/routes_accounts.go` — 4 新 REST 路由
- `internal/controlplane/console/routes_accounts.go` — 注册/租户/成员 Console 路由
- `internal/controlplane/console/templates/register.html` / `tenants.html` / `members.html` / `member_invited.html`

**修改：**
- `api/proto/sydom/admin/v1/admin.proto` — 4 RPC + 消息；`CreateApplicationRequest`/`ListApplicationsRequest` 改字段（随后 `make proto-gen` 重生成 `gen/`）
- `internal/controlplane/adminauthz/operator.go` — `EnsureTenantAdmin` 复用新 helper、补写 membership
- `internal/controlplane/mgmt/authz.go` — `rpcRule.scope` 重构、`AuthorizeRule` scope 解析、`tenantIDGetter`、`UnauthenticatedMethods`、ruleTable
- `internal/controlplane/mgmt/admin_ops.go` — `CreateApplication`(tenant_id)、`ListApplications`(tenant_id 过滤)
- `internal/controlplane/mgmt/server.go` — gRPC 拦截器用 exempt 变体
- `internal/auth/interceptor.go` — 新增 `UnaryServerInterceptorExempt`
- `internal/controlplane/restgw/handler.go` — `serve()` 对 exempt 方法跳认证/授权
- `internal/controlplane/restgw/routes.go` — `applicationRoutes` 改 tenant_id
- `internal/controlplane/console/handler.go` — 注册新路由组
- `internal/controlplane/console/routes_apps.go` — dashboard/createApp 改 tenant 感知
- `internal/controlplane/console/templates/dashboard.html` / `app_new.html`
- `examples/seed/seed.go` — 先 RegisterTenant 再 CreateApplication(tenant_id)
- `internal/controlplane/mgmt/admin_ops_test.go` / `admin_reads_test.go` — 随 proto 字段改

---

## 任务依赖与顺序

1 迁移 → 2 store helper → 3 proto+恢复编译 → 4 authz scope → 5 注册/自省 handler → 6 邀请/成员/app handler → 7 gRPC 装配 → 8 REST → 9 Console → 10 安全矩阵验收。每任务结束 build 绿。

---

### 任务 1：tenant_membership 迁移

**文件：**
- 创建：`db/migrations/000016_tenant_membership.up.sql`
- 创建：`db/migrations/000016_tenant_membership.down.sql`
- 测试：`internal/db/membership_schema_test.go`

- [ ] **步骤 1：编写迁移 SQL**

`000016_tenant_membership.up.sql`：
```sql
CREATE TABLE tenant_membership (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id   BIGINT      NOT NULL REFERENCES tenant(id)         ON DELETE CASCADE,
    operator_id BIGINT      NOT NULL REFERENCES admin_operator(id) ON DELETE CASCADE,
    tier        SMALLINT    NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_tenant_membership UNIQUE (tenant_id, operator_id)
);
CREATE INDEX idx_tenant_membership_operator ON tenant_membership(operator_id);
```

`000016_tenant_membership.down.sql`：
```sql
DROP TABLE tenant_membership;
```

- [ ] **步骤 2：编写失败的测试**

`internal/db/membership_schema_test.go`：
```go
package db_test

import (
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestTenantMembership_UniqueAndCascade(t *testing.T) {
	conn := dbtest.SetupSchema(t)

	var tenantID, opID int64
	require.NoError(t, conn.QueryRow(`INSERT INTO tenant (name) VALUES ('acme') RETURNING id`).Scan(&tenantID))
	require.NoError(t, conn.QueryRow(
		`INSERT INTO admin_operator (principal, secret_enc) VALUES ('u1', '\xab'::bytea) RETURNING id`).Scan(&opID))

	_, err := conn.Exec(`INSERT INTO tenant_membership (tenant_id, operator_id, tier) VALUES ($1,$2,1)`, tenantID, opID)
	require.NoError(t, err)

	// 唯一约束：同 (tenant, operator) 二次插入失败。
	_, err = conn.Exec(`INSERT INTO tenant_membership (tenant_id, operator_id, tier) VALUES ($1,$2,2)`, tenantID, opID)
	require.Error(t, err)

	// 级联：删 operator → membership 行随之消失。
	_, err = conn.Exec(`DELETE FROM admin_operator WHERE id=$1`, opID)
	require.NoError(t, err)
	var n int
	require.NoError(t, conn.QueryRow(`SELECT count(*) FROM tenant_membership WHERE tenant_id=$1`, tenantID).Scan(&n))
	require.Equal(t, 0, n)
}
```

- [ ] **步骤 3：运行测试验证失败**

运行：`go test ./internal/db/ -run TestTenantMembership_UniqueAndCascade -v`
预期：FAIL（迁移文件未被发现前为 `relation "tenant_membership" does not exist`；放好迁移后应 PASS）。

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/db/ -run TestTenantMembership_UniqueAndCascade -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add db/migrations/000016_tenant_membership.up.sql db/migrations/000016_tenant_membership.down.sql internal/db/membership_schema_test.go
git commit -m "feat(db): 新增 tenant_membership 账户层表(唯一约束+FK级联)"
```

---

### 任务 2：adminauthz 账户层 store helper + EnsureTenantAdmin 重构

**文件：**
- 创建：`internal/controlplane/adminauthz/membership.go`
- 创建：`internal/controlplane/adminauthz/membership_test.go`
- 修改：`internal/controlplane/adminauthz/operator.go`（`EnsureTenantAdmin` 复用新 helper）

- [ ] **步骤 1：编写失败的测试**

`internal/controlplane/adminauthz/membership_test.go`：
```go
package adminauthz_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/crypto"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestEnsureTenantAdmin_WritesMembershipAndBinding(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	mk := bytes.Repeat([]byte{0x11}, crypto.KeySize)

	tID, _ := dbtest.SeedAppInTenant(t, db, "t-a", "app-a", "AK_a")
	require.NoError(t, adminauthz.EnsureTenantAdmin(ctx, db, mk, tID, "alice", []byte("sa")))

	// membership(owner) 已写。
	ms, err := adminauthz.TenantsOfOperator(ctx, db, "alice")
	require.NoError(t, err)
	require.Len(t, ms, 1)
	require.Equal(t, tID, ms[0].TenantID)
	require.Equal(t, "t-a", ms[0].TenantName)
	require.Equal(t, adminauthz.TierOwner, ms[0].Tier)

	// casbin 绑定也在（I-1 锁步）：t:<id> 域存在 alice→tenant-admin-<id> 的 g 行。
	var n int
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM admin_subject_role sr
		 JOIN admin_operator o ON o.id=sr.operator_id
		 WHERE o.principal='alice' AND sr.domain=$1`, adminauthz.TenantDomain(tID)).Scan(&n))
	require.Equal(t, 1, n)

	// alice 非超管（运营平面标志 false）。
	op, err := adminauthz.IsOperatingPlane(ctx, db, "alice")
	require.NoError(t, err)
	require.False(t, op)
}

func TestInsertMembership_ReportsInserted(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	var tID, opID int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant(name) VALUES('x') RETURNING id`).Scan(&tID))
	require.NoError(t, db.QueryRow(`INSERT INTO admin_operator(principal,secret_enc) VALUES('u','\xab'::bytea) RETURNING id`).Scan(&opID))

	ins, err := adminauthz.InsertMembership(ctx, db, tID, opID, adminauthz.TierAdmin)
	require.NoError(t, err)
	require.True(t, ins)

	ins, err = adminauthz.InsertMembership(ctx, db, tID, opID, adminauthz.TierAdmin) // 重复
	require.NoError(t, err)
	require.False(t, ins) // ON CONFLICT DO NOTHING → 未插入
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/controlplane/adminauthz/ -run 'TestEnsureTenantAdmin_WritesMembership|TestInsertMembership' -v`
预期：FAIL（编译错误：`TenantsOfOperator`/`InsertMembership`/`TierOwner`/`IsOperatingPlane` 未定义）。

- [ ] **步骤 3：编写 membership.go**

`internal/controlplane/adminauthz/membership.go`：
```go
package adminauthz

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
)

// 成员档位（账户层 tenant_membership.tier）。本期签发 owner/admin；member 预留不签发。
const (
	TierOwner  int16 = 1
	TierAdmin  int16 = 2
	TierMember int16 = 3 // 预留，本期不签发
)

// Membership 是运营者在某租户的归属。
type Membership struct {
	TenantID   int64
	TenantName string
	Tier       int16
}

// InsertMembership 写一行 membership，返回是否真正插入（ON CONFLICT DO NOTHING → false）。
func InsertMembership(ctx context.Context, q cp.DBTX, tenantID, operatorID int64, tier int16) (bool, error) {
	res, err := q.ExecContext(ctx,
		`INSERT INTO tenant_membership (tenant_id, operator_id, tier) VALUES ($1,$2,$3)
		 ON CONFLICT (tenant_id, operator_id) DO NOTHING`, tenantID, operatorID, tier)
	if err != nil {
		return false, fmt.Errorf("adminauthz: insert membership: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("adminauthz: membership rows affected: %w", err)
	}
	return n > 0, nil
}

// TenantsOfOperator 返回 principal 的全部租户归属（按 tenant id 排序）。
func TenantsOfOperator(ctx context.Context, q cp.DBTX, principal string) ([]Membership, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT t.id, t.name, m.tier
		 FROM tenant_membership m
		 JOIN tenant t          ON t.id = m.tenant_id
		 JOIN admin_operator o  ON o.id = m.operator_id
		 WHERE o.principal = $1
		 ORDER BY t.id`, principal)
	if err != nil {
		return nil, fmt.Errorf("adminauthz: tenants of operator: %w", err)
	}
	defer rows.Close()
	var out []Membership
	for rows.Next() {
		var m Membership
		if err := rows.Scan(&m.TenantID, &m.TenantName, &m.Tier); err != nil {
			return nil, fmt.Errorf("adminauthz: scan membership: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("adminauthz: tenants of operator: %w", err)
	}
	return out, nil
}

// IsOperatingPlane 判定 principal 是否运营平面（超管）：在 "*" 域有任一角色绑定。
// 仅作 UI 提示（非授权决策；真正 enforce 仍在各 RPC）；与 DB 真相源一致。
func IsOperatingPlane(ctx context.Context, q cp.DBTX, principal string) (bool, error) {
	var exists bool
	err := q.QueryRowContext(ctx,
		`SELECT EXISTS (
		   SELECT 1 FROM admin_subject_role sr
		   JOIN admin_operator o ON o.id = sr.operator_id
		   WHERE o.principal = $1 AND sr.domain = '*')`, principal).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("adminauthz: is operating plane: %w", err)
	}
	return exists, nil
}

// EnsureOperator 幂等取/建 operator，返回 (id, created)。created=false 表示已存在（不覆盖凭据）。
// secretEnc 仅在新建时使用。供 RPC handler（自带 masterKey 加密后传入）复用。
func EnsureOperator(ctx context.Context, q cp.DBTX, principal string, secretEnc []byte) (int64, bool, error) {
	var id int64
	err := q.QueryRowContext(ctx, `SELECT id FROM admin_operator WHERE principal=$1`, principal).Scan(&id)
	if err == nil {
		return id, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false, fmt.Errorf("adminauthz: find operator: %w", err)
	}
	id, err = InsertOperator(ctx, q, principal, secretEnc)
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

// BindTenantAdminTx 在 t:<tenantID> 域把 operator 绑定为租户管理员：
// 取/建角色 tenant-admin-<id> + 授单条通配 (t:<id>,*,*) + 绑定 operator→角色@t:<id>。
// 不建 operator、不写 membership、不 bump（由调用方在同一事务统筹）。
func BindTenantAdminTx(ctx context.Context, q cp.DBTX, tenantID, operatorID int64) error {
	code := fmt.Sprintf("tenant-admin-%d", tenantID)
	var roleID int64
	err := q.QueryRowContext(ctx, `SELECT id FROM admin_role WHERE code=$1`, code).Scan(&roleID)
	if errors.Is(err, sql.ErrNoRows) {
		roleID, err = InsertRole(ctx, q, code, fmt.Sprintf("租户%d管理员", tenantID))
		if err != nil {
			return err
		}
	} else if err != nil {
		return fmt.Errorf("adminauthz: find tenant-admin role: %w", err)
	}
	dom := TenantDomain(tenantID)
	if err := InsertRoleGrant(ctx, q, roleID, dom, "*", "*"); err != nil {
		return err
	}
	return InsertSubjectRole(ctx, q, operatorID, roleID, dom)
}
```

- [ ] **步骤 4：重构 EnsureTenantAdmin 复用 helper + 补写 membership**

修改 `internal/controlplane/adminauthz/operator.go` 的 `EnsureTenantAdmin`（替换 §143-164 的角色/grant/绑定内联逻辑）：
```go
func EnsureTenantAdmin(ctx context.Context, db *sql.DB, masterKey []byte, tenantID int64, principal string, secret []byte) error {
	if len(masterKey) != crypto.KeySize {
		return crypto.ErrKeySize
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("adminauthz: begin: %w", err)
	}
	defer tx.Rollback()

	opID, err := ensureOperatorTx(ctx, tx, masterKey, principal, secret)
	if err != nil {
		return err
	}
	if _, err := InsertMembership(ctx, tx, tenantID, opID, TierOwner); err != nil {
		return err
	}
	if err := BindTenantAdminTx(ctx, tx, tenantID, opID); err != nil {
		return err
	}
	if err := BumpPolicyVersion(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("adminauthz: commit tenant admin: %w", err)
	}
	return nil
}
```
（`ensureOperatorTx` 保留不动；`fmt`/`errors`/`sql` 已在 operator.go 导入。）

- [ ] **步骤 5：运行测试验证通过**

运行：`go test ./internal/controlplane/adminauthz/ -v`
预期：PASS（含 M1.1 既有测试不回归）。

- [ ] **步骤 6：Commit**

```bash
git add internal/controlplane/adminauthz/membership.go internal/controlplane/adminauthz/membership_test.go internal/controlplane/adminauthz/operator.go
git commit -m "feat(adminauthz): 账户层 helper(membership/绑定/运营平面探测)+EnsureTenantAdmin 补写 membership"
```

---

### 任务 3：proto 4 新 RPC + 改 2 消息 + 重生成 + 恢复编译

**文件：**
- 修改：`api/proto/sydom/admin/v1/admin.proto`
- 修改（重生成）：`gen/sydom/admin/v1/*.go`（`make proto-gen`）
- 修改：`internal/controlplane/mgmt/admin_ops.go`（CreateApplication 用 tenant_id；ListApplications 暂保持列全量直到任务 6）
- 修改：`internal/controlplane/console/routes_apps.go:50`、`examples/seed/seed.go`、`internal/controlplane/mgmt/admin_ops_test.go`、`internal/controlplane/mgmt/admin_reads_test.go`（TenantName→tenant_id）

> **注意**：新 RPC 加入 service 后，`*AdminServer` 因内嵌 `UnimplementedAdminServiceServer` 自动满足接口（未实现方法返回 `Unimplemented`），故本任务**只需**保证编译绿，handler 实现留任务 5/6。

- [ ] **步骤 1：改 proto**

`api/proto/sydom/admin/v1/admin.proto` —— service 块内追加 4 RPC：
```proto
  // —— M1.2 账户层 ——
  rpc RegisterTenant(RegisterTenantRequest) returns (RegisterTenantResponse); // 免鉴权
  rpc ListMyTenants(ListMyTenantsRequest)   returns (ListMyTenantsResponse);  // self
  rpc InviteMember(InviteMemberRequest)     returns (InviteMemberResponse);   // tenant-target
  rpc ListMembers(ListMembersRequest)       returns (ListMembersResponse);    // tenant-target 读
```
文件末尾追加消息：
```proto
message RegisterTenantRequest  { string tenant_name = 1; string owner_principal = 2; }
message RegisterTenantResponse { uint64 tenant_id = 1; string owner_principal = 2; string owner_secret = 3; }

message ListMyTenantsRequest    {}
message TenantMembershipSummary { uint64 tenant_id = 1; string tenant_name = 2; uint32 tier = 3; }
message ListMyTenantsResponse   { repeated TenantMembershipSummary memberships = 1; bool is_operating_plane = 2; }

message InviteMemberRequest  { uint64 tenant_id = 1; string principal = 2; }
message InviteMemberResponse { uint64 operator_id = 1; string principal = 2; string secret = 3; }

message ListMembersRequest  { uint64 tenant_id = 1; }
message MemberSummary       { uint64 operator_id = 1; string principal = 2; uint32 tier = 3; uint32 status = 4; }
message ListMembersResponse { repeated MemberSummary members = 1; }
```
改 `CreateApplicationRequest`（保留旧字段名防误用，复用字段号 1）：
```proto
message CreateApplicationRequest {
  reserved "tenant_name";
  uint64 tenant_id = 1;
  string domain    = 2;
  string name      = 3;
  string app_key   = 4;
}
```
改 `ListApplicationsRequest`：
```proto
message ListApplicationsRequest { uint64 tenant_id = 1; }
```

- [ ] **步骤 2：重生成 gen/**

运行：`make proto-gen`
预期：`gen/sydom/admin/v1/admin.pb.go` 与 `admin_grpc.pb.go` 更新，新增 `RegisterTenantRequest` 等类型与 `GetTenantId()`；`CreateApplicationRequest.GetTenantName` 消失、出现 `GetTenantId`。

- [ ] **步骤 3：编译——观察破坏点**

运行：`go build ./...`
预期：FAIL，报 `r.TenantName undefined`（`mgmt/admin_ops.go:49`、`console/routes_apps.go:50`、`examples/seed/seed.go:88`）。

- [ ] **步骤 4：修 CreateApplication handler（admin_ops.go）**

把 `internal/controlplane/mgmt/admin_ops.go` 的 `CreateApplication`（§32-66）整体替换为单条插入（不再按名 upsert 租户）：
```go
func (s *AdminServer) CreateApplication(ctx context.Context, r *adminv1.CreateApplicationRequest) (*adminv1.CreateApplicationResponse, error) {
	secret, err := genSecret()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "gen secret: %v", err)
	}
	enc, err := crypto.Encrypt(s.masterKey, []byte(secret))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encrypt: %v", err)
	}
	var appID int64
	err = s.db.QueryRowContext(ctx,
		`INSERT INTO application (tenant_id, domain, name, app_key, app_secret_enc)
		 VALUES ($1,$2,$3,$4,$5) RETURNING id`,
		int64(r.TenantId), r.Domain, r.Name, r.AppKey, enc).Scan(&appID)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, status.Errorf(codes.AlreadyExists, "create application: %v", err)
		}
		if isForeignKeyViolation(err) { // 目标租户不存在
			return nil, status.Error(codes.InvalidArgument, "unknown tenant")
		}
		return nil, status.Errorf(codes.Internal, "create application: %v", err)
	}
	return &adminv1.CreateApplicationResponse{AppId: uint64(appID), AppSecret: secret}, nil
}
```
在 `admin_ops.go` 的 `isUniqueViolation` 旁补 FK 判定：
```go
// isForeignKeyViolation 判定是否外键冲突（SQLSTATE 23503）。
func isForeignKeyViolation(err error) bool {
	var pqErr *pq.Error
	return errors.As(err, &pqErr) && pqErr.Code == "23503"
}
```

- [ ] **步骤 5：修 Console createApp（routes_apps.go）**

`internal/controlplane/console/routes_apps.go:49-51` 的 msg 构造改为（tenant_id 取自表单，任务 9 再做「上下文隐式」）：
```go
	tid, _ := strconv.ParseUint(r.FormValue("tenant_id"), 10, 64)
	msg := &adminv1.CreateApplicationRequest{
		TenantId: tid, Domain: r.FormValue("domain"),
		Name: r.FormValue("name"), AppKey: r.FormValue("app_key")}
```
（`strconv` 已导入。）

- [ ] **步骤 6：修 seeder（examples/seed/seed.go）**

`examples/seed/seed.go:88` 处的 CreateApplication 之前先注册租户拿 tenant_id。把建 app 段（含 §88 的 `TenantName:` 调用）改为：
```go
	reg, err := cli.RegisterTenant(ctx, &adminv1.RegisterTenantRequest{
		TenantName: tenant, OwnerPrincipal: tenant + "-owner"})
	if err != nil {
		return fmt.Errorf("register tenant: %w", err)
	}
	appResp, err := cli.CreateApplication(ctx, &adminv1.CreateApplicationRequest{
		TenantId: reg.TenantId, Domain: domain, Name: appKey, AppKey: appKey,
	})
```
（变量名对齐文件现状；若已有 `appResp` 命名沿用之。RegisterTenant 免鉴权，但 seeder 用既有 admin 客户端调用亦可——免鉴权方法对带凭据请求同样放行。）

- [ ] **步骤 7：修受影响测试（admin_ops_test.go / admin_reads_test.go）**

把两文件中 `CreateApplicationRequest{TenantName: "...", ...}` 改为「先建 tenant 再传 `TenantId`」。两测试用 in-process `*AdminServer` 直调，故可直接预插租户：
```go
	var tID int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant(name) VALUES('seed-t') RETURNING id`).Scan(&tID))
	// ... CreateApplicationRequest{TenantId: uint64(tID), Domain: ..., Name: ..., AppKey: ...}
```
`ListApplicationsRequest{}` 调用保持（tenant_id=0，超管语义，任务 4 起授权后才生效；此处直调 handler 不过授权，列全量行为不变）。

- [ ] **步骤 8：编译 + 跑受影响测试**

运行：`go build ./... && go test ./internal/controlplane/mgmt/ -run 'CreateApplication|ListApplications' -v`
预期：build 绿；测试 PASS。

- [ ] **步骤 9：Commit**

```bash
git add api/proto/sydom/admin/v1/admin.proto gen/ internal/controlplane/mgmt/admin_ops.go internal/controlplane/console/routes_apps.go examples/seed/seed.go internal/controlplane/mgmt/admin_ops_test.go internal/controlplane/mgmt/admin_reads_test.go
git commit -m "feat(proto): M1.2 账户层 4 RPC + CreateApplication/ListApplications 改 tenant_id；恢复编译"
```

---

### 任务 4：鉴权 scope 重构（system/app/tenant/self + 免鉴权白名单）

**文件：**
- 修改：`internal/controlplane/mgmt/authz.go`
- 测试：`internal/controlplane/mgmt/authz_scope_test.go`（创建）

- [ ] **步骤 1：编写失败的测试**

`internal/controlplane/mgmt/authz_scope_test.go`：
```go
package mgmt_test

import (
	"bytes"
	"context"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/crypto"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestAuthorizeRule_TenantScope(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	mk := bytes.Repeat([]byte{0x11}, crypto.KeySize)

	tA, _ := dbtest.SeedAppInTenant(t, db, "ta", "appa", "AK_a")
	tB, _ := dbtest.SeedAppInTenant(t, db, "tb", "appb", "AK_b")
	require.NoError(t, adminauthz.EnsureTenantAdmin(ctx, db, mk, tA, "alice", []byte("sa")))
	require.NoError(t, adminauthz.EnsureTenantAdmin(ctx, db, mk, tB, "bob", []byte("sb")))
	require.NoError(t, adminauthz.EnsureRootOperator(ctx, db, mk, "root", []byte("sr")))
	enf, err := adminauthz.NewEnforcer(db)
	require.NoError(t, err)

	code := func(principal, method string, req any) codes.Code {
		_, err := mgmt.AuthorizeRule(ctx, enf, method, principal, req)
		return status.Code(err)
	}
	const (
		createApp = "/sydom.admin.v1.AdminService/CreateApplication"
		listApps  = "/sydom.admin.v1.AdminService/ListApplications"
		listMine  = "/sydom.admin.v1.AdminService/ListMyTenants"
		invite    = "/sydom.admin.v1.AdminService/InviteMember"
	)
	// tenant-target：本租户放行，跨租户 / 列全量(0) 拒绝。
	require.Equal(t, codes.OK, code("alice", createApp, &adminv1.CreateApplicationRequest{TenantId: uint64(tA)}))
	require.Equal(t, codes.PermissionDenied, code("alice", createApp, &adminv1.CreateApplicationRequest{TenantId: uint64(tB)}))
	require.Equal(t, codes.OK, code("alice", listApps, &adminv1.ListApplicationsRequest{TenantId: uint64(tA)}))
	require.Equal(t, codes.PermissionDenied, code("alice", listApps, &adminv1.ListApplicationsRequest{TenantId: uint64(tB)}))
	require.Equal(t, codes.PermissionDenied, code("alice", listApps, &adminv1.ListApplicationsRequest{TenantId: 0}), "租户管理员列全量(0)必须 403")
	require.Equal(t, codes.PermissionDenied, code("alice", invite, &adminv1.InviteMemberRequest{TenantId: uint64(tB)}))

	// super-admin：列全量(0) 与任一租户均放行。
	require.Equal(t, codes.OK, code("root", listApps, &adminv1.ListApplicationsRequest{TenantId: 0}))
	require.Equal(t, codes.OK, code("root", createApp, &adminv1.CreateApplicationRequest{TenantId: uint64(tA)}))

	// self：任一已认证 principal 放行（不 enforce）。
	require.Equal(t, codes.OK, code("alice", listMine, &adminv1.ListMyTenantsRequest{}))
	require.Equal(t, codes.OK, code("root", listMine, &adminv1.ListMyTenantsRequest{}))
}

func TestUnauthenticatedMethods_RegisterTenant(t *testing.T) {
	require.True(t, mgmt.UnauthenticatedMethods["/sydom.admin.v1.AdminService/RegisterTenant"])
	require.False(t, mgmt.UnauthenticatedMethods["/sydom.admin.v1.AdminService/CreateApplication"])
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/controlplane/mgmt/ -run 'TestAuthorizeRule_TenantScope|TestUnauthenticatedMethods' -v`
预期：FAIL（`UnauthenticatedMethods` 未定义；tenant scope 未实现，alice createApp(tA) 当前按 system 域 → 403 不符）。

- [ ] **步骤 3：重写 authz.go 的 rpcRule/ruleTable/AuthorizeRule**

替换 `internal/controlplane/mgmt/authz.go` 的 `rpcRule`/`ruleTable`/`AuthorizeRule`（§19-91），并新增 `ruleScope`、`tenantIDGetter`、`UnauthenticatedMethods`：
```go
// ruleScope 决定 AuthorizeRule 如何解析鉴权域。
type ruleScope int

const (
	scopeSystem ruleScope = iota // "*" 域：运营平面
	scopeApp                     // 域取自请求 app_id（M1.1 路径）
	scopeTenant                  // 域取自请求 tenant_id（0→"*"，否则 t:<id>）
	scopeSelf                    // 不 enforce，认证通过即放行
)

type rpcRule struct {
	resource string
	action   string
	isWrite  bool // 受 status 写拦截（仅具体 app 业务策略写）
	scope    ruleScope
}

// appIDGetter / tenantIDGetter 取请求中的域键。
type appIDGetter interface{ GetAppId() uint64 }
type tenantIDGetter interface{ GetTenantId() uint64 }

// UnauthenticatedMethods 是免鉴权 RPC 白名单（集中真相源，auth 与 authz 拦截器、REST serve 共用）。
var UnauthenticatedMethods = map[string]bool{
	"/sydom.admin.v1.AdminService/RegisterTenant": true,
}

var ruleTable = map[string]rpcRule{
	"/sydom.admin.v1.AdminService/CreateRole":            {"role", "create", true, scopeApp},
	"/sydom.admin.v1.AdminService/DeleteRole":            {"role", "delete", true, scopeApp},
	"/sydom.admin.v1.AdminService/UpsertPermission":      {"permission", "update", true, scopeApp},
	"/sydom.admin.v1.AdminService/GrantPermission":       {"grant", "create", true, scopeApp},
	"/sydom.admin.v1.AdminService/RevokePermission":      {"grant", "delete", true, scopeApp},
	"/sydom.admin.v1.AdminService/AddRoleInheritance":    {"inheritance", "create", true, scopeApp},
	"/sydom.admin.v1.AdminService/RemoveRoleInheritance": {"inheritance", "delete", true, scopeApp},
	"/sydom.admin.v1.AdminService/BindUserRole":          {"binding", "create", true, scopeApp},
	"/sydom.admin.v1.AdminService/UnbindUserRole":        {"binding", "delete", true, scopeApp},
	"/sydom.admin.v1.AdminService/UpsertDataPolicy":      {"data_policy", "update", true, scopeApp},
	"/sydom.admin.v1.AdminService/DeleteDataPolicy":      {"data_policy", "delete", true, scopeApp},
	"/sydom.admin.v1.AdminService/CreateApplication":     {"application", "create", false, scopeTenant},
	"/sydom.admin.v1.AdminService/SetApplicationStatus":  {"application", "update", false, scopeApp},
	"/sydom.admin.v1.AdminService/ListApplications":      {"application", "read", false, scopeTenant},
	"/sydom.admin.v1.AdminService/CreateOperator":        {"admin", "create", false, scopeSystem},
	"/sydom.admin.v1.AdminService/SetOperatorStatus":     {"admin", "update", false, scopeSystem},
	"/sydom.admin.v1.AdminService/CreateAdminRole":       {"admin", "create", false, scopeSystem},
	"/sydom.admin.v1.AdminService/GrantAdminRole":        {"admin", "update", false, scopeSystem},
	"/sydom.admin.v1.AdminService/BindOperatorRole":      {"admin", "update", false, scopeSystem},
	"/sydom.admin.v1.AdminService/ListRoles":             {"role", "read", false, scopeApp},
	"/sydom.admin.v1.AdminService/ListPermissions":       {"permission", "read", false, scopeApp},
	"/sydom.admin.v1.AdminService/ListGrants":            {"grant", "read", false, scopeApp},
	"/sydom.admin.v1.AdminService/ListRoleInheritances":  {"inheritance", "read", false, scopeApp},
	"/sydom.admin.v1.AdminService/ListUserBindings":      {"binding", "read", false, scopeApp},
	"/sydom.admin.v1.AdminService/ListDataPolicies":      {"data_policy", "read", false, scopeApp},
	"/sydom.admin.v1.AdminService/ListOperators":         {"admin", "read", false, scopeSystem},
	"/sydom.admin.v1.AdminService/ListAdminRoles":        {"admin", "read", false, scopeSystem},
	"/sydom.admin.v1.AdminService/ListMyTenants":         {"", "", false, scopeSelf},
	"/sydom.admin.v1.AdminService/InviteMember":          {"member", "create", false, scopeTenant},
	"/sydom.admin.v1.AdminService/ListMembers":           {"member", "read", false, scopeTenant},
}

func DomainOfAppID(appID int64) string { return strconv.FormatInt(appID, 10) }

// AuthorizeRule 据 ruleTable[fullMethod].scope 解析鉴权域并 enforce。gRPC/REST/Console 共用，唯一真相源。
func AuthorizeRule(ctx context.Context, enf *adminauthz.Enforcer, fullMethod, principal string, req any) (context.Context, error) {
	rule, known := ruleTable[fullMethod]
	if !known {
		return nil, status.Error(codes.PermissionDenied, "unknown method")
	}
	if rule.scope == scopeSelf {
		// 认证已由上游保证；自有数据由 handler 按 ctx principal 过滤。
		return cp.WithOperator(ctx, principal), nil
	}
	var domain, tdom string
	switch rule.scope {
	case scopeSystem:
		domain, tdom = "*", "*"
	case scopeApp:
		g, ok := req.(appIDGetter)
		if !ok {
			return nil, status.Error(codes.Internal, "request missing app_id")
		}
		appID := int64(g.GetAppId())
		domain = DomainOfAppID(appID)
		td, err := enf.TenantDomainOf(ctx, appID)
		if err != nil {
			return nil, status.Error(codes.PermissionDenied, "permission denied") // fail-close 无泄露
		}
		tdom = td
	case scopeTenant:
		g, ok := req.(tenantIDGetter)
		if !ok {
			return nil, status.Error(codes.Internal, "request missing tenant_id")
		}
		tid := int64(g.GetTenantId())
		if tid == 0 {
			domain, tdom = "*", "*" // 运营平面通配（仅超管 g(sub,*,"*") 命中）
		} else {
			domain = adminauthz.TenantDomain(tid)
			tdom = domain
		}
	}
	allow, err := enf.Enforce(ctx, principal, domain, tdom, rule.resource, rule.action)
	if err != nil || !allow {
		return nil, status.Error(codes.PermissionDenied, "permission denied")
	}
	return cp.WithOperator(ctx, principal), nil
}
```
同文件的 `AuthzUnaryInterceptor` 加白名单短路（在取 principal 之前）：
```go
func AuthzUnaryInterceptor(enf *adminauthz.Enforcer) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if UnauthenticatedMethods[info.FullMethod] {
			return handler(ctx, req) // 免鉴权：不取 principal、不 enforce
		}
		principal, ok := auth.AppIDFromContext(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "missing operator identity")
		}
		newCtx, err := AuthorizeRule(ctx, enf, info.FullMethod, principal, req)
		if err != nil {
			return nil, err
		}
		return handler(newCtx, req)
	}
}
```
（`CheckStatusWrite` 与 `StatusWriteUnaryInterceptor` 不变——仍按 `isWrite` 判定。）

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/controlplane/mgmt/ -run 'TestAuthorizeRule|TestUnauthenticatedMethods|CrossTenant' -v`
预期：PASS（含 M1.1 `TestAuthorizeRule_CrossTenantIsolation` 不回归）。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/mgmt/authz.go internal/controlplane/mgmt/authz_scope_test.go
git commit -m "feat(mgmt): 鉴权 scope 重构(system/app/tenant/self)+RegisterTenant 免鉴权白名单"
```

---

### 任务 5：handler RegisterTenant + ListMyTenants

**文件：**
- 创建：`internal/controlplane/mgmt/accounts.go`
- 创建：`internal/controlplane/mgmt/accounts_test.go`

- [ ] **步骤 1：编写失败的测试**

`internal/controlplane/mgmt/accounts_test.go`：
```go
package mgmt_test

import (
	"bytes"
	"context"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/controlplane/policy"
	"github.com/nickZFZ/Sydom/internal/controlplane/outbox"
	"github.com/nickZFZ/Sydom/internal/crypto"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func newServer(t *testing.T, db interface{ ... }) *mgmt.AdminServer { return nil } // 占位：见下方真实构造

func TestRegisterTenant_CreatesOwnerAndMembership(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	mk := bytes.Repeat([]byte{0x11}, crypto.KeySize)
	srv := mgmt.NewAdminServer(db, policy.NewPolicyManager(db, outbox.NewSink()), mk)

	resp, err := srv.RegisterTenant(ctx, &adminv1.RegisterTenantRequest{
		TenantName: "acme", OwnerPrincipal: "owner1"})
	require.NoError(t, err)
	require.NotZero(t, resp.TenantId)
	require.NotEmpty(t, resp.OwnerSecret) // 一次性明文返回

	// membership(owner) + casbin 绑定都在（I-1）。
	var tier int16
	require.NoError(t, db.QueryRow(
		`SELECT m.tier FROM tenant_membership m JOIN admin_operator o ON o.id=m.operator_id
		 WHERE o.principal='owner1' AND m.tenant_id=$1`, resp.TenantId).Scan(&tier))
	require.Equal(t, int16(1), tier)

	// 重复租户名 → AlreadyExists。
	_, err = srv.RegisterTenant(ctx, &adminv1.RegisterTenantRequest{TenantName: "acme", OwnerPrincipal: "owner2"})
	require.Equal(t, codes.AlreadyExists, status.Code(err))

	// 非法 principal → InvalidArgument。
	_, err = srv.RegisterTenant(ctx, &adminv1.RegisterTenantRequest{TenantName: "z", OwnerPrincipal: "bad principal!"})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestListMyTenants_ReturnsOwnMemberships(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	mk := bytes.Repeat([]byte{0x11}, crypto.KeySize)
	srv := mgmt.NewAdminServer(db, policy.NewPolicyManager(db, outbox.NewSink()), mk)

	r1, err := srv.RegisterTenant(ctx, &adminv1.RegisterTenantRequest{TenantName: "t1", OwnerPrincipal: "u"})
	require.NoError(t, err)

	out, err := srv.ListMyTenants(cp.WithOperator(ctx, "u"), &adminv1.ListMyTenantsRequest{})
	require.NoError(t, err)
	require.Len(t, out.Memberships, 1)
	require.Equal(t, r1.TenantId, out.Memberships[0].TenantId)
	require.False(t, out.IsOperatingPlane)
}
```
> 注：删去占位 `newServer`，测试直接用 `mgmt.NewAdminServer(db, policy.NewPolicyManager(db, outbox.NewSink()), mk)` 构造（与 app/run.go 一致）。

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/controlplane/mgmt/ -run 'TestRegisterTenant|TestListMyTenants' -v`
预期：FAIL（`RegisterTenant`/`ListMyTenants` 未实现 → `Unimplemented`）。

- [ ] **步骤 3：编写 accounts.go（RegisterTenant + ListMyTenants）**

`internal/controlplane/mgmt/accounts.go`：
```go
package mgmt

import (
	"context"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/auth"
	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/crypto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// RegisterTenant 自助注册（免鉴权）：建 tenant + owner operator + membership(owner) +
// tenant-admin 角色/grant/绑定，一事务。owner_secret 明文仅当场返回，绝不日志/落盘。
func (s *AdminServer) RegisterTenant(ctx context.Context, r *adminv1.RegisterTenantRequest) (*adminv1.RegisterTenantResponse, error) {
	if r.TenantName == "" || !auth.ValidPrincipal(r.OwnerPrincipal) {
		return nil, status.Error(codes.InvalidArgument, "tenant_name and valid owner_principal required")
	}
	secret, err := genSecret()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "gen secret: %v", err)
	}
	enc, err := crypto.Encrypt(s.masterKey, []byte(secret))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encrypt: %v", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin: %v", err)
	}
	defer tx.Rollback()

	var tenantID int64
	if err := tx.QueryRowContext(ctx,
		`INSERT INTO tenant (name) VALUES ($1) RETURNING id`, r.TenantName).Scan(&tenantID); err != nil {
		if isUniqueViolation(err) {
			return nil, status.Error(codes.AlreadyExists, "tenant name taken")
		}
		return nil, status.Errorf(codes.Internal, "create tenant: %v", err)
	}
	opID, err := adminauthz.InsertOperator(ctx, tx, r.OwnerPrincipal, enc)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, status.Error(codes.AlreadyExists, "principal taken")
		}
		return nil, status.Errorf(codes.Internal, "create owner: %v", err)
	}
	if _, err := adminauthz.InsertMembership(ctx, tx, tenantID, opID, adminauthz.TierOwner); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := adminauthz.BindTenantAdminTx(ctx, tx, tenantID, opID); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := adminauthz.BumpPolicyVersion(ctx, tx); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "commit: %v", err)
	}
	return &adminv1.RegisterTenantResponse{
		TenantId: uint64(tenantID), OwnerPrincipal: r.OwnerPrincipal, OwnerSecret: secret}, nil
}

// ListMyTenants（self）：返回 ctx principal 的租户归属 + 运营平面标志。
func (s *AdminServer) ListMyTenants(ctx context.Context, _ *adminv1.ListMyTenantsRequest) (*adminv1.ListMyTenantsResponse, error) {
	principal := cp.OperatorFromContext(ctx)
	ms, err := adminauthz.TenantsOfOperator(ctx, s.db, principal)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list my tenants: %v", err)
	}
	op, err := adminauthz.IsOperatingPlane(ctx, s.db, principal)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "operating plane: %v", err)
	}
	out := &adminv1.ListMyTenantsResponse{IsOperatingPlane: op}
	for _, m := range ms {
		out.Memberships = append(out.Memberships, &adminv1.TenantMembershipSummary{
			TenantId: uint64(m.TenantID), TenantName: m.TenantName, Tier: uint32(m.Tier)})
	}
	return out, nil
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/controlplane/mgmt/ -run 'TestRegisterTenant|TestListMyTenants' -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/mgmt/accounts.go internal/controlplane/mgmt/accounts_test.go
git commit -m "feat(mgmt): RegisterTenant(自助注册一事务)+ListMyTenants(self 自省)"
```

---

### 任务 6：handler InviteMember + ListMembers + ListApplications 租户过滤

**文件：**
- 修改：`internal/controlplane/mgmt/accounts.go`（追加 InviteMember/ListMembers）
- 修改：`internal/controlplane/mgmt/admin_ops.go`（ListApplications 按 tenant_id）
- 修改：`internal/controlplane/mgmt/accounts_test.go`（追加用例）

- [ ] **步骤 1：编写失败的测试**

在 `accounts_test.go` 追加：
```go
func TestInviteMember_NewAndExisting(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	mk := bytes.Repeat([]byte{0x11}, crypto.KeySize)
	srv := mgmt.NewAdminServer(db, policy.NewPolicyManager(db, outbox.NewSink()), mk)

	reg, err := srv.RegisterTenant(ctx, &adminv1.RegisterTenantRequest{TenantName: "t1", OwnerPrincipal: "owner"})
	require.NoError(t, err)

	// 新 principal → 返回一次性 secret + admin 档 membership。
	inv, err := srv.InviteMember(ctx, &adminv1.InviteMemberRequest{TenantId: reg.TenantId, Principal: "alice"})
	require.NoError(t, err)
	require.NotEmpty(t, inv.Secret)
	require.NotZero(t, inv.OperatorId)

	// ListMembers 含 owner + alice。
	lm, err := srv.ListMembers(ctx, &adminv1.ListMembersRequest{TenantId: reg.TenantId})
	require.NoError(t, err)
	require.Len(t, lm.Members, 2)

	// 重复邀请同 principal 同租户 → AlreadyExists。
	_, err = srv.InviteMember(ctx, &adminv1.InviteMemberRequest{TenantId: reg.TenantId, Principal: "alice"})
	require.Equal(t, codes.AlreadyExists, status.Code(err))

	// 既有 operator 被邀到另一租户 → 成功但不返回新 secret（复用既有凭据）。
	reg2, err := srv.RegisterTenant(ctx, &adminv1.RegisterTenantRequest{TenantName: "t2", OwnerPrincipal: "owner2"})
	require.NoError(t, err)
	inv2, err := srv.InviteMember(ctx, &adminv1.InviteMemberRequest{TenantId: reg2.TenantId, Principal: "alice"})
	require.NoError(t, err)
	require.Empty(t, inv2.Secret) // 既有 operator：无新 secret
}

func TestListApplications_TenantFilter(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	mk := bytes.Repeat([]byte{0x11}, crypto.KeySize)
	srv := mgmt.NewAdminServer(db, policy.NewPolicyManager(db, outbox.NewSink()), mk)

	rA, _ := srv.RegisterTenant(ctx, &adminv1.RegisterTenantRequest{TenantName: "ta", OwnerPrincipal: "oa"})
	rB, _ := srv.RegisterTenant(ctx, &adminv1.RegisterTenantRequest{TenantName: "tb", OwnerPrincipal: "ob"})
	_, err := srv.CreateApplication(ctx, &adminv1.CreateApplicationRequest{TenantId: rA.TenantId, Domain: "da", Name: "a", AppKey: "AK_a"})
	require.NoError(t, err)
	_, err = srv.CreateApplication(ctx, &adminv1.CreateApplicationRequest{TenantId: rB.TenantId, Domain: "db", Name: "b", AppKey: "AK_b"})
	require.NoError(t, err)

	a, err := srv.ListApplications(ctx, &adminv1.ListApplicationsRequest{TenantId: rA.TenantId})
	require.NoError(t, err)
	require.Len(t, a.Applications, 1) // 仅 A 的 app

	all, err := srv.ListApplications(ctx, &adminv1.ListApplicationsRequest{TenantId: 0})
	require.NoError(t, err)
	require.Len(t, all.Applications, 2) // 0=列全量
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/controlplane/mgmt/ -run 'TestInviteMember|TestListApplications_TenantFilter' -v`
预期：FAIL（InviteMember/ListMembers 未实现；ListApplications 仍列全量未过滤）。

- [ ] **步骤 3：实现 InviteMember + ListMembers（accounts.go 追加）**

```go
// InviteMember（tenant-target，owner/admin 可调）：在 tenant_id 建 admin 档成员。
// 新 principal 生成一次性 secret 返回；既有 operator（多租户）不返回新 secret。
func (s *AdminServer) InviteMember(ctx context.Context, r *adminv1.InviteMemberRequest) (*adminv1.InviteMemberResponse, error) {
	if !auth.ValidPrincipal(r.Principal) {
		return nil, status.Error(codes.InvalidArgument, "valid principal required")
	}
	secret, err := genSecret()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "gen secret: %v", err)
	}
	enc, err := crypto.Encrypt(s.masterKey, []byte(secret))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encrypt: %v", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin: %v", err)
	}
	defer tx.Rollback()

	opID, created, err := adminauthz.EnsureOperator(ctx, tx, r.Principal, enc)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "ensure operator: %v", err)
	}
	inserted, err := adminauthz.InsertMembership(ctx, tx, int64(r.TenantId), opID, adminauthz.TierAdmin)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if !inserted {
		return nil, status.Error(codes.AlreadyExists, "already a member")
	}
	if err := adminauthz.BindTenantAdminTx(ctx, tx, int64(r.TenantId), opID); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := adminauthz.BumpPolicyVersion(ctx, tx); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "commit: %v", err)
	}
	resp := &adminv1.InviteMemberResponse{OperatorId: opID, Principal: r.Principal}
	if created {
		resp.Secret = secret // 仅新建 operator 返回一次性 secret
	}
	return resp, nil
}

// ListMembers（tenant-target 读）：列 tenant_id 的成员；secret_enc 绝不出查询。
func (s *AdminServer) ListMembers(ctx context.Context, r *adminv1.ListMembersRequest) (*adminv1.ListMembersResponse, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT o.id, o.principal, m.tier, o.status
		 FROM tenant_membership m JOIN admin_operator o ON o.id = m.operator_id
		 WHERE m.tenant_id = $1 ORDER BY o.id`, int64(r.TenantId))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list members: %v", err)
	}
	defer rows.Close()
	out := &adminv1.ListMembersResponse{}
	for rows.Next() {
		var x adminv1.MemberSummary
		var tier, st int16
		if err := rows.Scan(&x.OperatorId, &x.Principal, &tier, &st); err != nil {
			return nil, status.Errorf(codes.Internal, "scan member: %v", err)
		}
		x.Tier, x.Status = uint32(tier), uint32(st)
		out.Members = append(out.Members, &x)
	}
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "rows member: %v", err)
	}
	return out, nil
}
```

- [ ] **步骤 4：ListApplications 按 tenant_id 过滤（admin_ops.go）**

替换 `ListApplications`（§80-102）的查询分支：
```go
func (s *AdminServer) ListApplications(ctx context.Context, r *adminv1.ListApplicationsRequest) (*adminv1.ListApplicationsResponse, error) {
	var rows *sql.Rows
	var err error
	if r.TenantId == 0 { // 运营平面：列全量（授权已确保仅超管可达 tenant_id=0）
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, domain, name, app_key, status, current_version FROM application ORDER BY id`)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, domain, name, app_key, status, current_version FROM application WHERE tenant_id=$1 ORDER BY id`, int64(r.TenantId))
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list: %v", err)
	}
	defer rows.Close()
	out := &adminv1.ListApplicationsResponse{}
	for rows.Next() {
		var a adminv1.ApplicationSummary
		var id, ver int64
		var st int16
		if err := rows.Scan(&id, &a.Domain, &a.Name, &a.AppKey, &st, &ver); err != nil {
			return nil, status.Errorf(codes.Internal, "scan: %v", err)
		}
		a.AppId, a.Status, a.CurrentVersion = uint64(id), uint32(st), uint64(ver)
		out.Applications = append(out.Applications, &a)
	}
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "rows: %v", err)
	}
	return out, nil
}
```
（`database/sql` 已在 admin_ops.go 范围外——确认 import；admin_ops.go 当前未导入 `sql`，需补 `"database/sql"`。）

- [ ] **步骤 5：运行测试验证通过**

运行：`go test ./internal/controlplane/mgmt/ -v`
预期：PASS（全 mgmt 包绿，含既有用例）。

- [ ] **步骤 6：Commit**

```bash
git add internal/controlplane/mgmt/accounts.go internal/controlplane/mgmt/accounts_test.go internal/controlplane/mgmt/admin_ops.go
git commit -m "feat(mgmt): InviteMember(多租户感知)+ListMembers(不泄露secret)+ListApplications 租户过滤"
```

---

### 任务 7：gRPC 装配——免鉴权 exempt 拦截器

**文件：**
- 修改：`internal/auth/interceptor.go`（新增 `UnaryServerInterceptorExempt`）
- 修改：`internal/controlplane/mgmt/server.go`（用 exempt 变体）
- 测试：`internal/auth/interceptor_exempt_test.go`（创建）

- [ ] **步骤 1：编写失败的测试**

`internal/auth/interceptor_exempt_test.go`：
```go
package auth_test

import (
	"context"
	"testing"

	"github.com/nickZFZ/Sydom/internal/auth"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type denyResolver struct{}

func (denyResolver) ResolveSecret(context.Context, string) ([]byte, error) {
	return nil, status.Error(codes.Unauthenticated, "no")
}

func TestUnaryServerInterceptorExempt(t *testing.T) {
	exempt := map[string]bool{"/svc/Public": true}
	ic := auth.UnaryServerInterceptorExempt(denyResolver{}, exempt)
	handler := func(ctx context.Context, req any) (any, error) { return "ok", nil }

	// 豁免方法：无凭据也放行。
	resp, err := ic(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/svc/Public"}, handler)
	require.NoError(t, err)
	require.Equal(t, "ok", resp)

	// 非豁免方法：无凭据 → Unauthenticated。
	_, err = ic(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/svc/Private"}, handler)
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/auth/ -run TestUnaryServerInterceptorExempt -v`
预期：FAIL（`UnaryServerInterceptorExempt` 未定义）。

- [ ] **步骤 3：新增 exempt 变体（interceptor.go）**

在 `internal/auth/interceptor.go` 追加（保留原 `UnaryServerInterceptor` 不动，sync 复用不受影响）：
```go
// UnaryServerInterceptorExempt 同 UnaryServerInterceptor，但 exempt 命中的 FullMethod 跳过 HMAC（用于公开 RPC）。
func UnaryServerInterceptorExempt(resolver SecretResolver, exempt map[string]bool) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if exempt[info.FullMethod] {
			return handler(ctx, req)
		}
		newCtx, err := authenticate(ctx, resolver, info.FullMethod, time.Now())
		if err != nil {
			return nil, err
		}
		return handler(newCtx, req)
	}
}
```

- [ ] **步骤 4：装配改用 exempt 变体（server.go）**

`internal/controlplane/mgmt/server.go` 的 `NewGRPCServer` 第一拦截器改：
```go
		auth.UnaryServerInterceptorExempt(resolver, UnauthenticatedMethods), // 1. HMAC 认证（RegisterTenant 豁免）
```

- [ ] **步骤 5：运行测试验证通过**

运行：`go test ./internal/auth/ ./internal/controlplane/mgmt/ -v`
预期：PASS。

- [ ] **步骤 6：Commit**

```bash
git add internal/auth/interceptor.go internal/auth/interceptor_exempt_test.go internal/controlplane/mgmt/server.go
git commit -m "feat(auth): UnaryServerInterceptorExempt + 装配 RegisterTenant 免鉴权"
```

---

### 任务 8：REST 4 新路由 + 改 app 路由 + exempt 管线

**文件：**
- 修改：`internal/controlplane/restgw/handler.go`（serve 对 exempt 跳认证/授权）
- 修改：`internal/controlplane/restgw/routes.go`（applicationRoutes 改 tenant_id）
- 创建：`internal/controlplane/restgw/routes_accounts.go`
- 测试：`internal/controlplane/restgw/routes_accounts_test.go`（创建）

- [ ] **步骤 1：编写失败的测试**

`internal/controlplane/restgw/routes_accounts_test.go`（用真实 HMAC 客户端难起，这里聚焦「免鉴权注册可达」与「已认证成员路由经授权」两点，复用既有 restgw 测试基建模式）：
```go
package restgw_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/controlplane/outbox"
	"github.com/nickZFZ/Sydom/internal/controlplane/policy"
	"github.com/nickZFZ/Sydom/internal/controlplane/restgw"
	"github.com/nickZFZ/Sydom/internal/crypto"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"log/slog"
	"io"
)

func TestREST_RegisterTenant_NoAuth(t *testing.T) {
	db := dbtest.SetupSchema(t)
	mk := bytes.Repeat([]byte{0x11}, crypto.KeySize)
	srv := mgmt.NewAdminServer(db, policy.NewPolicyManager(db, outbox.NewSink()), mk)
	resolver, err := adminauthz.NewOperatorResolver(db, mk)
	require.NoError(t, err)
	enf, err := adminauthz.NewEnforcer(db)
	require.NoError(t, err)
	h := restgw.NewHandler(srv, resolver, enf, db, slog.New(slog.NewTextHandler(io.Discard, nil)))

	body, _ := json.Marshal(map[string]string{"tenantName": "acme", "ownerPrincipal": "owner"})
	req := httptest.NewRequest(http.MethodPost, "/v1/tenants", bytes.NewReader(body)) // 无 HMAC 头
	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, req)
	require.Equal(t, http.StatusOK, rw.Code) // 免鉴权可达

	var out adminv1.RegisterTenantResponse
	require.NoError(t, protojsonUnmarshal(rw.Body.Bytes(), &out)) // 见下方 helper
	require.NotZero(t, out.TenantId)
	require.NotEmpty(t, out.OwnerSecret)

	_ = context.Background()
}
```
> 注：`protojsonUnmarshal` 用 `protojson.Unmarshal`；若 restgw 测试包已有等价 helper 则复用，否则内联 `protojson.Unmarshal(b, m)`。其余已认证路由（me/tenants、members）的端到端 HMAC 测试沿用 restgw 既有签名 helper 模式（与现有 routes 测试同构），断言 200/403。

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/controlplane/restgw/ -run TestREST_RegisterTenant_NoAuth -v`
预期：FAIL（`/v1/tenants` 未注册 → 404；且 serve 对未注册免鉴白名单仍会跑 authenticateHTTP）。

- [ ] **步骤 3：serve 对 exempt 跳认证/授权（handler.go）**

替换 `internal/controlplane/restgw/handler.go` 的 `serve`（§42-84）：
```go
func (h *Handler) serve(rt route) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		exempt := mgmt.UnauthenticatedMethods[rt.fullMethod]
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			writeError(w, h.logger, r.Header.Get(auth.HdrPrincipal), rt.fullMethod,
				status.Error(codes.InvalidArgument, "request body too large"))
			return
		}
		ctx := r.Context()
		principal := ""
		if !exempt {
			principal, err = authenticateHTTP(r, body, h.resolver, time.Now())
			if err != nil {
				writeError(w, h.logger, r.Header.Get(auth.HdrPrincipal), rt.fullMethod, err)
				return
			}
		}
		msg, err := rt.decode(r, body)
		if err != nil {
			writeError(w, h.logger, principal, rt.fullMethod, err)
			return
		}
		if !exempt {
			ctx, err = mgmt.AuthorizeRule(ctx, h.enf, rt.fullMethod, principal, msg)
			if err != nil {
				writeError(w, h.logger, principal, rt.fullMethod, err)
				return
			}
			if err := mgmt.CheckStatusWrite(ctx, h.db, rt.fullMethod, msg); err != nil {
				writeError(w, h.logger, principal, rt.fullMethod, err)
				return
			}
		}
		resp, err := rt.invoke(ctx, h.srv, msg)
		if err != nil {
			writeError(w, h.logger, principal, rt.fullMethod, err)
			return
		}
		h.writeJSON(w, principal, rt.fullMethod, resp)
	}
}
```

- [ ] **步骤 4：新增账户路由 + 汇入 allRoutes**

`internal/controlplane/restgw/routes_accounts.go`：
```go
package restgw

import (
	"context"
	"net/http"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"google.golang.org/protobuf/proto"
)

// accountRoutes 是 M1.2 账户层 4 路由。
func accountRoutes() []route {
	const pfx = "/sydom.admin.v1.AdminService/"
	return []route{
		{"POST", "/v1/tenants", pfx + "RegisterTenant", // 免鉴权（serve 据 UnauthenticatedMethods 跳认证）
			func(_ *http.Request, body []byte) (proto.Message, error) {
				m := &adminv1.RegisterTenantRequest{}
				if err := decodeBody(body, m); err != nil {
					return nil, err
				}
				return m, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.RegisterTenant(ctx, m.(*adminv1.RegisterTenantRequest))
			}},
		{"GET", "/v1/me/tenants", pfx + "ListMyTenants",
			func(_ *http.Request, _ []byte) (proto.Message, error) {
				return &adminv1.ListMyTenantsRequest{}, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.ListMyTenants(ctx, m.(*adminv1.ListMyTenantsRequest))
			}},
		{"POST", "/v1/tenants/{tenant_id}/members", pfx + "InviteMember",
			func(r *http.Request, body []byte) (proto.Message, error) {
				m := &adminv1.InviteMemberRequest{}
				if err := decodeBody(body, m); err != nil {
					return nil, err
				}
				id, err := pathUint64(r, "tenant_id")
				if err != nil {
					return nil, err
				}
				m.TenantId = id // 路径权威
				return m, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.InviteMember(ctx, m.(*adminv1.InviteMemberRequest))
			}},
		{"GET", "/v1/tenants/{tenant_id}/members", pfx + "ListMembers",
			func(r *http.Request, _ []byte) (proto.Message, error) {
				id, err := pathUint64(r, "tenant_id")
				if err != nil {
					return nil, err
				}
				return &adminv1.ListMembersRequest{TenantId: id}, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.ListMembers(ctx, m.(*adminv1.ListMembersRequest))
			}},
	}
}
```
`routes.go` 的 `allRoutes()` 追加 `rs = append(rs, accountRoutes()...)`，并把 `applicationRoutes` 的 ListApplications decode 改为读 `tenant_id` query：
```go
		{"GET", "/v1/applications", pfx + "ListApplications",
			func(r *http.Request, _ []byte) (proto.Message, error) {
				tid, err := queryInt64(r, "tenant_id")
				if err != nil {
					return nil, err
				}
				return &adminv1.ListApplicationsRequest{TenantId: uint64(tid)}, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.ListApplications(ctx, m.(*adminv1.ListApplicationsRequest))
			}},
```
（CreateApplication 路由 decode 已经走 `decodeBody`，tenant_id 随 body 反序列化，无需改。）

- [ ] **步骤 5：运行测试验证通过**

运行：`go test ./internal/controlplane/restgw/ -v`
预期：PASS（含既有 28→现 32 路由测试不回归）。

- [ ] **步骤 6：Commit**

```bash
git add internal/controlplane/restgw/handler.go internal/controlplane/restgw/routes.go internal/controlplane/restgw/routes_accounts.go internal/controlplane/restgw/routes_accounts_test.go
git commit -m "feat(restgw): 账户层 4 路由 + exempt 注册管线 + ListApplications tenant_id query"
```

---

### 任务 9：Console 注册/租户/成员页 + dashboard/建 app 租户感知

**文件：**
- 创建：`internal/controlplane/console/routes_accounts.go`
- 创建：`internal/controlplane/console/templates/register.html` / `tenants.html` / `members.html` / `member_invited.html`
- 修改：`internal/controlplane/console/handler.go`（注册新路由组）
- 修改：`internal/controlplane/console/routes_apps.go`（dashboard/createApp 租户感知）
- 修改：`internal/controlplane/console/templates/dashboard.html` / `app_new.html`
- 测试：`internal/controlplane/console/routes_accounts_test.go`（创建）

- [ ] **步骤 1：编写失败的测试**

`internal/controlplane/console/routes_accounts_test.go`（聚焦「公开注册页可达且 POST 建租户」「成员页需会话」，复用 console 既有测试基建）：
```go
package console_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// 复用本包既有的 newTestConsole(t) helper（起 PG+Redis、装 Handler、返回 *httptest.Server 与种子 principal/secret）。
func TestConsole_RegisterPage_PublicGET(t *testing.T) {
	ts, _ := newTestConsole(t)
	resp, err := http.Get(ts.URL + "/register")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode) // 公开，无需会话
}

func TestConsole_RegisterPost_CreatesTenant(t *testing.T) {
	ts, _ := newTestConsole(t)
	form := url.Values{"tenant_name": {"acme"}, "owner_principal": {"owner1"}}
	resp, err := http.Post(ts.URL+"/register", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := readAll(t, resp.Body)
	require.Contains(t, body, "owner1")     // 渲染一次性凭据页
	require.NotContains(t, body, "Redirect") // 不 PRG（一次性 secret 当场展示）
}

func TestConsole_Members_RequiresSession(t *testing.T) {
	ts, _ := newTestConsole(t)
	// 无 cookie 访问成员页 → 302 去 /login。
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(ts.URL + "/tenants/1/members")
	require.NoError(t, err)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
}
```
> 注：`newTestConsole`/`readAll` 为 console 测试包既有 helper（参照 console 现有 `*_test.go`）；若签名不同则按实际适配。

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/controlplane/console/ -run TestConsole_Register -v`
预期：FAIL（`/register` 未注册 → 现由 `GET /{$}` 之外无匹配 → 404 或 302）。

- [ ] **步骤 3：新增账户路由组（routes_accounts.go）**

`internal/controlplane/console/routes_accounts.go`：
```go
package console

import (
	"net/http"
	"strconv"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"google.golang.org/grpc/codes"
)

func (h *Handler) registerAccounts(mux *http.ServeMux) {
	mux.HandleFunc("GET /register", h.registerForm)   // 公开
	mux.HandleFunc("POST /register", h.registerPost)  // 公开
	mux.HandleFunc("GET /tenants", h.tenantsList)
	mux.HandleFunc("GET /tenants/{tenant_id}/members", h.membersList)
	mux.HandleFunc("POST /tenants/{tenant_id}/members", h.memberInvite)
}

// registerForm：公开注册表单（无会话）。
func (h *Handler) registerForm(w http.ResponseWriter, r *http.Request) {
	h.renderPage(w, r, "register.html", http.StatusOK, map[string]any{"Error": ""})
}

// registerPost：公开。免鉴权直调 srv.RegisterTenant；一次性 secret 当场渲染（不 PRG、不日志/落盘）。
func (h *Handler) registerPost(w http.ResponseWriter, r *http.Request) {
	msg := &adminv1.RegisterTenantRequest{
		TenantName: r.FormValue("tenant_name"), OwnerPrincipal: r.FormValue("owner_principal")}
	resp, err := h.srv.RegisterTenant(r.Context(), msg)
	if err != nil {
		h.renderGRPCError(w, r, "/sydom.admin.v1.AdminService/RegisterTenant", err)
		return
	}
	h.renderPage(w, r, "register.html", http.StatusOK, map[string]any{
		"Created": true, "TenantID": resp.TenantId,
		"OwnerPrincipal": resp.OwnerPrincipal, "OwnerSecret": resp.OwnerSecret}) // 一次性展示
}

// tenantsList：ListMyTenants（self）。
func (h *Handler) tenantsList(w http.ResponseWriter, r *http.Request) {
	principal, _, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	ctx := cp.WithOperator(r.Context(), principal)
	resp, err := h.srv.ListMyTenants(ctx, &adminv1.ListMyTenantsRequest{})
	if err != nil {
		h.renderGRPCError(w, r, "/sydom.admin.v1.AdminService/ListMyTenants", err)
		return
	}
	h.renderPage(w, r, "tenants.html", http.StatusOK, map[string]any{
		"Nav": "tenants", "Memberships": resp.Memberships, "IsOperatingPlane": resp.IsOperatingPlane})
}

// membersList：ListMembers（tenant-target 读，经共用 AuthorizeRule）。
func (h *Handler) membersList(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	tid, err := strconv.ParseUint(r.PathValue("tenant_id"), 10, 64)
	if err != nil {
		http.Redirect(w, r, "/tenants", http.StatusSeeOther)
		return
	}
	const fm = "/sydom.admin.v1.AdminService/ListMembers"
	msg := &adminv1.ListMembersRequest{TenantId: tid}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, fm, principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, fm, err)
		return
	}
	resp, err := h.srv.ListMembers(ctx, msg)
	if err != nil {
		h.renderGRPCError(w, r, fm, err)
		return
	}
	h.renderPage(w, r, "members.html", http.StatusOK, map[string]any{
		"Nav": "tenants", "TenantID": tid, "Members": resp.Members, "CSRF": sess.CSRF})
}

// memberInvite：InviteMember（CSRF → 授权 → 直调 → 一次性 secret 当场渲染，不 PRG）。
func (h *Handler) memberInvite(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if !h.checkCSRF(r, sess) {
		h.renderError(w, r, codes.PermissionDenied, "CSRF 校验失败", nil)
		return
	}
	tid, err := strconv.ParseUint(r.PathValue("tenant_id"), 10, 64)
	if err != nil {
		http.Redirect(w, r, "/tenants", http.StatusSeeOther)
		return
	}
	const fm = "/sydom.admin.v1.AdminService/InviteMember"
	msg := &adminv1.InviteMemberRequest{TenantId: tid, Principal: r.FormValue("principal")}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, fm, principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, fm, err)
		return
	}
	resp, err := h.srv.InviteMember(ctx, msg)
	if err != nil {
		h.renderGRPCError(w, r, fm, err)
		return
	}
	h.renderPage(w, r, "member_invited.html", http.StatusOK, map[string]any{
		"Nav": "tenants", "TenantID": tid,
		"Principal": resp.Principal, "Secret": resp.Secret}) // Secret 可能为空（既有 operator）
}
```
`handler.go` 的 `NewHandler` 在路由注册区追加：`h.registerAccounts(mux)`。

- [ ] **步骤 4：新增模板**

`templates/register.html`（公开页，含 layout）：
```html
{{define "content"}}
<h1>注册租户</h1>
{{if .Created}}
  <div class="card">
    <p>租户已创建：ID <code>{{.TenantID}}</code></p>
    <p>管理员：<code>{{.OwnerPrincipal}}</code></p>
    <p><strong>一次性凭据（仅此一次显示，请妥善保存）：</strong></p>
    <pre>{{.OwnerSecret}}</pre>
    <a href="/login">前往登录</a>
  </div>
{{else}}
  {{if .Error}}<p class="error">{{.Error}}</p>{{end}}
  <form method="post" action="/register">
    <label>租户名 <input name="tenant_name" required></label>
    <label>管理员标识 <input name="owner_principal" required></label>
    <button type="submit">注册</button>
  </form>
{{end}}
{{end}}
```
`templates/tenants.html`：
```html
{{define "content"}}
<h1>我的租户</h1>
{{if .IsOperatingPlane}}<p>运营平面（超级管理员）</p>{{end}}
<ul>
  {{range .Memberships}}
    <li>{{.TenantName}}（tier {{.Tier}}）—
      <a href="/tenants/{{.TenantId}}/members">成员</a> ·
      <a href="/?tenant_id={{.TenantId}}">应用</a></li>
  {{else}}
    <li>暂无租户，<a href="/register">注册一个</a></li>
  {{end}}
</ul>
{{end}}
```
`templates/members.html`：
```html
{{define "content"}}
<h1>成员（租户 {{.TenantID}}）</h1>
<table>
  <tr><th>ID</th><th>标识</th><th>档位</th><th>状态</th></tr>
  {{range .Members}}<tr><td>{{.OperatorId}}</td><td>{{.Principal}}</td><td>{{.Tier}}</td><td>{{.Status}}</td></tr>{{end}}
</table>
<h2>邀请成员</h2>
<form method="post" action="/tenants/{{.TenantID}}/members">
  <input type="hidden" name="csrf_token" value="{{.CSRF}}">
  <label>标识 <input name="principal" required></label>
  <button type="submit">邀请</button>
</form>
{{end}}
```
`templates/member_invited.html`：
```html
{{define "content"}}
<h1>已邀请：{{.Principal}}</h1>
{{if .Secret}}
  <p><strong>一次性凭据（仅此一次显示）：</strong></p>
  <pre>{{.Secret}}</pre>
{{else}}
  <p>该用户已有凭据（多租户成员），沿用既有凭据登录。</p>
{{end}}
<a href="/tenants/{{.TenantID}}/members">返回成员</a>
{{end}}
```

- [ ] **步骤 5：dashboard/建 app 租户感知（routes_apps.go）**

`dashboard`（routes_apps.go §103-128）改为：先 `ListMyTenants` 选上下文；带 `?tenant_id=` 用之，否则取首个租户（超管用 0 列全量）：
```go
func (h *Handler) dashboard(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	selfCtx := cp.WithOperator(r.Context(), principal)
	mine, err := h.srv.ListMyTenants(selfCtx, &adminv1.ListMyTenantsRequest{})
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListMyTenants", err)
		return
	}
	// 选定租户：query 优先；否则单租户隐式；超管(无 membership)用 0 列全量。
	var tid uint64
	if q := r.URL.Query().Get("tenant_id"); q != "" {
		tid, _ = strconv.ParseUint(q, 10, 64)
	} else if len(mine.Memberships) > 0 {
		tid = mine.Memberships[0].TenantId
	} // else tid=0（超管列全量；非超管将被授权拒绝并降级）

	const fm = svc + "ListApplications"
	msg := &adminv1.ListApplicationsRequest{TenantId: tid}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, fm, principal, msg)
	if err != nil {
		if status.Code(err) == codes.PermissionDenied {
			h.renderPage(w, r, "dashboard.html", http.StatusOK,
				map[string]any{"Nav": "apps", "Degraded": true, "CSRF": sess.CSRF, "Tenants": mine.Memberships})
			return
		}
		h.renderGRPCError(w, r, fm, err)
		return
	}
	resp, err := h.srv.ListApplications(ctx, msg)
	if err != nil {
		h.renderGRPCError(w, r, fm, err)
		return
	}
	h.renderPage(w, r, "dashboard.html", http.StatusOK, map[string]any{
		"Nav": "apps", "Degraded": false, "Apps": resp.Applications, "CSRF": sess.CSRF,
		"Tenants": mine.Memberships, "TenantID": tid})
}
```
`createApp`（routes_apps.go §39-64）的 msg 改为 tenant_id 来自表单 hidden（建 app 表单在选定租户上下文渲染）：
```go
	tid, _ := strconv.ParseUint(r.FormValue("tenant_id"), 10, 64)
	msg := &adminv1.CreateApplicationRequest{
		TenantId: tid, Domain: r.FormValue("domain"),
		Name: r.FormValue("name"), AppKey: r.FormValue("app_key")}
```
（imports 补 `cp "github.com/nickZFZ/Sydom/internal/controlplane"`。）`app_new.html` 把原 `tenant_name` 文本框换成隐藏 `tenant_id`（由 dashboard「在此租户建应用」链接带入 `?tenant_id=`，appNewForm 读 query 渲染）：
```go
// appNewForm 追加 tenant_id 透传
func (h *Handler) appNewForm(w http.ResponseWriter, r *http.Request) {
	_, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	h.renderPage(w, r, "app_new.html", http.StatusOK,
		map[string]any{"Nav": "apps", "CSRF": sess.CSRF, "TenantID": r.URL.Query().Get("tenant_id")})
}
```
`app_new.html` 表单内：把 `<input name="tenant_name">` 改为 `<input type="hidden" name="tenant_id" value="{{.TenantID}}">`。`dashboard.html` 顶部加「我的租户」链接与（当 `.TenantID`）「在此租户建应用」指向 `/apps/new?tenant_id={{.TenantID}}`。

- [ ] **步骤 6：运行测试验证通过**

运行：`go test ./internal/controlplane/console/ -v`
预期：PASS（含既有 Console 测试不回归）。

- [ ] **步骤 7：Commit**

```bash
git add internal/controlplane/console/
git commit -m "feat(console): 注册/我的租户/成员页 + dashboard·建app 租户感知"
```

---

### 任务 10：跨租户账户层安全矩阵（退风险验收）

**文件：**
- 创建：`internal/controlplane/mgmt/account_isolation_test.go`

- [ ] **步骤 1：编写矩阵测试**

`internal/controlplane/mgmt/account_isolation_test.go`：
```go
package mgmt_test

import (
	"bytes"
	"context"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/controlplane/outbox"
	"github.com/nickZFZ/Sydom/internal/controlplane/policy"
	"github.com/nickZFZ/Sydom/internal/crypto"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestAccountLayer_CrossTenantIsolation 是 M1.2 退风险验收矩阵：
// 自助注册 + 邀请产出真实主体，证明账户层跨租户隔离在共用 AuthorizeRule 层正确。
func TestAccountLayer_CrossTenantIsolation(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	mk := bytes.Repeat([]byte{0x11}, crypto.KeySize)
	srv := mgmt.NewAdminServer(db, policy.NewPolicyManager(db, outbox.NewSink()), mk)
	require.NoError(t, adminauthz.EnsureRootOperator(ctx, db, mk, "root", []byte("sr")))

	rA, err := srv.RegisterTenant(ctx, &adminv1.RegisterTenantRequest{TenantName: "ta", OwnerPrincipal: "alice"})
	require.NoError(t, err)
	rB, err := srv.RegisterTenant(ctx, &adminv1.RegisterTenantRequest{TenantName: "tb", OwnerPrincipal: "carol"})
	require.NoError(t, err)
	// alice 邀 bob 进 A。
	_, err = srv.InviteMember(ctx, &adminv1.InviteMemberRequest{TenantId: rA.TenantId, Principal: "bob"})
	require.NoError(t, err)

	enf, err := adminauthz.NewEnforcer(db) // RegisterTenant/Invite 后已 bump，重建加载最新策略
	require.NoError(t, err)
	code := func(principal, method string, req any) codes.Code {
		_, err := mgmt.AuthorizeRule(ctx, enf, method, principal, req)
		return status.Code(err)
	}
	const (
		createApp = "/sydom.admin.v1.AdminService/CreateApplication"
		listApps  = "/sydom.admin.v1.AdminService/ListApplications"
		invite    = "/sydom.admin.v1.AdminService/InviteMember"
		members   = "/sydom.admin.v1.AdminService/ListMembers"
		createOp  = "/sydom.admin.v1.AdminService/CreateOperator"
	)
	appReq := func(tid uint64) *adminv1.CreateApplicationRequest { return &adminv1.CreateApplicationRequest{TenantId: tid} }
	listReq := func(tid uint64) *adminv1.ListApplicationsRequest { return &adminv1.ListApplicationsRequest{TenantId: tid} }
	invReq := func(tid uint64) *adminv1.InviteMemberRequest { return &adminv1.InviteMemberRequest{TenantId: tid, Principal: "x"} }
	memReq := func(tid uint64) *adminv1.ListMembersRequest { return &adminv1.ListMembersRequest{TenantId: tid} }

	// owner alice：本租户全放行。
	require.Equal(t, codes.OK, code("alice", createApp, appReq(rA.TenantId)))
	require.Equal(t, codes.OK, code("alice", listApps, listReq(rA.TenantId)))
	require.Equal(t, codes.OK, code("alice", invite, invReq(rA.TenantId)))
	require.Equal(t, codes.OK, code("alice", members, memReq(rA.TenantId)))
	// alice 跨租户 B：全 403。
	require.Equal(t, codes.PermissionDenied, code("alice", createApp, appReq(rB.TenantId)))
	require.Equal(t, codes.PermissionDenied, code("alice", listApps, listReq(rB.TenantId)))
	require.Equal(t, codes.PermissionDenied, code("alice", invite, invReq(rB.TenantId)))
	require.Equal(t, codes.PermissionDenied, code("alice", members, memReq(rB.TenantId)))
	// alice 列全量(0) → 403（非运营平面）。
	require.Equal(t, codes.PermissionDenied, code("alice", listApps, listReq(0)))
	// alice 碰 system RPC（CreateOperator）→ 403。
	require.Equal(t, codes.PermissionDenied, code("alice", createOp, &adminv1.CreateOperatorRequest{Principal: "z"}))

	// 被邀 admin bob：与 owner 同权（A 放行、B 403）。
	require.Equal(t, codes.OK, code("bob", createApp, appReq(rA.TenantId)))
	require.Equal(t, codes.PermissionDenied, code("bob", createApp, appReq(rB.TenantId)))

	// carol（B owner）：B 放行、A 403。
	require.Equal(t, codes.OK, code("carol", members, memReq(rB.TenantId)))
	require.Equal(t, codes.PermissionDenied, code("carol", members, memReq(rA.TenantId)))

	// root 超管：两租户 + 列全量(0) 均放行。
	require.Equal(t, codes.OK, code("root", createApp, appReq(rA.TenantId)))
	require.Equal(t, codes.OK, code("root", createApp, appReq(rB.TenantId)))
	require.Equal(t, codes.OK, code("root", listApps, listReq(0)))

	// I-1 锁步：bob 在 A 既有 membership 行，也有 t:<A> 域 casbin 绑定。
	var nm, ng int
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM tenant_membership m JOIN admin_operator o ON o.id=m.operator_id
		 WHERE o.principal='bob' AND m.tenant_id=$1`, rA.TenantId).Scan(&nm))
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM admin_subject_role sr JOIN admin_operator o ON o.id=sr.operator_id
		 WHERE o.principal='bob' AND sr.domain=$1`, adminauthz.TenantDomain(int64(rA.TenantId))).Scan(&ng))
	require.Equal(t, 1, nm)
	require.Equal(t, 1, ng)
}
```

- [ ] **步骤 2：运行验证通过**

运行：`go test ./internal/controlplane/mgmt/ -run TestAccountLayer_CrossTenantIsolation -v`
预期：PASS。

- [ ] **步骤 3：全仓兜底**

运行：`go build ./... && go vet ./... && go test ./...`
预期：全绿（29+ 包 0 FAIL）。修复任何回归（尤其 e2e、console、restgw 因 proto 字段或 ListApplications 语义变化）。

- [ ] **步骤 4：Commit**

```bash
git add internal/controlplane/mgmt/account_isolation_test.go
git commit -m "test(mgmt): 跨租户账户层安全矩阵(注册+邀请产出真实主体, I-1 锁步验证)"
```

---

## 完成后

所有任务完成后，分派最终整体代码审查 + opus 整体安全评审：逐条核验设计 §7 的 I-1..I-6 + M1.1 既有 7 不变量不回归，然后用 **superpowers:finishing-a-development-branch** 收尾入 main。

## 自检结果（writing-plans 自检）

**1. 规格覆盖度**：§3 数据模型→任务1；§4 RPC→任务3(proto)+5/6(handler)；§5 authz scope→任务4；§5.4 免鉴权白名单→任务4(authz)+7(gRPC)+8(REST serve)+9(Console 公开页)；§6 流程→任务5/6/9；§7 不变量→任务10 矩阵；§8 Console/REST→任务8/9；EnsureTenantAdmin 接入→任务2(补 membership)+5(RegisterTenant API 实现)。全覆盖。

**2. 占位符扫描**：任务5 测试出现的 `newServer` 占位已在该任务注里指明删除、直接用 `mgmt.NewAdminServer(...)`；其余无 TODO/待定/"类似任务"。

**3. 类型一致性**：`InsertMembership` 返回 `(bool, error)`（任务2 定义，任务5/6 按此用）；`EnsureOperator` 返回 `(int64, bool, error)`（任务2 定义，任务6 按此用）；`BindTenantAdminTx(ctx, q cp.DBTX, tenantID, operatorID int64)`（任务2 定义，任务2/5/6 一致调用）；`ruleScope`/`scopeTenant`/`tenantIDGetter`/`UnauthenticatedMethods`（任务4 定义，任务7/8 引用一致）；`adminauthz.TierOwner/TierAdmin int16`、`TenantDomain(int64) string`（M1.1 既有/任务2）全一致。
