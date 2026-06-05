# Sidecar 数据权限引擎（④-2）实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 实现 `internal/sidecar/dataperm` 包——纯内存数据权限引擎：维护按 app 下发的 DataPolicy 内存表（实现 `kernel.DataPolicyApplier`）+ 条件树求值 + 主体角色展开（复用内核角色图）+ allow/deny deny-overrides 合并 + sql/raw 参数化渲染。

**架构：** `Table` 持内存表并实现 applier（apply 时解析条件树、按 resource 索引、RWMutex）；`Filter` 无状态编排（窄接口 `RoleResolver` 展开主体 → 收集拆分 allow/deny → 合并 `OR(allow) AND NOT OR(deny)` → 变量解析 → 渲染）。空匹配两层语义（未配置→无过滤；配了未命中→`1=0`）。fail-close：未就绪/越域/中毒策略/缺变量一律返错。本期仅给已发的 `kernel.DataPolicy` 加 `Effect` 字段，其上游生产链留 ④-3。

**技术栈：** Go 1.26 + 标准库（encoding/json、regexp、strings）。依赖 `internal/sidecar/kernel`。测试用 testify。**全部纯单测，无 Docker/testcontainers。**

**规格：** `docs/superpowers/specs/2026-06-04-sydom-sidecar-data-policy-engine-design.md`。

---

## 已核实的内核 API（实现者直接据此写，勿臆测）

- `kernel.DataPolicy{ID uint64; SubjectType, SubjectID, Resource, Condition string}`（任务 1 追加 `Effect string`）。
- `kernel.ChangeOp`：`ChangeAdd`/`ChangeUpdate`/`ChangeRemove`。
- `kernel.DataPolicyApplier interface { ApplySnapshot([]DataPolicy); ApplyChange(ChangeOp, DataPolicy) }`。
- `kernel.Engine.GetImplicitRolesForUser(user, dom string) ([]string, error)`（满足本包 `RoleResolver`）。
- `kernel.New(domain string, c cache.Cache, applier DataPolicyApplier) (*Engine, error)`；构造时把 applier 注入。
- 哨兵：`kernel.ErrNotReady`/`kernel.ErrForeignDomain`。
- 内核 `engine_test.go` 已有 `spyApplier`（记录 ApplySnapshot/ApplyChange）和 `mgrSnapshot`。

---

## 文件结构

包 `internal/sidecar/dataperm`，模块 `github.com/nickZFZ/Sydom`：

| 文件 | 职责 | 任务 |
|---|---|---|
| `internal/sidecar/kernel/types.go` | 给 `DataPolicy` 加 `Effect string`（唯一改动的已发代码） | 1 |
| `errors.go` | 哨兵错误 `ErrInvalidPolicy`/`ErrMissingVar` | 2 |
| `condition.go` | 条件树 `Condition` + 算子常量 + `parseCondition`（结构/字段名/算子/元数校验） | 2 |
| `table.go` | `Table`（实现 `kernel.DataPolicyApplier`，apply 时解析/中毒、按 resource 索引、RWMutex） | 3 |
| `filter.go` | `RoleResolver` 接口 + `Filter` + `buildPlan` 流水线 + 变量解析 + `FilterRaw` | 4 |
| `render_sql.go` | `SQLResult` + `FilterSQL`（参数化 SQL 渲染） | 5 |

每个 `*_test.go` 与被测文件同包（`package dataperm`，白盒）。

---

## 任务 1：内核 DataPolicy 加 Effect 字段

**文件：**
- 修改：`internal/sidecar/kernel/types.go`
- 测试：`internal/sidecar/kernel/engine_test.go`（追加用例）

- [ ] **步骤 1：编写失败的测试**（追加到 `internal/sidecar/kernel/engine_test.go`）

```go
func TestEngine_ApplySnapshot_RoutesDataPolicyEffect(t *testing.T) {
	spy := &spyApplier{}
	e, _ := New("dom1", nil, spy)
	require.NoError(t, e.ApplySnapshot(Snapshot{Version: 1, DataPolicies: []DataPolicy{
		{ID: 1, SubjectType: "role", SubjectID: "manager", Resource: "order", Condition: "{}", Effect: "deny"},
	}}))
	require.Len(t, spy.snapshots, 1)
	require.Equal(t, "deny", spy.snapshots[0][0].Effect)
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/sidecar/kernel/ -run TestEngine_ApplySnapshot_RoutesDataPolicyEffect -v`
预期：FAIL（`DataPolicy` 无 `Effect` 字段，编译错误）。

- [ ] **步骤 3：编写实现**（修改 `internal/sidecar/kernel/types.go` 的 `DataPolicy`）

把：
```go
// DataPolicy 是一条数据权限规则（Condition 为不透明 JSON 串，求值归 ④-2）。
type DataPolicy struct {
	ID          uint64
	SubjectType string
	SubjectID   string
	Resource    string
	Condition   string
}
```
改为（仅追加最后一行字段）：
```go
// DataPolicy 是一条数据权限规则（Condition 为不透明 JSON 串，求值归 ④-2）。
type DataPolicy struct {
	ID          uint64
	SubjectType string
	SubjectID   string
	Resource    string
	Condition   string
	Effect      string // "allow" | "deny"；空串按 "allow"（对齐 DB 默认）。内核不解读，仅透传给 applier。
}
```

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/sidecar/kernel/ -count=1`
预期：PASS（新用例 + 全部既有用例，加字段不破已有构造）。

- [ ] **步骤 5：Commit**

```bash
git add internal/sidecar/kernel/types.go internal/sidecar/kernel/engine_test.go
git commit -m "feat(sidecar/kernel): DataPolicy 加 Effect 字段（供 ④-2 allow/deny，内核仅透传）"
```

---

## 任务 2：哨兵错误 + 条件树解析（errors.go / condition.go）

**文件：**
- 创建：`internal/sidecar/dataperm/errors.go`、`internal/sidecar/dataperm/condition.go`
- 测试：`internal/sidecar/dataperm/condition_test.go`

- [ ] **步骤 1：编写失败的测试**

`internal/sidecar/dataperm/condition_test.go`：
```go
package dataperm

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseCondition_ValidTree(t *testing.T) {
	c, err := parseCondition(`{"op":"AND","children":[
		{"field":"department","op":"EQ","value":"$user.department"},
		{"field":"status","op":"IN","value":["pending","approved"]}
	]}`)
	require.NoError(t, err)
	require.Equal(t, OpAnd, c.Op)
	require.Len(t, c.Children, 2)
	require.Equal(t, "department", c.Children[0].Field)
}

func TestParseCondition_RejectsBadField(t *testing.T) {
	_, err := parseCondition(`{"field":"dept; DROP TABLE","op":"EQ","value":"x"}`)
	require.ErrorIs(t, err, ErrInvalidPolicy)
}

func TestParseCondition_RejectsUnknownOp(t *testing.T) {
	_, err := parseCondition(`{"field":"a","op":"REGEX","value":"x"}`)
	require.ErrorIs(t, err, ErrInvalidPolicy)
}

func TestParseCondition_RejectsArityMismatch(t *testing.T) {
	_, err := parseCondition(`{"field":"a","op":"IN","value":"notarray"}`)
	require.ErrorIs(t, err, ErrInvalidPolicy)
	_, err = parseCondition(`{"field":"a","op":"BETWEEN","value":[1]}`)
	require.ErrorIs(t, err, ErrInvalidPolicy)
	_, err = parseCondition(`{"op":"NOT","children":[]}`)
	require.ErrorIs(t, err, ErrInvalidPolicy)
}

func TestParseCondition_RejectsBadJSON(t *testing.T) {
	_, err := parseCondition(`{not json`)
	require.ErrorIs(t, err, ErrInvalidPolicy)
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/sidecar/dataperm/ -run TestParseCondition -v`
预期：FAIL（`parseCondition`/`ErrInvalidPolicy`/`OpAnd` 未定义）。

- [ ] **步骤 3：编写实现**

`internal/sidecar/dataperm/errors.go`：
```go
package dataperm

import "errors"

var (
	// ErrInvalidPolicy 表示策略条件树非法（JSON/字段名/算子/元数/effect），命中时 fail-close 拒绝。
	ErrInvalidPolicy = errors.New("dataperm: invalid data policy condition")
	// ErrMissingVar 表示条件引用的 $user.xxx 在请求 userAttrs 中缺失，fail-close 拒绝。
	ErrMissingVar = errors.New("dataperm: missing user attribute for runtime variable")
)
```

`internal/sidecar/dataperm/condition.go`：
```go
package dataperm

import (
	"encoding/json"
	"fmt"
	"regexp"
)

// 逻辑算子。
const (
	OpAnd = "AND"
	OpOr  = "OR"
	OpNot = "NOT"
)

// 叶子算子。
const (
	OpEQ        = "EQ"
	OpNE        = "NE"
	OpGT        = "GT"
	OpGE        = "GE"
	OpLT        = "LT"
	OpLE        = "LE"
	OpIN        = "IN"
	OpNotIn     = "NOT_IN"
	OpLike      = "LIKE"
	OpNotLike   = "NOT_LIKE"
	OpIsNull    = "IS_NULL"
	OpIsNotNull = "IS_NOT_NULL"
	OpBetween   = "BETWEEN"
)

// fieldNameRe 是字段名白名单：字段进 SQL 文本而非参数，必须为合法标识符（堵注入）。
var fieldNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Condition 是条件树节点：逻辑节点（Op∈{AND,OR,NOT} + Children）或叶子（Field+Op+Value）。
type Condition struct {
	Op       string       `json:"op"`
	Children []*Condition `json:"children,omitempty"`
	Field    string       `json:"field,omitempty"`
	Value    any          `json:"value,omitempty"`
}

func isLogicalOp(op string) bool { return op == OpAnd || op == OpOr || op == OpNot }

func isLeafOp(op string) bool {
	switch op {
	case OpEQ, OpNE, OpGT, OpGE, OpLT, OpLE, OpIN, OpNotIn, OpLike, OpNotLike, OpIsNull, OpIsNotNull, OpBetween:
		return true
	}
	return false
}

// parseCondition 从不透明 JSON 解析并校验整棵树（fail-close）。
func parseCondition(raw string) (*Condition, error) {
	var c Condition
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return nil, fmt.Errorf("%w: 非法 JSON: %v", ErrInvalidPolicy, err)
	}
	if err := validate(&c); err != nil {
		return nil, err
	}
	return &c, nil
}

func validate(c *Condition) error {
	switch {
	case isLogicalOp(c.Op):
		if c.Op == OpNot {
			if len(c.Children) != 1 {
				return fmt.Errorf("%w: NOT 必须恰好 1 个子节点", ErrInvalidPolicy)
			}
		} else if len(c.Children) == 0 {
			return fmt.Errorf("%w: %s 至少 1 个子节点", ErrInvalidPolicy, c.Op)
		}
		for _, ch := range c.Children {
			if err := validate(ch); err != nil {
				return err
			}
		}
		return nil
	case isLeafOp(c.Op):
		return validateLeaf(c)
	default:
		return fmt.Errorf("%w: 未知算子 %q", ErrInvalidPolicy, c.Op)
	}
}

func validateLeaf(c *Condition) error {
	if !fieldNameRe.MatchString(c.Field) {
		return fmt.Errorf("%w: 非法字段名 %q", ErrInvalidPolicy, c.Field)
	}
	switch c.Op {
	case OpIsNull, OpIsNotNull:
		if c.Value != nil {
			return fmt.Errorf("%w: %s 不应带 value", ErrInvalidPolicy, c.Op)
		}
	case OpIN, OpNotIn:
		arr, ok := c.Value.([]any)
		if !ok || len(arr) == 0 {
			return fmt.Errorf("%w: %s 需非空数组 value", ErrInvalidPolicy, c.Op)
		}
	case OpBetween:
		arr, ok := c.Value.([]any)
		if !ok || len(arr) != 2 {
			return fmt.Errorf("%w: BETWEEN 需 2 元数组 value", ErrInvalidPolicy)
		}
	default: // 标量比较 / LIKE
		if c.Value == nil {
			return fmt.Errorf("%w: %s 需 value", ErrInvalidPolicy, c.Op)
		}
		if _, isArr := c.Value.([]any); isArr {
			return fmt.Errorf("%w: %s value 不应为数组", ErrInvalidPolicy, c.Op)
		}
	}
	return nil
}
```

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/sidecar/dataperm/ -run TestParseCondition -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/sidecar/dataperm/errors.go internal/sidecar/dataperm/condition.go internal/sidecar/dataperm/condition_test.go
git commit -m "feat(sidecar/dataperm): 条件树解析 + 字段名白名单/算子/元数校验 + 哨兵错误"
```

---

## 任务 3：内存表 Table（table.go）

**文件：**
- 创建：`internal/sidecar/dataperm/table.go`
- 测试：`internal/sidecar/dataperm/table_test.go`

- [ ] **步骤 1：编写失败的测试**

`internal/sidecar/dataperm/table_test.go`：
```go
package dataperm

import (
	"testing"

	"github.com/nickZFZ/Sydom/internal/sidecar/kernel"
	"github.com/stretchr/testify/require"
)

// dp 是构造 kernel.DataPolicy 的测试助手（本包内复用）。
func dp(id uint64, stype, sid, res, cond, eff string) kernel.DataPolicy {
	return kernel.DataPolicy{ID: id, SubjectType: stype, SubjectID: sid, Resource: res, Condition: cond, Effect: eff}
}

func TestTable_ImplementsApplier(t *testing.T) {
	var _ kernel.DataPolicyApplier = (*Table)(nil)
}

func TestTable_ApplySnapshot_IndexesByResource(t *testing.T) {
	tbl := NewTable()
	tbl.ApplySnapshot([]kernel.DataPolicy{
		dp(1, "role", "manager", "order", `{"field":"a","op":"EQ","value":1}`, "allow"),
		dp(2, "user", "alice", "invoice", `{"field":"b","op":"EQ","value":2}`, "allow"),
	})
	got, ok := tbl.Lookup("order")
	require.True(t, ok)
	require.Len(t, got, 1)
	require.Equal(t, "manager", got[0].subjectID)
	_, ok = tbl.Lookup("nope")
	require.False(t, ok)
}

func TestTable_ApplyChange_AddUpdateRemove(t *testing.T) {
	tbl := NewTable()
	tbl.ApplyChange(kernel.ChangeAdd, dp(1, "role", "manager", "order", `{"field":"a","op":"EQ","value":1}`, "allow"))
	got, ok := tbl.Lookup("order")
	require.True(t, ok)
	require.Len(t, got, 1)

	tbl.ApplyChange(kernel.ChangeUpdate, dp(1, "role", "manager", "order", `{"field":"a","op":"EQ","value":2}`, "deny"))
	got, _ = tbl.Lookup("order")
	require.Len(t, got, 1)
	require.Equal(t, "deny", got[0].effect)

	tbl.ApplyChange(kernel.ChangeRemove, dp(1, "role", "manager", "order", `{}`, "allow"))
	_, ok = tbl.Lookup("order")
	require.False(t, ok, "移除最后一条后 resource 回到未配置")
}

func TestTable_PoisonsBadPolicy(t *testing.T) {
	tbl := NewTable()
	tbl.ApplySnapshot([]kernel.DataPolicy{
		dp(1, "role", "manager", "order", `{bad json`, "allow"),
		dp(2, "role", "manager", "order", `{"field":"a","op":"EQ","value":1}`, "weird"),
	})
	got, _ := tbl.Lookup("order")
	require.Len(t, got, 2)
	require.Error(t, got[0].parseErr, "非法 JSON 应中毒")
	require.Error(t, got[1].parseErr, "非法 effect 应中毒")
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/sidecar/dataperm/ -run TestTable -v`
预期：FAIL（`Table`/`NewTable` 未定义）。

- [ ] **步骤 3：编写实现**

`internal/sidecar/dataperm/table.go`：
```go
package dataperm

import (
	"fmt"
	"sync"

	"github.com/nickZFZ/Sydom/internal/sidecar/kernel"
)

// 主体类型（与控制面 data_policy.subject_type 对齐）。
const (
	SubjectUser = "user"
	SubjectRole = "role"
)

// stored 是一条已解析（或中毒）的数据策略。
type stored struct {
	id          uint64
	subjectType string
	subjectID   string
	effect      string     // 归一化后 "allow"/"deny"（中毒时为空）
	cond        *Condition // 解析成功时的条件树
	parseErr    error      // 中毒时的解析错误（命中即 fail-close）
}

// Table 是内存 DataPolicy 表，实现 kernel.DataPolicyApplier。
// apply 时解析条件树（解析一次、求值多次）；按 resource 索引；读写用 RWMutex。
type Table struct {
	mu    sync.RWMutex
	byRes map[string][]stored
}

func NewTable() *Table {
	return &Table{byRes: make(map[string][]stored)}
}

func normalizeEffect(e string) (string, bool) {
	switch e {
	case "", "allow":
		return "allow", true
	case "deny":
		return "deny", true
	default:
		return "", false
	}
}

// toStored 解析一条策略为 stored；effect/condition 任一非法即标记中毒。
func toStored(p kernel.DataPolicy) stored {
	s := stored{id: p.ID, subjectType: p.SubjectType, subjectID: p.SubjectID}
	eff, ok := normalizeEffect(p.Effect)
	if !ok {
		s.parseErr = fmt.Errorf("%w: 未知 effect %q", ErrInvalidPolicy, p.Effect)
		return s
	}
	s.effect = eff
	cond, err := parseCondition(p.Condition)
	if err != nil {
		s.parseErr = err
		return s
	}
	s.cond = cond
	return s
}

// ApplySnapshot 全量重建内存表。
func (t *Table) ApplySnapshot(policies []kernel.DataPolicy) {
	next := make(map[string][]stored, len(policies))
	for _, p := range policies {
		next[p.Resource] = append(next[p.Resource], toStored(p))
	}
	t.mu.Lock()
	t.byRes = next
	t.mu.Unlock()
}

// ApplyChange 增量改表：add 追加；remove 按 ID 删；update = 删旧 + 加新。
func (t *Table) ApplyChange(op kernel.ChangeOp, p kernel.DataPolicy) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch op {
	case kernel.ChangeAdd:
		t.byRes[p.Resource] = append(t.byRes[p.Resource], toStored(p))
	case kernel.ChangeRemove:
		t.removeLocked(p.Resource, p.ID)
	case kernel.ChangeUpdate:
		t.removeLocked(p.Resource, p.ID)
		t.byRes[p.Resource] = append(t.byRes[p.Resource], toStored(p))
	}
}

// removeLocked 删某 resource 桶里的指定 ID；桶空则删 key（维持「未配置」语义）。
func (t *Table) removeLocked(resource string, id uint64) {
	bucket := t.byRes[resource]
	for i, s := range bucket {
		if s.id == id {
			bucket = append(bucket[:i], bucket[i+1:]...)
			break
		}
	}
	if len(bucket) == 0 {
		delete(t.byRes, resource)
		return
	}
	t.byRes[resource] = bucket
}

// Lookup 返回某 resource 的全部策略（副本）与「是否已配置」（key 是否存在）。
func (t *Table) Lookup(resource string) ([]stored, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	bucket, ok := t.byRes[resource]
	if !ok {
		return nil, false
	}
	out := make([]stored, len(bucket))
	copy(out, bucket)
	return out, true
}

var _ kernel.DataPolicyApplier = (*Table)(nil)
```

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/sidecar/dataperm/ -run TestTable -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/sidecar/dataperm/table.go internal/sidecar/dataperm/table_test.go
git commit -m "feat(sidecar/dataperm): 内存 DataPolicy 表（实现 applier，解析即存/中毒，按 resource 索引）"
```

---

## 任务 4：Filter 流水线 + 变量解析 + FilterRaw（filter.go）

**文件：**
- 创建：`internal/sidecar/dataperm/filter.go`
- 测试：`internal/sidecar/dataperm/filter_test.go`

- [ ] **步骤 1：编写失败的测试**

`internal/sidecar/dataperm/filter_test.go`：
```go
package dataperm

import (
	"testing"

	"github.com/nickZFZ/Sydom/internal/sidecar/kernel"
	"github.com/stretchr/testify/require"
)

// fakeRoles 是测试用 RoleResolver。
type fakeRoles struct {
	roles map[string][]string
	err   error
}

func (f fakeRoles) GetImplicitRolesForUser(user, dom string) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.roles[user], nil
}

// newFilter 用快照构造一个挂 fakeRoles 的 Filter。
func newFilter(roles map[string][]string, pols ...kernel.DataPolicy) *Filter {
	tbl := NewTable()
	tbl.ApplySnapshot(pols)
	return NewFilter(fakeRoles{roles: roles}, tbl)
}

func TestFilterRaw_Unconfigured_NoRestriction(t *testing.T) {
	f := newFilter(map[string][]string{"alice": {"manager"}})
	res, err := f.FilterRaw("alice", "dom1", "order", nil)
	require.NoError(t, err)
	require.Equal(t, "all", res.Match)
}

func TestFilterRaw_ConfiguredNoMatch_DenyAll(t *testing.T) {
	f := newFilter(map[string][]string{"alice": {"viewer"}},
		dp(1, "role", "manager", "order", `{"field":"a","op":"EQ","value":1}`, "allow"))
	res, err := f.FilterRaw("alice", "dom1", "order", nil)
	require.NoError(t, err)
	require.Equal(t, "none", res.Match)
}

func TestFilterRaw_DenyOverrides(t *testing.T) {
	f := newFilter(map[string][]string{"alice": {"manager"}},
		dp(1, "role", "manager", "order", `{"field":"dept","op":"EQ","value":"HR"}`, "allow"),
		dp(2, "role", "manager", "order", `{"field":"status","op":"EQ","value":"locked"}`, "deny"))
	res, err := f.FilterRaw("alice", "dom1", "order", nil)
	require.NoError(t, err)
	require.Equal(t, "conditional", res.Match)
	require.Equal(t, OpAnd, res.Tree.Op)
	require.Equal(t, OpNot, res.Tree.Children[1].Op)
}

func TestFilter_MissingVar_FailClose(t *testing.T) {
	f := newFilter(map[string][]string{"alice": {"manager"}},
		dp(1, "role", "manager", "order", `{"field":"dept","op":"EQ","value":"$user.department"}`, "allow"))
	_, err := f.FilterRaw("alice", "dom1", "order", map[string]any{})
	require.ErrorIs(t, err, ErrMissingVar)
}

func TestFilter_PoisonHit_FailClose(t *testing.T) {
	f := newFilter(map[string][]string{"alice": {"manager"}},
		dp(1, "role", "manager", "order", `{bad`, "allow"))
	_, err := f.FilterRaw("alice", "dom1", "order", nil)
	require.ErrorIs(t, err, ErrInvalidPolicy)
}

func TestFilter_ResolverError_FailClose(t *testing.T) {
	tbl := NewTable()
	tbl.ApplySnapshot([]kernel.DataPolicy{dp(1, "role", "manager", "order", `{"field":"a","op":"EQ","value":1}`, "allow")})
	f := NewFilter(fakeRoles{err: kernel.ErrNotReady}, tbl)
	_, err := f.FilterRaw("alice", "dom1", "order", nil)
	require.ErrorIs(t, err, kernel.ErrNotReady)
}

func TestFilter_UserSubjectMatch(t *testing.T) {
	f := newFilter(map[string][]string{"alice": {}},
		dp(1, "user", "alice", "order", `{"field":"owner","op":"EQ","value":"$user.id"}`, "allow"))
	res, err := f.FilterRaw("alice", "dom1", "order", map[string]any{"id": "alice"})
	require.NoError(t, err)
	require.Equal(t, "conditional", res.Match)
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/sidecar/dataperm/ -run TestFilter -v`
预期：FAIL（`Filter`/`NewFilter`/`RoleResolver`/`RawResult` 未定义）。

- [ ] **步骤 3：编写实现**

`internal/sidecar/dataperm/filter.go`：
```go
package dataperm

import (
	"fmt"
	"strings"
)

// RoleResolver 把用户展开为隐式角色集（含继承）。*kernel.Engine 满足之。
type RoleResolver interface {
	GetImplicitRolesForUser(user, dom string) ([]string, error)
}

// Filter 是无状态查询/渲染编排器。
type Filter struct {
	roles RoleResolver
	table *Table
}

func NewFilter(roles RoleResolver, table *Table) *Filter {
	return &Filter{roles: roles, table: table}
}

// planMode 区分三种渲染前结果。
type planMode int

const (
	modeNoFilter planMode = iota // resource 未配置 → 无行过滤
	modeDenyAll                  // 配了但无 allow 命中 → 1=0
	modeTree                     // 有合并树（变量已解析）
)

type plan struct {
	mode planMode
	tree *Condition // 仅 modeTree
}

// buildPlan 跑规格 §5 流水线（除最终方言渲染外）：
// tier-1 守卫 → 主体展开 → 收集拆分 allow/deny → 空 allow 守卫 → 合并 → 变量解析。
func (f *Filter) buildPlan(user, dom, resource string, attrs map[string]any) (plan, error) {
	bucket, configured := f.table.Lookup(resource)
	if !configured {
		return plan{mode: modeNoFilter}, nil
	}
	roles, err := f.roles.GetImplicitRolesForUser(user, dom)
	if err != nil {
		return plan{}, err // fail-close 透传（含 ErrNotReady/ErrForeignDomain）
	}
	roleSet := make(map[string]struct{}, len(roles))
	for _, r := range roles {
		roleSet[r] = struct{}{}
	}

	var allow, deny []*Condition
	for _, s := range bucket {
		if !subjectMatches(s, user, roleSet) {
			continue
		}
		if s.parseErr != nil {
			return plan{}, s.parseErr // 命中中毒策略 → fail-close（绝不静默丢，丢 deny 会扩权）
		}
		resolved, err := resolveVars(s.cond, attrs)
		if err != nil {
			return plan{}, err // ErrMissingVar
		}
		if s.effect == "deny" {
			deny = append(deny, resolved)
		} else {
			allow = append(allow, resolved)
		}
	}
	if len(allow) == 0 {
		return plan{mode: modeDenyAll}, nil
	}
	merged := orAll(allow)
	if len(deny) > 0 {
		merged = &Condition{Op: OpAnd, Children: []*Condition{
			merged,
			{Op: OpNot, Children: []*Condition{orAll(deny)}},
		}}
	}
	return plan{mode: modeTree, tree: merged}, nil
}

// subjectMatches 判定一条策略的主体是否落在请求用户的主体集内。
func subjectMatches(s stored, user string, roleSet map[string]struct{}) bool {
	switch s.subjectType {
	case SubjectUser:
		return s.subjectID == user
	case SubjectRole:
		_, ok := roleSet[s.subjectID]
		return ok
	default:
		return false // 未知主体类型 inert（既不 allow 也不 deny）
	}
}

// orAll 把多个条件折叠为 OR（单个直接返回，避免冗余 OR 包裹）。
func orAll(cs []*Condition) *Condition {
	if len(cs) == 1 {
		return cs[0]
	}
	return &Condition{Op: OpOr, Children: cs}
}

// resolveVars 深拷贝条件树并把叶子里的 $user.xxx 解析为 attrs 的具体值（缺键→ErrMissingVar）。
func resolveVars(c *Condition, attrs map[string]any) (*Condition, error) {
	if isLogicalOp(c.Op) {
		children := make([]*Condition, len(c.Children))
		for i, ch := range c.Children {
			rc, err := resolveVars(ch, attrs)
			if err != nil {
				return nil, err
			}
			children[i] = rc
		}
		return &Condition{Op: c.Op, Children: children}, nil
	}
	val, err := resolveValue(c.Value, attrs)
	if err != nil {
		return nil, err
	}
	return &Condition{Op: c.Op, Field: c.Field, Value: val}, nil
}

// resolveValue 把 "$user.xxx" 解析为 attrs 值（数组逐元素解析；缺键→ErrMissingVar）。
func resolveValue(v any, attrs map[string]any) (any, error) {
	switch tv := v.(type) {
	case string:
		if name, ok := userVarName(tv); ok {
			val, present := attrs[name]
			if !present {
				return nil, fmt.Errorf("%w: $user.%s", ErrMissingVar, name)
			}
			return val, nil
		}
		return tv, nil
	case []any:
		out := make([]any, len(tv))
		for i, e := range tv {
			rv, err := resolveValue(e, attrs)
			if err != nil {
				return nil, err
			}
			out[i] = rv
		}
		return out, nil
	default:
		return v, nil // number/bool/nil
	}
}

// userVarName 识别 "$user.xxx" 并返回 xxx。
func userVarName(s string) (string, bool) {
	const prefix = "$user."
	if strings.HasPrefix(s, prefix) {
		return s[len(prefix):], true
	}
	return "", false
}

// RawResult 是 raw 方言结果：Match 表达整体语义，Tree 为变量已解析的合并树（仅 conditional）。
type RawResult struct {
	Match string     // "all"（无限制）| "none"（deny-all）| "conditional"
	Tree  *Condition // 仅 Match=="conditional"
}

// FilterRaw 返回合并后的条件树（变量已解析），交 SDK 自渲染参数化语句。
func (f *Filter) FilterRaw(user, dom, resource string, attrs map[string]any) (RawResult, error) {
	p, err := f.buildPlan(user, dom, resource, attrs)
	if err != nil {
		return RawResult{}, err
	}
	switch p.mode {
	case modeNoFilter:
		return RawResult{Match: "all"}, nil
	case modeDenyAll:
		return RawResult{Match: "none"}, nil
	default:
		return RawResult{Match: "conditional", Tree: p.tree}, nil
	}
}
```

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/sidecar/dataperm/ -run TestFilter -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/sidecar/dataperm/filter.go internal/sidecar/dataperm/filter_test.go
git commit -m "feat(sidecar/dataperm): Filter 流水线（主体展开+allow/deny 合并+变量解析）+ FilterRaw + fail-close"
```

---

## 任务 5：SQL 渲染 + FilterSQL（render_sql.go）

**文件：**
- 创建：`internal/sidecar/dataperm/render_sql.go`
- 测试：`internal/sidecar/dataperm/render_sql_test.go`

- [ ] **步骤 1：编写失败的测试**

`internal/sidecar/dataperm/render_sql_test.go`：
```go
package dataperm

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFilterSQL_Unconfigured_Empty(t *testing.T) {
	f := newFilter(map[string][]string{"alice": {"manager"}})
	res, err := f.FilterSQL("alice", "dom1", "order", nil)
	require.NoError(t, err)
	require.Equal(t, "", res.SQL)
	require.Empty(t, res.Args)
}

func TestFilterSQL_DenyAll(t *testing.T) {
	f := newFilter(map[string][]string{"alice": {"viewer"}},
		dp(1, "role", "manager", "order", `{"field":"a","op":"EQ","value":1}`, "allow"))
	res, err := f.FilterSQL("alice", "dom1", "order", nil)
	require.NoError(t, err)
	require.Equal(t, "1=0", res.SQL)
}

func TestFilterSQL_ParamizedAndDenyOverrides(t *testing.T) {
	f := newFilter(map[string][]string{"alice": {"manager"}},
		dp(1, "role", "manager", "order", `{"field":"dept","op":"EQ","value":"$user.department"}`, "allow"),
		dp(2, "role", "manager", "order", `{"field":"status","op":"IN","value":["locked","void"]}`, "deny"))
	res, err := f.FilterSQL("alice", "dom1", "order", map[string]any{"department": "HR"})
	require.NoError(t, err)
	require.Equal(t, "(dept = ? AND NOT (status IN (?, ?)))", res.SQL)
	require.Equal(t, []any{"HR", "locked", "void"}, res.Args)
}

func TestFilterSQL_Operators(t *testing.T) {
	f := newFilter(map[string][]string{"alice": {"manager"}},
		dp(1, "role", "manager", "order", `{"op":"OR","children":[
			{"field":"amount","op":"BETWEEN","value":[10,20]},
			{"field":"note","op":"IS_NULL"}
		]}`, "allow"))
	res, err := f.FilterSQL("alice", "dom1", "order", nil)
	require.NoError(t, err)
	require.Equal(t, "(amount BETWEEN ? AND ? OR note IS NULL)", res.SQL)
	require.Equal(t, []any{float64(10), float64(20)}, res.Args)
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/sidecar/dataperm/ -run TestFilterSQL -v`
预期：FAIL（`FilterSQL`/`SQLResult` 未定义）。

- [ ] **步骤 3：编写实现**

`internal/sidecar/dataperm/render_sql.go`：
```go
package dataperm

import (
	"fmt"
	"strings"
)

// SQLResult 是 sql 方言结果：参数化模板 + 参数（值绝不进 SQL 文本）。
type SQLResult struct {
	SQL  string
	Args []any
}

// FilterSQL 渲染参数化 SQL 片段。无过滤→空串；deny-all→"1=0"；否则参数化条件。
func (f *Filter) FilterSQL(user, dom, resource string, attrs map[string]any) (SQLResult, error) {
	p, err := f.buildPlan(user, dom, resource, attrs)
	if err != nil {
		return SQLResult{}, err
	}
	switch p.mode {
	case modeNoFilter:
		return SQLResult{}, nil
	case modeDenyAll:
		return SQLResult{SQL: "1=0"}, nil
	default:
		var b strings.Builder
		var args []any
		renderSQL(p.tree, &b, &args)
		return SQLResult{SQL: b.String(), Args: args}, nil
	}
}

// renderSQL 递归渲染已解析条件树为参数化 SQL。字段名已在解析期白名单校验，可安全内联。
func renderSQL(c *Condition, b *strings.Builder, args *[]any) {
	switch c.Op {
	case OpAnd, OpOr:
		sep := " AND "
		if c.Op == OpOr {
			sep = " OR "
		}
		b.WriteByte('(')
		for i, ch := range c.Children {
			if i > 0 {
				b.WriteString(sep)
			}
			renderSQL(ch, b, args)
		}
		b.WriteByte(')')
	case OpNot:
		b.WriteString("NOT (")
		renderSQL(c.Children[0], b, args)
		b.WriteByte(')')
	default:
		renderLeaf(c, b, args)
	}
}

func renderLeaf(c *Condition, b *strings.Builder, args *[]any) {
	switch c.Op {
	case OpIsNull:
		fmt.Fprintf(b, "%s IS NULL", c.Field)
	case OpIsNotNull:
		fmt.Fprintf(b, "%s IS NOT NULL", c.Field)
	case OpIN, OpNotIn:
		arr := c.Value.([]any)
		kw := "IN"
		if c.Op == OpNotIn {
			kw = "NOT IN"
		}
		ph := make([]string, len(arr))
		for i := range arr {
			ph[i] = "?"
			*args = append(*args, arr[i])
		}
		fmt.Fprintf(b, "%s %s (%s)", c.Field, kw, strings.Join(ph, ", "))
	case OpBetween:
		arr := c.Value.([]any)
		fmt.Fprintf(b, "%s BETWEEN ? AND ?", c.Field)
		*args = append(*args, arr[0], arr[1])
	default: // 标量比较 / LIKE
		fmt.Fprintf(b, "%s %s ?", c.Field, sqlComparator(c.Op))
		*args = append(*args, c.Value)
	}
}

func sqlComparator(op string) string {
	switch op {
	case OpEQ:
		return "="
	case OpNE:
		return "<>"
	case OpGT:
		return ">"
	case OpGE:
		return ">="
	case OpLT:
		return "<"
	case OpLE:
		return "<="
	case OpLike:
		return "LIKE"
	case OpNotLike:
		return "NOT LIKE"
	default:
		return "="
	}
}
```

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/sidecar/dataperm/ -run TestFilterSQL -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/sidecar/dataperm/render_sql.go internal/sidecar/dataperm/render_sql_test.go
git commit -m "feat(sidecar/dataperm): SQL 参数化渲染 + FilterSQL（无过滤/1=0/参数化条件）"
```

---

## 收尾：全量验证

- [ ] 运行全部 dataperm + kernel 测试 + race + 静态检查：

```bash
go build ./...
go test ./internal/sidecar/dataperm/ ./internal/sidecar/kernel/ -v -count=1
go test ./internal/sidecar/dataperm/ -race -count=1   # 表读(Filter)/写(apply)并发，跑 race
go vet ./internal/sidecar/dataperm/
gofmt -l internal/sidecar/dataperm/                    # 应无输出（注意 CJK 尾注释对齐）
```
预期：全 PASS、race 干净、gofmt 干净。

- [ ] 完成后用 `superpowers:finishing-a-development-branch` 收尾分支。

---

## 自检结果

- **规格覆盖**：§3 包结构→任务 2-5 全覆盖（errors/condition/table/filter/render_sql）；§4.1 条件树+算子→任务 2；§4.2 Effect 字段→任务 1；§5 流水线（tier-1/主体展开/拆分/空allow/合并/变量解析）→任务 4；sql 渲染→任务 5；§6 fail-close 矩阵→任务 2(解析中毒)/3(effect 中毒)/4(resolver 错/中毒命中/缺变量)/5(无过滤/1=0)；§7 主体解析共享角色图→任务 4(`subjectMatches` + `RoleResolver`)；§8 测试用例分布任务 2-5。
- **占位符扫描**：无 TODO/待定；每步含完整可编译代码。
- **类型一致性**：`Condition`/`Op*` 常量、`parseCondition`/`validate`/`validateLeaf`、`Table`/`NewTable`/`stored`/`toStored`/`Lookup`/`ApplySnapshot`/`ApplyChange`、`Filter`/`NewFilter`/`RoleResolver`/`buildPlan`/`plan`/`planMode`(`modeNoFilter`/`modeDenyAll`/`modeTree`)/`subjectMatches`/`orAll`/`resolveVars`/`resolveValue`/`userVarName`、`RawResult`/`FilterRaw`、`SQLResult`/`FilterSQL`/`renderSQL`/`renderLeaf`/`sqlComparator`、测试助手 `dp`(任务 3 定义,任务 4/5 复用)/`fakeRoles`/`newFilter`(任务 4 定义,任务 5 复用) 跨任务一致。`SubjectUser`/`SubjectRole` 常量与 `subjectMatches` 一致。
- **范围说明**：与 spec §6 不同处——因采用显式 `FilterSQL`/`FilterRaw` 双方法（设计 Q&A 确认）而非 dialect 枚举，无运行时 dialect 分派，故 `ErrUnsupportedDialect` 不需要，计划只保留 `ErrInvalidPolicy`/`ErrMissingVar`。
- **依赖说明**：任务 4/5 测试用 `kernel.ErrNotReady`（已导出）；`dp` 助手在任务 3 的 `table_test.go` 定义、被任务 4/5 同包复用；`newFilter`/`fakeRoles` 在任务 4 的 `filter_test.go` 定义、被任务 5 复用。

相关：规格 `2026-06-04-sydom-sidecar-data-policy-engine-design.md`；[[feedback-consistency-over-simplicity]]、[[feedback-verify-casbin-before-asserting]]。
