# 司域数据库 Schema 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 用 golang-migrate 在 PostgreSQL 上落地司域控制面的 10 张表，并用 testcontainers 起真实 PG 验证表结构、约束与版本号并发机制。

**架构：** 纯 SQL migration（golang-migrate 的 `.up.sql`/`.down.sql`，每张表一个版本，按外键依赖顺序编号）+ Go 集成测试（testcontainers-go 起 `postgres:16-alpine`，跑 migration，断言约束行为）。投影执行逻辑、DB Adapter 不在本计划（按规格 [§8](../specs/2026-05-31-sydom-database-schema-design.md) 划归控制面计划）。

**技术栈：** Go 1.22+、golang-migrate v4、testcontainers-go、lib/pq、testify。数据库 PostgreSQL（MySQL 方言后续补）。

**规格来源：** [docs/superpowers/specs/2026-05-31-sydom-database-schema-design.md](../specs/2026-05-31-sydom-database-schema-design.md)

---

## 文件结构

| 文件 | 职责 |
|------|------|
| `go.mod` / `go.sum` | Go module 与依赖 |
| `db/migrations/0000NN_<table>.up.sql` / `.down.sql` | 各表的建表 / 回滚 DDL（PostgreSQL 方言） |
| `internal/db/migrate.go` | `RunMigrations(dsn, sourceURL)` —— 封装 golang-migrate，供测试与运行复用 |
| `internal/db/helpers_test.go` | `startPostgres(t)` 起容器、`setupSchema(t)` 跑全量 migration 返回 `*sql.DB` |
| `internal/db/schema_test.go` | 各表约束行为测试、版本号并发测试、up/down 往返测试 |
| `Makefile` | `migrate-up` / `migrate-down` / `test` 命令 |

migration 版本号按外键依赖排序：tenant→application→role→permission→role_permission→role_inheritance→user_role_binding→data_policy→casbin_rule→policy_audit_log。golang-migrate 回滚时按版本反序执行 down，子表先于父表 DROP，外键依赖自然满足。

---

## 任务 1：初始化 Go module 与项目骨架

**文件：**
- 创建：`go.mod`
- 创建：`db/migrations/.gitkeep`
- 创建：`internal/db/doc.go`

- [ ] **步骤 1：初始化 module 与依赖**

运行（module path 按实际远程仓库替换）：
```bash
cd /home/tongyu/codes/Sydom
go mod init github.com/sydom/sydom
go get github.com/golang-migrate/migrate/v4@v4.17.1
go get github.com/golang-migrate/migrate/v4/database/postgres@v4.17.1
go get github.com/golang-migrate/migrate/v4/source/file@v4.17.1
go get github.com/testcontainers/testcontainers-go@v0.31.0
go get github.com/testcontainers/testcontainers-go/modules/postgres@v0.31.0
go get github.com/lib/pq@v1.10.9
go get github.com/stretchr/testify@v1.9.0
mkdir -p db/migrations internal/db
touch db/migrations/.gitkeep
```

- [ ] **步骤 2：创建包占位文件**

`internal/db/doc.go`：
```go
// Package db 提供司域控制面数据库的 migration 运行与 schema 测试支撑。
package db
```

- [ ] **步骤 3：验证构建**

运行：`go build ./...`
预期：无输出、退出码 0（仅 doc.go，无编译错误）。

- [ ] **步骤 4：确认 .gitignore 不漏掉 go.sum**

运行：`grep -q "go.sum" .gitignore && echo "WARN: go.sum ignored" || echo "ok"`
预期：`ok`（go.sum 必须入库）。

- [ ] **步骤 5：Commit**

```bash
git add go.mod go.sum db/migrations/.gitkeep internal/db/doc.go
git commit -m "chore: 初始化 Go module 与数据库包骨架"
```

---

## 任务 2：测试基础设施（PG 容器 + migration runner）

**文件：**
- 创建：`internal/db/migrate.go`
- 创建：`internal/db/helpers_test.go`
- 测试：`internal/db/schema_test.go`

- [ ] **步骤 1：编写 migration runner**

`internal/db/migrate.go`：
```go
package db

import (
	"errors"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

// RunMigrations 对 dsn 指向的数据库应用 sourceURL 下的全部 up migration。
// sourceURL 形如 "file://../../db/migrations"，dsn 形如 "postgres://...".
func RunMigrations(dsn, sourceURL string) error {
	m, err := migrate.New(sourceURL, dsn)
	if err != nil {
		return err
	}
	defer m.Close()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

// MigrateDown 回滚全部 migration（供 up/down 往返测试使用）。
func MigrateDown(dsn, sourceURL string) error {
	m, err := migrate.New(sourceURL, dsn)
	if err != nil {
		return err
	}
	defer m.Close()
	if err := m.Down(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}
```

- [ ] **步骤 2：编写测试 helper**

`internal/db/helpers_test.go`：
```go
package db

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

const migrationsSource = "file://../../db/migrations"

// startPostgres 起一个临时 PostgreSQL 容器，返回 sslmode=disable 的 DSN。
func startPostgres(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	ctr, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("sydom"),
		postgres.WithUsername("sydom"),
		postgres.WithPassword("sydom"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ctr.Terminate(ctx) })

	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	return dsn
}

// setupSchema 起容器、跑全量 migration，返回已连接的 *sql.DB。
func setupSchema(t *testing.T) *sql.DB {
	t.Helper()
	dsn := startPostgres(t)
	require.NoError(t, RunMigrations(dsn, migrationsSource))

	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.Ping())
	return db
}
```

- [ ] **步骤 3：编写容器冒烟测试**

`internal/db/schema_test.go`：
```go
package db

import (
	"database/sql"
	"testing"

	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestPostgresContainerStarts(t *testing.T) {
	dsn := startPostgres(t)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, db.Ping())
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/db/ -run TestPostgresContainerStarts -v`
预期：PASS（首次会拉取 `postgres:16-alpine` 镜像，需要本机有 Docker）。

- [ ] **步骤 5：Commit**

```bash
git add internal/db/migrate.go internal/db/helpers_test.go internal/db/schema_test.go go.mod go.sum
git commit -m "test: 搭建 PG testcontainer 与 migration runner 基础设施"
```

---

## 任务 3：tenant 表

**文件：**
- 创建：`db/migrations/000001_tenant.up.sql`、`db/migrations/000001_tenant.down.sql`
- 测试：`internal/db/schema_test.go`

- [ ] **步骤 1：编写失败的测试**

在 `internal/db/schema_test.go` 追加：
```go
func TestTenant_NameUnique(t *testing.T) {
	db := setupSchema(t)

	_, err := db.Exec(`INSERT INTO tenant (name) VALUES ('acme')`)
	require.NoError(t, err)

	// 同名再插入必须违反唯一约束
	_, err = db.Exec(`INSERT INTO tenant (name) VALUES ('acme')`)
	require.Error(t, err)

	// status 默认应为 1
	var status int
	require.NoError(t, db.QueryRow(
		`SELECT status FROM tenant WHERE name = 'acme'`).Scan(&status))
	require.Equal(t, 1, status)
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/db/ -run TestTenant_NameUnique -v`
预期：FAIL（`relation "tenant" does not exist`）。

- [ ] **步骤 3：编写 migration**

`db/migrations/000001_tenant.up.sql`：
```sql
CREATE TABLE tenant (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name       VARCHAR(128) NOT NULL,
    status     SMALLINT     NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uq_tenant_name UNIQUE (name)
);
```

`db/migrations/000001_tenant.down.sql`：
```sql
DROP TABLE IF EXISTS tenant;
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/db/ -run TestTenant_NameUnique -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add db/migrations/000001_tenant.up.sql db/migrations/000001_tenant.down.sql internal/db/schema_test.go
git commit -m "feat(db): 新增 tenant 表"
```

---

## 任务 4：application 表

**文件：**
- 创建：`db/migrations/000002_application.up.sql`、`db/migrations/000002_application.down.sql`
- 测试：`internal/db/schema_test.go`

- [ ] **步骤 1：编写失败的测试**

追加：
```go
func TestApplication_Constraints(t *testing.T) {
	db := setupSchema(t)

	var tenantID int64
	require.NoError(t, db.QueryRow(
		`INSERT INTO tenant (name) VALUES ('acme') RETURNING id`).Scan(&tenantID))

	_, err := db.Exec(`INSERT INTO application (tenant_id, domain, name, app_key, app_secret_hash)
		VALUES ($1, 'order-system', '订单系统', 'AK_order', 'hash1')`, tenantID)
	require.NoError(t, err)

	// current_version 默认 0
	var ver int64
	require.NoError(t, db.QueryRow(
		`SELECT current_version FROM application WHERE app_key = 'AK_order'`).Scan(&ver))
	require.Equal(t, int64(0), ver)

	// app_key 全局唯一
	_, err = db.Exec(`INSERT INTO application (tenant_id, domain, name, app_key, app_secret_hash)
		VALUES ($1, 'other', '其他', 'AK_order', 'hash2')`, tenantID)
	require.Error(t, err)

	// (tenant_id, domain) 唯一
	_, err = db.Exec(`INSERT INTO application (tenant_id, domain, name, app_key, app_secret_hash)
		VALUES ($1, 'order-system', '重复域', 'AK_dup', 'hash3')`, tenantID)
	require.Error(t, err)

	// tenant_id 外键：不存在的租户应被拒绝
	_, err = db.Exec(`INSERT INTO application (tenant_id, domain, name, app_key, app_secret_hash)
		VALUES (999999, 'x', 'x', 'AK_x', 'hashx')`)
	require.Error(t, err)
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/db/ -run TestApplication_Constraints -v`
预期：FAIL（`relation "application" does not exist`）。

- [ ] **步骤 3：编写 migration**

`db/migrations/000002_application.up.sql`：
```sql
CREATE TABLE application (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id       BIGINT       NOT NULL REFERENCES tenant(id),
    domain          VARCHAR(64)  NOT NULL,
    name            VARCHAR(128) NOT NULL,
    app_key         VARCHAR(64)  NOT NULL,
    app_secret_hash VARCHAR(255) NOT NULL,
    current_version BIGINT       NOT NULL DEFAULT 0,
    status          SMALLINT     NOT NULL DEFAULT 1,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uq_application_app_key      UNIQUE (app_key),
    CONSTRAINT uq_application_tenant_domain UNIQUE (tenant_id, domain)
);
```

`db/migrations/000002_application.down.sql`：
```sql
DROP TABLE IF EXISTS application;
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/db/ -run TestApplication_Constraints -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add db/migrations/000002_application.up.sql db/migrations/000002_application.down.sql internal/db/schema_test.go
git commit -m "feat(db): 新增 application 表（含 current_version 与唯一/外键约束）"
```

---

## 任务 5：role 表

**文件：**
- 创建：`db/migrations/000003_role.up.sql`、`db/migrations/000003_role.down.sql`
- 测试：`internal/db/schema_test.go`

- [ ] **步骤 1：编写失败的测试**

追加 helper（供后续多个任务复用，定义一次）与测试：
```go
// seedApp 建一个租户+应用，返回 app_id。供需要 app 上下文的表测试复用。
func seedApp(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var tenantID, appID int64
	require.NoError(t, db.QueryRow(
		`INSERT INTO tenant (name) VALUES ('acme') RETURNING id`).Scan(&tenantID))
	require.NoError(t, db.QueryRow(
		`INSERT INTO application (tenant_id, domain, name, app_key, app_secret_hash)
		 VALUES ($1, 'order-system', '订单系统', 'AK_order', 'hash1') RETURNING id`,
		tenantID).Scan(&appID))
	return appID
}

func TestRole_AppCodeUnique(t *testing.T) {
	db := setupSchema(t)
	appID := seedApp(t, db)

	_, err := db.Exec(`INSERT INTO role (app_id, code, name) VALUES ($1, 'manager', '经理')`, appID)
	require.NoError(t, err)

	// 同 app 下 code 唯一
	_, err = db.Exec(`INSERT INTO role (app_id, code, name) VALUES ($1, 'manager', '重复')`, appID)
	require.Error(t, err)
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/db/ -run TestRole_AppCodeUnique -v`
预期：FAIL（`relation "role" does not exist`）。

- [ ] **步骤 3：编写 migration**

`db/migrations/000003_role.up.sql`：
```sql
CREATE TABLE role (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    app_id      BIGINT       NOT NULL REFERENCES application(id),
    code        VARCHAR(64)  NOT NULL,
    name        VARCHAR(128) NOT NULL,
    description VARCHAR(512),
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uq_role_app_code UNIQUE (app_id, code)
);
```

`db/migrations/000003_role.down.sql`：
```sql
DROP TABLE IF EXISTS role;
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/db/ -run TestRole_AppCodeUnique -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add db/migrations/000003_role.up.sql db/migrations/000003_role.down.sql internal/db/schema_test.go
git commit -m "feat(db): 新增 role 表"
```

---

## 任务 6：permission 表

**文件：**
- 创建：`db/migrations/000004_permission.up.sql`、`db/migrations/000004_permission.down.sql`
- 测试：`internal/db/schema_test.go`

- [ ] **步骤 1：编写失败的测试**

追加：
```go
func TestPermission_AppCodeUnique(t *testing.T) {
	db := setupSchema(t)
	appID := seedApp(t, db)

	_, err := db.Exec(`INSERT INTO permission (app_id, code, resource, action, type, name)
		VALUES ($1, 'order:create', 'order', 'create', 'api', '创建订单')`, appID)
	require.NoError(t, err)

	// source 默认 manual
	var source string
	require.NoError(t, db.QueryRow(
		`SELECT source FROM permission WHERE app_id = $1 AND code = 'order:create'`,
		appID).Scan(&source))
	require.Equal(t, "manual", source)

	// 同 app 下 code 唯一（幂等 upsert 的去重基础）
	_, err = db.Exec(`INSERT INTO permission (app_id, code, resource, action, type, name)
		VALUES ($1, 'order:create', 'order', 'create', 'api', '重复')`, appID)
	require.Error(t, err)
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/db/ -run TestPermission_AppCodeUnique -v`
预期：FAIL（`relation "permission" does not exist`）。

- [ ] **步骤 3：编写 migration**

`db/migrations/000004_permission.up.sql`：
```sql
CREATE TABLE permission (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    app_id      BIGINT       NOT NULL REFERENCES application(id),
    code        VARCHAR(255) NOT NULL,
    resource    VARCHAR(128) NOT NULL,
    action      VARCHAR(64)  NOT NULL,
    type        VARCHAR(16)  NOT NULL,
    name        VARCHAR(128) NOT NULL,
    description VARCHAR(512),
    source      VARCHAR(8)   NOT NULL DEFAULT 'manual',
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uq_permission_app_code UNIQUE (app_id, code)
);
```

`db/migrations/000004_permission.down.sql`：
```sql
DROP TABLE IF EXISTS permission;
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/db/ -run TestPermission_AppCodeUnique -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add db/migrations/000004_permission.up.sql db/migrations/000004_permission.down.sql internal/db/schema_test.go
git commit -m "feat(db): 新增 permission 权限点注册表"
```

---

## 任务 7：role_permission 表

**文件：**
- 创建：`db/migrations/000005_role_permission.up.sql`、`db/migrations/000005_role_permission.down.sql`
- 测试：`internal/db/schema_test.go`

- [ ] **步骤 1：编写失败的测试**

追加：
```go
func TestRolePermission_Constraints(t *testing.T) {
	db := setupSchema(t)
	appID := seedApp(t, db)

	var roleID, permID int64
	require.NoError(t, db.QueryRow(
		`INSERT INTO role (app_id, code, name) VALUES ($1, 'manager', '经理') RETURNING id`,
		appID).Scan(&roleID))
	require.NoError(t, db.QueryRow(
		`INSERT INTO permission (app_id, code, resource, action, type, name)
		 VALUES ($1, 'order:create', 'order', 'create', 'api', '创建订单') RETURNING id`,
		appID).Scan(&permID))

	_, err := db.Exec(`INSERT INTO role_permission (app_id, role_id, permission_id)
		VALUES ($1, $2, $3)`, appID, roleID, permID)
	require.NoError(t, err)

	// eft 默认 allow
	var eft string
	require.NoError(t, db.QueryRow(
		`SELECT eft FROM role_permission WHERE role_id = $1 AND permission_id = $2`,
		roleID, permID).Scan(&eft))
	require.Equal(t, "allow", eft)

	// (app_id, role_id, permission_id) 唯一
	_, err = db.Exec(`INSERT INTO role_permission (app_id, role_id, permission_id)
		VALUES ($1, $2, $3)`, appID, roleID, permID)
	require.Error(t, err)

	// 外键：不存在的 permission_id 应被拒绝
	_, err = db.Exec(`INSERT INTO role_permission (app_id, role_id, permission_id)
		VALUES ($1, $2, 999999)`, appID, roleID)
	require.Error(t, err)
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/db/ -run TestRolePermission_Constraints -v`
预期：FAIL（`relation "role_permission" does not exist`）。

- [ ] **步骤 3：编写 migration**

`db/migrations/000005_role_permission.up.sql`：
```sql
CREATE TABLE role_permission (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    app_id        BIGINT      NOT NULL REFERENCES application(id),
    role_id       BIGINT      NOT NULL REFERENCES role(id),
    permission_id BIGINT      NOT NULL REFERENCES permission(id),
    eft           VARCHAR(8)  NOT NULL DEFAULT 'allow',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_role_permission UNIQUE (app_id, role_id, permission_id)
);
```

`db/migrations/000005_role_permission.down.sql`：
```sql
DROP TABLE IF EXISTS role_permission;
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/db/ -run TestRolePermission_Constraints -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add db/migrations/000005_role_permission.up.sql db/migrations/000005_role_permission.down.sql internal/db/schema_test.go
git commit -m "feat(db): 新增 role_permission 授权表（投影 p 行）"
```

---

## 任务 8：role_inheritance 表

**文件：**
- 创建：`db/migrations/000006_role_inheritance.up.sql`、`db/migrations/000006_role_inheritance.down.sql`
- 测试：`internal/db/schema_test.go`

- [ ] **步骤 1：编写失败的测试**

追加：
```go
func TestRoleInheritance_EdgeUnique(t *testing.T) {
	db := setupSchema(t)
	appID := seedApp(t, db)

	var parentID, childID int64
	require.NoError(t, db.QueryRow(
		`INSERT INTO role (app_id, code, name) VALUES ($1, 'admin', '管理员') RETURNING id`,
		appID).Scan(&parentID))
	require.NoError(t, db.QueryRow(
		`INSERT INTO role (app_id, code, name) VALUES ($1, 'manager', '经理') RETURNING id`,
		appID).Scan(&childID))

	_, err := db.Exec(`INSERT INTO role_inheritance (app_id, parent_role_id, child_role_id)
		VALUES ($1, $2, $3)`, appID, parentID, childID)
	require.NoError(t, err)

	// 同一条继承边唯一（防重复边；环检测由控制面 detector.Check 负责，不在表层）
	_, err = db.Exec(`INSERT INTO role_inheritance (app_id, parent_role_id, child_role_id)
		VALUES ($1, $2, $3)`, appID, parentID, childID)
	require.Error(t, err)
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/db/ -run TestRoleInheritance_EdgeUnique -v`
预期：FAIL（`relation "role_inheritance" does not exist`）。

- [ ] **步骤 3：编写 migration**

`db/migrations/000006_role_inheritance.up.sql`：
```sql
CREATE TABLE role_inheritance (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    app_id         BIGINT      NOT NULL REFERENCES application(id),
    parent_role_id BIGINT      NOT NULL REFERENCES role(id),
    child_role_id  BIGINT      NOT NULL REFERENCES role(id),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT uq_role_inheritance UNIQUE (app_id, parent_role_id, child_role_id)
);
```

`db/migrations/000006_role_inheritance.down.sql`：
```sql
DROP TABLE IF EXISTS role_inheritance;
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/db/ -run TestRoleInheritance_EdgeUnique -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add db/migrations/000006_role_inheritance.up.sql db/migrations/000006_role_inheritance.down.sql internal/db/schema_test.go
git commit -m "feat(db): 新增 role_inheritance 角色继承表（投影 g 行）"
```

---

## 任务 9：user_role_binding 表

**文件：**
- 创建：`db/migrations/000007_user_role_binding.up.sql`、`db/migrations/000007_user_role_binding.down.sql`
- 测试：`internal/db/schema_test.go`

- [ ] **步骤 1：编写失败的测试**

追加：
```go
func TestUserRoleBinding_Unique(t *testing.T) {
	db := setupSchema(t)
	appID := seedApp(t, db)

	var roleID int64
	require.NoError(t, db.QueryRow(
		`INSERT INTO role (app_id, code, name) VALUES ($1, 'manager', '经理') RETURNING id`,
		appID).Scan(&roleID))

	_, err := db.Exec(`INSERT INTO user_role_binding (app_id, user_id, role_id)
		VALUES ($1, 'alice', $2)`, appID, roleID)
	require.NoError(t, err)

	// (app_id, user_id, role_id) 唯一
	_, err = db.Exec(`INSERT INTO user_role_binding (app_id, user_id, role_id)
		VALUES ($1, 'alice', $2)`, appID, roleID)
	require.Error(t, err)

	// 外键：role_id 必须存在
	_, err = db.Exec(`INSERT INTO user_role_binding (app_id, user_id, role_id)
		VALUES ($1, 'bob', 999999)`, appID)
	require.Error(t, err)
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/db/ -run TestUserRoleBinding_Unique -v`
预期：FAIL（`relation "user_role_binding" does not exist`）。

- [ ] **步骤 3：编写 migration**

`db/migrations/000007_user_role_binding.up.sql`：
```sql
CREATE TABLE user_role_binding (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    app_id     BIGINT       NOT NULL REFERENCES application(id),
    user_id    VARCHAR(128) NOT NULL,
    role_id    BIGINT       NOT NULL REFERENCES role(id),
    created_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
    CONSTRAINT uq_user_role_binding UNIQUE (app_id, user_id, role_id)
);
```

`db/migrations/000007_user_role_binding.down.sql`：
```sql
DROP TABLE IF EXISTS user_role_binding;
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/db/ -run TestUserRoleBinding_Unique -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add db/migrations/000007_user_role_binding.up.sql db/migrations/000007_user_role_binding.down.sql internal/db/schema_test.go
git commit -m "feat(db): 新增 user_role_binding 用户角色绑定表（投影 g 行）"
```

---

## 任务 10：data_policy 表

**文件：**
- 创建：`db/migrations/000008_data_policy.up.sql`、`db/migrations/000008_data_policy.down.sql`
- 测试：`internal/db/schema_test.go`

- [ ] **步骤 1：编写失败的测试**

追加（验证 jsonb 列可存取条件树）：
```go
func TestDataPolicy_JSONBCondition(t *testing.T) {
	db := setupSchema(t)
	appID := seedApp(t, db)

	cond := `{"op":"AND","children":[{"field":"department","op":"EQ","value":"$user.department"}]}`
	_, err := db.Exec(`INSERT INTO data_policy (app_id, subject_type, subject_id, resource, condition, version)
		VALUES ($1, 'role', 'manager', 'order', $2::jsonb, 1)`, appID, cond)
	require.NoError(t, err)

	// jsonb 路径查询可用，证明确为 jsonb 而非纯文本
	var op string
	require.NoError(t, db.QueryRow(
		`SELECT condition->>'op' FROM data_policy WHERE app_id = $1 AND subject_id = 'manager'`,
		appID).Scan(&op))
	require.Equal(t, "AND", op)
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/db/ -run TestDataPolicy_JSONBCondition -v`
预期：FAIL（`relation "data_policy" does not exist`）。

- [ ] **步骤 3：编写 migration**

`db/migrations/000008_data_policy.up.sql`：
```sql
CREATE TABLE data_policy (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    app_id       BIGINT       NOT NULL REFERENCES application(id),
    subject_type VARCHAR(8)   NOT NULL,
    subject_id   VARCHAR(128) NOT NULL,
    resource     VARCHAR(128) NOT NULL,
    condition    JSONB        NOT NULL,
    description  VARCHAR(512),
    version      BIGINT       NOT NULL,
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE INDEX idx_data_policy_subject  ON data_policy (app_id, subject_type, subject_id);
CREATE INDEX idx_data_policy_resource ON data_policy (app_id, resource);
```

`db/migrations/000008_data_policy.down.sql`：
```sql
DROP TABLE IF EXISTS data_policy;
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/db/ -run TestDataPolicy_JSONBCondition -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add db/migrations/000008_data_policy.up.sql db/migrations/000008_data_policy.down.sql internal/db/schema_test.go
git commit -m "feat(db): 新增 data_policy 数据权限条件树表（jsonb）"
```

---

## 任务 11：casbin_rule 表

**文件：**
- 创建：`db/migrations/000009_casbin_rule.up.sql`、`db/migrations/000009_casbin_rule.down.sql`
- 测试：`internal/db/schema_test.go`

- [ ] **步骤 1：编写失败的测试**

追加（验证 v 列默认空串 + 全 v 列去重唯一）：
```go
func TestCasbinRule_DefaultsAndUnique(t *testing.T) {
	db := setupSchema(t)

	// 只给 ptype/v0/v1/v2，其余 v 列应默认空串
	_, err := db.Exec(`INSERT INTO casbin_rule (app_id, ptype, v0, v1, v2, version)
		VALUES (1, 'g', 'alice', 'manager', 'order-system', 1)`)
	require.NoError(t, err)

	var v3, v4, v5 string
	require.NoError(t, db.QueryRow(
		`SELECT v3, v4, v5 FROM casbin_rule WHERE app_id = 1 AND v0 = 'alice'`).Scan(&v3, &v4, &v5))
	require.Equal(t, "", v3)
	require.Equal(t, "", v4)
	require.Equal(t, "", v5)

	// 完整 v 元组去重：同 (app_id, ptype, v0..v5) 再插入应失败
	_, err = db.Exec(`INSERT INTO casbin_rule (app_id, ptype, v0, v1, v2, version)
		VALUES (1, 'g', 'alice', 'manager', 'order-system', 2)`)
	require.Error(t, err)
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/db/ -run TestCasbinRule_DefaultsAndUnique -v`
预期：FAIL（`relation "casbin_rule" does not exist`）。

- [ ] **步骤 3：编写 migration**

`db/migrations/000009_casbin_rule.up.sql`（派生表，`app_id` 不设 FK——批量重写性能优先，规格 §4.4）：
```sql
CREATE TABLE casbin_rule (
    id      BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    app_id  BIGINT       NOT NULL,
    ptype   VARCHAR(8)   NOT NULL,
    v0      VARCHAR(128) NOT NULL DEFAULT '',
    v1      VARCHAR(128) NOT NULL DEFAULT '',
    v2      VARCHAR(128) NOT NULL DEFAULT '',
    v3      VARCHAR(128) NOT NULL DEFAULT '',
    v4      VARCHAR(128) NOT NULL DEFAULT '',
    v5      VARCHAR(128) NOT NULL DEFAULT '',
    version BIGINT       NOT NULL,
    CONSTRAINT uq_casbin_rule UNIQUE (app_id, ptype, v0, v1, v2, v3, v4, v5)
);
CREATE INDEX idx_casbin_rule_app_ptype   ON casbin_rule (app_id, ptype);
CREATE INDEX idx_casbin_rule_app_version ON casbin_rule (app_id, version);
```

`db/migrations/000009_casbin_rule.down.sql`：
```sql
DROP TABLE IF EXISTS casbin_rule;
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/db/ -run TestCasbinRule_DefaultsAndUnique -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add db/migrations/000009_casbin_rule.up.sql db/migrations/000009_casbin_rule.down.sql internal/db/schema_test.go
git commit -m "feat(db): 新增 casbin_rule 物化投影表"
```

---

## 任务 12：policy_audit_log 表

**文件：**
- 创建：`db/migrations/000010_policy_audit_log.up.sql`、`db/migrations/000010_policy_audit_log.down.sql`
- 测试：`internal/db/schema_test.go`

- [ ] **步骤 1：编写失败的测试**

追加：
```go
func TestPolicyAuditLog_Insert(t *testing.T) {
	db := setupSchema(t)
	appID := seedApp(t, db)

	diff := `{"before":null,"after":{"code":"manager"}}`
	_, err := db.Exec(`INSERT INTO policy_audit_log
		(app_id, operator, action, entity_type, entity_id, diff, version)
		VALUES ($1, 'admin@acme', 'create', 'role', '1', $2::jsonb, 1)`, appID, diff)
	require.NoError(t, err)

	// entity_id 允许为 NULL（某些变更无单一实体）
	_, err = db.Exec(`INSERT INTO policy_audit_log
		(app_id, operator, action, entity_type, version)
		VALUES ($1, 'admin@acme', 'update', 'role', 2)`, appID)
	require.NoError(t, err)

	var cnt int
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM policy_audit_log WHERE app_id = $1`, appID).Scan(&cnt))
	require.Equal(t, 2, cnt)
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/db/ -run TestPolicyAuditLog_Insert -v`
预期：FAIL（`relation "policy_audit_log" does not exist`）。

- [ ] **步骤 3：编写 migration**

`db/migrations/000010_policy_audit_log.up.sql`：
```sql
CREATE TABLE policy_audit_log (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    app_id      BIGINT       NOT NULL,
    operator    VARCHAR(128) NOT NULL,
    action      VARCHAR(16)  NOT NULL,
    entity_type VARCHAR(32)  NOT NULL,
    entity_id   VARCHAR(128),
    diff        JSONB,
    version     BIGINT       NOT NULL,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
);
CREATE INDEX idx_audit_app_created ON policy_audit_log (app_id, created_at);
CREATE INDEX idx_audit_app_version ON policy_audit_log (app_id, version);
CREATE INDEX idx_audit_app_entity  ON policy_audit_log (app_id, entity_type, entity_id);
```

`db/migrations/000010_policy_audit_log.down.sql`：
```sql
DROP TABLE IF EXISTS policy_audit_log;
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/db/ -run TestPolicyAuditLog_Insert -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add db/migrations/000010_policy_audit_log.up.sql db/migrations/000010_policy_audit_log.down.sql internal/db/schema_test.go
git commit -m "feat(db): 新增 policy_audit_log 审计表"
```

---

## 任务 13：版本号并发递增机制测试（规格 §6）

验证 schema 支持 §6 写入时序的核心：`SELECT ... FOR UPDATE` 行锁让同一 app 的 `current_version` 在并发下严格单调、无丢失更新。**不涉及投影逻辑**（投影属控制面计划）。

**文件：**
- 测试：`internal/db/schema_test.go`

- [ ] **步骤 1：编写测试**

追加：
```go
func TestApplication_VersionBumpSerialized(t *testing.T) {
	db := setupSchema(t)
	appID := seedApp(t, db)

	const goroutines = 10
	const bumpsEach = 20

	var wg sync.WaitGroup
	errCh := make(chan error, goroutines)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < bumpsEach; i++ {
				if err := bumpVersion(db, appID); err != nil {
					errCh <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}

	var final int64
	require.NoError(t, db.QueryRow(
		`SELECT current_version FROM application WHERE id = $1`, appID).Scan(&final))
	// 无丢失更新：最终版本号 == 总递增次数
	require.Equal(t, int64(goroutines*bumpsEach), final)
}

// bumpVersion 复现规格 §6 步骤 1-2、5：行锁读取 current_version 后递增写回。
func bumpVersion(db *sql.DB, appID int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var cur int64
	if err := tx.QueryRow(
		`SELECT current_version FROM application WHERE id = $1 FOR UPDATE`,
		appID).Scan(&cur); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`UPDATE application SET current_version = $1 WHERE id = $2`, cur+1, appID); err != nil {
		return err
	}
	return tx.Commit()
}
```

并在 `schema_test.go` 顶部 import 块补充 `"sync"`。

- [ ] **步骤 2：运行测试验证通过**

运行：`go test ./internal/db/ -run TestApplication_VersionBumpSerialized -v`
预期：PASS（最终 `current_version == 200`）。若改用无 `FOR UPDATE` 的读-改-写会出现丢失更新、断言失败——这正是该测试守护的不变量。

- [ ] **步骤 3：Commit**

```bash
git add internal/db/schema_test.go
git commit -m "test(db): 验证 current_version 行锁串行化（规格 §6 写入时序）"
```

---

## 任务 14：全量 up/down 往返测试 + Makefile

验证每个 down migration 正确（可完整回滚再重建），并提供本地/CI 运行命令。

**文件：**
- 测试：`internal/db/schema_test.go`
- 创建：`Makefile`

- [ ] **步骤 1：编写 up/down 往返测试**

追加：
```go
func TestMigrations_UpDownRoundTrip(t *testing.T) {
	dsn := startPostgres(t)

	// up
	require.NoError(t, RunMigrations(dsn, migrationsSource))

	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	defer db.Close()

	// 10 张业务表均存在
	tables := []string{
		"tenant", "application", "role", "permission", "role_permission",
		"role_inheritance", "user_role_binding", "data_policy",
		"casbin_rule", "policy_audit_log",
	}
	for _, tbl := range tables {
		var reg sql.NullString
		require.NoError(t, db.QueryRow(`SELECT to_regclass($1)`, tbl).Scan(&reg))
		require.Truef(t, reg.Valid, "表 %s 应在 up 后存在", tbl)
	}

	// down：全部回滚
	require.NoError(t, MigrateDown(dsn, migrationsSource))
	for _, tbl := range tables {
		var reg sql.NullString
		require.NoError(t, db.QueryRow(`SELECT to_regclass($1)`, tbl).Scan(&reg))
		require.Falsef(t, reg.Valid, "表 %s 应在 down 后被删除", tbl)
	}

	// 再次 up：验证 down 未损坏可重建性
	require.NoError(t, RunMigrations(dsn, migrationsSource))
}
```

- [ ] **步骤 2：运行测试验证通过**

运行：`go test ./internal/db/ -run TestMigrations_UpDownRoundTrip -v`
预期：PASS。

- [ ] **步骤 3：编写 Makefile**

`Makefile`：
```makefile
MIGRATIONS := db/migrations
# 用法：make migrate-up DSN='postgres://sydom:sydom@localhost:5432/sydom?sslmode=disable'
migrate-up:
	go run -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate \
		-path $(MIGRATIONS) -database '$(DSN)' up

migrate-down:
	go run -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate \
		-path $(MIGRATIONS) -database '$(DSN)' down

test:
	go test ./... -v

.PHONY: migrate-up migrate-down test
```

- [ ] **步骤 4：运行全量测试套件**

运行：`go test ./... -v`
预期：全部 PASS（任务 2-14 的所有测试）。

- [ ] **步骤 5：Commit**

```bash
git add internal/db/schema_test.go Makefile
git commit -m "test(db): 全量 up/down 往返测试 + 新增 Makefile 迁移命令"
```

---

## 自检结论

**规格覆盖度：**

| 规格章节 | 对应任务 |
|---|---|
| §4.1 tenant / application | 任务 3、4 |
| §4.2 role / permission / role_permission / role_inheritance / user_role_binding | 任务 5–9 |
| §4.3 data_policy（jsonb 条件树） | 任务 10 |
| §4.4 casbin_rule（v 列默认空串 + 全 v 列去重） | 任务 11 |
| §4.5 policy_audit_log | 任务 12 |
| §5 投影规则 | 表结构就位（任务 7/9/11）；投影**执行**逻辑按规格 §8 划归控制面计划，不在本计划 |
| §6 版本号写入事务时序 | 任务 13（`FOR UPDATE` 串行化的 schema 支撑） |
| §7 PG/MySQL 兼容性 | 本计划落 PG；MySQL 第二套 migration 后续计划 |

**占位符扫描：** 无 TODO/待定；每个步骤含完整 SQL 与 Go 测试代码。

**类型一致性：** `RunMigrations`/`MigrateDown`（任务 2）、`startPostgres`/`setupSchema`（任务 2）、`seedApp`（任务 5 定义，任务 6–13 复用）、`bumpVersion`（任务 13）、常量 `migrationsSource`（任务 2 定义，任务 14 复用）签名前后一致。

**已知边界（非缺陷，规格已划出）：** 投影执行、DB BatchAdapter、gRPC 下发、condition 校验渲染、MySQL 方言——均属后续 spec/计划。
