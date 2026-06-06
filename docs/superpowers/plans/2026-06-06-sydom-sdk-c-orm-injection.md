# 司域 SDK ⑤-C：数据权限 ORM 注入 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 给业务一个方言感知、fail-close 的工具，把 `sydom.Client.FilterSQL` 返回的数据权限片段安全拼进 `database/sql` 查询与 GORM 查询。

**架构：** 双层——通用核心 `sdk/go/sydomsql`（纯函数 `Build`/`AndWhere` + 薄便捷 `Apply`，方言感知占位符改写 + 三态 fail-close，零第三方 ORM 依赖）；薄适配 `sdk/go/sydomgorm`（GORM `Scope`/`ScopeApply`，用 Question 方言交 GORM 自译，仅此包引 `gorm.io/gorm`）。

**技术栈：** Go 1.26.3；标准库 `strings`/`strconv`；`gorm.io/gorm`（仅适配包）；测试用 `gorm.io/driver/mysql` + `github.com/DATA-DOG/go-sqlmock`（DryRun，离线）。

---

## 文件结构

| 文件 | 职责 |
|---|---|
| `sdk/go/sydomsql/sydomsql.go` | 创建：`Dialect`/`Kind`/`Clause` 类型 + `Build` + `AndWhere` + 内部 `toDollar` |
| `sdk/go/sydomsql/sydomsql_test.go` | 创建：`Build`/`AndWhere` 全矩阵单测 |
| `sdk/go/sydomsql/apply.go` | 创建：窄接口 `Filterer` + 便捷 `Apply` |
| `sdk/go/sydomsql/apply_test.go` | 创建：`Apply` stub 单测（错误透传） |
| `sdk/go/sydomgorm/sydomgorm.go` | 创建：`Scope` + `ScopeApply` |
| `sdk/go/sydomgorm/sydomgorm_test.go` | 创建：DryRun + sqlmock 单测 |
| `go.mod` / `go.sum` | 修改：加 `gorm.io/gorm`、`gorm.io/driver/mysql`、`go-sqlmock` |

**不变量（已回源核实 `internal/sidecar/dataperm/render_sql.go`）：** FilterResult 片段里除真正占位符外无任何 `?`，且 `?` 数严格 == `len(Args)`；`""`/`"1=0"` 两特例也无 `?`。`toDollar` 从左到右改写因此安全。

---

### 任务 1：sydomsql 核心类型 + Build

**文件：**
- 创建：`sdk/go/sydomsql/sydomsql.go`
- 测试：`sdk/go/sydomsql/sydomsql_test.go`

- [ ] **步骤 1：编写失败的测试**

写入 `sdk/go/sydomsql/sydomsql_test.go`：

```go
package sydomsql_test

import (
	"testing"

	"github.com/nickZFZ/Sydom/sdk/go/sydom"
	"github.com/nickZFZ/Sydom/sdk/go/sydomsql"
)

func TestBuild_MatchAll(t *testing.T) {
	cl, err := sydomsql.Build(sydom.FilterResult{SQL: ""}, sydomsql.Postgres, 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if cl.Kind != sydomsql.MatchAll || cl.SQL != "" || cl.Args != nil {
		t.Fatalf("got %+v", cl)
	}
}

func TestBuild_MatchNone(t *testing.T) {
	cl, err := sydomsql.Build(sydom.FilterResult{SQL: "1=0"}, sydomsql.Question, 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if cl.Kind != sydomsql.MatchNone || cl.SQL != "1=0" {
		t.Fatalf("got %+v", cl)
	}
}

func TestBuild_Conditional_Question_Passthrough(t *testing.T) {
	fr := sydom.FilterResult{SQL: "dept = ? AND status <> ?", Args: []any{"HR", "void"}}
	cl, err := sydomsql.Build(fr, sydomsql.Question, 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if cl.Kind != sydomsql.Conditional || cl.SQL != "dept = ? AND status <> ?" {
		t.Fatalf("got sql=%q", cl.SQL)
	}
	if len(cl.Args) != 2 || cl.Args[0] != "HR" || cl.Args[1] != "void" {
		t.Fatalf("args=%v", cl.Args)
	}
}

func TestBuild_Conditional_Postgres_Renumber(t *testing.T) {
	fr := sydom.FilterResult{SQL: "(dept = ? AND NOT (status IN (?, ?)))", Args: []any{"HR", "locked", "void"}}
	cl, err := sydomsql.Build(fr, sydomsql.Postgres, 1) // 既有 1 个占位符，片段从 $2 起
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := "(dept = $2 AND NOT (status IN ($3, $4)))"
	if cl.SQL != want {
		t.Fatalf("got %q want %q", cl.SQL, want)
	}
}

func TestBuild_InvariantViolation(t *testing.T) {
	// 2 个 ? 但只有 1 个 arg → fail-close
	_, err := sydomsql.Build(sydom.FilterResult{SQL: "a = ? AND b = ?", Args: []any{1}}, sydomsql.Question, 0)
	if err == nil {
		t.Fatal("want error on placeholder/arg mismatch")
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./sdk/go/sydomsql/ -run TestBuild -v`
预期：编译失败（`sydomsql` 包不存在 / 未定义 `Build` 等）。

- [ ] **步骤 3：编写最少实现代码**

写入 `sdk/go/sydomsql/sydomsql.go`：

```go
// Package sydomsql 把司域数据权限的参数化 SQL 片段（sydom.FilterResult）译成目标
// 数据库方言并安全拼入查询。通用核心，不依赖任何具体 ORM。
package sydomsql

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/nickZFZ/Sydom/sdk/go/sydom"
)

// Dialect 决定占位符风格。
type Dialect int

const (
	// Postgres 用 $1, $2, … 编号占位符。
	Postgres Dialect = iota
	// Question 用 ?（MySQL / SQLite），与 FilterResult 原生风格一致。
	Question
)

// Kind 是数据权限片段的整体语义三态。
type Kind int

const (
	// MatchAll 无过滤：该 resource 未配数据策略，不应附加 WHERE（非泄漏）。
	MatchAll Kind = iota
	// MatchNone deny-all：恒假，必须返回空集。
	MatchNone
	// Conditional 有条件片段。
	Conditional
)

// Clause 是译成目标方言后的结果。用 Build 的调用方必须 switch Kind 并显式处理 MatchNone，
// 否则丢弃 deny-all 会导致行级越权泄漏。
type Clause struct {
	Kind Kind
	SQL  string // MatchAll=""；MatchNone="1=0"；Conditional=已按方言改写的片段
	Args []any  // MatchAll/MatchNone=nil；Conditional=占位符实参
}

// denyAllSQL 是 deny-all 的恒假谓词（两方言通用，无占位符）。
const denyAllSQL = "1=0"

// Build 把 FilterResult 译成目标方言的 Clause。
// startIndex 为既有占位符数（Postgres 续号偏移；Question 方言忽略）。
// 不变量：片段内 ? 数须 == len(fr.Args)，否则返回 error（fail-close，不带病拼 SQL）。
func Build(fr sydom.FilterResult, d Dialect, startIndex int) (Clause, error) {
	switch fr.SQL {
	case "":
		return Clause{Kind: MatchAll}, nil
	case denyAllSQL:
		return Clause{Kind: MatchNone, SQL: denyAllSQL}, nil
	}
	n := strings.Count(fr.SQL, "?")
	if n != len(fr.Args) {
		return Clause{}, fmt.Errorf("sydomsql: 占位符数 %d 与参数数 %d 不一致", n, len(fr.Args))
	}
	sql := fr.SQL
	if d == Postgres {
		sql = toDollar(fr.SQL, startIndex)
	}
	return Clause{Kind: Conditional, SQL: sql, Args: append([]any(nil), fr.Args...)}, nil
}

// toDollar 把从左到右的每个 ? 替换为 $startIndex+1, $startIndex+2, …
// 前置不变量：s 中除占位符外无其它 ?（已回源核实 dataperm 渲染器）。
func toDollar(s string, startIndex int) string {
	var b strings.Builder
	idx := startIndex
	for i := 0; i < len(s); i++ {
		if s[i] == '?' {
			idx++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(idx))
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./sdk/go/sydomsql/ -run TestBuild -v`
预期：全部 PASS。

- [ ] **步骤 5：Commit**

```bash
git add sdk/go/sydomsql/sydomsql.go sdk/go/sydomsql/sydomsql_test.go
git commit -m "feat(sdk): sydomsql 核心 Build — 方言占位符改写 + 三态分类（⑤-C 任务 1）"
```

---

### 任务 2：AndWhere

**文件：**
- 修改：`sdk/go/sydomsql/sydomsql.go`（追加 `AndWhere`）
- 测试：`sdk/go/sydomsql/sydomsql_test.go`（追加）

- [ ] **步骤 1：编写失败的测试**

追加到 `sdk/go/sydomsql/sydomsql_test.go`：

```go
func TestAndWhere_MatchAll_Unchanged(t *testing.T) {
	where, args, err := sydomsql.AndWhere("tenant_id = $1", []any{42}, sydom.FilterResult{SQL: ""}, sydomsql.Postgres)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if where != "tenant_id = $1" || len(args) != 1 || args[0] != 42 {
		t.Fatalf("got where=%q args=%v", where, args)
	}
}

func TestAndWhere_MatchNone_BaseNonEmpty_NeverOpen(t *testing.T) {
	where, args, err := sydomsql.AndWhere("tenant_id = $1", []any{42}, sydom.FilterResult{SQL: "1=0"}, sydomsql.Postgres)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if where != "(tenant_id = $1) AND (1=0)" {
		t.Fatalf("deny-all 退化为放行: %q", where)
	}
	if len(args) != 1 {
		t.Fatalf("args=%v", args)
	}
}

func TestAndWhere_MatchNone_BaseEmpty(t *testing.T) {
	where, _, err := sydomsql.AndWhere("", nil, sydom.FilterResult{SQL: "1=0"}, sydomsql.Question)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if where != "1=0" {
		t.Fatalf("got %q", where)
	}
}

func TestAndWhere_Conditional_Postgres_OffsetByBaseArgs(t *testing.T) {
	fr := sydom.FilterResult{SQL: "(dept = ? AND NOT (status IN (?, ?)))", Args: []any{"HR", "locked", "void"}}
	where, args, err := sydomsql.AndWhere("tenant_id = $1", []any{42}, fr, sydomsql.Postgres)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := "(tenant_id = $1) AND ((dept = $2 AND NOT (status IN ($3, $4))))"
	if where != want {
		t.Fatalf("got %q want %q", where, want)
	}
	if len(args) != 4 || args[0] != 42 || args[1] != "HR" || args[3] != "void" {
		t.Fatalf("args=%v", args)
	}
}

func TestAndWhere_Conditional_BaseEmpty_Question(t *testing.T) {
	fr := sydom.FilterResult{SQL: "dept = ?", Args: []any{"HR"}}
	where, args, err := sydomsql.AndWhere("", nil, fr, sydomsql.Question)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if where != "dept = ?" || len(args) != 1 || args[0] != "HR" {
		t.Fatalf("got where=%q args=%v", where, args)
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./sdk/go/sydomsql/ -run TestAndWhere -v`
预期：编译失败（`AndWhere` 未定义）。

- [ ] **步骤 3：编写最少实现代码**

追加到 `sdk/go/sydomsql/sydomsql.go`：

```go
// AndWhere 把数据权限片段 AND 进既有 WHERE，自动处理三态 + Postgres 续号。
//   MatchAll    → (base, baseArgs) 原样
//   MatchNone   → base 非空 "(base) AND (1=0)"；base 空 "1=0"（恒假护栏，绝不放行）
//   Conditional → base 非空 "(base) AND (frag)"；base 空 frag；args = baseArgs + fragArgs
// Postgres 下片段占位符按 len(baseArgs) 续号，杜绝 $N 撞号。
func AndWhere(base string, baseArgs []any, fr sydom.FilterResult, d Dialect) (where string, args []any, err error) {
	cl, err := Build(fr, d, len(baseArgs))
	if err != nil {
		return "", nil, err
	}
	switch cl.Kind {
	case MatchAll:
		return base, baseArgs, nil
	case MatchNone:
		if base == "" {
			return denyAllSQL, baseArgs, nil
		}
		return "(" + base + ") AND (" + denyAllSQL + ")", baseArgs, nil
	default:
		merged := append(append([]any(nil), baseArgs...), cl.Args...)
		if base == "" {
			return cl.SQL, merged, nil
		}
		return "(" + base + ") AND (" + cl.SQL + ")", merged, nil
	}
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./sdk/go/sydomsql/ -v`
预期：全部 PASS（任务 1 + 任务 2 用例）。

- [ ] **步骤 5：Commit**

```bash
git add sdk/go/sydomsql/sydomsql.go sdk/go/sydomsql/sydomsql_test.go
git commit -m "feat(sdk): sydomsql AndWhere — 三态拼接 + Postgres 续号 + deny-all 不退化（⑤-C 任务 2）"
```

---

### 任务 3：Apply 便捷封装 + Filterer 窄接口

**文件：**
- 创建：`sdk/go/sydomsql/apply.go`
- 测试：`sdk/go/sydomsql/apply_test.go`

- [ ] **步骤 1：编写失败的测试**

写入 `sdk/go/sydomsql/apply_test.go`：

```go
package sydomsql_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nickZFZ/Sydom/sdk/go/sydom"
	"github.com/nickZFZ/Sydom/sdk/go/sydomsql"
)

type stubFilterer struct {
	fr  sydom.FilterResult
	err error
}

func (s stubFilterer) FilterSQL(ctx context.Context, subject, resource string, attrs map[string]any) (sydom.FilterResult, error) {
	return s.fr, s.err
}

func TestApply_HappyPath_PostgresRenumber(t *testing.T) {
	f := stubFilterer{fr: sydom.FilterResult{SQL: "dept = ?", Args: []any{"HR"}}}
	cl, err := sydomsql.Apply(context.Background(), f, "alice", "order", nil, sydomsql.Postgres, 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if cl.Kind != sydomsql.Conditional || cl.SQL != "dept = $1" {
		t.Fatalf("got kind=%v sql=%q", cl.Kind, cl.SQL)
	}
}

func TestApply_UnavailablePassthrough(t *testing.T) {
	f := stubFilterer{err: sydom.ErrUnavailable}
	_, err := sydomsql.Apply(context.Background(), f, "alice", "order", nil, sydomsql.Question, 0)
	if !errors.Is(err, sydom.ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./sdk/go/sydomsql/ -run TestApply -v`
预期：编译失败（`Apply`/`Filterer` 未定义）。

- [ ] **步骤 3：编写最少实现代码**

写入 `sdk/go/sydomsql/apply.go`：

```go
package sydomsql

import (
	"context"

	"github.com/nickZFZ/Sydom/sdk/go/sydom"
)

// Filterer 是 Apply 对核心客户端的窄依赖；*sydom.Client 自动满足。
type Filterer interface {
	FilterSQL(ctx context.Context, subject, resource string, attrs map[string]any) (sydom.FilterResult, error)
}

// Apply 调 f.FilterSQL 取数据权限片段，再 Build 成目标方言 Clause。
// FilterSQL 的错误（含 sydom.ErrUnavailable 哨兵）原样透传，由调用方据风险自定 fail-open/close。
func Apply(ctx context.Context, f Filterer, subject, resource string, attrs map[string]any, d Dialect, startIndex int) (Clause, error) {
	fr, err := f.FilterSQL(ctx, subject, resource, attrs)
	if err != nil {
		return Clause{}, err
	}
	return Build(fr, d, startIndex)
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./sdk/go/sydomsql/ -v`
预期：全部 PASS。同时确认 `*sydom.Client` 满足 `Filterer`（编译期）：在 `apply_test.go` 顶部已隐含——如需显式可加 `var _ sydomsql.Filterer = (*sydom.Client)(nil)`（可选，本步不强制）。

- [ ] **步骤 5：Commit**

```bash
git add sdk/go/sydomsql/apply.go sdk/go/sydomsql/apply_test.go
git commit -m "feat(sdk): sydomsql Apply 便捷封装 + Filterer 窄接口（⑤-C 任务 3）"
```

---

### 任务 4：sydomgorm 适配（Scope + ScopeApply）

**文件：**
- 创建：`sdk/go/sydomgorm/sydomgorm.go`
- 测试：`sdk/go/sydomgorm/sydomgorm_test.go`
- 修改：`go.mod` / `go.sum`

- [ ] **步骤 1：拉取依赖**

运行：

```bash
go get gorm.io/gorm@latest
go get gorm.io/driver/mysql@latest
go get github.com/DATA-DOG/go-sqlmock@latest
```

预期：三个模块写入 `go.mod`（联网下载）。

- [ ] **步骤 2：编写失败的测试**

写入 `sdk/go/sydomgorm/sydomgorm_test.go`：

```go
package sydomgorm_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/nickZFZ/Sydom/sdk/go/sydom"
	"github.com/nickZFZ/Sydom/sdk/go/sydomgorm"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

type order struct {
	ID     uint
	Dept   string
	Status string
}

type stubFilterer struct {
	fr  sydom.FilterResult
	err error
}

func (s stubFilterer) FilterSQL(ctx context.Context, subject, resource string, attrs map[string]any) (sydom.FilterResult, error) {
	return s.fr, s.err
}

func newDryRunDB(t *testing.T) *gorm.DB {
	t.Helper()
	sqlDB, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	gdb, err := gorm.Open(mysql.New(mysql.Config{
		Conn:                      sqlDB,
		SkipInitializeWithVersion: true,
	}), &gorm.Config{DryRun: true})
	if err != nil {
		t.Fatalf("gorm open: %v", err)
	}
	return gdb
}

func TestScope_Conditional(t *testing.T) {
	db := newDryRunDB(t)
	fr := sydom.FilterResult{SQL: "dept = ? AND status <> ?", Args: []any{"HR", "void"}}
	res := db.Model(&order{}).Scopes(sydomgorm.Scope(fr)).Find(&[]order{})
	if res.Error != nil {
		t.Fatalf("err: %v", res.Error)
	}
	got := res.Statement.SQL.String()
	if !strings.Contains(got, "dept = ?") || !strings.Contains(got, "status <> ?") {
		t.Fatalf("missing where: %q", got)
	}
	if len(res.Statement.Vars) != 2 {
		t.Fatalf("want 2 vars, got %v", res.Statement.Vars)
	}
}

func TestScope_MatchNone(t *testing.T) {
	db := newDryRunDB(t)
	res := db.Model(&order{}).Scopes(sydomgorm.Scope(sydom.FilterResult{SQL: "1=0"})).Find(&[]order{})
	if res.Error != nil {
		t.Fatalf("err: %v", res.Error)
	}
	if !strings.Contains(res.Statement.SQL.String(), "1=0") {
		t.Fatalf("deny-all 丢失: %q", res.Statement.SQL.String())
	}
}

func TestScope_MatchAll_NoWhere(t *testing.T) {
	db := newDryRunDB(t)
	res := db.Model(&order{}).Scopes(sydomgorm.Scope(sydom.FilterResult{SQL: ""})).Find(&[]order{})
	if res.Error != nil {
		t.Fatalf("err: %v", res.Error)
	}
	if strings.Contains(res.Statement.SQL.String(), "WHERE") {
		t.Fatalf("MatchAll 不应有 WHERE: %q", res.Statement.SQL.String())
	}
}

func TestScope_InvariantError(t *testing.T) {
	db := newDryRunDB(t)
	// 1 个 ? 但 0 个 arg → Build 报错 → AddError，fail-close
	res := db.Model(&order{}).Scopes(sydomgorm.Scope(sydom.FilterResult{SQL: "a = ?"})).Find(&[]order{})
	if res.Error == nil {
		t.Fatal("want error on invariant violation")
	}
}

func TestScopeApply_UnavailablePropagates(t *testing.T) {
	db := newDryRunDB(t)
	f := stubFilterer{err: sydom.ErrUnavailable}
	res := db.Model(&order{}).Scopes(sydomgorm.ScopeApply(context.Background(), f, "alice", "order", nil)).Find(&[]order{})
	if !errors.Is(res.Error, sydom.ErrUnavailable) {
		t.Fatalf("want ErrUnavailable, got %v", res.Error)
	}
}
```

- [ ] **步骤 3：运行测试验证失败**

运行：`go test ./sdk/go/sydomgorm/ -v`
预期：编译失败（`sydomgorm` 包不存在）。

- [ ] **步骤 4：编写最少实现代码**

写入 `sdk/go/sydomgorm/sydomgorm.go`：

```go
// Package sydomgorm 把司域数据权限片段注入 GORM 查询。薄适配层：只 wrap sydomsql.Build。
package sydomgorm

import (
	"context"

	"github.com/nickZFZ/Sydom/sdk/go/sydom"
	"github.com/nickZFZ/Sydom/sdk/go/sydomsql"
	"gorm.io/gorm"
)

// Scope 把数据权限片段注入 GORM 查询，返回 gorm scope。
// 用 Question 方言（?）交 GORM 按驱动自译占位符。
//   MatchAll    → 原样返回 db（不加过滤）
//   MatchNone   → db.Where("1=0")（deny-all，绝不放行）
//   Conditional → db.Where(frag, args...)
// Build 出错经 db.AddError 注入，使后续 Find 返回该 error 而非执行无过滤查询（fail-close）。
func Scope(fr sydom.FilterResult) func(*gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		cl, err := sydomsql.Build(fr, sydomsql.Question, 0)
		if err != nil {
			_ = db.AddError(err)
			return db
		}
		switch cl.Kind {
		case sydomsql.MatchAll:
			return db
		case sydomsql.MatchNone:
			return db.Where(cl.SQL)
		default:
			return db.Where(cl.SQL, cl.Args...)
		}
	}
}

// ScopeApply 便捷封装：调 f.FilterSQL 取片段再 Scope。
// FilterSQL 的错误（含 sydom.ErrUnavailable）经 db.AddError 注入，业务据 db.Error 自定 fail-open/close。
func ScopeApply(ctx context.Context, f sydomsql.Filterer, subject, resource string, attrs map[string]any) func(*gorm.DB) *gorm.DB {
	return func(db *gorm.DB) *gorm.DB {
		fr, err := f.FilterSQL(ctx, subject, resource, attrs)
		if err != nil {
			_ = db.AddError(err)
			return db
		}
		return Scope(fr)(db)
	}
}
```

- [ ] **步骤 5：运行测试验证通过**

运行：`go test ./sdk/go/sydomgorm/ -v`
预期：全部 PASS。

- [ ] **步骤 6：整理依赖并 Commit**

```bash
go mod tidy
git add go.mod go.sum sdk/go/sydomgorm/
git commit -m "feat(sdk): sydomgorm 适配 — GORM Scope/ScopeApply（Question 方言交 GORM 自译）（⑤-C 任务 4）"
```

---

### 任务 5：全量验证 + 收尾

**文件：** 无（仅验证）

- [ ] **步骤 1：gofmt 检查**

运行：`gofmt -l sdk/`
预期：无输出（全部已格式化）。若有输出，`gofmt -w` 后并入相应任务的 commit。

- [ ] **步骤 2：vet + build**

运行：`go vet ./sdk/... && go build ./...`
预期：均无输出、退出码 0。

- [ ] **步骤 3：全量测试（含竞态）**

运行：`go test ./sdk/... -race`
预期：`sydom`、`sydomhttp`、`sydomsql`、`sydomgorm` 四包全 `ok`。

- [ ] **步骤 4：确认对既有包零改动**

运行：`git diff --name-only main..HEAD -- ':!sdk' ':!docs' ':!go.mod' ':!go.sum'`
预期：无输出（除 `sdk/`、`docs/` 与 go.mod/go.sum 依赖追加外零改动）。

- [ ] **步骤 5：收尾**

进入 finishing-a-development-branch 流程（验证 → 选项 → 合并/PR）。

---

## 自检

**1. 规格覆盖度**（对照 `2026-06-06-sydom-sdk-c-orm-injection-design.md`）：
- §2 D1 双层结构 → 任务 1-3（sydomsql）+ 任务 4（sydomgorm）✓
- §2 D2 方案 A（Build/AndWhere/Apply）→ 任务 1/2/3 ✓
- §2 D3 两方言 → 任务 1（Postgres/Question）✓
- §2 D4 类型化 Clause 三态 fail-close → 任务 1（Build Kind）+ 任务 2（AndWhere deny-all 不退化）✓
- §2 D5 GORM 用 Question 方言 → 任务 4（`Build(fr, Question, 0)`）✓
- §3 占位符不变量校验 → 任务 1（`?` 数 == len(Args) 校验 + `TestBuild_InvariantViolation`）✓
- §4.1 sydomsql API → 任务 1/2/3 全覆盖 ✓
- §4.2 sydomgorm API + AddError fail-close → 任务 4（`TestScope_InvariantError`/`TestScopeApply_UnavailablePropagates`）✓
- §6 错误处理表 → 任务 1（不变量）/ 任务 3（Unavailable 透传）/ 任务 4（AddError）✓
- §7 测试策略（两方言 × 三态、续号、deny-all 不退化、GORM DryRun、Apply 透传）→ 任务 1/2/3/4 全覆盖 ✓
- §8 对既有包零改动 → 任务 5 步骤 4 校验 ✓

**2. 占位符扫描：** 计划内无 TODO/待定；所有步骤含完整可编译代码。

**3. 类型一致性：** `Dialect`（Postgres/Question）、`Kind`（MatchAll/MatchNone/Conditional）、`Clause{Kind,SQL,Args}`、`Build(fr,d,startIndex)`、`AndWhere(base,baseArgs,fr,d)`、`Apply(ctx,f,subject,resource,attrs,d,startIndex)`、`Filterer.FilterSQL`、`Scope(fr)`、`ScopeApply(ctx,f,subject,resource,attrs)` 在任务 1→4 间签名一致；`denyAllSQL="1=0"` 单一来源。
