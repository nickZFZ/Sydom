# 控制面 ③-3 管理 API 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 为司域控制面实现 gRPC 管理写入面：管理员经 `AdminService` 增删改业务策略，元-RBAC 鉴权 + status 写拦截，写产生的 Delta 经事务性 outbox + relay 可靠广播到 ③-2 下发链路。

**架构：** 三个新包 `adminauthz`（独立 casbin RBAC-with-domain 鉴权 enforcer + 操作者凭据解析 + bootstrap）、`outbox`（写事务内 DeltaSink 落库 + relay 投递）、`mgmt`（AdminService + 认证/鉴权/status 三道拦截器）。复用 ③-1 `PolicyManager`（仅注入 `DeltaSink`）、② `auth` HMAC 拦截器与 `crypto` 加密、③-2 `broadcast.Publisher`/`translate`。两张新 migration（admin schema + policy_outbox）。

**技术栈：** Go、PostgreSQL（golang-migrate）、casbin v3.10.0（`github.com/casbin/casbin/v3`）、gRPC + buf v2、go-redis、testcontainers（PG+Redis）、bufconn。

**规格：** `docs/superpowers/specs/2026-06-02-sydom-control-plane-mgmt-api-design.md`

---

## 文件结构

| 文件 | 职责 |
|---|---|
| `db/migrations/000013_admin_schema.{up,down}.sql` | admin_operator / admin_role / admin_role_grant / admin_subject_role / admin_policy_version + super-admin 角色种子 |
| `db/migrations/000014_policy_outbox.{up,down}.sql` | policy_outbox 表 + 未发布部分索引 |
| `internal/controlplane/adminauthz/store.go` + `_test.go` | admin 表 DAO：operator/role/grant/binding 的 CUD + 读 p/g 行 + bump/read admin_policy_version |
| `internal/controlplane/adminauthz/enforcer.go` + `_test.go` | casbin model 常量 + 只读 Adapter + 版本化重载的 Enforcer 包装（Enforce） |
| `internal/controlplane/adminauthz/operator.go` + `_test.go` | OperatorResolver（实现 `auth.SecretResolver`，解密 secret_enc + status 校验，fail-close）+ EnsureRootOperator（bootstrap，幂等） |
| `internal/controlplane/outbox/sink.go` + `_test.go` | DeltaSink 实现（translate→marshal→INSERT policy_outbox） |
| `internal/controlplane/outbox/relay.go` + `_test.go` | RunRelayLoop（drain 未发布→Publisher.Publish→标记 published_at） |
| `internal/controlplane/policy/manager.go`（修改） | PolicyManager 注入可选 `DeltaSink`，两个 runVersionedWrite* 提交前调用 |
| `api/proto/sydom/admin/v1/admin.proto` + `gen/sydom/admin/v1/*` | AdminService 定义 + buf 生成代码 |
| `internal/controlplane/mgmt/authz.go` + `_test.go` | RPC→(resource,action,write,domain) 映射表 + 鉴权拦截器 + status 写拦截器 |
| `internal/controlplane/mgmt/server.go` + `_test.go` | AdminService 实现（业务写/应用管理/管理员自管）+ NewGRPCServer 装配 |
| `internal/controlplane/mgmt/endtoend_test.go` | 写→outbox→relay→Redis→③-2 Subscribe 端到端 |

**依赖顺序：** 1→2（migration）；3→4→5（adminauthz）；6→7（outbox，6 改 PolicyManager）；8（proto）；9→10→11（mgmt，依赖 3-8）；12（端到端，依赖全部）。

---

## 任务 1：admin schema migration

**文件：**
- 创建：`db/migrations/000013_admin_schema.up.sql`、`db/migrations/000013_admin_schema.down.sql`
- 测试：`internal/db/admin_schema_test.go`

- [ ] **步骤 1：编写失败的测试**

`internal/db/admin_schema_test.go`：

```go
package db_test

import (
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestAdminSchema_TablesAndSeed(t *testing.T) {
	conn := dbtest.SetupSchema(t)

	// 五张 admin 表存在
	for _, tbl := range []string{"admin_operator", "admin_role", "admin_role_grant", "admin_subject_role", "admin_policy_version"} {
		var exists bool
		require.NoError(t, conn.QueryRow(
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name=$1)`, tbl).Scan(&exists))
		require.True(t, exists, "缺表 %s", tbl)
	}

	// 种子：super-admin 角色存在，且在 * 域拥有 (*,*) 全权
	var roleID int64
	require.NoError(t, conn.QueryRow(`SELECT id FROM admin_role WHERE code='super-admin'`).Scan(&roleID))
	var n int
	require.NoError(t, conn.QueryRow(
		`SELECT count(*) FROM admin_role_grant WHERE role_id=$1 AND domain='*' AND resource='*' AND action='*'`, roleID).Scan(&n))
	require.Equal(t, 1, n)

	// admin_policy_version 单行初始为 0
	var v int64
	require.NoError(t, conn.QueryRow(`SELECT version FROM admin_policy_version WHERE id=1`).Scan(&v))
	require.Equal(t, int64(0), v)
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/db/ -run TestAdminSchema -v`
预期：FAIL（表不存在）。

- [ ] **步骤 3：编写 migration**

`db/migrations/000013_admin_schema.up.sql`：

```sql
CREATE TABLE admin_operator (
    id          BIGSERIAL PRIMARY KEY,
    principal   VARCHAR(128) NOT NULL UNIQUE,
    secret_enc  BYTEA        NOT NULL,
    status      SMALLINT     NOT NULL DEFAULT 1,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE TABLE admin_role (
    id    BIGSERIAL PRIMARY KEY,
    code  VARCHAR(64)  NOT NULL UNIQUE,
    name  VARCHAR(128) NOT NULL
);

CREATE TABLE admin_role_grant (
    id        BIGSERIAL PRIMARY KEY,
    role_id   BIGINT      NOT NULL REFERENCES admin_role(id) ON DELETE CASCADE,
    domain    VARCHAR(64) NOT NULL,
    resource  VARCHAR(64) NOT NULL,
    action    VARCHAR(32) NOT NULL,
    UNIQUE (role_id, domain, resource, action)
);

CREATE TABLE admin_subject_role (
    id          BIGSERIAL PRIMARY KEY,
    operator_id BIGINT      NOT NULL REFERENCES admin_operator(id) ON DELETE CASCADE,
    role_id     BIGINT      NOT NULL REFERENCES admin_role(id) ON DELETE CASCADE,
    domain      VARCHAR(64) NOT NULL,
    UNIQUE (operator_id, role_id, domain)
);

CREATE TABLE admin_policy_version (
    id      SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    version BIGINT   NOT NULL DEFAULT 0
);
INSERT INTO admin_policy_version (id, version) VALUES (1, 0);

-- 内置 super-admin：在 * 域拥有全资源全动作（matcher 用通配处理）
INSERT INTO admin_role (code, name) VALUES ('super-admin', '超级管理员');
INSERT INTO admin_role_grant (role_id, domain, resource, action)
    SELECT id, '*', '*', '*' FROM admin_role WHERE code='super-admin';
```

`db/migrations/000013_admin_schema.down.sql`：

```sql
DROP TABLE IF EXISTS admin_subject_role;
DROP TABLE IF EXISTS admin_role_grant;
DROP TABLE IF EXISTS admin_policy_version;
DROP TABLE IF EXISTS admin_role;
DROP TABLE IF EXISTS admin_operator;
```

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/db/ -run TestAdminSchema -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add db/migrations/000013_admin_schema.up.sql db/migrations/000013_admin_schema.down.sql internal/db/admin_schema_test.go
git commit -m "feat(db): admin schema migration（元-RBAC 表 + super-admin 种子）"
```

---

## 任务 2：policy_outbox migration

**文件：**
- 创建：`db/migrations/000014_policy_outbox.up.sql`、`db/migrations/000014_policy_outbox.down.sql`
- 测试：`internal/db/outbox_schema_test.go`

- [ ] **步骤 1：编写失败的测试**

`internal/db/outbox_schema_test.go`：

```go
package db_test

import (
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestOutboxSchema_TableExists(t *testing.T) {
	conn := dbtest.SetupSchema(t)
	var exists bool
	require.NoError(t, conn.QueryRow(
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name='policy_outbox')`).Scan(&exists))
	require.True(t, exists)

	// 可插入一行并默认 published_at 为 NULL
	_, err := conn.Exec(
		`INSERT INTO policy_outbox (app_id, version, delta_proto) VALUES (1, 1, '\x00'::bytea)`)
	require.NoError(t, err)
	var pubNull bool
	require.NoError(t, conn.QueryRow(
		`SELECT published_at IS NULL FROM policy_outbox LIMIT 1`).Scan(&pubNull))
	require.True(t, pubNull)
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/db/ -run TestOutboxSchema -v`
预期：FAIL（表不存在）。

- [ ] **步骤 3：编写 migration**

`db/migrations/000014_policy_outbox.up.sql`：

```sql
CREATE TABLE policy_outbox (
    id           BIGSERIAL PRIMARY KEY,
    app_id       BIGINT      NOT NULL,
    version      BIGINT      NOT NULL,
    delta_proto  BYTEA       NOT NULL,
    published_at TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_policy_outbox_unpublished ON policy_outbox (id) WHERE published_at IS NULL;
```

`db/migrations/000014_policy_outbox.down.sql`：

```sql
DROP TABLE IF EXISTS policy_outbox;
```

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/db/ -run TestOutboxSchema -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add db/migrations/000014_policy_outbox.up.sql db/migrations/000014_policy_outbox.down.sql internal/db/outbox_schema_test.go
git commit -m "feat(db): policy_outbox migration（事务性 outbox 表）"
```

---

## 任务 3：adminauthz store —— admin 表 DAO

**文件：**
- 创建：`internal/controlplane/adminauthz/store.go`
- 测试：`internal/controlplane/adminauthz/store_test.go`

提供 admin 元数据的结构化读写，并把 grant/binding 读成 casbin p/g 行（供任务 4 的 Adapter）。所有写在调用方事务内 bump `admin_policy_version`（任务 4/10 用版本判定重载）。

- [ ] **步骤 1：编写失败的测试**

`internal/controlplane/adminauthz/store_test.go`：

```go
package adminauthz_test

import (
	"context"
	"testing"

	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestStore_CreateAndLoadPolicyRows(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()

	// 建操作者 / 角色 / grant / 绑定（domain="7" 模拟某 app）
	opID, err := adminauthz.InsertOperator(ctx, db, "alice", []byte("enc-secret"))
	require.NoError(t, err)
	roleID, err := adminauthz.InsertRole(ctx, db, "app7-admin", "App7 管理员")
	require.NoError(t, err)
	require.NoError(t, adminauthz.InsertRoleGrant(ctx, db, roleID, "7", "role", "create"))
	require.NoError(t, adminauthz.InsertSubjectRole(ctx, db, opID, roleID, "7"))

	// 读 p 行（角色 grant）：[role_code, domain, resource, action]
	pRows, err := adminauthz.LoadPolicyRows(ctx, db)
	require.NoError(t, err)
	require.Contains(t, pRows, []string{"app7-admin", "7", "role", "create"})
	// super-admin 种子也在
	require.Contains(t, pRows, []string{"super-admin", "*", "*", "*"})

	// 读 g 行（绑定）：[operator_principal, role_code, domain]
	gRows, err := adminauthz.LoadGroupingRows(ctx, db)
	require.NoError(t, err)
	require.Contains(t, gRows, []string{"alice", "app7-admin", "7"})
}

func TestStore_BumpPolicyVersion(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()

	v0, err := adminauthz.ReadPolicyVersion(ctx, db)
	require.NoError(t, err)
	require.NoError(t, adminauthz.BumpPolicyVersion(ctx, db))
	v1, err := adminauthz.ReadPolicyVersion(ctx, db)
	require.NoError(t, err)
	require.Equal(t, v0+1, v1)
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/adminauthz/ -run TestStore -v`
预期：FAIL（包/函数未定义）。

- [ ] **步骤 3：编写实现**

`internal/controlplane/adminauthz/store.go`：

```go
// Package adminauthz 实现控制面管理鉴权：独立 casbin RBAC-with-domain enforcer、
// 操作者凭据解析与 bootstrap。与 ③-1 业务策略投影完全分离。
package adminauthz

import (
	"context"
	"fmt"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
)

// InsertOperator 建管理操作者，返回 id。secretEnc 为已加密的凭据字节。
func InsertOperator(ctx context.Context, q cp.DBTX, principal string, secretEnc []byte) (int64, error) {
	var id int64
	err := q.QueryRowContext(ctx,
		`INSERT INTO admin_operator (principal, secret_enc) VALUES ($1,$2) RETURNING id`,
		principal, secretEnc).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("adminauthz: insert operator: %w", err)
	}
	return id, nil
}

// InsertRole 建管理角色，返回 id。
func InsertRole(ctx context.Context, q cp.DBTX, code, name string) (int64, error) {
	var id int64
	err := q.QueryRowContext(ctx,
		`INSERT INTO admin_role (code, name) VALUES ($1,$2) RETURNING id`, code, name).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("adminauthz: insert role: %w", err)
	}
	return id, nil
}

// InsertRoleGrant 给角色加一条管理权（casbin p 行）。
func InsertRoleGrant(ctx context.Context, q cp.DBTX, roleID int64, domain, resource, action string) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO admin_role_grant (role_id, domain, resource, action) VALUES ($1,$2,$3,$4)
		 ON CONFLICT (role_id, domain, resource, action) DO NOTHING`,
		roleID, domain, resource, action)
	if err != nil {
		return fmt.Errorf("adminauthz: insert role grant: %w", err)
	}
	return nil
}

// InsertSubjectRole 绑定操作者到角色（casbin g 行）。
func InsertSubjectRole(ctx context.Context, q cp.DBTX, operatorID, roleID int64, domain string) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO admin_subject_role (operator_id, role_id, domain) VALUES ($1,$2,$3)
		 ON CONFLICT (operator_id, role_id, domain) DO NOTHING`,
		operatorID, roleID, domain)
	if err != nil {
		return fmt.Errorf("adminauthz: insert subject role: %w", err)
	}
	return nil
}

// LoadPolicyRows 读全部 p 行：[role_code, domain, resource, action]。
func LoadPolicyRows(ctx context.Context, q cp.DBTX) ([][]string, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT r.code, g.domain, g.resource, g.action
		 FROM admin_role_grant g JOIN admin_role r ON r.id = g.role_id`)
	if err != nil {
		return nil, fmt.Errorf("adminauthz: load policy rows: %w", err)
	}
	defer rows.Close()
	var out [][]string
	for rows.Next() {
		var code, domain, resource, action string
		if err := rows.Scan(&code, &domain, &resource, &action); err != nil {
			return nil, fmt.Errorf("adminauthz: scan policy row: %w", err)
		}
		out = append(out, []string{code, domain, resource, action})
	}
	return out, rows.Err()
}

// LoadGroupingRows 读全部 g 行：[operator_principal, role_code, domain]。
func LoadGroupingRows(ctx context.Context, q cp.DBTX) ([][]string, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT o.principal, r.code, sr.domain
		 FROM admin_subject_role sr
		 JOIN admin_operator o ON o.id = sr.operator_id
		 JOIN admin_role r     ON r.id = sr.role_id`)
	if err != nil {
		return nil, fmt.Errorf("adminauthz: load grouping rows: %w", err)
	}
	defer rows.Close()
	var out [][]string
	for rows.Next() {
		var principal, code, domain string
		if err := rows.Scan(&principal, &code, &domain); err != nil {
			return nil, fmt.Errorf("adminauthz: scan grouping row: %w", err)
		}
		out = append(out, []string{principal, code, domain})
	}
	return out, rows.Err()
}

// ReadPolicyVersion 读 admin 策略版本（单调递增）。
func ReadPolicyVersion(ctx context.Context, q cp.DBTX) (int64, error) {
	var v int64
	if err := q.QueryRowContext(ctx,
		`SELECT version FROM admin_policy_version WHERE id=1`).Scan(&v); err != nil {
		return 0, fmt.Errorf("adminauthz: read policy version: %w", err)
	}
	return v, nil
}

// BumpPolicyVersion 自增 admin 策略版本（任何 admin 写后调用，触发 enforcer 重载）。
func BumpPolicyVersion(ctx context.Context, q cp.DBTX) error {
	_, err := q.ExecContext(ctx,
		`UPDATE admin_policy_version SET version = version + 1 WHERE id=1`)
	if err != nil {
		return fmt.Errorf("adminauthz: bump policy version: %w", err)
	}
	return nil
}
```

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/controlplane/adminauthz/ -run TestStore -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/adminauthz/store.go internal/controlplane/adminauthz/store_test.go
git commit -m "feat(adminauthz): admin 表 DAO + p/g 行读取 + 策略版本"
```

---

## 任务 4：adminauthz enforcer —— casbin 模型 + 只读 Adapter + 版本化重载

**文件：**
- 创建：`internal/controlplane/adminauthz/enforcer.go`
- 测试：`internal/controlplane/adminauthz/enforcer_test.go`

先 `go get github.com/casbin/casbin/v3@v3.10.0`（在 worktree 内执行；它会写入 go.mod/go.sum）。

- [ ] **步骤 1：编写失败的测试**

`internal/controlplane/adminauthz/enforcer_test.go`：

```go
package adminauthz_test

import (
	"context"
	"testing"

	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestEnforcer_Matrix(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()

	// app7-admin 仅在域 "7" 有 role:create
	opID, _ := adminauthz.InsertOperator(ctx, db, "alice", []byte("x"))
	roleID, _ := adminauthz.InsertRole(ctx, db, "app7-admin", "n")
	require.NoError(t, adminauthz.InsertRoleGrant(ctx, db, roleID, "7", "role", "create"))
	require.NoError(t, adminauthz.InsertSubjectRole(ctx, db, opID, roleID, "7"))
	// 超级管理员 bob（绑定种子 super-admin@*）
	sid, _ := adminauthz.InsertOperator(ctx, db, "bob", []byte("x"))
	var superID int64
	require.NoError(t, db.QueryRow(`SELECT id FROM admin_role WHERE code='super-admin'`).Scan(&superID))
	require.NoError(t, adminauthz.InsertSubjectRole(ctx, db, sid, superID, "*"))

	enf, err := adminauthz.NewEnforcer(db)
	require.NoError(t, err)

	// alice：域 7 放行 role:create；越权（域 9 / 其它资源）拒绝
	allow, err := enf.Enforce(ctx, "alice", "7", "role", "create")
	require.NoError(t, err)
	require.True(t, allow)
	deny, _ := enf.Enforce(ctx, "alice", "9", "role", "create")
	require.False(t, deny, "跨 app 域必须拒绝")
	deny2, _ := enf.Enforce(ctx, "alice", "7", "application", "create")
	require.False(t, deny2, "未授予的资源必须拒绝")

	// bob：超级管理员对任意域任意资源放行
	for _, dom := range []string{"7", "9", "*"} {
		ok, _ := enf.Enforce(ctx, "bob", dom, "application", "create")
		require.True(t, ok, "super-admin 在域 %s 应放行", dom)
	}

	// 未知 operator 拒绝（fail-close）
	no, _ := enf.Enforce(ctx, "ghost", "7", "role", "create")
	require.False(t, no)
}

func TestEnforcer_ReloadOnVersionBump(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	opID, _ := adminauthz.InsertOperator(ctx, db, "alice", []byte("x"))
	roleID, _ := adminauthz.InsertRole(ctx, db, "app7-admin", "n")
	require.NoError(t, adminauthz.InsertSubjectRole(ctx, db, opID, roleID, "7"))

	enf, err := adminauthz.NewEnforcer(db)
	require.NoError(t, err)
	// 初始无 grant → 拒绝
	ok, _ := enf.Enforce(ctx, "alice", "7", "role", "create")
	require.False(t, ok)

	// 加 grant 并 bump 版本 → enforcer 应在下次 Enforce 前重载
	require.NoError(t, adminauthz.InsertRoleGrant(ctx, db, roleID, "7", "role", "create"))
	require.NoError(t, adminauthz.BumpPolicyVersion(ctx, db))
	ok2, _ := enf.Enforce(ctx, "alice", "7", "role", "create")
	require.True(t, ok2, "版本 bump 后应重载并放行")
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/adminauthz/ -run TestEnforcer -v`
预期：FAIL（NewEnforcer 未定义）。

- [ ] **步骤 3：编写实现**

`internal/controlplane/adminauthz/enforcer.go`：

```go
package adminauthz

import (
	"context"
	"database/sql"
	"fmt"
	"sync"

	"github.com/casbin/casbin/v3"
	"github.com/casbin/casbin/v3/model"
	"github.com/casbin/casbin/v3/persist"
	cp "github.com/nickZFZ/Sydom/internal/controlplane"
)

// modelText 是管理鉴权的 casbin RBAC-with-domain 模型。
// system 域超管：matcher 额外接受在 "*" 域持有的角色（g 按域隔离，需显式兜住）。
const modelText = `
[request_definition]
r = sub, dom, res, act
[policy_definition]
p = sub, dom, res, act
[role_definition]
g = _, _, _
[policy_effect]
e = some(where (p.eft == allow))
[matchers]
m = (g(r.sub, p.sub, r.dom) || g(r.sub, p.sub, "*")) && (p.dom == r.dom || p.dom == "*") && (p.res == r.res || p.res == "*") && (p.act == r.act || p.act == "*")
`

// dbAdapter 是只读 casbin Adapter：LoadPolicy 从 admin 表组装 p/g 行。
// 写操作（AddPolicy 等）不走 casbin（走 store.go 的结构化方法），故为 no-op。
type dbAdapter struct{ db *sql.DB }

func (a *dbAdapter) LoadPolicy(m model.Model) error {
	ctx := context.Background()
	pRows, err := LoadPolicyRows(ctx, a.db)
	if err != nil {
		return err
	}
	for _, r := range pRows { // [role_code, domain, resource, action]
		if err := persist.LoadPolicyArray(append([]string{"p"}, r...), m); err != nil {
			return err
		}
	}
	gRows, err := LoadGroupingRows(ctx, a.db)
	if err != nil {
		return err
	}
	for _, r := range gRows { // [operator, role, domain]
		if err := persist.LoadPolicyArray(append([]string{"g"}, r...), m); err != nil {
			return err
		}
	}
	return nil
}

func (a *dbAdapter) SavePolicy(model.Model) error                            { return nil }
func (a *dbAdapter) AddPolicy(string, string, []string) error                { return nil }
func (a *dbAdapter) RemovePolicy(string, string, []string) error             { return nil }
func (a *dbAdapter) RemoveFilteredPolicy(string, string, int, ...string) error { return nil }

// Enforcer 包装 casbin enforcer，带 admin 策略版本化重载（控制面多副本一致性）。
type Enforcer struct {
	db      *sql.DB
	mu      sync.Mutex
	e       *casbin.Enforcer
	loadedV int64
}

// NewEnforcer 构造并首次加载。
func NewEnforcer(db *sql.DB) (*Enforcer, error) {
	m, err := model.NewModelFromString(modelText)
	if err != nil {
		return nil, fmt.Errorf("adminauthz: parse model: %w", err)
	}
	ce, err := casbin.NewEnforcer(m, &dbAdapter{db: db})
	if err != nil {
		return nil, fmt.Errorf("adminauthz: new enforcer: %w", err)
	}
	v, err := ReadPolicyVersion(context.Background(), db)
	if err != nil {
		return nil, err
	}
	return &Enforcer{db: db, e: ce, loadedV: v}, nil
}

// Enforce 判定 (operator, domain, resource, action)；DB 版本变化时先重载。
// 任意错误一律返回 (false, err)，fail-close 由调用方据 err 拒绝。
func (en *Enforcer) Enforce(ctx context.Context, sub, dom, res, act string) (bool, error) {
	en.mu.Lock()
	defer en.mu.Unlock()
	cur, err := ReadPolicyVersion(ctx, en.db)
	if err != nil {
		return false, err
	}
	if cur != en.loadedV {
		if err := en.e.LoadPolicy(); err != nil {
			return false, fmt.Errorf("adminauthz: reload policy: %w", err)
		}
		en.loadedV = cur
	}
	return en.e.Enforce(sub, dom, res, act)
}
```

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/controlplane/adminauthz/ -run TestEnforcer -v`
预期：PASS（矩阵 + 版本重载）。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/adminauthz/enforcer.go internal/controlplane/adminauthz/enforcer_test.go go.mod go.sum
git commit -m "feat(adminauthz): casbin RBAC-with-domain enforcer + 只读 Adapter + 版本化重载"
```

---

## 任务 5：adminauthz operator —— 凭据解析 + bootstrap

**文件：**
- 创建：`internal/controlplane/adminauthz/operator.go`
- 测试：`internal/controlplane/adminauthz/operator_test.go`

`OperatorResolver` 实现 `auth.SecretResolver`（`ResolveSecret(ctx, principal) ([]byte, error)`），使任务 10 可直接复用 `auth.UnaryServerInterceptor` 做 HMAC 认证；解密失败 / 未知 / 停用 operator 一律 fail-close。`EnsureRootOperator` 幂等播种 bootstrap 超管。

- [ ] **步骤 1：编写失败的测试**

`internal/controlplane/adminauthz/operator_test.go`：

```go
package adminauthz_test

import (
	"context"
	"testing"

	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/crypto"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func masterKey() []byte {
	k := make([]byte, crypto.KeySize)
	for i := range k {
		k[i] = 0x2a
	}
	return k
}

func TestOperatorResolver_ResolveSecret(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	r, err := adminauthz.NewOperatorResolver(db, masterKey())
	require.NoError(t, err)

	enc, err := crypto.Encrypt(masterKey(), []byte("alice-secret"))
	require.NoError(t, err)
	_, err = adminauthz.InsertOperator(ctx, db, "alice", enc)
	require.NoError(t, err)

	got, err := r.ResolveSecret(ctx, "alice")
	require.NoError(t, err)
	require.Equal(t, []byte("alice-secret"), got)

	// 未知 operator → fail-close（返回 error）
	_, err = r.ResolveSecret(ctx, "ghost")
	require.Error(t, err)
}

func TestOperatorResolver_DisabledOperatorFailClose(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	r, _ := adminauthz.NewOperatorResolver(db, masterKey())
	enc, _ := crypto.Encrypt(masterKey(), []byte("s"))
	_, err := adminauthz.InsertOperator(ctx, db, "alice", enc)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE admin_operator SET status=2 WHERE principal='alice'`)
	require.NoError(t, err)

	_, err = r.ResolveSecret(ctx, "alice")
	require.Error(t, err, "停用 operator 必须 fail-close")
}

func TestEnsureRootOperator_Idempotent(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()

	require.NoError(t, adminauthz.EnsureRootOperator(ctx, db, masterKey(), "root", []byte("root-secret")))
	require.NoError(t, adminauthz.EnsureRootOperator(ctx, db, masterKey(), "root", []byte("root-secret")))

	// 恰一个 root operator，且绑定 super-admin@*
	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM admin_operator WHERE principal='root'`).Scan(&n))
	require.Equal(t, 1, n)
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM admin_subject_role sr
		 JOIN admin_operator o ON o.id=sr.operator_id
		 JOIN admin_role r ON r.id=sr.role_id
		 WHERE o.principal='root' AND r.code='super-admin' AND sr.domain='*'`).Scan(&n))
	require.Equal(t, 1, n)

	// 凭据可解密
	r, _ := adminauthz.NewOperatorResolver(db, masterKey())
	got, err := r.ResolveSecret(ctx, "root")
	require.NoError(t, err)
	require.Equal(t, []byte("root-secret"), got)
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/adminauthz/ -run 'TestOperator|TestEnsureRoot' -v`
预期：FAIL（未定义）。

- [ ] **步骤 3：编写实现**

`internal/controlplane/adminauthz/operator.go`：

```go
package adminauthz

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/crypto"
)

// OperatorResolver 按 operator principal 解密返回其凭据原文，供 auth HMAC 认证复用。
// 实现 auth.SecretResolver。
type OperatorResolver struct {
	db        *sql.DB
	masterKey []byte
}

// NewOperatorResolver 构造，校验主密钥长度（fail-close）。
func NewOperatorResolver(db *sql.DB, masterKey []byte) (*OperatorResolver, error) {
	if len(masterKey) != crypto.KeySize {
		return nil, crypto.ErrKeySize
	}
	k := make([]byte, len(masterKey)) // 深拷贝，防调用方后续改动
	copy(k, masterKey)
	return &OperatorResolver{db: db, masterKey: k}, nil
}

// ResolveSecret 解密 active operator 的凭据；未知/停用/解密失败一律 error（fail-close）。
func (r *OperatorResolver) ResolveSecret(ctx context.Context, principal string) ([]byte, error) {
	var enc []byte
	var status int16
	err := r.db.QueryRowContext(ctx,
		`SELECT secret_enc, status FROM admin_operator WHERE principal=$1`, principal).Scan(&enc, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("adminauthz: unknown operator %q", principal)
	}
	if err != nil {
		return nil, fmt.Errorf("adminauthz: query operator: %w", err)
	}
	if status != 1 {
		return nil, fmt.Errorf("adminauthz: operator %q disabled", principal)
	}
	plain, err := crypto.Decrypt(r.masterKey, enc)
	if err != nil {
		return nil, fmt.Errorf("adminauthz: decrypt operator secret: %w", err)
	}
	return plain, nil
}

// EnsureRootOperator 幂等播种 bootstrap 超管：principal 不存在则建并绑定 super-admin@*。
// 已存在则不动（不覆盖凭据）。masterKey 用于加密初始凭据。
func EnsureRootOperator(ctx context.Context, db *sql.DB, masterKey []byte, principal string, secret []byte) error {
	if len(masterKey) != crypto.KeySize {
		return crypto.ErrKeySize
	}
	var exists bool
	if err := db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM admin_operator WHERE principal=$1)`, principal).Scan(&exists); err != nil {
		return fmt.Errorf("adminauthz: check root: %w", err)
	}
	if exists {
		return nil
	}
	enc, err := crypto.Encrypt(masterKey, secret)
	if err != nil {
		return fmt.Errorf("adminauthz: encrypt root secret: %w", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("adminauthz: begin: %w", err)
	}
	defer tx.Rollback()
	opID, err := InsertOperator(ctx, tx, principal, enc)
	if err != nil {
		return err
	}
	var superID int64
	if err := tx.QueryRowContext(ctx,
		`SELECT id FROM admin_role WHERE code='super-admin'`).Scan(&superID); err != nil {
		return fmt.Errorf("adminauthz: find super-admin role: %w", err)
	}
	if err := InsertSubjectRole(ctx, tx, opID, superID, "*"); err != nil {
		return err
	}
	if err := BumpPolicyVersion(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("adminauthz: commit root: %w", err)
	}
	return nil
}

// 确保 *sql.Tx 满足 cp.DBTX（InsertOperator 等接受 cp.DBTX）。
var _ cp.DBTX = (*sql.Tx)(nil)
```

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/controlplane/adminauthz/ -run 'TestOperator|TestEnsureRoot' -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/adminauthz/operator.go internal/controlplane/adminauthz/operator_test.go
git commit -m "feat(adminauthz): OperatorResolver（凭据解密 fail-close）+ EnsureRootOperator 幂等播种"
```

---

## 任务 6：outbox DeltaSink + PolicyManager 注入

**文件：**
- 创建：`internal/controlplane/outbox/sink.go`
- 修改：`internal/controlplane/policy/manager.go`
- 测试：`internal/controlplane/outbox/sink_test.go`

给 `PolicyManager` 注入可选 `DeltaSink`，在两个 `runVersionedWrite*` 的 `tx.Commit()` 前、delta 非 nil 时调用；`outbox.Sink` 把 Delta 翻译并写入 `policy_outbox`（同事务，原子）。

- [ ] **步骤 1：编写失败的测试**

`internal/controlplane/outbox/sink_test.go`：

```go
package outbox_test

import (
	"context"
	"testing"

	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/outbox"
	"github.com/nickZFZ/Sydom/internal/controlplane/policy"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestSink_PolicyWritePersistsOutboxRow(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	ctx := context.Background()

	mgr := policy.NewPolicyManager(db, outbox.NewSink())
	// 造一次有策略影响的写：建角色 + 权限 + 授权（grant 产生 casbin_rule）
	roleID, _, err := mgr.CreateRole(ctx, appID, "manager", "经理")
	require.NoError(t, err)
	permID, _, err := mgr.UpsertPermission(ctx, appID, "order.read", "order", "read", "p", "读订单")
	require.NoError(t, err)
	d, err := mgr.GrantPermission(ctx, appID, roleID, permID, "allow")
	require.NoError(t, err)
	require.NotNil(t, d, "授权应产生 Delta")

	// outbox 恰有一行对应该版本，且 delta_proto 可解出 version
	var blob []byte
	var ver int64
	require.NoError(t, db.QueryRow(
		`SELECT version, delta_proto FROM policy_outbox WHERE app_id=$1 ORDER BY id DESC LIMIT 1`, appID).Scan(&ver, &blob))
	require.Equal(t, d.Version, ver)
	var pd syncv1.Delta
	require.NoError(t, proto.Unmarshal(blob, &pd))
	require.Equal(t, uint64(d.Version), pd.Version)
}

func TestSink_FailureRollsBackWrite(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	ctx := context.Background()

	// 注入永远失败的 sink：写必须整体回滚，版本不变、无 outbox 行
	mgr := policy.NewPolicyManager(db, failingSink{})
	roleID, _, _ := mgr.CreateRole(ctx, appID, "manager", "经理")
	permID, _, _ := mgr.UpsertPermission(ctx, appID, "order.read", "order", "read", "p", "读订单")
	_, err := mgr.GrantPermission(ctx, appID, roleID, permID, "allow")
	require.Error(t, err, "sink 失败应使写事务回滚并返错")

	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM policy_outbox`).Scan(&n))
	require.Equal(t, 0, n)
	var ver int64
	require.NoError(t, db.QueryRow(`SELECT current_version FROM application WHERE id=$1`, appID).Scan(&ver))
	require.Equal(t, int64(0), ver, "回滚后版本不应 bump")
}

type failingSink struct{}

func (failingSink) Persist(ctx context.Context, tx cp.DBTX, appID int64, d *cp.Delta) error {
	return assertErr
}

var assertErr = errSink("boom")

type errSink string

func (e errSink) Error() string { return string(e) }
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/outbox/ -run TestSink -v`
预期：FAIL（`outbox.NewSink`、`policy.NewPolicyManager` 双参签名未定义）。

- [ ] **步骤 3：编写实现**

修改 `internal/controlplane/policy/manager.go`：

1) 顶部 import 增加：
```go
	"github.com/nickZFZ/Sydom/internal/controlplane/translate" // 仅类型对齐用——见下：sink 自带翻译，无需此 import
```
> 注：实际无需 translate import（翻译在 outbox 包内）。PolicyManager 只新增 `DeltaSink` 接口与字段。

2) 在 `PolicyManager` 结构与构造改为：
```go
// DeltaSink 在写事务内（提交前）持久化产出的 Delta；返回 error 触发整笔写回滚。
type DeltaSink interface {
	Persist(ctx context.Context, tx cp.DBTX, appID int64, delta *cp.Delta) error
}

// PolicyManager 是控制面真相源写入引擎。
type PolicyManager struct {
	db   *sql.DB
	sink DeltaSink // 可为 nil（退化为不落 outbox 的纯写）
}

// NewPolicyManager 构造 PolicyManager。sink 可为 nil。
func NewPolicyManager(db *sql.DB, sink DeltaSink) *PolicyManager {
	return &PolicyManager{db: db, sink: sink}
}
```

3) 在 `runVersionedWrite` 的 `tx.Commit()`（原 line 90）之前插入 sink 调用：
```go
	delta := &cp.Delta{
		AppID: appID, Version: vNew,
		RuleAdds: adds, RuleRemoves: removes, DataChanges: dataChanges,
	}
	if m.sink != nil {
		if err := m.sink.Persist(ctx, tx, appID, delta); err != nil {
			return nil, fmt.Errorf("policy: sink %s v%d: %w", op.action, vNew, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("policy: commit %s v%d: %w", op.action, vNew, err)
	}
	return delta, nil
```
（删除原先直接 `return &cp.Delta{...}` 的结尾。）

4) 在 `runVersionedWriteData` 的 `tx.Commit()`（原 line 264）之前同样插入：
```go
	delta := &cp.Delta{AppID: appID, Version: vNew, DataChanges: changes}
	if m.sink != nil {
		if err := m.sink.Persist(ctx, tx, appID, delta); err != nil {
			return nil, fmt.Errorf("policy: sink %s v%d: %w", op.action, vNew, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("policy: commit %s v%d: %w", op.action, vNew, err)
	}
	return delta, nil
```

> 既有 `policy` 包的测试调用 `NewPolicyManager(db)` 需同步改为 `NewPolicyManager(db, nil)`。在本步骤一并修正 `internal/controlplane/policy/*_test.go` 中所有 `NewPolicyManager(` 调用点（用 `grep -rn "NewPolicyManager(" internal/controlplane/policy/` 找全），保持既有行为（sink=nil）。

创建 `internal/controlplane/outbox/sink.go`：

```go
// Package outbox 实现控制面写事务性 outbox：写事务内落 Delta，独立 relay 可靠投递。
package outbox

import (
	"context"
	"fmt"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/translate"
	"google.golang.org/protobuf/proto"
)

// Sink 把写事务产出的 Delta 翻译为 syncv1 并写入 policy_outbox（同事务，原子）。
type Sink struct{}

// NewSink 构造 Sink。
func NewSink() *Sink { return &Sink{} }

// Persist 实现 policy.DeltaSink。
func (s *Sink) Persist(ctx context.Context, tx cp.DBTX, appID int64, d *cp.Delta) error {
	pd := translate.DeltaToProto(*d)
	blob, err := proto.Marshal(pd)
	if err != nil {
		return fmt.Errorf("outbox: marshal delta: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO policy_outbox (app_id, version, delta_proto) VALUES ($1,$2,$3)`,
		appID, d.Version, blob); err != nil {
		return fmt.Errorf("outbox: insert: %w", err)
	}
	return nil
}
```

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/controlplane/outbox/ ./internal/controlplane/policy/ -v`
预期：PASS（含既有 policy 测试在 sink=nil 下不变）。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/outbox/sink.go internal/controlplane/outbox/sink_test.go internal/controlplane/policy/
git commit -m "feat(outbox): DeltaSink 写事务内落 outbox + PolicyManager 注入（原子）"
```

---

## 任务 7：outbox relay 循环

**文件：**
- 创建：`internal/controlplane/outbox/relay.go`
- 测试：`internal/controlplane/outbox/relay_test.go`

`RunRelayLoop` 阻塞至 ctx 取消；每轮 drain 未发布行→`Publisher.Publish`→标记 `published_at`。publish 失败保持未发布、下轮重试，不动 DB。

- [ ] **步骤 1：编写失败的测试**

`internal/controlplane/outbox/relay_test.go`：

```go
package outbox_test

import (
	"context"
	"sync"
	"testing"
	"time"

	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/outbox"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// recordingPub 记录收到的 (appID, version)。
type recordingPub struct {
	mu   sync.Mutex
	got  []uint64
	fail bool
}

func (p *recordingPub) Publish(ctx context.Context, appID int64, d *syncv1.Delta) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fail {
		return assertErr
	}
	p.got = append(p.got, d.Version)
	return nil
}
func (p *recordingPub) versions() []uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]uint64(nil), p.got...)
}

func seedOutbox(t *testing.T, db interface {
	Exec(string, ...any) (interface{ RowsAffected() (int64, error) }, error)
}) {} // 占位，见下用直接 SQL

func TestRelay_DrainsAndMarksPublished(t *testing.T) {
	db := dbtest.SetupSchema(t)
	// 直接灌两条未发布 outbox 行（app=1 v1、v2）
	for _, v := range []int64{1, 2} {
		blob, _ := proto.Marshal(&syncv1.Delta{Version: uint64(v)})
		_, err := db.Exec(`INSERT INTO policy_outbox (app_id, version, delta_proto) VALUES (1,$1,$2)`, v, blob)
		require.NoError(t, err)
	}

	pub := &recordingPub{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = outbox.RunRelayLoop(ctx, db, pub, 20*time.Millisecond) }()

	require.Eventually(t, func() bool {
		var unpublished int
		if err := db.QueryRow(`SELECT count(*) FROM policy_outbox WHERE published_at IS NULL`).Scan(&unpublished); err != nil {
			return false
		}
		return unpublished == 0
	}, 5*time.Second, 50*time.Millisecond)

	assert.ElementsMatch(t, []uint64{1, 2}, pub.versions())
}

func TestRelay_PublishFailureKeepsRowUnpublished(t *testing.T) {
	db := dbtest.SetupSchema(t)
	blob, _ := proto.Marshal(&syncv1.Delta{Version: 1})
	_, err := db.Exec(`INSERT INTO policy_outbox (app_id, version, delta_proto) VALUES (1,1,$1)`, blob)
	require.NoError(t, err)

	pub := &recordingPub{fail: true}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = outbox.RunRelayLoop(ctx, db, pub, 20*time.Millisecond) }()

	// 给若干轮机会；行应始终保持未发布
	require.Never(t, func() bool {
		var published int
		_ = db.QueryRow(`SELECT count(*) FROM policy_outbox WHERE published_at IS NOT NULL`).Scan(&published)
		return published > 0
	}, 500*time.Millisecond, 50*time.Millisecond)
}
```

> 注：`seedOutbox` 占位函数仅为示意，实际用上面内联 SQL。实现子代理可删除该占位。

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/outbox/ -run TestRelay -v`
预期：FAIL（`RunRelayLoop` 未定义）。

- [ ] **步骤 3：编写实现**

`internal/controlplane/outbox/relay.go`：

```go
package outbox

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/broadcast"
	"google.golang.org/protobuf/proto"
)

// RunRelayLoop 跑 outbox 投递循环：drain 未发布行→Publisher.Publish→标记 published_at。
// 阻塞至 ctx 取消。poll 为无新行时的轮询间隔。at-least-once：失败行下轮重试，不动业务数据。
func RunRelayLoop(ctx context.Context, db *sql.DB, pub broadcast.Publisher, poll time.Duration) error {
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		n, err := drainOnce(ctx, db, pub)
		if err != nil && ctx.Err() == nil {
			// 记录但不中断循环（DB 抖动等）；下轮重试。
			// 生产可接日志；此处吞掉以维持续投。
			n = 0
		}
		if n > 0 {
			continue // 还有积压，立刻下一批
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// drainOnce 取一批未发布行并尝试发布；返回成功发布的行数。
func drainOnce(ctx context.Context, db *sql.DB, pub broadcast.Publisher) (int, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, app_id, delta_proto FROM policy_outbox
		 WHERE published_at IS NULL ORDER BY id ASC LIMIT 100`)
	if err != nil {
		return 0, fmt.Errorf("outbox: query unpublished: %w", err)
	}
	type rec struct {
		id    int64
		appID int64
		blob  []byte
	}
	var batch []rec
	for rows.Next() {
		var r rec
		if err := rows.Scan(&r.id, &r.appID, &r.blob); err != nil {
			rows.Close()
			return 0, fmt.Errorf("outbox: scan: %w", err)
		}
		batch = append(batch, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	published := 0
	for _, r := range batch {
		var d syncv1.Delta
		if err := proto.Unmarshal(r.blob, &d); err != nil {
			// 坏行：跳过不阻塞后续（与 ③-2 坏消息跳过一致）。可记日志。
			continue
		}
		if err := pub.Publish(ctx, r.appID, &d); err != nil {
			// 发布失败：保持未发布，停止本批（按序投递），下轮重试。
			break
		}
		if _, err := db.ExecContext(ctx,
			`UPDATE policy_outbox SET published_at=now() WHERE id=$1`, r.id); err != nil {
			return published, fmt.Errorf("outbox: mark published: %w", err)
		}
		published++
	}
	return published, nil
}
```

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/controlplane/outbox/ -run TestRelay -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/outbox/relay.go internal/controlplane/outbox/relay_test.go
git commit -m "feat(outbox): RunRelayLoop 可靠投递（drain→publish→标记，at-least-once）"
```

---

## 任务 8：admin.proto + buf 生成

**文件：**
- 创建：`api/proto/sydom/admin/v1/admin.proto`
- 生成：`gen/sydom/admin/v1/*.pb.go`、`*_grpc.pb.go`
- 测试：`internal/controlplane/mgmt/proto_smoke_test.go`

> buf DEFAULT lint 的 `PACKAGE_DIRECTORY_MATCH` 要求 proto 目录匹配 package：目录 `api/proto/sydom/admin/v1/`、package `sydom.admin.v1`、go_package 指向 `gen/sydom/admin/v1`。

- [ ] **步骤 1：编写失败的测试（先放编译冒烟）**

`internal/controlplane/mgmt/proto_smoke_test.go`：

```go
package mgmt_test

import (
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
)

func TestProtoGenerated(t *testing.T) {
	_ = &adminv1.GrantPermissionRequest{}
	_ = &adminv1.WriteResponse{}
	_ = &adminv1.CreateApplicationRequest{}
	_ = &adminv1.CreateOperatorRequest{}
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go build ./internal/controlplane/mgmt/`
预期：FAIL（`gen/sydom/admin/v1` 不存在）。

- [ ] **步骤 3：编写 proto + 生成**

`api/proto/sydom/admin/v1/admin.proto`：

```proto
syntax = "proto3";

package sydom.admin.v1;

option go_package = "github.com/nickZFZ/Sydom/gen/sydom/admin/v1;adminv1";

// AdminService 是控制面管理写入面（gRPC 优先；REST 后补）。
// 认证：HMAC（复用 ② auth，metadata 携 operator principal + 签名）。
// 鉴权：每个写 RPC 由服务端按 (resource, action, app_id 域) 做元-RBAC 校验。
service AdminService {
  // —— 业务策略写（下沉 PolicyManager）——
  rpc CreateRole(CreateRoleRequest) returns (CreateRoleResponse);
  rpc DeleteRole(DeleteRoleRequest) returns (WriteResponse);
  rpc UpsertPermission(UpsertPermissionRequest) returns (UpsertPermissionResponse);
  rpc GrantPermission(GrantPermissionRequest) returns (WriteResponse);
  rpc RevokePermission(RevokePermissionRequest) returns (WriteResponse);
  rpc AddRoleInheritance(RoleInheritanceRequest) returns (WriteResponse);
  rpc RemoveRoleInheritance(RoleInheritanceRequest) returns (WriteResponse);
  rpc BindUserRole(UserRoleRequest) returns (WriteResponse);
  rpc UnbindUserRole(UserRoleRequest) returns (WriteResponse);
  rpc UpsertDataPolicy(UpsertDataPolicyRequest) returns (UpsertDataPolicyResponse);
  rpc DeleteDataPolicy(DeleteDataPolicyRequest) returns (WriteResponse);

  // —— 应用管理 ——
  rpc CreateApplication(CreateApplicationRequest) returns (CreateApplicationResponse);
  rpc SetApplicationStatus(SetApplicationStatusRequest) returns (WriteResponse);
  rpc ListApplications(ListApplicationsRequest) returns (ListApplicationsResponse);

  // —— 管理员自身管理（system 域，super-admin 专属）——
  rpc CreateOperator(CreateOperatorRequest) returns (CreateOperatorResponse);
  rpc SetOperatorStatus(SetOperatorStatusRequest) returns (WriteResponse);
  rpc CreateAdminRole(CreateAdminRoleRequest) returns (CreateAdminRoleResponse);
  rpc GrantAdminRole(GrantAdminRoleRequest) returns (WriteResponse);
  rpc BindOperatorRole(BindOperatorRoleRequest) returns (WriteResponse);
}

// WriteResponse 是写类 RPC 通用响应：回带写后版本（无策略影响时 version 同当前、changed=false）。
message WriteResponse {
  uint64 version = 1;
  bool changed = 2;
}

message CreateRoleRequest {
  uint64 app_id = 1;
  string code = 2;
  string name = 3;
}
message CreateRoleResponse {
  int64 role_id = 1;
  uint64 version = 2;
  bool changed = 3;
}
message DeleteRoleRequest {
  uint64 app_id = 1;
  int64 role_id = 2;
}
message UpsertPermissionRequest {
  uint64 app_id = 1;
  string code = 2;
  string resource = 3;
  string action = 4;
  string ptype = 5;
  string name = 6;
}
message UpsertPermissionResponse {
  int64 permission_id = 1;
  uint64 version = 2;
  bool changed = 3;
}
message GrantPermissionRequest {
  uint64 app_id = 1;
  int64 role_id = 2;
  int64 permission_id = 3;
  string eft = 4; // "allow" / "deny"
}
message RevokePermissionRequest {
  uint64 app_id = 1;
  int64 role_id = 2;
  int64 permission_id = 3;
}
message RoleInheritanceRequest {
  uint64 app_id = 1;
  int64 child_role_id = 2;
  int64 parent_role_id = 3;
}
message UserRoleRequest {
  uint64 app_id = 1;
  string user_id = 2;
  int64 role_id = 3;
}
message UpsertDataPolicyRequest {
  uint64 app_id = 1;
  int64 id = 2; // 0=新增
  string subject_type = 3;
  string subject_id = 4;
  string resource = 5;
  string condition = 6; // 条件树 JSON
}
message UpsertDataPolicyResponse {
  int64 data_policy_id = 1;
  uint64 version = 2;
  bool changed = 3;
}
message DeleteDataPolicyRequest {
  uint64 app_id = 1;
  int64 data_policy_id = 2;
}

message CreateApplicationRequest {
  string tenant_name = 1;
  string domain = 2;
  string name = 3;
  string app_key = 4;
}
message CreateApplicationResponse {
  uint64 app_id = 1;
  string app_secret = 2; // 明文仅此一次返回，服务端只存加密
}
message SetApplicationStatusRequest {
  uint64 app_id = 1;
  uint32 status = 2; // 1=active, 2=disabled
}
message ListApplicationsRequest {}
message ApplicationSummary {
  uint64 app_id = 1;
  string domain = 2;
  string name = 3;
  string app_key = 4;
  uint32 status = 5;
  uint64 current_version = 6;
}
message ListApplicationsResponse {
  repeated ApplicationSummary applications = 1;
}

message CreateOperatorRequest {
  string principal = 1;
}
message CreateOperatorResponse {
  int64 operator_id = 1;
  string secret = 2; // 明文仅此一次返回
}
message SetOperatorStatusRequest {
  int64 operator_id = 1;
  uint32 status = 2;
}
message CreateAdminRoleRequest {
  string code = 1;
  string name = 2;
}
message CreateAdminRoleResponse {
  int64 role_id = 1;
}
message GrantAdminRoleRequest {
  int64 role_id = 1;
  string domain = 2;   // app_id 字符串或 "*"
  string resource = 3;
  string action = 4;
}
message BindOperatorRoleRequest {
  int64 operator_id = 1;
  int64 role_id = 2;
  string domain = 3;
}
```

生成（在 worktree 根，PATH 含 GOBIN，参照 Makefile）：
```bash
make proto-gen   # = buf lint && buf generate
```

- [ ] **步骤 4：运行验证通过**

运行：`go build ./internal/controlplane/mgmt/ && go test ./internal/controlplane/mgmt/ -run TestProtoGenerated -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add api/proto/sydom/admin/v1/admin.proto gen/sydom/admin/v1/ internal/controlplane/mgmt/proto_smoke_test.go
git commit -m "feat(admin): AdminService proto + buf 生成代码"
```

---

## 任务 9：mgmt 鉴权映射 + 鉴权/status 拦截器

**文件：**
- 创建：`internal/controlplane/mgmt/authz.go`
- 测试：`internal/controlplane/mgmt/authz_test.go`

定义 RPC FullMethod → `(resource, action, isWrite, domainOf)` 静态映射；鉴权拦截器据此调 `adminauthz.Enforcer.Enforce`；status 写拦截器对"业务策略写 + 具体 app"读 `application.status`，非 active 拒绝。

`domainOf` 从请求提取 app_id 作 domain（system 级动作返回 `"*"`）。principal 来自 `auth.AppIDFromContext`（任务 10 用 `auth.UnaryServerInterceptor` 注入）。

- [ ] **步骤 1：编写失败的测试**

`internal/controlplane/mgmt/authz_test.go`：

```go
package mgmt_test

import (
	"context"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/auth"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestAuthzInterceptor_EnforcesPerAppDomain(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	ctx := context.Background()

	// alice 仅在本 app 域有 grant:create（GrantPermission 的资源/动作见映射表）
	opID, _ := adminauthz.InsertOperator(ctx, db, "alice", []byte("x"))
	roleID, _ := adminauthz.InsertRole(ctx, db, "r", "n")
	domain := mgmt.DomainOfAppID(appID) // 把 app_id 转成 domain 字符串
	require.NoError(t, adminauthz.InsertRoleGrant(ctx, db, roleID, domain, "grant", "create"))
	require.NoError(t, adminauthz.InsertSubjectRole(ctx, db, opID, roleID, domain))

	enf, err := adminauthz.NewEnforcer(db)
	require.NoError(t, err)
	interceptor := mgmt.AuthzUnaryInterceptor(enf)

	// 放行：GrantPermission 针对本 app
	authedCtx := auth.WithAppID(ctx, "alice")
	called := false
	_, err = interceptor(authedCtx,
		&adminv1.GrantPermissionRequest{AppId: uint64(appID), RoleId: roleID, PermissionId: 1, Eft: "allow"},
		&grpc.UnaryServerInfo{FullMethod: "/sydom.admin.v1.AdminService/GrantPermission"},
		func(ctx context.Context, req any) (any, error) { called = true; return nil, nil })
	require.NoError(t, err)
	require.True(t, called)

	// 拒绝：同一 operator 对另一个 app（域不符）
	_, err = interceptor(authedCtx,
		&adminv1.GrantPermissionRequest{AppId: 999, RoleId: roleID, PermissionId: 1, Eft: "allow"},
		&grpc.UnaryServerInfo{FullMethod: "/sydom.admin.v1.AdminService/GrantPermission"},
		func(ctx context.Context, req any) (any, error) { return nil, nil })
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

func TestStatusInterceptor_BlocksWriteOnDisabledApp(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	_, err := db.Exec(`UPDATE application SET status=2 WHERE id=$1`, appID)
	require.NoError(t, err)

	interceptor := mgmt.StatusWriteUnaryInterceptor(db)
	_, err = interceptor(context.Background(),
		&adminv1.GrantPermissionRequest{AppId: uint64(appID)},
		&grpc.UnaryServerInfo{FullMethod: "/sydom.admin.v1.AdminService/GrantPermission"},
		func(ctx context.Context, req any) (any, error) { return nil, nil })
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/mgmt/ -run 'TestAuthz|TestStatus' -v`
预期：FAIL（未定义）。

- [ ] **步骤 3：编写实现**

`internal/controlplane/mgmt/authz.go`：

```go
package mgmt

import (
	"context"
	"database/sql"
	"strconv"

	"github.com/nickZFZ/Sydom/internal/auth"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// appIDGetter 是带 app_id 的请求消息（业务策略写与应用 status 写均含 app_id）。
type appIDGetter interface{ GetAppId() uint64 }

// rpcRule 描述某 RPC 的鉴权要素。
type rpcRule struct {
	resource string
	action   string
	isWrite  bool // 是否受 status 写拦截（仅针对具体 app 的业务策略写）
	system   bool // true=system 域（"*"），不取请求 app_id
}

// ruleTable 把 FullMethod 映射到鉴权要素。集中维护，避免散落判断。
var ruleTable = map[string]rpcRule{
	"/sydom.admin.v1.AdminService/CreateRole":            {"role", "create", true, false},
	"/sydom.admin.v1.AdminService/DeleteRole":            {"role", "delete", true, false},
	"/sydom.admin.v1.AdminService/UpsertPermission":      {"permission", "update", true, false},
	"/sydom.admin.v1.AdminService/GrantPermission":       {"grant", "create", true, false},
	"/sydom.admin.v1.AdminService/RevokePermission":      {"grant", "delete", true, false},
	"/sydom.admin.v1.AdminService/AddRoleInheritance":    {"inheritance", "create", true, false},
	"/sydom.admin.v1.AdminService/RemoveRoleInheritance": {"inheritance", "delete", true, false},
	"/sydom.admin.v1.AdminService/BindUserRole":          {"binding", "create", true, false},
	"/sydom.admin.v1.AdminService/UnbindUserRole":        {"binding", "delete", true, false},
	"/sydom.admin.v1.AdminService/UpsertDataPolicy":      {"data_policy", "update", true, false},
	"/sydom.admin.v1.AdminService/DeleteDataPolicy":      {"data_policy", "delete", true, false},
	"/sydom.admin.v1.AdminService/CreateApplication":     {"application", "create", false, true},
	"/sydom.admin.v1.AdminService/SetApplicationStatus":  {"application", "update", false, false},
	"/sydom.admin.v1.AdminService/ListApplications":      {"application", "read", false, true},
	"/sydom.admin.v1.AdminService/CreateOperator":        {"admin", "create", false, true},
	"/sydom.admin.v1.AdminService/SetOperatorStatus":     {"admin", "update", false, true},
	"/sydom.admin.v1.AdminService/CreateAdminRole":       {"admin", "create", false, true},
	"/sydom.admin.v1.AdminService/GrantAdminRole":        {"admin", "update", false, true},
	"/sydom.admin.v1.AdminService/BindOperatorRole":      {"admin", "update", false, true},
}

// DomainOfAppID 把 app_id 转成 casbin domain 字符串。
func DomainOfAppID(appID int64) string { return strconv.FormatInt(appID, 10) }

// AuthzUnaryInterceptor 据 ruleTable + 请求 app_id 做元-RBAC 校验，并注入 operator 到 cp.WithOperator。
func AuthzUnaryInterceptor(enf *adminauthz.Enforcer) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		principal, ok := auth.AppIDFromContext(ctx) // 认证拦截器注入的 operator principal
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "missing operator identity")
		}
		rule, known := ruleTable[info.FullMethod]
		if !known {
			return nil, status.Error(codes.PermissionDenied, "unknown method")
		}
		domain := "*"
		if !rule.system {
			g, ok := req.(appIDGetter)
			if !ok {
				return nil, status.Error(codes.Internal, "request missing app_id")
			}
			domain = DomainOfAppID(int64(g.GetAppId()))
		}
		allow, err := enf.Enforce(ctx, principal, domain, rule.resource, rule.action)
		if err != nil || !allow {
			return nil, status.Error(codes.PermissionDenied, "permission denied")
		}
		// 鉴权通过：注入 operator 供审计（接 ③-1 policy_audit_log）。
		return handler(cp.WithOperator(ctx, principal), req)
	}
}

// StatusWriteUnaryInterceptor 对"具体 app 的业务策略写"拦截 disabled app。
func StatusWriteUnaryInterceptor(db *sql.DB) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		rule, known := ruleTable[info.FullMethod]
		if !known || !rule.isWrite {
			return handler(ctx, req)
		}
		g, ok := req.(appIDGetter)
		if !ok {
			return handler(ctx, req)
		}
		var st int16
		err := db.QueryRowContext(ctx,
			`SELECT status FROM application WHERE id=$1`, int64(g.GetAppId())).Scan(&st)
		if err != nil {
			return nil, status.Error(codes.NotFound, "unknown application")
		}
		if st != 1 {
			return nil, status.Error(codes.FailedPrecondition, "application disabled")
		}
		return handler(ctx, req)
	}
}
```

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/controlplane/mgmt/ -run 'TestAuthz|TestStatus' -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/mgmt/authz.go internal/controlplane/mgmt/authz_test.go
git commit -m "feat(mgmt): RPC 鉴权映射 + 元-RBAC 鉴权拦截器 + status 写拦截器"
```

---

## 任务 10：mgmt AdminService 业务写 RPC + gRPC 装配

**文件：**
- 创建：`internal/controlplane/mgmt/server.go`
- 测试：`internal/controlplane/mgmt/server_test.go`

实现 `AdminServer`（业务策略写 RPC，下沉 `PolicyManager`）与 `NewGRPCServer`（链接认证→鉴权→status 三拦截器）。认证复用 `auth.UnaryServerInterceptor(operatorResolver)`。

- [ ] **步骤 1：编写失败的测试**

`internal/controlplane/mgmt/server_test.go`：

```go
package mgmt_test

import (
	"context"
	"database/sql"
	"net"
	"testing"
	"time"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/auth"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/controlplane/outbox"
	"github.com/nickZFZ/Sydom/internal/controlplane/policy"
	"github.com/nickZFZ/Sydom/internal/crypto"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func mk() []byte { k := make([]byte, crypto.KeySize); for i := range k { k[i] = 0x2a }; return k }

// startMgmt 起带三拦截器的 AdminService，播种一个 super-admin operator 并返回其客户端连接。
func startMgmt(t *testing.T, db *sql.DB) (adminv1.AdminServiceClient, string) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, adminauthz.EnsureRootOperator(ctx, db, mk(), "root", []byte("root-secret")))

	resolver, err := adminauthz.NewOperatorResolver(db, mk())
	require.NoError(t, err)
	enf, err := adminauthz.NewEnforcer(db)
	require.NoError(t, err)
	mgr := policy.NewPolicyManager(db, outbox.NewSink())

	g := mgmt.NewGRPCServer(mgmt.NewAdminServer(db, mgr, mk()), resolver, enf, db)
	lis := bufconn.Listen(1 << 20)
	go func() { _ = g.Serve(lis) }()
	t.Cleanup(g.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(auth.NewPerRPCCredentials("root", []byte("root-secret"), false)),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return adminv1.NewAdminServiceClient(conn), "root"
}

func TestAdminService_CreateRoleAndGrant_WritesVersionAndOutbox(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	cli, _ := startMgmt(t, db)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cr, err := cli.CreateRole(ctx, &adminv1.CreateRoleRequest{AppId: uint64(appID), Code: "manager", Name: "经理"})
	require.NoError(t, err)
	up, err := cli.UpsertPermission(ctx, &adminv1.UpsertPermissionRequest{
		AppId: uint64(appID), Code: "order.read", Resource: "order", Action: "read", Ptype: "p", Name: "读订单"})
	require.NoError(t, err)
	w, err := cli.GrantPermission(ctx, &adminv1.GrantPermissionRequest{
		AppId: uint64(appID), RoleId: cr.RoleId, PermissionId: up.PermissionId, Eft: "allow"})
	require.NoError(t, err)
	require.True(t, w.Changed)
	require.Greater(t, w.Version, uint64(0))

	// outbox 落了该版本
	var n int
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM policy_outbox WHERE app_id=$1 AND version=$2`, appID, w.Version).Scan(&n))
	require.Equal(t, 1, n)
}

func TestAdminService_UnauthenticatedRejected(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	_, _ = startMgmt(t, db)

	// 无凭据连接
	lis := bufconn.Listen(1 << 20) // 复用一个独立 server 太重；直接验证：用错误 secret
	_ = lis
	cli, _ := startMgmt(t, db)
	_ = cli
	// 用错误 secret 的连接
	resolver, _ := adminauthz.NewOperatorResolver(db, mk())
	enf, _ := adminauthz.NewEnforcer(db)
	mgr := policy.NewPolicyManager(db, outbox.NewSink())
	g := mgmt.NewGRPCServer(mgmt.NewAdminServer(db, mgr, mk()), resolver, enf, db)
	l := bufconn.Listen(1 << 20)
	go func() { _ = g.Serve(l) }()
	t.Cleanup(g.Stop)
	conn, _ := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return l.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(auth.NewPerRPCCredentials("root", []byte("WRONG"), false)))
	t.Cleanup(func() { _ = conn.Close() })
	bad := adminv1.NewAdminServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := bad.CreateRole(ctx, &adminv1.CreateRoleRequest{AppId: uint64(appID), Code: "x", Name: "y"})
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}
```

> 说明：`NewAdminServer(db, mgr, appQuery)` 第三参为应用读取依赖；本期直接传 `*sql.DB`（同库）。`NewGRPCServer(srv, resolver, enf, db)` 装配三拦截器。

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/mgmt/ -run TestAdminService -v`
预期：FAIL（NewAdminServer/NewGRPCServer 未定义）。

- [ ] **步骤 3：编写实现**

`internal/controlplane/mgmt/server.go`：

```go
package mgmt

import (
	"context"
	"database/sql"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/auth"
	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/policy"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const maxMsgSize = 16 * 1024 * 1024

// AdminServer 实现 adminv1.AdminServiceServer。
type AdminServer struct {
	adminv1.UnimplementedAdminServiceServer
	db        *sql.DB
	mgr       *policy.PolicyManager
	masterKey []byte // 加密新建的 app/operator 凭据（任务 11 用）
}

// NewAdminServer 构造。masterKey 用于加密 CreateApplication/CreateOperator 生成的凭据。
func NewAdminServer(db *sql.DB, mgr *policy.PolicyManager, masterKey []byte) *AdminServer {
	k := make([]byte, len(masterKey))
	copy(k, masterKey)
	return &AdminServer{db: db, mgr: mgr, masterKey: k}
}

// writeResp 把 (delta, err) 归一为 WriteResponse；delta==nil 表示无策略影响。
func writeResp(d *cp.Delta, err error) (*adminv1.WriteResponse, error) {
	if err != nil {
		return nil, status.Errorf(codes.Internal, "write: %v", err)
	}
	if d == nil {
		return &adminv1.WriteResponse{Changed: false}, nil
	}
	return &adminv1.WriteResponse{Version: uint64(d.Version), Changed: true}, nil
}

func (s *AdminServer) CreateRole(ctx context.Context, r *adminv1.CreateRoleRequest) (*adminv1.CreateRoleResponse, error) {
	roleID, d, err := s.mgr.CreateRole(ctx, int64(r.AppId), r.Code, r.Name)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create role: %v", err)
	}
	resp := &adminv1.CreateRoleResponse{RoleId: roleID}
	if d != nil {
		resp.Version, resp.Changed = uint64(d.Version), true
	}
	return resp, nil
}

func (s *AdminServer) DeleteRole(ctx context.Context, r *adminv1.DeleteRoleRequest) (*adminv1.WriteResponse, error) {
	return writeResp(s.mgr.DeleteRole(ctx, int64(r.AppId), r.RoleId))
}

func (s *AdminServer) UpsertPermission(ctx context.Context, r *adminv1.UpsertPermissionRequest) (*adminv1.UpsertPermissionResponse, error) {
	permID, d, err := s.mgr.UpsertPermission(ctx, int64(r.AppId), r.Code, r.Resource, r.Action, r.Ptype, r.Name)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "upsert permission: %v", err)
	}
	resp := &adminv1.UpsertPermissionResponse{PermissionId: permID}
	if d != nil {
		resp.Version, resp.Changed = uint64(d.Version), true
	}
	return resp, nil
}

func (s *AdminServer) GrantPermission(ctx context.Context, r *adminv1.GrantPermissionRequest) (*adminv1.WriteResponse, error) {
	return writeResp(s.mgr.GrantPermission(ctx, int64(r.AppId), r.RoleId, r.PermissionId, r.Eft))
}
func (s *AdminServer) RevokePermission(ctx context.Context, r *adminv1.RevokePermissionRequest) (*adminv1.WriteResponse, error) {
	return writeResp(s.mgr.RevokePermission(ctx, int64(r.AppId), r.RoleId, r.PermissionId))
}
func (s *AdminServer) AddRoleInheritance(ctx context.Context, r *adminv1.RoleInheritanceRequest) (*adminv1.WriteResponse, error) {
	return writeResp(s.mgr.AddRoleInheritance(ctx, int64(r.AppId), r.ChildRoleId, r.ParentRoleId))
}
func (s *AdminServer) RemoveRoleInheritance(ctx context.Context, r *adminv1.RoleInheritanceRequest) (*adminv1.WriteResponse, error) {
	return writeResp(s.mgr.RemoveRoleInheritance(ctx, int64(r.AppId), r.ChildRoleId, r.ParentRoleId))
}
func (s *AdminServer) BindUserRole(ctx context.Context, r *adminv1.UserRoleRequest) (*adminv1.WriteResponse, error) {
	return writeResp(s.mgr.BindUserRole(ctx, int64(r.AppId), r.UserId, r.RoleId))
}
func (s *AdminServer) UnbindUserRole(ctx context.Context, r *adminv1.UserRoleRequest) (*adminv1.WriteResponse, error) {
	return writeResp(s.mgr.UnbindUserRole(ctx, int64(r.AppId), r.UserId, r.RoleId))
}
func (s *AdminServer) UpsertDataPolicy(ctx context.Context, r *adminv1.UpsertDataPolicyRequest) (*adminv1.UpsertDataPolicyResponse, error) {
	d, err := s.mgr.UpsertDataPolicy(ctx, int64(r.AppId), cp.DataPolicy{
		ID: r.Id, SubjectType: r.SubjectType, SubjectID: r.SubjectId, Resource: r.Resource, Condition: r.Condition,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "upsert data policy: %v", err)
	}
	resp := &adminv1.UpsertDataPolicyResponse{}
	if d != nil && len(d.DataChanges) > 0 {
		resp.DataPolicyId = d.DataChanges[0].Policy.ID
		resp.Version, resp.Changed = uint64(d.Version), true
	}
	return resp, nil
}
func (s *AdminServer) DeleteDataPolicy(ctx context.Context, r *adminv1.DeleteDataPolicyRequest) (*adminv1.WriteResponse, error) {
	return writeResp(s.mgr.DeleteDataPolicy(ctx, int64(r.AppId), r.DataPolicyId))
}

// NewGRPCServer 装配认证→鉴权→status 三拦截器（按序）并注册 AdminService。
func NewGRPCServer(srv *AdminServer, resolver auth.SecretResolver, enf *adminauthz.Enforcer, db *sql.DB) *grpc.Server {
	chain := grpc.ChainUnaryInterceptor(
		auth.UnaryServerInterceptor(resolver),   // 1. HMAC 认证 → 注入 principal（auth.AppIDFromContext）
		AuthzUnaryInterceptor(enf),               // 2. 元-RBAC 鉴权 → 注入 cp.WithOperator
		StatusWriteUnaryInterceptor(db),          // 3. status 写拦截
	)
	g := grpc.NewServer(grpc.MaxRecvMsgSize(maxMsgSize), grpc.MaxSendMsgSize(maxMsgSize), chain)
	adminv1.RegisterAdminServiceServer(g, srv)
	return g
}
```

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/controlplane/mgmt/ -run TestAdminService -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/mgmt/server.go internal/controlplane/mgmt/server_test.go
git commit -m "feat(mgmt): AdminService 业务写 RPC + 三拦截器 gRPC 装配"
```

---

## 任务 11：mgmt 应用管理 + 管理员自管 RPC

**文件：**
- 修改：`internal/controlplane/mgmt/server.go`（追加 RPC）
- 创建：`internal/controlplane/mgmt/admin_ops.go`（应用/管理员 DAO 辅助）
- 测试：`internal/controlplane/mgmt/admin_ops_test.go`

实现 `CreateApplication`/`SetApplicationStatus`/`ListApplications` 与管理员自管 `CreateOperator`/`SetOperatorStatus`/`CreateAdminRole`/`GrantAdminRole`/`BindOperatorRole`。创建类生成随机凭据，明文仅返回一次、入库存密文（复用 `crypto`）。管理员自管的写需 bump `admin_policy_version`（触发 enforcer 重载）。

- [ ] **步骤 1：编写失败的测试**

`internal/controlplane/mgmt/admin_ops_test.go`：

```go
package mgmt_test

import (
	"context"
	"testing"
	"time"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestAdminService_ApplicationLifecycle(t *testing.T) {
	db := dbtest.SetupSchema(t)
	cli, _ := startMgmt(t, db) // root = super-admin
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cr, err := cli.CreateApplication(ctx, &adminv1.CreateApplicationRequest{
		TenantName: "acme", Domain: "order-system", Name: "订单", AppKey: "AK_x"})
	require.NoError(t, err)
	require.NotEmpty(t, cr.AppSecret)
	require.Greater(t, cr.AppId, uint64(0))

	// disable 后业务写被拦
	_, err = cli.SetApplicationStatus(ctx, &adminv1.SetApplicationStatusRequest{AppId: cr.AppId, Status: 2})
	require.NoError(t, err)
	_, err = cli.CreateRole(ctx, &adminv1.CreateRoleRequest{AppId: cr.AppId, Code: "r", Name: "n"})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))

	apps, err := cli.ListApplications(ctx, &adminv1.ListApplicationsRequest{})
	require.NoError(t, err)
	require.NotEmpty(t, apps.Applications)
}

func TestAdminService_OperatorSelfManagement(t *testing.T) {
	db := dbtest.SetupSchema(t)
	cli, _ := startMgmt(t, db)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// super-admin 建一个新 operator + app 级角色 + grant + 绑定
	co, err := cli.CreateOperator(ctx, &adminv1.CreateOperatorRequest{Principal: "bob"})
	require.NoError(t, err)
	require.NotEmpty(t, co.Secret)
	role, err := cli.CreateAdminRole(ctx, &adminv1.CreateAdminRoleRequest{Code: "app1-admin", Name: "n"})
	require.NoError(t, err)
	_, err = cli.GrantAdminRole(ctx, &adminv1.GrantAdminRoleRequest{
		RoleId: role.RoleId, Domain: "1", Resource: "role", Action: "create"})
	require.NoError(t, err)
	_, err = cli.BindOperatorRole(ctx, &adminv1.BindOperatorRoleRequest{
		OperatorId: co.OperatorId, RoleId: role.RoleId, Domain: "1"})
	require.NoError(t, err)
	_ = time.Now()
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/mgmt/ -run 'TestAdminService_Application|TestAdminService_Operator' -v`
预期：FAIL（RPC 未实现，返回 Unimplemented）。

- [ ] **步骤 3：编写实现**

`internal/controlplane/mgmt/admin_ops.go`：

```go
package mgmt

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/crypto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// genSecret 生成 32 字节随机凭据的 hex 串。
func genSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (s *AdminServer) CreateApplication(ctx context.Context, r *adminv1.CreateApplicationRequest) (*adminv1.CreateApplicationResponse, error) {
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
	// 复用同名租户或新建（domain/app_key 唯一约束保证幂等失败可暴露）
	if err := tx.QueryRowContext(ctx,
		`INSERT INTO tenant (name) VALUES ($1)
		 ON CONFLICT (name) DO UPDATE SET name=EXCLUDED.name RETURNING id`, r.TenantName).Scan(&tenantID); err != nil {
		return nil, status.Errorf(codes.Internal, "tenant: %v", err)
	}
	var appID int64
	if err := tx.QueryRowContext(ctx,
		`INSERT INTO application (tenant_id, domain, name, app_key, app_secret_enc)
		 VALUES ($1,$2,$3,$4,$5) RETURNING id`,
		tenantID, r.Domain, r.Name, r.AppKey, enc).Scan(&appID); err != nil {
		return nil, status.Errorf(codes.AlreadyExists, "create application: %v", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "commit: %v", err)
	}
	return &adminv1.CreateApplicationResponse{AppId: uint64(appID), AppSecret: secret}, nil
}

func (s *AdminServer) SetApplicationStatus(ctx context.Context, r *adminv1.SetApplicationStatusRequest) (*adminv1.WriteResponse, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE application SET status=$1 WHERE id=$2`, int16(r.Status), int64(r.AppId))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "set status: %v", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, status.Error(codes.NotFound, "unknown application")
	}
	return &adminv1.WriteResponse{Changed: true}, nil
}

func (s *AdminServer) ListApplications(ctx context.Context, _ *adminv1.ListApplicationsRequest) (*adminv1.ListApplicationsResponse, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, domain, name, app_key, status, current_version FROM application ORDER BY id`)
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
	return out, rows.Err()
}

// —— 管理员自管：写后 bump admin_policy_version 触发 enforcer 重载 ——

func (s *AdminServer) CreateOperator(ctx context.Context, r *adminv1.CreateOperatorRequest) (*adminv1.CreateOperatorResponse, error) {
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
	id, err := adminauthz.InsertOperator(ctx, tx, r.Principal, enc)
	if err != nil {
		return nil, status.Errorf(codes.AlreadyExists, "%v", err)
	}
	if err := adminauthz.BumpPolicyVersion(ctx, tx); err != nil {
		return nil, status.Errorf(codes.Internal, "bump: %v", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "commit: %v", err)
	}
	return &adminv1.CreateOperatorResponse{OperatorId: id, Secret: secret}, nil
}

func (s *AdminServer) SetOperatorStatus(ctx context.Context, r *adminv1.SetOperatorStatusRequest) (*adminv1.WriteResponse, error) {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE admin_operator SET status=$1 WHERE id=$2`, int16(r.Status), r.OperatorId); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE admin_policy_version SET version=version+1 WHERE id=1`)
	return &adminv1.WriteResponse{Changed: true}, nil
}

func (s *AdminServer) CreateAdminRole(ctx context.Context, r *adminv1.CreateAdminRoleRequest) (*adminv1.CreateAdminRoleResponse, error) {
	id, err := adminauthz.InsertRole(ctx, s.db, r.Code, r.Name)
	if err != nil {
		return nil, status.Errorf(codes.AlreadyExists, "%v", err)
	}
	return &adminv1.CreateAdminRoleResponse{RoleId: id}, nil
}

func (s *AdminServer) GrantAdminRole(ctx context.Context, r *adminv1.GrantAdminRoleRequest) (*adminv1.WriteResponse, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin: %v", err)
	}
	defer tx.Rollback()
	if err := adminauthz.InsertRoleGrant(ctx, tx, r.RoleId, r.Domain, r.Resource, r.Action); err != nil {
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

func (s *AdminServer) BindOperatorRole(ctx context.Context, r *adminv1.BindOperatorRoleRequest) (*adminv1.WriteResponse, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin: %v", err)
	}
	defer tx.Rollback()
	if err := adminauthz.InsertSubjectRole(ctx, tx, r.OperatorId, r.RoleId, r.Domain); err != nil {
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

var _ = fmt.Sprintf // 保留 fmt 以便实现期插入诊断；如未用请删除
```

> 注：`AdminServer` 已在任务 10 带 `masterKey` 字段与构造参数，本任务的 `admin_ops.go` 方法直接用 `s.masterKey`，无需改 `server.go` 签名。

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/controlplane/mgmt/ -v`
预期：PASS（全部 mgmt 测试，含任务 10 用例）。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/mgmt/admin_ops.go internal/controlplane/mgmt/admin_ops_test.go
git commit -m "feat(mgmt): 应用管理 + 管理员自管 RPC（凭据加密 + 版本 bump 触发重载）"
```

---

## 任务 12：端到端 —— 写 → outbox → relay → Redis → ③-2 Subscribe

**文件：**
- 测试：`internal/controlplane/mgmt/endtoend_test.go`

验证完整链路：AdminService 写 → policy_outbox → `outbox.RunRelayLoop` → Redis → ③-2 `policysync.RunDispatchLoop`/Hub → `Subscribe` 流收到翻译后的 Delta。

- [ ] **步骤 1：编写失败的测试**

`internal/controlplane/mgmt/endtoend_test.go`：

```go
package mgmt_test

import (
	"context"
	"net"
	"testing"
	"time"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
	"github.com/nickZFZ/Sydom/internal/auth"
	"github.com/nickZFZ/Sydom/internal/controlplane/broadcast"
	"github.com/nickZFZ/Sydom/internal/controlplane/outbox"
	"github.com/nickZFZ/Sydom/internal/controlplane/policysync"
	"github.com/nickZFZ/Sydom/internal/controlplane/secret"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func TestEndToEnd_AdminWriteReachesSidecarStream(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	addr := dbtest.StartRedis(t)

	// 1) 管理面（含 outbox sink）
	cli, _ := startMgmt(t, db)

	// 2) relay：outbox → Redis
	pub := broadcast.NewRedisPublisher(redis.NewClient(&redis.Options{Addr: addr}))
	relayCtx, relayCancel := context.WithCancel(context.Background())
	defer relayCancel()
	go func() { _ = outbox.RunRelayLoop(relayCtx, db, pub, 20*time.Millisecond) }()

	// 3) ③-2 PolicySync 服务端 + RunDispatchLoop（Redis → Hub）
	//    需给 SeedApp 的 app 写一个可解密 secret 以便 Sidecar 认证。
	res, err := secret.NewResolver(db, mk())
	require.NoError(t, err)
	enc, err := res.EncryptSecret([]byte("sidecar-secret"))
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE application SET app_secret_enc=$1 WHERE app_key=$2`, enc, dbtest.SeedAppKey)
	require.NoError(t, err)

	ps := policysync.NewServer(db, policysync.Config{HeartbeatInterval: 50 * time.Millisecond, BufSize: 8})
	sub := broadcast.NewRedisSubscriber(redis.NewClient(&redis.Options{Addr: addr}))
	dispCtx, dispCancel := context.WithCancel(context.Background())
	defer dispCancel()
	go func() { _ = ps.RunDispatchLoop(dispCtx, sub) }()

	psSrv := policysync.NewGRPCServer(ps, res)
	lis := bufconn.Listen(1 << 20)
	go func() { _ = psSrv.Serve(lis) }()
	t.Cleanup(psSrv.Stop)
	sidecarConn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(auth.NewPerRPCCredentials(dbtest.SeedAppKey, []byte("sidecar-secret"), false)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = sidecarConn.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	stream, err := syncv1.NewPolicySyncClient(sidecarConn).Subscribe(ctx, &syncv1.SubscribeRequest{LastAppliedVersion: 0})
	require.NoError(t, err)

	// 4) 管理面持续写（产生 Delta），直到 Sidecar 流上收到 Delta
	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		var i int
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				i++
				cr, e := cli.CreateRole(ctx, &adminv1.CreateRoleRequest{
					AppId: uint64(appID), Code: "r" + itoa(i), Name: "n"})
				if e != nil || cr == nil {
					continue
				}
				up, e := cli.UpsertPermission(ctx, &adminv1.UpsertPermissionRequest{
					AppId: uint64(appID), Code: "p" + itoa(i), Resource: "res", Action: "read", Ptype: "p", Name: "n"})
				if e != nil || up == nil {
					continue
				}
				_, _ = cli.GrantPermission(ctx, &adminv1.GrantPermissionRequest{
					AppId: uint64(appID), RoleId: cr.RoleId, PermissionId: up.PermissionId, Eft: "allow"})
			}
		}
	}()

	var got *syncv1.Delta
	for got == nil {
		ev, err := stream.Recv()
		require.NoError(t, err)
		got = ev.GetDelta() // 跳过 SnapshotRequired/Heartbeat
	}
	require.Greater(t, got.Version, uint64(0))

	cancel()
	<-writeDone
}

// itoa 避免引入 strconv 仅为拼名。
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
```

> 提醒（已知陷阱）：后台写 goroutine 内**不得**用 `require.*`（非测试 goroutine Goexit 会吞诊断）；前台 Recv 循环用 require 合规。

- [ ] **步骤 2：运行验证失败**

先确认依赖齐全后运行：`go test ./internal/controlplane/mgmt/ -run TestEndToEnd -v`
预期：初次若有编译/装配缺口则 FAIL；补齐后转 PASS。

- [ ] **步骤 3：实现**

本任务无新生产代码——它串联既有组件。若 FAIL 源于装配细节（如 relay 未投、Sidecar 未认证），按错误定位修正测试装配，不改生产代码（除非暴露真实缺陷，则回溯对应任务修复）。

- [ ] **步骤 4：运行验证通过**

运行：
```bash
go test ./internal/controlplane/mgmt/ -run TestEndToEnd -count=5 -v   # 稳定性（应对时序）
go test ./internal/controlplane/mgmt/ -race -count=1
```
预期：均 PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/mgmt/endtoend_test.go
git commit -m "test(mgmt): 端到端——管理写→outbox→relay→Redis→Subscribe 流收 Delta"
```

---

## 收尾：全量验证

- [ ] `go build ./...` —— 无错误。
- [ ] `go test ./...` —— 全绿（需 Docker 起 PG+Redis；既有包不受影响）。
- [ ] `go test ./internal/controlplane/adminauthz/ ./internal/controlplane/outbox/ ./internal/controlplane/mgmt/ -race -count=1` —— 无数据竞争。
- [ ] `go vet ./...` 与 `gofmt -l internal/`（排除 gen/）—— 无告警、无未格式化。
- [ ] `make proto-check` —— 生成代码与 proto 同步且已入库。
- [ ] 调用 `superpowers:finishing-a-development-branch` 收尾合入。

---

## 自检结果

**1. 规格覆盖度：**
- §3 组件结构 → 任务 3-11 覆盖 adminauthz/outbox/mgmt 三包 + proto。
- §4.1 admin schema + 种子 → 任务 1。§4.2 outbox 表 → 任务 2。
- §5.1 DeltaSink 注入（写事务内原子落库）→ 任务 6。§5.2 relay → 任务 7。§5.3 AdminService 三组 RPC → 任务 8（proto）/10（业务写）/11（应用+管理员自管）。§5.4 三道拦截器 → 任务 9（鉴权+status）/10（认证复用 + 装配链）。
- §6 enforcer 模型/Adapter/版本化重载 → 任务 4。§7 bootstrap/fail-close/写原子/relay 容灾 → 任务 5（bootstrap）/6（原子）/7（容灾）/4-5（fail-close）。§8 测试 → 各任务 TDD + 任务 12 端到端。
- §9 接 ③-2 publish 侧 → 任务 6/7/12；沿用 ③-1（仅注入 DeltaSink）→ 任务 6。

**2. 占位符扫描：** 任务 7 测试含一个显式标注的 `seedOutbox` 占位（已注明删除）；任务 11 末尾 `var _ = fmt.Sprintf` 标注按需删除。无 "TODO/待定/后续实现" 式空洞步骤；所有代码步骤含完整代码。

**3. 类型一致性：**
- `adminauthz`：`InsertOperator/InsertRole/InsertRoleGrant/InsertSubjectRole/LoadPolicyRows/LoadGroupingRows/ReadPolicyVersion/BumpPolicyVersion`（任务 3）、`NewEnforcer/(*Enforcer).Enforce`（任务 4）、`NewOperatorResolver/(*OperatorResolver).ResolveSecret/EnsureRootOperator`（任务 5）跨任务一致。
- `policy.NewPolicyManager(db, sink)` 双参 + `policy.DeltaSink`（任务 6）被任务 10/11/12 一致使用。
- `outbox.NewSink()`（任务 6）、`outbox.RunRelayLoop(ctx, db, pub, poll)`（任务 7）签名一致。
- `mgmt.DomainOfAppID/AuthzUnaryInterceptor/StatusWriteUnaryInterceptor`（任务 9）、`NewAdminServer(db, mgr, masterKey)` + `NewGRPCServer(srv, resolver, enf, db)`（任务 10，签名自任务 10 即定型，任务 11 仅追加方法不改签名）一致。
- proto 消息/RPC 名（任务 8）与任务 10/11 调用一致：`GetAppId()`（鉴权拦截器 appIDGetter 依赖所有带 app_id 请求生成 `GetAppId()`，proto3 自动生成）。
- 复用既有：`auth.UnaryServerInterceptor/SecretResolver/AppIDFromContext/WithAppID/NewPerRPCCredentials`、`crypto.Encrypt/Decrypt/KeySize/ErrKeySize`、`translate.DeltaToProto`、`broadcast.Publisher/NewRedisPublisher/NewRedisSubscriber`、`policysync.NewServer/Config/NewGRPCServer/RunDispatchLoop`、`secret.NewResolver/EncryptSecret`、`cp.WithOperator/OperatorFromContext/DBTX/Delta/DataPolicy` 均与现有实现签名核对一致。

**注意事项（实现期）：**
- 任务 4 需先 `go get github.com/casbin/casbin/v3@v3.10.0`（casbin 当前仅为参考克隆，非 go.mod 依赖）。
- 任务 6 改 ③-1 `policy` 包：务必同步修正其 `*_test.go` 全部 `NewPolicyManager(` 调用为双参（sink=nil）。
- 任务 8 proto 目录/package 必须满足 buf `PACKAGE_DIRECTORY_MATCH`（`sydom.admin.v1` ↔ `api/proto/sydom/admin/v1/`）。
- 端到端/并发测试遵循已知陷阱：`Eventually`/后台 goroutine 内禁用 `require.*`。
