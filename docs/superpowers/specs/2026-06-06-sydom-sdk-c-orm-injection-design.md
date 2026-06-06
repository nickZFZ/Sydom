# 司域 SDK ⑤-C：数据权限 ORM 注入 设计

> 日期：2026-06-06　状态：设计已批准，待实现
> 关联：⑤ SDK A+B 切片已并入 main（`83dd09a`，核心客户端 `sydom` + net/http 中间件 `sydomhttp`）。本文是 SDK 的 **C 切片**——数据权限行级过滤的 ORM 注入。D 切片（权限点上报）另立 spec。

## 1. 背景与范围

A 切片的 `sydom.Client.FilterSQL(ctx, subject, resource, attrs)` 已能返回数据权限的参数化 SQL 片段：

```go
type FilterResult struct {
    SQL  string // 无过滤=""；deny-all="1=0"；否则参数化片段，如 (dept = ? AND NOT (status IN (?, ?)))
    Args []any  // 占位符实参（JSON 标量）
}
```

但业务要把这个片段真正拼进查询，还差三件 `database/sql` 不替你做的事，C 切片补齐：

1. **方言占位符**：`FilterResult.SQL` 用 `?`；PostgreSQL 驱动要 `$N`，MySQL/SQLite 用 `?`。
2. **占位符续号**：Postgres 下若既有查询已用 `$1..$k`，注入片段的占位符须接续从 `$k+1` 编号，杜绝撞号。
3. **三态语义的 fail-close**：`""`=无过滤（不加 WHERE）、`"1=0"`=deny-all（必须返回空集）、否则=条件片段；**deny-all 绝不能被静默丢成无过滤**（那是行级越权泄漏，触碰本项目"一致性优先于简化"红线）。

**范围**：本切片交付 ① 通用核心包 `sdk/go/sydomsql`（方言感知的纯函数改写器 + 一个调 client 的薄便捷封装）；② 薄 ORM 适配包 `sdk/go/sydomgorm`（GORM scope，只 wrap 核心）。**对既有包零改动。**

**不在范围**：JPA / MyBatis / 其它语言 ORM 适配（各为独立"适配层"，后续单起）；ORDER BY / JOIN / 字段投影注入（只注行级 WHERE）；MySQL 之外的更多方言。

## 2. 设计决策

- **D1 双层结构**：通用核心 `sydomsql`（零第三方 ORM 依赖，只依赖 `sydom` 的 `FilterResult` 类型）+ 适配层 `sydomgorm`（薄，wrap 核心，仅该包引 `gorm.io/gorm`）。ORM 框架按"适配层"定位与通用核心解耦——换语言/框架只换适配层。
- **D2 API = 方案 A**：`Build` + `AndWhere` 纯函数为核心，`Apply` 为调 `*sydom.Client.FilterSQL` 的薄便捷封装。纯函数不碰 gRPC、可纯单测。
- **D3 两方言**：`Postgres`（`$N`）与 `Question`（`?`，MySQL/SQLite）。对齐 DB schema 的"PG 优先、MySQL 第二套"。
- **D4 类型化 Clause + 三态 fail-close**：`Build` 返回带 `Kind`（MatchAll/MatchNone/Conditional）的 `Clause`，**强制调用方显式处理 deny-all**；`AndWhere` 便捷封装在 MatchNone 时恒返回含 `1=0` 的恒假谓词，绝不退化为放行。
- **D5 GORM 适配用 Question 方言**：GORM 的 `db.Where("x = ?", v)` 自身按驱动把 `?` 译成 `$N`，故适配层喂 **Question（`?`）** 风格交 GORM 自译，**不**在适配层做 Postgres 改写（否则与 GORM 的再翻译叠加出错）。

## 3. 占位符改写正确性不变量（回源核实）

C 的方言改写把 `?` 从左到右替换为 `$startIndex+1, $startIndex+2, …`。其正确性依赖一个不变量，已回源核实 `internal/sidecar/dataperm/render_sql.go`：

- 渲染器把**每个值都 append 进 Args**，SQL 文本里只写 `?` 占位符；
- 字段名解析期经白名单 `^[A-Za-z_][A-Za-z0-9_]*$` 校验，**不可能含 `?`**；
- 关键字（`IN` / `NOT IN` / `BETWEEN ? AND ?` / `IS NULL` / 比较符 / `AND`/`OR`/`NOT`）均无 `?`；
- 两特例 `""` 与 `"1=0"` 也无 `?`。

**结论**：片段里除真正占位符外没有任何 `?`，且 `?` 数严格 == `len(Args)`。从左到右朴素改写安全正确。`Build` 额外断言"`?` 计数 == len(Args)"，不符即返回 error（不变量被破坏时 fail-close，不带病拼 SQL）。

## 4. 组件分解

### 4.1 `sdk/go/sydomsql`（通用核心）

```go
package sydomsql

// Dialect 决定占位符风格。
type Dialect int
const (
    Postgres Dialect = iota // $1, $2, …
    Question                // ?, ?      （MySQL / SQLite，FilterResult 原生风格）
)

// Kind 是数据权限片段的整体语义三态。
type Kind int
const (
    MatchAll    Kind = iota // 无过滤：不加 WHERE（该 resource 未配数据策略，非泄漏）
    MatchNone               // deny-all：恒假，必须返回空集
    Conditional            // 有条件片段
)

// Clause 是译成目标方言后的结果。调用方若用 Build，必须 switch Kind 并显式处理 MatchNone。
type Clause struct {
    Kind Kind
    SQL  string // MatchAll=""；MatchNone="1=0"；Conditional=已按方言改写的片段
    Args []any  // MatchAll/MatchNone=nil；Conditional=占位符实参
}

// Build 把 FilterResult 译成目标方言的 Clause。
// startIndex = 既有占位符数（Postgres 续号偏移；Question 方言忽略）。
// 不变量校验：片段内 ? 数须 == len(fr.Args)，否则返回 error（fail-close）。
func Build(fr sydom.FilterResult, d Dialect, startIndex int) (Clause, error)

// AndWhere 把数据权限片段 AND 进既有 WHERE，自动处理三态 + 续号。
//   MatchAll    → 返回 (base, baseArgs) 原样
//   MatchNone   → base 非空: "(base) AND (1=0)"；base 空: "1=0"（恒假，fail-close 护栏）
//   Conditional → base 非空: "(base) AND (frag)"；base 空: frag；args = baseArgs + fragArgs
// Postgres 下片段占位符按 len(baseArgs) 续号。
func AndWhere(base string, baseArgs []any, fr sydom.FilterResult, d Dialect) (where string, args []any, err error)

// Apply 是薄便捷封装：调 client.FilterSQL 取片段，再 Build 成 Clause。
// 供"先拿过滤再自行拼装"的调用方少写一步；与 Build 共享全部三态/fail-close 语义。
func Apply(ctx context.Context, c *sydom.Client, subject, resource string, attrs map[string]any, d Dialect, startIndex int) (Clause, error)
```

### 4.2 `sdk/go/sydomgorm`（GORM 适配层，薄）

```go
package sydomgorm

// Scope 把数据权限片段注入 GORM 查询，返回 gorm scope。用 Question 方言交 GORM 自译占位符。
//   MatchAll    → 原样返回 db
//   MatchNone   → db.Where("1=0")
//   Conditional → db.Where(frag, args...)
// 用法：db.Scopes(sydomgorm.Scope(fr)).Find(&xs)
func Scope(fr sydom.FilterResult) func(*gorm.DB) *gorm.DB

// ScopeApply 便捷封装：调 client.FilterSQL 取片段再 Scope（错误经 db.AddError 传播，fail-close）。
func ScopeApply(ctx context.Context, c *sydom.Client, subject, resource string, attrs map[string]any) func(*gorm.DB) *gorm.DB
```

> GORM scope 内部出错（如 Build 不变量破坏）经 `db.AddError(err)` 注入，使后续 `Find` 返回该 error 而非执行无过滤查询——**适配层也守 fail-close**。

## 5. 数据流

```
业务查询前
  → client.FilterSQL(subject, resource, attrs)        // A 切片已有
      ⇒ FilterResult{SQL:"(dept = ? AND NOT (status IN (?, ?)))", Args:["HR","locked","void"]}
  → sydomsql.AndWhere("tenant_id = $1", []any{42}, fr, sydomsql.Postgres)
      ⇒ where = "(tenant_id = $1) AND (dept = $2 AND NOT (status IN ($3, $4)))"
        args  = [42, "HR", "locked", "void"]
  → db.Query("SELECT ... WHERE "+where, args...)
```

GORM 路径：

```
db.Scopes(sydomgorm.Scope(fr)).Find(&orders)
  ⇒ GORM 拼 WHERE (dept = ? AND NOT (status IN (?, ?)))，按驱动自译 $N
```

## 6. 错误处理

| 情形 | 行为 |
|---|---|
| `Build`/`AndWhere`：`?` 数 ≠ len(Args)（不变量破坏） | 返回 error，调用方不得带病拼 SQL（fail-close） |
| `FilterResult` = MatchNone（`"1=0"`） | 恒假谓词，**绝不**退化为空 WHERE |
| `FilterResult` = MatchAll（`""`） | 不加过滤（语义即"未配数据策略"，非泄漏） |
| `Apply`/`ScopeApply`：`client.FilterSQL` 返回 `ErrUnavailable` 或硬错误 | 原样透传 error（含 `sydom.ErrUnavailable` 哨兵），由调用方据风险自定 fail-open/close——SDK 不替业务决定数据层放行 |
| Postgres 续号 | 严格按 `len(baseArgs)` 偏移，杜绝 `$N` 撞号 |

## 7. 测试策略

纯单测，无 Docker / 无 gRPC（核心纯函数；GORM 用 sqlmock 或 DryRun 模式断言生成 SQL）：

- **两方言 × 三态全矩阵**：{Postgres, Question} × {MatchAll, MatchNone, Conditional}。
- **Postgres 续号**：既有 `$1` → 片段从 `$2` 起；多占位符连续编号正确。
- **不变量校验**：构造 `?` 数与 Args 数不一致的 `FilterResult` → `Build` 返回 error。
- **deny-all 不退化**：MatchNone 经 `AndWhere`/`Scope` 后 WHERE 恒含 `1=0`。
- **真实片段端到端**：`(dept = ? AND NOT (status IN (?, ?)))` 分别译成 PG（`$N`）/ MySQL（`?`）两版断言。
- **GORM 适配**：`db.Session(&gorm.Session{DryRun:true})` 断言三态生成的 SQL；MatchAll 不加 WHERE。
- **Apply/ScopeApply**：验证 `ErrUnavailable` 透传。〔实现说明：因 `Apply`/`ScopeApply` 经窄接口 `Filterer` 解耦，用轻量 `stubFilterer` 桩即可验证错误透传，无需 bufconn 起真 gRPC——真链路已由 A 切片 e2e 覆盖，此处不重复。〕

## 8. 对既有包零改动

C 切片只新增 `sdk/go/sydomsql`、`sdk/go/sydomgorm` 两包与各自测试，不动 `sydom`/`sydomhttp` 及任何 `internal/` 代码。`gorm.io/gorm` 仅 `sydomgorm` 包引入（进 go.mod，但核心 `sydomsql` 与既有包零新依赖）。

## 9. YAGNI / 范围外

- 不做 JPA/MyBatis/其它语言 ORM 适配（独立适配层后续单起）。
- 不做非行级注入（ORDER BY / JOIN / SELECT 字段裁剪）。
- 不做 Postgres/Question 之外的方言（如 Oracle `:name`、SQL Server `@p`）。
- 不内置"自动在每条 ORM 查询前拉过滤"的全局 Hook——由业务在查询点显式调用（显式优于隐式，避免漏配/性能黑箱）。

## 10. 自检

- **占位符扫描**：无 TODO/待定；所有 API 签名、三态、续号语义均已写实。
- **内部一致性**：D5（GORM 用 Question 方言）与 §4.2、§5 GORM 数据流一致；fail-close 在 §1.3、§4.1 D4、§6 三处口径一致。
- **范围检查**：聚焦单一实现计划可覆盖（两薄包 + 测试）。
- **模糊性检查**：`Build`（低层、调用方 switch Kind）与 `AndWhere`（高层、自动三态）职责边界明确；`Apply`/`ScopeApply` 仅多一步 client 调用，错误语义与底层一致。
