# 司域 控制面 · 策略核心引擎 (③-1 Policy Core) 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 实现司域控制面的真相源写入引擎——业务表变更原子落库、投影成 `casbin_rule`、生成领域 `Delta`，守住权限一致性（fail-close）。

**架构：** 控制面**不跑 casbin**（决策 A）。`internal/controlplane` 根包放共享领域类型（`Rule`/`Delta`/`DBTX`）；子包 `projection`（业务表→期望规则集 + Diff + 环检测）、`store`（`casbin_rule` DAO + 业务表 CUD 函数 + audit + 版本行锁）、`policy`（`PolicyManager`：统一 §6 版本号写事务 + 对外写方法）、`secret`（`SecretResolver`：解密 `app_secret_enc`）。delta = 全量重投影 + diff（单一正确性来源）。

**技术栈：** Go 1.26.3、`database/sql` + `github.com/lib/pq`、`internal/crypto`(AES-GCM，已存在)、`internal/auth`(SecretResolver 接口，已存在)、testcontainers-go(`postgres:17-alpine`，依赖 Docker)、testify。module `github.com/nickZFZ/Sydom`。

**边界（spec §9，本计划不做）：** Redis 广播 / PolicySync gRPC 服务端 / PullSnapshot 推送 / Delta→syncv1 翻译（→③-2）；REST/gRPC 对外接口 / 管理鉴权（→③-3）。本计划只交付纯 DB 层写引擎，零网络，全程 testcontainers PG 测试。

---

## 文件结构

| 文件 | 职责 |
|---|---|
| `internal/dbtest/dbtest.go` | 跨包共享的 testcontainers PG 基建（`StartPostgres`/`SetupSchema`/`SeedApp`），repo 相对 migrations 路径 |
| `internal/dbtest/dbtest_smoke_test.go` | 基建冒烟测试 |
| `internal/controlplane/types.go` | 共享领域类型：`Rule`/`ChangeOp`/`DataPolicy`/`DataPolicyChange`/`Delta`/`DBTX` + operator context |
| `internal/controlplane/projection/diff.go` | `Diff` 纯函数 |
| `internal/controlplane/projection/diff_test.go` | Diff 单元测试（无 Docker） |
| `internal/controlplane/projection/project.go` | `ProjectApp`（§5 投影）+ `CheckNoCycle`（DFS） |
| `internal/controlplane/projection/project_test.go` | 投影正确性 + 环检测（testcontainers） |
| `internal/controlplane/store/store.go` | `casbin_rule` DAO（`ReadAppRules`/`ApplyDiff`）+ 版本行锁/bump + audit + 业务表 CUD 函数 |
| `internal/controlplane/store/store_test.go` | casbin_rule 往返 / ApplyDiff（testcontainers） |
| `internal/controlplane/policy/manager.go` | `PolicyManager`：`runVersionedWrite` 核心 + 对外写方法 |
| `internal/controlplane/policy/manager_test.go` | 写事务端到端（testcontainers） |
| `internal/controlplane/secret/resolver.go` | `Resolver`：`ResolveSecret`/`EncryptSecret` |
| `internal/controlplane/secret/resolver_test.go` | 加解密往返 / fail-close（testcontainers） |

依赖方向（无环）：`policy → {projection, store, controlplane}`；`projection/store → controlplane`；`secret → crypto + controlplane`；测试 → `dbtest → internal/db`。

---

## 任务 1：internal/dbtest 共享测试基建

**背景：** 现有 testcontainers 基建在 `internal/db/helpers_test.go` 内、是 `db` 包私有测试代码，控制面各包无法复用。抽成可跨包导入的 `internal/dbtest` 包（用 `runtime.Caller` 定位 repo 根，使 migrations 路径与调用方位置无关）。该包只被 `_test.go` 导入，不进生产二进制。

**文件：**
- 创建：`internal/dbtest/dbtest.go`
- 创建：`internal/dbtest/dbtest_smoke_test.go`

- [ ] **步骤 1：编写实现**

`internal/dbtest/dbtest.go`：

```go
// Package dbtest 提供跨包共享的 testcontainers PostgreSQL 测试基建。
// 仅供 _test.go 导入；不进生产二进制。
package dbtest

import (
	"context"
	"database/sql"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/nickZFZ/Sydom/internal/db"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// 种子应用的固定属性，供投影类测试断言 domain / app_key。
const (
	SeedDomain = "order-system"
	SeedAppKey = "AK_order"
)

// migrationsSource 基于本文件位置算出 file://<repo>/db/migrations，与调用方目录无关。
func migrationsSource() string {
	_, thisFile, _, _ := runtime.Caller(0) // <repo>/internal/dbtest/dbtest.go
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	return "file://" + filepath.Join(repoRoot, "db", "migrations")
}

// StartPostgres 起一个临时 PostgreSQL 容器，返回 sslmode=disable 的 DSN。
func StartPostgres(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	ctr, err := postgres.RunContainer(ctx,
		testcontainers.WithImage("docker.io/postgres:17-alpine"),
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

// SetupSchema 起容器、跑全量 migration，返回已连接的 *sql.DB。
func SetupSchema(t *testing.T) *sql.DB {
	t.Helper()
	dsn := StartPostgres(t)
	require.NoError(t, db.RunMigrations(dsn, migrationsSource()))

	conn, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	require.NoError(t, conn.Ping())
	return conn
}

// SeedApp 建一个租户+应用，返回 app_id。app_secret_enc 用占位字节（不参与本包断言）。
func SeedApp(t *testing.T, conn *sql.DB) int64 {
	t.Helper()
	var tenantID, appID int64
	require.NoError(t, conn.QueryRow(
		`INSERT INTO tenant (name) VALUES ('acme') RETURNING id`).Scan(&tenantID))
	require.NoError(t, conn.QueryRow(
		`INSERT INTO application (tenant_id, domain, name, app_key, app_secret_enc)
		 VALUES ($1, $2, '订单系统', $3, '\xab'::bytea) RETURNING id`,
		tenantID, SeedDomain, SeedAppKey).Scan(&appID))
	return appID
}
```

- [ ] **步骤 2：编写冒烟测试**

`internal/dbtest/dbtest_smoke_test.go`：

```go
package dbtest_test

import (
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestDBTest_SetupAndSeed(t *testing.T) {
	conn := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, conn)
	require.Positive(t, appID)

	// casbin_rule 表存在且初始为空
	var n int
	require.NoError(t, conn.QueryRow(
		`SELECT count(*) FROM casbin_rule WHERE app_id = $1`, appID).Scan(&n))
	require.Equal(t, 0, n)

	// 种子应用的 domain 可读且等于约定值
	var domain string
	require.NoError(t, conn.QueryRow(
		`SELECT domain FROM application WHERE id = $1`, appID).Scan(&domain))
	require.Equal(t, dbtest.SeedDomain, domain)
}
```

- [ ] **步骤 3：运行验证**

运行：`go test ./internal/dbtest/ -v`
预期：PASS（起容器、跑 migration、seed、断言）。

- [ ] **步骤 4：Commit**

```bash
git add internal/dbtest/
git commit -m "test(dbtest): 抽出跨包共享 testcontainers PG 基建"
```

---

## 任务 2：controlplane 根类型 + projection.Diff 纯函数

**背景：** 定义跨子包共享的领域类型，并实现 `Diff` 纯函数（集合差，无 DB，可快速单测）。

**文件：**
- 创建：`internal/controlplane/types.go`
- 创建：`internal/controlplane/projection/diff.go`
- 测试：`internal/controlplane/projection/diff_test.go`

- [ ] **步骤 1：编写共享类型**

`internal/controlplane/types.go`：

```go
// Package controlplane 持有控制面策略核心引擎的共享领域类型。
package controlplane

import (
	"context"
	"database/sql"
)

// Rule 是一条 casbin_rule（ptype + v0..v5）。空位用空串（casbin 惯例）。
type Rule struct {
	Ptype string
	V     [6]string
}

// ChangeOp 是 data_policy 变更类型。
type ChangeOp int

const (
	ChangeAdd ChangeOp = iota
	ChangeUpdate
	ChangeRemove
)

// DataPolicy 是一条数据权限规则（条件树以 JSON 字符串承载，协议层不透明）。
type DataPolicy struct {
	ID          int64
	SubjectType string // "role" / "user"
	SubjectID   string
	Resource    string
	Condition   string // 条件树 JSON
}

// DataPolicyChange 是一次 data_policy 变更。
type DataPolicyChange struct {
	Op     ChangeOp
	Policy DataPolicy
}

// Delta 是一次写事务的产物，供 ③-2 翻译并下发。Version 用 int64（与 DB BIGINT 一致）。
type Delta struct {
	AppID       int64
	Version     int64
	RuleAdds    []Rule
	RuleRemoves []Rule
	DataChanges []DataPolicyChange
}

// DBTX 是 *sql.DB 与 *sql.Tx 的公共子集，使 DB 访问函数既能在事务内、也能独立调用。
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type operatorKey struct{}

// WithOperator 把操作者标识注入 context（③-3 从认证上下文设置）。
func WithOperator(ctx context.Context, operator string) context.Context {
	return context.WithValue(ctx, operatorKey{}, operator)
}

// OperatorFromContext 取操作者；未设置时返回 "system"。
func OperatorFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(operatorKey{}).(string); ok && v != "" {
		return v
	}
	return "system"
}
```

- [ ] **步骤 2：编写失败的 Diff 测试**

`internal/controlplane/projection/diff_test.go`：

```go
package projection

import (
	"testing"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/stretchr/testify/require"
)

func r(ptype string, vs ...string) cp.Rule {
	var v [6]string
	copy(v[:], vs)
	return cp.Rule{Ptype: ptype, V: v}
}

func TestDiff_AddsAndRemoves(t *testing.T) {
	current := []cp.Rule{
		r("p", "admin", "d", "order", "read"),
		r("g", "alice", "admin", "d"),
	}
	desired := []cp.Rule{
		r("p", "admin", "d", "order", "read"), // 不变
		r("p", "admin", "d", "order", "write"), // 新增
	}
	adds, removes := Diff(current, desired)

	require.ElementsMatch(t, []cp.Rule{r("p", "admin", "d", "order", "write")}, adds)
	require.ElementsMatch(t, []cp.Rule{r("g", "alice", "admin", "d")}, removes)
}

func TestDiff_Empty(t *testing.T) {
	rules := []cp.Rule{r("p", "admin", "d", "order", "read")}
	adds, removes := Diff(rules, rules)
	require.Empty(t, adds)
	require.Empty(t, removes)
}
```

- [ ] **步骤 3：运行验证失败**

运行：`go test ./internal/controlplane/projection/ -run TestDiff -v`
预期：FAIL（`Diff` / package 未定义，编译失败）。

- [ ] **步骤 4：编写 Diff 实现**

`internal/controlplane/projection/diff.go`：

```go
// Package projection 把业务表投影为 casbin_rule，并计算变更 diff 与角色继承环检测。
package projection

import (
	"strings"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
)

// ruleKey 把一条规则编码为可比较的字符串键（ptype + v0..v5，分隔符不可能出现在值中）。
func ruleKey(r cp.Rule) string {
	var b strings.Builder
	b.WriteString(r.Ptype)
	for i := range r.V {
		b.WriteByte('\x1f')
		b.WriteString(r.V[i])
	}
	return b.String()
}

// Diff 计算集合差：desired - current = adds，current - desired = removes。
func Diff(current, desired []cp.Rule) (adds, removes []cp.Rule) {
	cur := make(map[string]cp.Rule, len(current))
	for _, r := range current {
		cur[ruleKey(r)] = r
	}
	des := make(map[string]cp.Rule, len(desired))
	for _, r := range desired {
		des[ruleKey(r)] = r
	}
	for k, r := range des {
		if _, ok := cur[k]; !ok {
			adds = append(adds, r)
		}
	}
	for k, r := range cur {
		if _, ok := des[k]; !ok {
			removes = append(removes, r)
		}
	}
	return adds, removes
}
```

- [ ] **步骤 5：运行验证通过**

运行：`go test ./internal/controlplane/projection/ -run TestDiff -v`
预期：PASS。

- [ ] **步骤 6：Commit**

```bash
git add internal/controlplane/types.go internal/controlplane/projection/diff.go internal/controlplane/projection/diff_test.go
git commit -m "feat(controlplane): 共享领域类型 + projection.Diff 纯函数"
```

---

## 任务 3：projection.ProjectApp + CheckNoCycle

**背景：** `ProjectApp` 按 spec §5 从业务表算出该 app 的期望 `casbin_rule` 全集（3 个 JOIN 查询）；`CheckNoCycle` 用 DFS 校验加角色继承边不成环。

**文件：**
- 创建：`internal/controlplane/projection/project.go`
- 测试：`internal/controlplane/projection/project_test.go`

- [ ] **步骤 1：编写失败的测试**

`internal/controlplane/projection/project_test.go`：

```go
package projection_test

import (
	"context"
	"database/sql"
	"testing"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/projection"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// 在种子 app 下建一个角色、一个权限点、一条授权、一个用户绑定，返回 roleID。
func seedRBAC(t *testing.T, db *sql.DB, appID int64) int64 {
	t.Helper()
	var roleID, permID int64
	require.NoError(t, db.QueryRow(
		`INSERT INTO role (app_id, code, name) VALUES ($1,'manager','经理') RETURNING id`,
		appID).Scan(&roleID))
	require.NoError(t, db.QueryRow(
		`INSERT INTO permission (app_id, code, resource, action, type, name)
		 VALUES ($1,'order:read','order','read','api','读订单') RETURNING id`,
		appID).Scan(&permID))
	_, err := db.Exec(
		`INSERT INTO role_permission (app_id, role_id, permission_id, eft)
		 VALUES ($1,$2,$3,'allow')`, appID, roleID, permID)
	require.NoError(t, err)
	_, err = db.Exec(
		`INSERT INTO user_role_binding (app_id, user_id, role_id)
		 VALUES ($1,'u-100',$2)`, appID, roleID)
	require.NoError(t, err)
	return roleID
}

func TestProjectApp_PAndGRows(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	seedRBAC(t, db, appID)

	rules, err := projection.ProjectApp(context.Background(), db, appID)
	require.NoError(t, err)

	d := dbtest.SeedDomain
	require.ElementsMatch(t, []cp.Rule{
		{Ptype: "p", V: [6]string{"manager", d, "order", "read", "allow", ""}},
		{Ptype: "g", V: [6]string{"u-100", "manager", d, "", "", ""}},
	}, rules)
}

func TestProjectApp_InheritanceGRow(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)

	var parentID, childID int64
	require.NoError(t, db.QueryRow(
		`INSERT INTO role (app_id, code, name) VALUES ($1,'admin','管理员') RETURNING id`,
		appID).Scan(&parentID))
	require.NoError(t, db.QueryRow(
		`INSERT INTO role (app_id, code, name) VALUES ($1,'manager','经理') RETURNING id`,
		appID).Scan(&childID))
	_, err := db.Exec(
		`INSERT INTO role_inheritance (app_id, parent_role_id, child_role_id)
		 VALUES ($1,$2,$3)`, appID, parentID, childID)
	require.NoError(t, err)

	rules, err := projection.ProjectApp(context.Background(), db, appID)
	require.NoError(t, err)
	// child 继承 parent → g(child.code, parent.code, domain)
	require.Contains(t, rules,
		cp.Rule{Ptype: "g", V: [6]string{"manager", "admin", dbtest.SeedDomain, "", "", ""}})
}

func TestCheckNoCycle(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)

	var a, b int64
	require.NoError(t, db.QueryRow(
		`INSERT INTO role (app_id, code, name) VALUES ($1,'A','A') RETURNING id`, appID).Scan(&a))
	require.NoError(t, db.QueryRow(
		`INSERT INTO role (app_id, code, name) VALUES ($1,'B','B') RETURNING id`, appID).Scan(&b))
	// 已有 A 继承 B（child=A, parent=B）
	_, err := db.Exec(
		`INSERT INTO role_inheritance (app_id, parent_role_id, child_role_id) VALUES ($1,$2,$3)`,
		appID, b, a)
	require.NoError(t, err)

	// 再加 B 继承 A（child=B, parent=A）会成环 → 报错
	require.Error(t, projection.CheckNoCycle(context.Background(), db, appID, b, a))
	// 自环 → 报错
	require.Error(t, projection.CheckNoCycle(context.Background(), db, appID, a, a))
	// 合法：新角色 C 继承 A（child=C,parent=A），无环 → 通过
	var c int64
	require.NoError(t, db.QueryRow(
		`INSERT INTO role (app_id, code, name) VALUES ($1,'C','C') RETURNING id`, appID).Scan(&c))
	require.NoError(t, projection.CheckNoCycle(context.Background(), db, appID, c, a))
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/projection/ -run 'TestProjectApp|TestCheckNoCycle' -v`
预期：FAIL（`ProjectApp`/`CheckNoCycle` 未定义）。

- [ ] **步骤 3：编写实现**

`internal/controlplane/projection/project.go`：

```go
package projection

import (
	"context"
	"errors"
	"fmt"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
)

// ProjectApp 按 spec §5 投影规则，从业务表算出该 app 的期望 casbin_rule 全集。
func ProjectApp(ctx context.Context, q cp.DBTX, appID int64) ([]cp.Rule, error) {
	var rules []cp.Rule

	// p 行：role_permission ⋈ role ⋈ permission ⋈ application
	pRows, err := q.QueryContext(ctx, `
		SELECT r.code, app.domain, p.resource, p.action, rp.eft
		FROM role_permission rp
		JOIN role r        ON rp.role_id = r.id
		JOIN permission p  ON rp.permission_id = p.id
		JOIN application app ON rp.app_id = app.id
		WHERE rp.app_id = $1`, appID)
	if err != nil {
		return nil, fmt.Errorf("project p rows: %w", err)
	}
	defer pRows.Close()
	for pRows.Next() {
		var sub, dom, obj, act, eft string
		if err := pRows.Scan(&sub, &dom, &obj, &act, &eft); err != nil {
			return nil, err
		}
		rules = append(rules, cp.Rule{Ptype: "p", V: [6]string{sub, dom, obj, act, eft, ""}})
	}
	if err := pRows.Err(); err != nil {
		return nil, err
	}

	// g 行（用户→角色）：user_role_binding ⋈ role ⋈ application
	gURows, err := q.QueryContext(ctx, `
		SELECT urb.user_id, r.code, app.domain
		FROM user_role_binding urb
		JOIN role r          ON urb.role_id = r.id
		JOIN application app ON urb.app_id = app.id
		WHERE urb.app_id = $1`, appID)
	if err != nil {
		return nil, fmt.Errorf("project g(user) rows: %w", err)
	}
	defer gURows.Close()
	for gURows.Next() {
		var user, role, dom string
		if err := gURows.Scan(&user, &role, &dom); err != nil {
			return nil, err
		}
		rules = append(rules, cp.Rule{Ptype: "g", V: [6]string{user, role, dom, "", "", ""}})
	}
	if err := gURows.Err(); err != nil {
		return nil, err
	}

	// g 行（子→父）：role_inheritance ⋈ role(child) ⋈ role(parent) ⋈ application
	gIRows, err := q.QueryContext(ctx, `
		SELECT cr.code, pr.code, app.domain
		FROM role_inheritance ri
		JOIN role cr         ON ri.child_role_id = cr.id
		JOIN role pr         ON ri.parent_role_id = pr.id
		JOIN application app ON ri.app_id = app.id
		WHERE ri.app_id = $1`, appID)
	if err != nil {
		return nil, fmt.Errorf("project g(inherit) rows: %w", err)
	}
	defer gIRows.Close()
	for gIRows.Next() {
		var child, parent, dom string
		if err := gIRows.Scan(&child, &parent, &dom); err != nil {
			return nil, err
		}
		rules = append(rules, cp.Rule{Ptype: "g", V: [6]string{child, parent, dom, "", "", ""}})
	}
	if err := gIRows.Err(); err != nil {
		return nil, err
	}

	return rules, nil
}

// ErrCycle 表示加入该继承边会在角色继承图中形成环。
var ErrCycle = errors.New("projection: role inheritance cycle")

// CheckNoCycle 校验把边 (childID 继承 parentID) 加入该 app 的角色继承图后不成环。
// 继承语义：role_inheritance(child_role_id, parent_role_id) = child 继承 parent。
// 加边 child→parent 成环，当且仅当 child==parent，或 parent 已（传递）继承 child。
func CheckNoCycle(ctx context.Context, q cp.DBTX, appID, childID, parentID int64) error {
	if childID == parentID {
		return fmt.Errorf("%w: self loop on role %d", ErrCycle, childID)
	}
	// 构建邻接：child → []parent（"继承"方向）
	rows, err := q.QueryContext(ctx,
		`SELECT child_role_id, parent_role_id FROM role_inheritance WHERE app_id = $1`, appID)
	if err != nil {
		return err
	}
	defer rows.Close()
	adj := map[int64][]int64{}
	for rows.Next() {
		var c, p int64
		if err := rows.Scan(&c, &p); err != nil {
			return err
		}
		adj[c] = append(adj[c], p)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	// 从 parentID 出发沿"继承"边 DFS，若能到达 childID，则加 child→parent 成环。
	visited := map[int64]bool{}
	var dfs func(n int64) bool
	dfs = func(n int64) bool {
		if n == childID {
			return true
		}
		if visited[n] {
			return false
		}
		visited[n] = true
		for _, nxt := range adj[n] {
			if dfs(nxt) {
				return true
			}
		}
		return false
	}
	if dfs(parentID) {
		return fmt.Errorf("%w: adding %d->%d closes a loop", ErrCycle, childID, parentID)
	}
	return nil
}
```

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/controlplane/projection/ -v`
预期：PASS（Diff + ProjectApp + InheritanceGRow + CheckNoCycle）。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/projection/project.go internal/controlplane/projection/project_test.go
git commit -m "feat(projection): ProjectApp 投影(§5) + CheckNoCycle 环检测"
```

---

## 任务 4：store —— casbin_rule DAO + 版本行锁 + audit + 业务表 CUD

**背景：** `store` 包封装全部 DB 访问：`casbin_rule` 读/diff 应用、`application` 版本行锁与递增、audit 写入、各业务表的 CUD 函数。这些函数都接受 `cp.DBTX`，可在事务内调用。

**文件：**
- 创建：`internal/controlplane/store/store.go`
- 测试：`internal/controlplane/store/store_test.go`

- [ ] **步骤 1：编写失败的测试**

`internal/controlplane/store/store_test.go`：

```go
package store_test

import (
	"context"
	"testing"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestCasbinRule_ApplyDiffAndRead(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	ctx := context.Background()

	adds := []cp.Rule{
		{Ptype: "p", V: [6]string{"admin", "d", "order", "read", "allow", ""}},
		{Ptype: "g", V: [6]string{"alice", "admin", "d", "", "", ""}},
	}
	require.NoError(t, store.ApplyDiff(ctx, db, appID, adds, nil, 1))

	got, err := store.ReadAppRules(ctx, db, appID)
	require.NoError(t, err)
	require.ElementsMatch(t, adds, got)

	// 删一条、加一条，version=2
	require.NoError(t, store.ApplyDiff(ctx, db, appID,
		[]cp.Rule{{Ptype: "p", V: [6]string{"admin", "d", "order", "write", "allow", ""}}},
		[]cp.Rule{{Ptype: "g", V: [6]string{"alice", "admin", "d", "", "", ""}}}, 2))

	got, err = store.ReadAppRules(ctx, db, appID)
	require.NoError(t, err)
	require.ElementsMatch(t, []cp.Rule{
		{Ptype: "p", V: [6]string{"admin", "d", "order", "read", "allow", ""}},
		{Ptype: "p", V: [6]string{"admin", "d", "order", "write", "allow", ""}},
	}, got)
}

func TestLockAndBumpVersion(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	ctx := context.Background()

	tx, err := db.Begin()
	require.NoError(t, err)
	defer tx.Rollback()

	cur, err := store.LockAppVersion(ctx, tx, appID)
	require.NoError(t, err)
	require.Equal(t, int64(0), cur)
	require.NoError(t, store.BumpAppVersion(ctx, tx, appID, cur+1))
	require.NoError(t, tx.Commit())

	var v int64
	require.NoError(t, db.QueryRow(
		`SELECT current_version FROM application WHERE id=$1`, appID).Scan(&v))
	require.Equal(t, int64(1), v)
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/store/ -v`
预期：FAIL（package/函数未定义）。

- [ ] **步骤 3：编写实现**

`internal/controlplane/store/store.go`：

```go
// Package store 封装控制面对数据库的访问：casbin_rule DAO、版本行锁、audit、业务表 CUD。
// 所有函数接受 cp.DBTX，可在事务内调用。
package store

import (
	"context"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
)

// ReadAppRules 读取某 app 当前全部 casbin_rule 行（diff 基准 / 快照来源）。
func ReadAppRules(ctx context.Context, q cp.DBTX, appID int64) ([]cp.Rule, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT ptype, v0, v1, v2, v3, v4, v5 FROM casbin_rule WHERE app_id = $1`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []cp.Rule
	for rows.Next() {
		var r cp.Rule
		if err := rows.Scan(&r.Ptype, &r.V[0], &r.V[1], &r.V[2], &r.V[3], &r.V[4], &r.V[5]); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ApplyDiff 按 diff 删除 removes、插入 adds（version 标为 version）。
func ApplyDiff(ctx context.Context, ex cp.DBTX, appID int64, adds, removes []cp.Rule, version int64) error {
	for _, r := range removes {
		if _, err := ex.ExecContext(ctx, `
			DELETE FROM casbin_rule
			WHERE app_id=$1 AND ptype=$2 AND v0=$3 AND v1=$4 AND v2=$5 AND v3=$6 AND v4=$7 AND v5=$8`,
			appID, r.Ptype, r.V[0], r.V[1], r.V[2], r.V[3], r.V[4], r.V[5]); err != nil {
			return err
		}
	}
	for _, r := range adds {
		if _, err := ex.ExecContext(ctx, `
			INSERT INTO casbin_rule (app_id, ptype, v0, v1, v2, v3, v4, v5, version)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			appID, r.Ptype, r.V[0], r.V[1], r.V[2], r.V[3], r.V[4], r.V[5], version); err != nil {
			return err
		}
	}
	return nil
}

// LockAppVersion 以 FOR UPDATE 行锁读取并返回 app 当前版本号（串行化本 app 变更）。
func LockAppVersion(ctx context.Context, ex cp.DBTX, appID int64) (int64, error) {
	var v int64
	err := ex.QueryRowContext(ctx,
		`SELECT current_version FROM application WHERE id=$1 FOR UPDATE`, appID).Scan(&v)
	return v, err
}

// BumpAppVersion 把 app 版本号写为 vNew。
func BumpAppVersion(ctx context.Context, ex cp.DBTX, appID, vNew int64) error {
	_, err := ex.ExecContext(ctx,
		`UPDATE application SET current_version=$1, updated_at=now() WHERE id=$2`, vNew, appID)
	return err
}

// InsertAudit 写一条审计记录。
func InsertAudit(ctx context.Context, ex cp.DBTX, appID int64, operator, action, entityType, entityID string, version int64) error {
	_, err := ex.ExecContext(ctx, `
		INSERT INTO policy_audit_log (app_id, operator, action, entity_type, entity_id, version)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		appID, operator, action, entityType, entityID, version)
	return err
}

// ── 业务表 CUD（节选实现，全部幂等友好） ──

// InsertRole 建角色，返回 id。
func InsertRole(ctx context.Context, ex cp.DBTX, appID int64, code, name string) (int64, error) {
	var id int64
	err := ex.QueryRowContext(ctx,
		`INSERT INTO role (app_id, code, name) VALUES ($1,$2,$3) RETURNING id`,
		appID, code, name).Scan(&id)
	return id, err
}

// DeleteRole 删角色，并先删其全部引用（role_permission / role_inheritance / user_role_binding），避免 FK 冲突。
func DeleteRole(ctx context.Context, ex cp.DBTX, appID, roleID int64) error {
	stmts := []string{
		`DELETE FROM role_permission WHERE app_id=$1 AND role_id=$2`,
		`DELETE FROM role_inheritance WHERE app_id=$1 AND (parent_role_id=$2 OR child_role_id=$2)`,
		`DELETE FROM user_role_binding WHERE app_id=$1 AND role_id=$2`,
		`DELETE FROM role WHERE app_id=$1 AND id=$2`,
	}
	for _, s := range stmts {
		if _, err := ex.ExecContext(ctx, s, appID, roleID); err != nil {
			return err
		}
	}
	return nil
}

// UpsertPermission 幂等注册权限点（按 app_id+code 去重），返回 id。
func UpsertPermission(ctx context.Context, ex cp.DBTX, appID int64, code, resource, action, ptype, name string) (int64, error) {
	var id int64
	err := ex.QueryRowContext(ctx, `
		INSERT INTO permission (app_id, code, resource, action, type, name)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (app_id, code) DO UPDATE SET name=EXCLUDED.name, updated_at=now()
		RETURNING id`, appID, code, resource, action, ptype, name).Scan(&id)
	return id, err
}

// InsertRolePermission 幂等授权（已存在则不动）。
func InsertRolePermission(ctx context.Context, ex cp.DBTX, appID, roleID, permID int64, eft string) error {
	_, err := ex.ExecContext(ctx, `
		INSERT INTO role_permission (app_id, role_id, permission_id, eft)
		VALUES ($1,$2,$3,$4) ON CONFLICT (app_id, role_id, permission_id) DO NOTHING`,
		appID, roleID, permID, eft)
	return err
}

// DeleteRolePermission 撤权。
func DeleteRolePermission(ctx context.Context, ex cp.DBTX, appID, roleID, permID int64) error {
	_, err := ex.ExecContext(ctx,
		`DELETE FROM role_permission WHERE app_id=$1 AND role_id=$2 AND permission_id=$3`,
		appID, roleID, permID)
	return err
}

// InsertRoleInheritance 幂等加继承边。
func InsertRoleInheritance(ctx context.Context, ex cp.DBTX, appID, childID, parentID int64) error {
	_, err := ex.ExecContext(ctx, `
		INSERT INTO role_inheritance (app_id, parent_role_id, child_role_id)
		VALUES ($1,$2,$3) ON CONFLICT (app_id, parent_role_id, child_role_id) DO NOTHING`,
		appID, parentID, childID)
	return err
}

// DeleteRoleInheritance 删继承边。
func DeleteRoleInheritance(ctx context.Context, ex cp.DBTX, appID, childID, parentID int64) error {
	_, err := ex.ExecContext(ctx,
		`DELETE FROM role_inheritance WHERE app_id=$1 AND parent_role_id=$2 AND child_role_id=$3`,
		appID, parentID, childID)
	return err
}

// InsertUserRoleBinding 幂等绑定用户角色。
func InsertUserRoleBinding(ctx context.Context, ex cp.DBTX, appID int64, userID string, roleID int64) error {
	_, err := ex.ExecContext(ctx, `
		INSERT INTO user_role_binding (app_id, user_id, role_id)
		VALUES ($1,$2,$3) ON CONFLICT (app_id, user_id, role_id) DO NOTHING`,
		appID, userID, roleID)
	return err
}

// DeleteUserRoleBinding 解绑。
func DeleteUserRoleBinding(ctx context.Context, ex cp.DBTX, appID int64, userID string, roleID int64) error {
	_, err := ex.ExecContext(ctx,
		`DELETE FROM user_role_binding WHERE app_id=$1 AND user_id=$2 AND role_id=$3`,
		appID, userID, roleID)
	return err
}

// UpsertDataPolicy 新增或更新一条数据策略，写入 version；返回行 id 与是否为新增。
func UpsertDataPolicy(ctx context.Context, ex cp.DBTX, appID int64, p cp.DataPolicy, version int64) (id int64, created bool, err error) {
	if p.ID == 0 {
		err = ex.QueryRowContext(ctx, `
			INSERT INTO data_policy (app_id, subject_type, subject_id, resource, condition, version)
			VALUES ($1,$2,$3,$4,$5::jsonb,$6) RETURNING id`,
			appID, p.SubjectType, p.SubjectID, p.Resource, p.Condition, version).Scan(&id)
		return id, true, err
	}
	_, err = ex.ExecContext(ctx, `
		UPDATE data_policy SET subject_type=$1, subject_id=$2, resource=$3, condition=$4::jsonb,
		       version=$5, updated_at=now()
		WHERE app_id=$6 AND id=$7`,
		p.SubjectType, p.SubjectID, p.Resource, p.Condition, version, appID, p.ID)
	return p.ID, false, err
}

// DeleteDataPolicy 删一条数据策略。
func DeleteDataPolicy(ctx context.Context, ex cp.DBTX, appID, id int64) error {
	_, err := ex.ExecContext(ctx, `DELETE FROM data_policy WHERE app_id=$1 AND id=$2`, appID, id)
	return err
}
```

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/controlplane/store/ -v`
预期：PASS（ApplyDiff/ReadAppRules 往返、Lock/Bump 版本）。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/store/
git commit -m "feat(store): casbin_rule DAO + 版本行锁 + audit + 业务表 CUD"
```

---

## 任务 5：policy —— runVersionedWrite 核心 + GrantPermission/RevokePermission

**背景：** `PolicyManager` 的统一 §6 写事务模板抽成私有 `runVersionedWrite`，每个公开写方法只提供"业务表变更闭包 + 审计元信息"。本任务实现核心模板 + 首批两个方法，覆盖端到端、幂等无 op、原子回滚、版本串行化。

**文件：**
- 创建：`internal/controlplane/policy/manager.go`
- 测试：`internal/controlplane/policy/manager_test.go`

- [ ] **步骤 1：编写失败的测试**

`internal/controlplane/policy/manager_test.go`：

```go
package policy_test

import (
	"context"
	"database/sql"
	"sync"
	"testing"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/policy"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// 建一个角色 + 权限点，返回 (roleID, permID)。
func seedRoleAndPerm(t *testing.T, db *sql.DB, appID int64) (int64, int64) {
	t.Helper()
	var roleID, permID int64
	require.NoError(t, db.QueryRow(
		`INSERT INTO role (app_id, code, name) VALUES ($1,'manager','经理') RETURNING id`,
		appID).Scan(&roleID))
	require.NoError(t, db.QueryRow(
		`INSERT INTO permission (app_id, code, resource, action, type, name)
		 VALUES ($1,'order:read','order','read','api','读订单') RETURNING id`,
		appID).Scan(&permID))
	return roleID, permID
}

func TestGrantPermission_EndToEnd(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	roleID, permID := seedRoleAndPerm(t, db, appID)
	m := policy.NewPolicyManager(db)

	d, err := m.GrantPermission(context.Background(), appID, roleID, permID, "allow")
	require.NoError(t, err)
	require.NotNil(t, d)
	require.Equal(t, int64(1), d.Version)
	require.ElementsMatch(t, []cp.Rule{
		{Ptype: "p", V: [6]string{"manager", dbtest.SeedDomain, "order", "read", "allow", ""}},
	}, d.RuleAdds)
	require.Empty(t, d.RuleRemoves)

	// casbin_rule 落库、版本 +1、audit 一行
	var rules, ver, audits int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM casbin_rule WHERE app_id=$1`, appID).Scan(&rules))
	require.Equal(t, 1, rules)
	require.NoError(t, db.QueryRow(`SELECT current_version FROM application WHERE id=$1`, appID).Scan(&ver))
	require.Equal(t, 1, ver)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM policy_audit_log WHERE app_id=$1`, appID).Scan(&audits))
	require.Equal(t, 1, audits)
}

func TestGrantPermission_IdempotentNoOp(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	roleID, permID := seedRoleAndPerm(t, db, appID)
	m := policy.NewPolicyManager(db)

	_, err := m.GrantPermission(context.Background(), appID, roleID, permID, "allow")
	require.NoError(t, err)
	// 第二次同样授权：无投影变更 → 返回 nil Delta、版本不变
	d, err := m.GrantPermission(context.Background(), appID, roleID, permID, "allow")
	require.NoError(t, err)
	require.Nil(t, d)

	var ver int
	require.NoError(t, db.QueryRow(`SELECT current_version FROM application WHERE id=$1`, appID).Scan(&ver))
	require.Equal(t, 1, ver)
}

func TestGrantPermission_AtomicRollback(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	roleID, _ := seedRoleAndPerm(t, db, appID)
	m := policy.NewPolicyManager(db)

	// 不存在的 permID → 业务表 INSERT 触发 FK 失败 → 整体回滚
	_, err := m.GrantPermission(context.Background(), appID, roleID, 999999, "allow")
	require.Error(t, err)

	var rules, ver int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM casbin_rule WHERE app_id=$1`, appID).Scan(&rules))
	require.Equal(t, 0, rules)
	require.NoError(t, db.QueryRow(`SELECT current_version FROM application WHERE id=$1`, appID).Scan(&ver))
	require.Equal(t, 0, ver) // 版本未变
}

func TestRevokePermission(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	roleID, permID := seedRoleAndPerm(t, db, appID)
	m := policy.NewPolicyManager(db)

	_, err := m.GrantPermission(context.Background(), appID, roleID, permID, "allow")
	require.NoError(t, err)
	d, err := m.RevokePermission(context.Background(), appID, roleID, permID)
	require.NoError(t, err)
	require.NotNil(t, d)
	require.Equal(t, int64(2), d.Version)
	require.Len(t, d.RuleRemoves, 1)

	var rules int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM casbin_rule WHERE app_id=$1`, appID).Scan(&rules))
	require.Equal(t, 0, rules)
}

func TestVersionSerialized(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	m := policy.NewPolicyManager(db)

	// 预建 N 个角色 + 1 权限点，并发各自授权（不同 role → 各产生一条 p 行）
	const n = 10
	var permID int64
	require.NoError(t, db.QueryRow(
		`INSERT INTO permission (app_id, code, resource, action, type, name)
		 VALUES ($1,'order:read','order','read','api','读订单') RETURNING id`, appID).Scan(&permID))
	roleIDs := make([]int64, n)
	for i := range roleIDs {
		require.NoError(t, db.QueryRow(
			`INSERT INTO role (app_id, code, name) VALUES ($1,$2,$2) RETURNING id`,
			appID, "r"+string(rune('A'+i))).Scan(&roleIDs[i]))
	}

	var wg sync.WaitGroup
	errCh := make(chan error, n)
	for _, rid := range roleIDs {
		wg.Add(1)
		go func(rid int64) {
			defer wg.Done()
			if _, err := m.GrantPermission(context.Background(), appID, rid, permID, "allow"); err != nil {
				errCh <- err
			}
		}(rid)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}

	var ver int64
	require.NoError(t, db.QueryRow(`SELECT current_version FROM application WHERE id=$1`, appID).Scan(&ver))
	require.Equal(t, int64(n), ver) // 严格单调、无丢失
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/policy/ -v`
预期：FAIL（package/方法未定义）。

- [ ] **步骤 3：编写实现**

`internal/controlplane/policy/manager.go`：

```go
// Package policy 编排控制面的版本号写事务，对外暴露策略写方法，返回领域 Delta。
package policy

import (
	"context"
	"database/sql"
	"fmt"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/projection"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
)

// PolicyManager 是控制面真相源写入引擎。
type PolicyManager struct {
	db *sql.DB
}

// NewPolicyManager 构造 PolicyManager。
func NewPolicyManager(db *sql.DB) *PolicyManager {
	return &PolicyManager{db: db}
}

// writeOp 描述一次版本化写：审计元信息 + 业务表变更闭包 + 可选的 data 变更产出。
type writeOp struct {
	action     string
	entityType string
	entityID   string
	// mutate 在事务内执行业务表 CUD；返回本次的 data_policy 变更（功能权限类返回 nil）。
	mutate func(ctx context.Context, tx *sql.Tx) ([]cp.DataPolicyChange, error)
	// preCheck 在加锁后、mutate 前执行（如环检测）；可为 nil。
	preCheck func(ctx context.Context, tx *sql.Tx) error
}

// runVersionedWrite 是 spec §6 统一写事务模板。
func (m *PolicyManager) runVersionedWrite(ctx context.Context, appID int64, op writeOp) (*cp.Delta, error) {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() // COMMIT 成功后再次 Rollback 是 no-op；失败路径确保回滚

	// 1. 行锁串行化本 app
	cur, err := store.LockAppVersion(ctx, tx, appID)
	if err != nil {
		return nil, err
	}
	// 2. 前置校验（环检测等）
	if op.preCheck != nil {
		if err := op.preCheck(ctx, tx); err != nil {
			return nil, err
		}
	}
	// 3. 业务表 CUD
	dataChanges, err := op.mutate(ctx, tx)
	if err != nil {
		return nil, err
	}
	// 4. 重投影 + diff
	desired, err := projection.ProjectApp(ctx, tx, appID)
	if err != nil {
		return nil, err
	}
	current, err := store.ReadAppRules(ctx, tx, appID)
	if err != nil {
		return nil, err
	}
	adds, removes := projection.Diff(current, desired)

	// 5. 无策略影响（无规则变更且无 data 变更）→ COMMIT 业务态，不 bump、不 audit、返回 nil
	if len(adds) == 0 && len(removes) == 0 && len(dataChanges) == 0 {
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, nil
	}

	// 6. bump 版本、写 casbin_rule、写 audit
	vNew := cur + 1
	if err := store.ApplyDiff(ctx, tx, appID, adds, removes, vNew); err != nil {
		return nil, err
	}
	if err := store.BumpAppVersion(ctx, tx, appID, vNew); err != nil {
		return nil, err
	}
	if err := store.InsertAudit(ctx, tx, appID,
		cp.OperatorFromContext(ctx), op.action, op.entityType, op.entityID, vNew); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &cp.Delta{
		AppID: appID, Version: vNew,
		RuleAdds: adds, RuleRemoves: removes, DataChanges: dataChanges,
	}, nil
}

// GrantPermission 给角色授予权限点（幂等）。
func (m *PolicyManager) GrantPermission(ctx context.Context, appID, roleID, permID int64, eft string) (*cp.Delta, error) {
	return m.runVersionedWrite(ctx, appID, writeOp{
		action: "grant", entityType: "role_permission", entityID: fmt.Sprintf("%d:%d", roleID, permID),
		mutate: func(ctx context.Context, tx *sql.Tx) ([]cp.DataPolicyChange, error) {
			return nil, store.InsertRolePermission(ctx, tx, appID, roleID, permID, eft)
		},
	})
}

// RevokePermission 撤销角色的权限点。
func (m *PolicyManager) RevokePermission(ctx context.Context, appID, roleID, permID int64) (*cp.Delta, error) {
	return m.runVersionedWrite(ctx, appID, writeOp{
		action: "revoke", entityType: "role_permission", entityID: fmt.Sprintf("%d:%d", roleID, permID),
		mutate: func(ctx context.Context, tx *sql.Tx) ([]cp.DataPolicyChange, error) {
			return nil, store.DeleteRolePermission(ctx, tx, appID, roleID, permID)
		},
	})
}
```

> 说明：`mutate` 的签名用 `*sql.Tx`（具体类型）而非 `cp.DBTX`——因 store 函数接受 `cp.DBTX` 接口，`*sql.Tx` 满足之，闭包内直接把 `tx` 传给 store 函数即可。

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/controlplane/policy/ -v`
预期：PASS（端到端 / 幂等无 op / 原子回滚 / Revoke / 版本串行化）。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/policy/
git commit -m "feat(policy): runVersionedWrite 核心 + Grant/RevokePermission"
```

---

## 任务 6：policy —— 角色/权限点/继承/绑定写方法

**背景：** 在 `runVersionedWrite` 之上补齐其余功能权限类写方法，覆盖 g 行投影、环检测、删除级联。

**文件：**
- 修改：`internal/controlplane/policy/manager.go`
- 测试：`internal/controlplane/policy/manager_rbac_test.go`

- [ ] **步骤 1：编写失败的测试**

`internal/controlplane/policy/manager_rbac_test.go`：

```go
package policy_test

import (
	"context"
	"testing"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/policy"
	"github.com/nickZFZ/Sydom/internal/controlplane/projection"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestBindUserRole_ProducesGRow(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	m := policy.NewPolicyManager(db)

	rid, _, err := m.CreateRole(context.Background(), appID, "manager", "经理")
	require.NoError(t, err)
	d, err := m.BindUserRole(context.Background(), appID, "u-100", rid)
	require.NoError(t, err)
	require.NotNil(t, d)
	require.Contains(t, d.RuleAdds,
		cp.Rule{Ptype: "g", V: [6]string{"u-100", "manager", dbtest.SeedDomain, "", "", ""}})
}

func TestAddRoleInheritance_CycleRejected(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	m := policy.NewPolicyManager(db)

	a, _, err := m.CreateRole(context.Background(), appID, "A", "A")
	require.NoError(t, err)
	b, _, err := m.CreateRole(context.Background(), appID, "B", "B")
	require.NoError(t, err)

	// A 继承 B
	_, err = m.AddRoleInheritance(context.Background(), appID, a, b)
	require.NoError(t, err)
	// B 继承 A → 成环，必须被拒，且无任何变更
	_, err = m.AddRoleInheritance(context.Background(), appID, b, a)
	require.ErrorIs(t, err, projection.ErrCycle)

	var n int
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM role_inheritance WHERE app_id=$1`, appID).Scan(&n))
	require.Equal(t, 1, n) // 仍只有一条边
}

func TestDeleteRole_CascadesAndRemovesRules(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	m := policy.NewPolicyManager(db)

	permID, _, err := m.UpsertPermission(context.Background(), appID, "order:read", "order", "read", "api", "读订单")
	require.NoError(t, err)
	roleID, _, err := m.CreateRole(context.Background(), appID, "manager", "经理")
	require.NoError(t, err)
	_, err = m.GrantPermission(context.Background(), appID, roleID, permID, "allow")
	require.NoError(t, err)

	d, err := m.DeleteRole(context.Background(), appID, roleID)
	require.NoError(t, err)
	require.NotNil(t, d)

	var rules int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM casbin_rule WHERE app_id=$1`, appID).Scan(&rules))
	require.Equal(t, 0, rules) // 角色删除后其 p 行被重投影清掉
}
```

> 注：`UpsertPermission` 返回 `(permID int64, d *Delta, err error)`、`CreateRole` 返回 `(roleID int64, d *Delta, err error)`——首返回值分别是 permission/role 的 id。

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/policy/ -run 'TestBindUserRole|TestAddRoleInheritance|TestDeleteRole' -v`
预期：FAIL（方法未定义）。

- [ ] **步骤 3：在 manager.go 追加方法**

在 `internal/controlplane/policy/manager.go` 追加（复用已 import 的 store/projection/cp/sql/context/fmt）：

```go
// CreateRole 建角色。建角色本身不产生 casbin_rule（无绑定/授权），故通常返回 nil Delta。
func (m *PolicyManager) CreateRole(ctx context.Context, appID int64, code, name string) (roleID int64, d *cp.Delta, err error) {
	d, err = m.runVersionedWrite(ctx, appID, writeOp{
		action: "create_role", entityType: "role", entityID: code,
		mutate: func(ctx context.Context, tx *sql.Tx) ([]cp.DataPolicyChange, error) {
			id, e := store.InsertRole(ctx, tx, appID, code, name)
			roleID = id
			return nil, e
		},
	})
	return roleID, d, err
}

// DeleteRole 删角色（级联删其全部引用），重投影会清掉相关 casbin_rule 行。
func (m *PolicyManager) DeleteRole(ctx context.Context, appID, roleID int64) (*cp.Delta, error) {
	return m.runVersionedWrite(ctx, appID, writeOp{
		action: "delete_role", entityType: "role", entityID: fmt.Sprintf("%d", roleID),
		mutate: func(ctx context.Context, tx *sql.Tx) ([]cp.DataPolicyChange, error) {
			return nil, store.DeleteRole(ctx, tx, appID, roleID)
		},
	})
}

// UpsertPermission 幂等注册权限点。仅注册不授权时不产生 casbin_rule。
func (m *PolicyManager) UpsertPermission(ctx context.Context, appID int64, code, resource, action, ptype, name string) (permID int64, d *cp.Delta, err error) {
	d, err = m.runVersionedWrite(ctx, appID, writeOp{
		action: "upsert_permission", entityType: "permission", entityID: code,
		mutate: func(ctx context.Context, tx *sql.Tx) ([]cp.DataPolicyChange, error) {
			id, e := store.UpsertPermission(ctx, tx, appID, code, resource, action, ptype, name)
			permID = id
			return nil, e
		},
	})
	return permID, d, err
}

// AddRoleInheritance 加角色继承边（child 继承 parent），加锁后先做环检测。
func (m *PolicyManager) AddRoleInheritance(ctx context.Context, appID, childID, parentID int64) (*cp.Delta, error) {
	return m.runVersionedWrite(ctx, appID, writeOp{
		action: "add_inheritance", entityType: "role_inheritance", entityID: fmt.Sprintf("%d->%d", childID, parentID),
		preCheck: func(ctx context.Context, tx *sql.Tx) error {
			return projection.CheckNoCycle(ctx, tx, appID, childID, parentID)
		},
		mutate: func(ctx context.Context, tx *sql.Tx) ([]cp.DataPolicyChange, error) {
			return nil, store.InsertRoleInheritance(ctx, tx, appID, childID, parentID)
		},
	})
}

// RemoveRoleInheritance 删角色继承边。
func (m *PolicyManager) RemoveRoleInheritance(ctx context.Context, appID, childID, parentID int64) (*cp.Delta, error) {
	return m.runVersionedWrite(ctx, appID, writeOp{
		action: "remove_inheritance", entityType: "role_inheritance", entityID: fmt.Sprintf("%d->%d", childID, parentID),
		mutate: func(ctx context.Context, tx *sql.Tx) ([]cp.DataPolicyChange, error) {
			return nil, store.DeleteRoleInheritance(ctx, tx, appID, childID, parentID)
		},
	})
}

// BindUserRole 绑定用户到角色（幂等）。
func (m *PolicyManager) BindUserRole(ctx context.Context, appID int64, userID string, roleID int64) (*cp.Delta, error) {
	return m.runVersionedWrite(ctx, appID, writeOp{
		action: "bind_user", entityType: "user_role_binding", entityID: fmt.Sprintf("%s:%d", userID, roleID),
		mutate: func(ctx context.Context, tx *sql.Tx) ([]cp.DataPolicyChange, error) {
			return nil, store.InsertUserRoleBinding(ctx, tx, appID, userID, roleID)
		},
	})
}

// UnbindUserRole 解绑用户角色。
func (m *PolicyManager) UnbindUserRole(ctx context.Context, appID int64, userID string, roleID int64) (*cp.Delta, error) {
	return m.runVersionedWrite(ctx, appID, writeOp{
		action: "unbind_user", entityType: "user_role_binding", entityID: fmt.Sprintf("%s:%d", userID, roleID),
		mutate: func(ctx context.Context, tx *sql.Tx) ([]cp.DataPolicyChange, error) {
			return nil, store.DeleteUserRoleBinding(ctx, tx, appID, userID, roleID)
		},
	})
}
```

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/controlplane/policy/ -v`
预期：PASS（含任务 5 的与本任务的全部用例）。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/policy/
git commit -m "feat(policy): 角色/权限点/继承(环检测)/用户绑定写方法"
```

---

## 任务 7：policy —— 数据策略写方法

**背景：** `data_policy` 不参与投影：变更只 bump 版本、更新 `data_policy.version`、不动 `casbin_rule`，并在 `Delta.DataChanges` 带变更。

**文件：**
- 修改：`internal/controlplane/policy/manager.go`
- 测试：`internal/controlplane/policy/manager_datapolicy_test.go`

- [ ] **步骤 1：编写失败的测试**

`internal/controlplane/policy/manager_datapolicy_test.go`：

```go
package policy_test

import (
	"context"
	"testing"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/policy"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestUpsertDataPolicy(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	m := policy.NewPolicyManager(db)

	d, err := m.UpsertDataPolicy(context.Background(), appID, cp.DataPolicy{
		SubjectType: "role", SubjectID: "manager", Resource: "order",
		Condition: `{"op":"EQ","field":"dept","value":"HR"}`,
	})
	require.NoError(t, err)
	require.NotNil(t, d)
	require.Equal(t, int64(1), d.Version)
	require.Len(t, d.DataChanges, 1)
	require.Equal(t, cp.ChangeAdd, d.DataChanges[0].Op)

	// 版本 +1，但 casbin_rule 不受影响
	var rules, ver, dpVer int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM casbin_rule WHERE app_id=$1`, appID).Scan(&rules))
	require.Equal(t, 0, rules)
	require.NoError(t, db.QueryRow(`SELECT current_version FROM application WHERE id=$1`, appID).Scan(&ver))
	require.Equal(t, 1, ver)
	require.NoError(t, db.QueryRow(`SELECT version FROM data_policy WHERE app_id=$1`, appID).Scan(&dpVer))
	require.Equal(t, 1, dpVer)
}

func TestDeleteDataPolicy(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	m := policy.NewPolicyManager(db)

	d, err := m.UpsertDataPolicy(context.Background(), appID, cp.DataPolicy{
		SubjectType: "role", SubjectID: "manager", Resource: "order", Condition: `{"op":"ALL"}`,
	})
	require.NoError(t, err)
	id := d.DataChanges[0].Policy.ID
	require.Positive(t, id)

	d2, err := m.DeleteDataPolicy(context.Background(), appID, id)
	require.NoError(t, err)
	require.NotNil(t, d2)
	require.Equal(t, int64(2), d2.Version)
	require.Equal(t, cp.ChangeRemove, d2.DataChanges[0].Op)

	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM data_policy WHERE app_id=$1`, appID).Scan(&n))
	require.Equal(t, 0, n)
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/policy/ -run 'TestUpsertDataPolicy|TestDeleteDataPolicy' -v`
预期：FAIL（方法未定义）。

- [ ] **步骤 3：在 manager.go 追加方法**

```go
// UpsertDataPolicy 新增/更新一条数据策略（不参与投影，只 bump 版本 + 更新 data_policy.version）。
// 注意：data 变更的 version 写入需用本次事务的 v_new，但 v_new 在 runVersionedWrite 内部才确定。
// 因此这里用 dataMutate 变体：先在闭包里占位，runVersionedWrite 用回填的 version 落 data_policy。
func (m *PolicyManager) UpsertDataPolicy(ctx context.Context, appID int64, p cp.DataPolicy) (*cp.Delta, error) {
	return m.runVersionedWriteData(ctx, appID, writeOpData{
		action: "upsert_data_policy", entityType: "data_policy", entityID: p.SubjectID,
		apply: func(ctx context.Context, tx *sql.Tx, vNew int64) ([]cp.DataPolicyChange, error) {
			id, created, err := store.UpsertDataPolicy(ctx, tx, appID, p, vNew)
			if err != nil {
				return nil, err
			}
			p.ID = id
			op := cp.ChangeUpdate
			if created {
				op = cp.ChangeAdd
			}
			return []cp.DataPolicyChange{{Op: op, Policy: p}}, nil
		},
	})
}

// DeleteDataPolicy 删一条数据策略。
func (m *PolicyManager) DeleteDataPolicy(ctx context.Context, appID, dataPolicyID int64) (*cp.Delta, error) {
	return m.runVersionedWriteData(ctx, appID, writeOpData{
		action: "delete_data_policy", entityType: "data_policy", entityID: fmt.Sprintf("%d", dataPolicyID),
		apply: func(ctx context.Context, tx *sql.Tx, vNew int64) ([]cp.DataPolicyChange, error) {
			if err := store.DeleteDataPolicy(ctx, tx, appID, dataPolicyID); err != nil {
				return nil, err
			}
			return []cp.DataPolicyChange{{Op: cp.ChangeRemove, Policy: cp.DataPolicy{ID: dataPolicyID}}}, nil
		},
	})
}

// writeOpData 是 data_policy 写变体：apply 接收回填的 v_new（data_policy 需写入 version）。
type writeOpData struct {
	action, entityType, entityID string
	apply                        func(ctx context.Context, tx *sql.Tx, vNew int64) ([]cp.DataPolicyChange, error)
}

// runVersionedWriteData 是 data_policy 类的写事务：始终 bump 版本（data 变更即策略变更），
// 不动 casbin_rule，data_policy 写入本次 v_new。
func (m *PolicyManager) runVersionedWriteData(ctx context.Context, appID int64, op writeOpData) (*cp.Delta, error) {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	cur, err := store.LockAppVersion(ctx, tx, appID)
	if err != nil {
		return nil, err
	}
	vNew := cur + 1
	changes, err := op.apply(ctx, tx, vNew)
	if err != nil {
		return nil, err
	}
	if err := store.BumpAppVersion(ctx, tx, appID, vNew); err != nil {
		return nil, err
	}
	if err := store.InsertAudit(ctx, tx, appID,
		cp.OperatorFromContext(ctx), op.action, op.entityType, op.entityID, vNew); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &cp.Delta{AppID: appID, Version: vNew, DataChanges: changes}, nil
}
```

> 说明：data_policy 变更引入 `writeOpData`/`runVersionedWriteData` 是因为它需要在写库时拿到本次 `v_new`（写入 `data_policy.version`），而功能权限类的 `runVersionedWrite` 在 mutate 后才算 v_new。两条模板共享 store 原语、各自职责单一。data_policy 变更视为必然的策略变更（不做"无变更"短路）。

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/controlplane/policy/ -v`
预期：PASS（全部 policy 用例）。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/policy/
git commit -m "feat(policy): 数据策略 Upsert/Delete 写方法（bump 版本，不动 casbin_rule）"
```

---

## 任务 8：secret —— SecretResolver 实现

**背景：** 实现 ② 的 `auth.SecretResolver`：按 `app_key` 查 `application`、解密 `app_secret_enc`。主密钥由外部注入、绝不入库；缺失/错长即 fail-close。

**文件：**
- 创建：`internal/controlplane/secret/resolver.go`
- 测试：`internal/controlplane/secret/resolver_test.go`

- [ ] **步骤 1：编写失败的测试**

`internal/controlplane/secret/resolver_test.go`：

```go
package secret_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/nickZFZ/Sydom/internal/controlplane/secret"
	"github.com/nickZFZ/Sydom/internal/crypto"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func masterKey() []byte { return bytes.Repeat([]byte{0x2a}, crypto.KeySize) }

func TestResolveSecret_RoundTrip(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)

	r, err := secret.NewResolver(db, masterKey())
	require.NoError(t, err)

	plain := []byte("the-real-app-secret")
	enc, err := r.EncryptSecret(plain)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE application SET app_secret_enc=$1 WHERE id=$2`, enc, appID)
	require.NoError(t, err)

	got, err := r.ResolveSecret(context.Background(), dbtest.SeedAppKey)
	require.NoError(t, err)
	require.Equal(t, plain, got)
}

func TestResolveSecret_UnknownApp(t *testing.T) {
	db := dbtest.SetupSchema(t)
	r, err := secret.NewResolver(db, masterKey())
	require.NoError(t, err)
	_, err = r.ResolveSecret(context.Background(), "AK_nonexistent")
	require.Error(t, err)
}

func TestNewResolver_BadKeyFailsClose(t *testing.T) {
	db := dbtest.SetupSchema(t)
	_, err := secret.NewResolver(db, []byte("short"))
	require.Error(t, err) // 主密钥非 32 字节 → 构造即失败
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/secret/ -v`
预期：FAIL（package/方法未定义）。

- [ ] **步骤 3：编写实现**

`internal/controlplane/secret/resolver.go`：

```go
// Package secret 实现控制面对 AppSecret 的加解密：解密 application.app_secret_enc。
package secret

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/nickZFZ/Sydom/internal/auth"
	"github.com/nickZFZ/Sydom/internal/crypto"
)

// 编译期断言：*Resolver 实现 auth.SecretResolver。
var _ auth.SecretResolver = (*Resolver)(nil)

// Resolver 从 application.app_secret_enc 解密取 AppSecret 原文，实现 auth.SecretResolver。
type Resolver struct {
	db        *sql.DB
	masterKey []byte // 32 字节 AES-256 主密钥，外部注入，绝不入库
}

// NewResolver 构造 Resolver；主密钥长度非法即报错（fail-close）。
func NewResolver(db *sql.DB, masterKey []byte) (*Resolver, error) {
	if len(masterKey) != crypto.KeySize {
		return nil, fmt.Errorf("secret: master key must be %d bytes", crypto.KeySize)
	}
	return &Resolver{db: db, masterKey: masterKey}, nil
}

// ResolveSecret 按 app_key=appID 查 application → 解密 app_secret_enc。实现 auth.SecretResolver。
func (r *Resolver) ResolveSecret(ctx context.Context, appID string) ([]byte, error) {
	var enc []byte
	err := r.db.QueryRowContext(ctx,
		`SELECT app_secret_enc FROM application WHERE app_key=$1`, appID).Scan(&enc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("secret: unknown app_key %q", appID)
	}
	if err != nil {
		return nil, err
	}
	return crypto.Decrypt(r.masterKey, enc)
}

// EncryptSecret 供建应用时加密 AppSecret 写入 app_secret_enc。
func (r *Resolver) EncryptSecret(plaintext []byte) ([]byte, error) {
	return crypto.Encrypt(r.masterKey, plaintext)
}
```

- [ ] **步骤 4：运行验证通过 + 接口符合性**

运行：`go build ./...`（含 resolver.go 顶部的 `var _ auth.SecretResolver = (*Resolver)(nil)` 编译期断言，确保实现了接口、无环依赖）。
运行：`go test ./internal/controlplane/secret/ -v`
预期：PASS（往返 / 未知 app / 错主密钥 fail-close）。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/secret/
git commit -m "feat(secret): SecretResolver 实现（解密 app_secret_enc，fail-close）"
```

---

## 收尾：全量验证

- [ ] `go build ./...` —— 无错误。
- [ ] `go test ./... ` —— 全绿（dbtest/controlplane/* 需 Docker 起 PG；既有 db/auth/crypto/wire 不受影响）。
- [ ] `go vet ./...` 与 `gofmt -l internal/`（排除 gen/）—— 无告警、无未格式化。
- [ ] 调用 `superpowers:finishing-a-development-branch` 收尾合入。

---

## 自检结果

**1. 规格覆盖度（对照 spec 各节）：**
- §2 决策 1（不跑 casbin/纯 SQL 投影）→ projection 全 SQL、无 casbin import（任务 2/3）。决策 2（BatchAdapter→DAO）→ store 纯 SQL DAO（任务 4）。决策 3（重投影+diff）→ runVersionedWrite 步骤 4（任务 5）。决策 5（环检测 DFS）→ CheckNoCycle（任务 3）+ AddRoleInheritance preCheck（任务 6）。决策 6（Delta int64 不耦合 gRPC）→ types.go（任务 2）。
- §3 包结构 → dbtest(任务1) + controlplane/{types,projection,store,policy,secret}（任务 2-8），依赖无环。
- §4 类型与接口 → types.go + 各包函数签名（注：spec 的 Queryer/Execer 在计划中合并为单一 `DBTX` 接口，更简洁，语义不变）。
- §5 投影规则 → ProjectApp 三查询逐列对齐表格（任务 3）。
- §6 写事务时序 → runVersionedWrite（功能权限）+ runVersionedWriteData（data_policy）（任务 5/7）。
- §7 一致性铁律 → 原子单事务回滚（任务 5 TestAtomicRollback）、无变更不 bump（任务 5 TestIdempotentNoOp）、投影键不可变（store 不暴露改 code 的方法；Rename 类不在 PolicyManager，归 ③-3）、环检测前置（任务 6）、主密钥 fail-close（任务 8）。
- §8 测试策略 → 各任务 TDD 测试逐条覆盖。

**2. 占位符扫描：** 无 TODO/待定/"类似任务 N"。每个代码步骤含完整可运行代码。`DeleteRole` 级联、`writeOpData` 因 v_new 回填而独立于 `writeOp` 均已说明缘由，非占位。

**3. 类型一致性：** `cp.Rule{Ptype, V[6]}`、`cp.Delta{AppID,Version int64,RuleAdds,RuleRemoves,DataChanges}`、`cp.DBTX`、`cp.DataPolicyChange{Op,Policy}`、`store.*`/`projection.*` 函数签名、`PolicyManager` 方法（`CreateRole`/`UpsertPermission` 返回 `(int64,*Delta,error)`；其余返回 `(*Delta,error)`）跨任务一致。projection 测试用 `package projection`（任务 2 内部测 Diff）与 `package projection_test`（任务 3 外部测）并存合法（Go 允许同目录两包）。
