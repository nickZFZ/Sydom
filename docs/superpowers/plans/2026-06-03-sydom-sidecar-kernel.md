# Sidecar 鉴权内核（④-1）实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 实现 `internal/sidecar/kernel` 包——单 app（单 casbin domain）的纯内存功能权限内核 `Engine`，封装 casbin 装配 + 原子 apply（snapshot/delta）+ 缓存铁律 + fail-close。

**架构：** `SyncedCachedEnforcer`（决策缓存）+ 只读 `memoryAdapter`（autoSave=false）+ 有界 LRU（SetCache 注入）。`Engine` 拥有 apply 编排：每条 snapshot/delta 原子改 casbin 内存模型 + 路由数据策略给注入的 `DataPolicyApplier` + 全量清缓存 + 记统一版本号。fail-close：未就绪/越域/出错一律拒绝。纯内存、纯单测、不出 binary。

**技术栈：** Go 1.26 + casbin v3.10.0（`github.com/casbin/casbin/v3` 及 `/model`、`/persist`、`/persist/cache`，已在 go.mod）。测试用 testify。

**规格：** `docs/superpowers/specs/2026-06-03-sydom-sidecar-kernel-design.md`。

---

## 已回源核实的 casbin API（实现者请直接据此写，勿臆测；如需复核见 `casbin/` 克隆）

- `model.NewModelFromString(text string) (model.Model, error)`（`model/model.go:151`）。
- `casbin.NewSyncedCachedEnforcer(params ...interface{}) (*SyncedCachedEnforcer, error)`（`enforcer_cached_synced.go:35`）——传 `(model.Model, persist.Adapter)`，构造时自动 `LoadPolicy` 一次（空 adapter 即加载空）。
- `*SyncedCachedEnforcer` 方法：`Enforce(rvals ...interface{}) (bool, error)`、`SetCache(c cache.Cache)`、`InvalidateCache() error`、`EnableCache(bool)`（默认已开）。
- 经内嵌 `*SyncedEnforcer`/`*Enforcer` 提升、**且 SyncedEnforcer 均重写带 RWMutex 锁**（并发安全，apply 直接调即可，**勿手动持锁**）：`ClearPolicy()`、`AddNamedPolicies(ptype string, rules [][]string) (bool, error)`、`RemoveNamedPolicies(ptype, rules)`、`AddNamedGroupingPolicies(ptype, rules)`、`RemoveNamedGroupingPolicies(ptype, rules)`、`BatchEnforce(requests [][]interface{}) ([]bool, error)`、`GetLock() *sync.RWMutex`。
- 经内嵌 `*Enforcer` 提升（**SyncedEnforcer 未重写、无锁**）：`EnableAutoSave(bool)`、`EnableAutoNotifyWatcher(bool)`、`GetImplicitRolesForUser(name string, domain ...string) ([]string, error)`（`rbac_api.go:233`）——读角色图，需用 `GetLock().RLock()` 自行加读锁。
- **段区分（关键）**：p 段行用 `AddNamedPolicies("p",…)`；g 段行用 `AddNamedGroupingPolicies("g",…)`（`AddPolicies` 只动 p 段）。g 段高层增删自动 `BuildIncrementalRoleLinks`。
- `persist.Adapter`：`LoadPolicy(model.Model) error`、`SavePolicy(model.Model) error`、`AddPolicy(sec, ptype string, rule []string) error`、`RemovePolicy(sec, ptype string, rule []string) error`、`RemoveFilteredPolicy(sec, ptype string, fieldIndex int, fieldValues ...string) error`。
- `persist.BatchAdapter`：+ `AddPolicies(sec, ptype string, rules [][]string) error`、`RemovePolicies(sec, ptype string, rules [][]string) error`。
- `persist.FilteredAdapter`：+ `LoadFilteredPolicy(model.Model, filter interface{}) error`、`IsFiltered() bool`。
- `persist.LoadPolicyArray(rule []string, m model.Model) error`（`persist/adapter.go`）——`rule[0]` 为 ptype key，`rule[0][:1]` 为段，内部去重。
- `cache.Cache`：`Set(key string, value bool, extra ...interface{}) error`、`Get(key string) (bool, error)`、`Delete(key string) error`、`Clear() error`；缺键返回 `cache.ErrNoSuchKey`（`persist/cache/cache.go:19`）。
- **缓存论断**：`SyncedCachedEnforcer` 仅重写 `AddPolicy/AddPolicies/RemovePolicy/RemovePolicies`（按规则主体 key 删）；`AddNamedPolicies`/`AddNamedGroupingPolicies`/`ClearPolicy`/`UpdateNamedPolicy` 等**不碰缓存**——故每次 apply 后必须显式 `InvalidateCache()` 全量清（按 key 删在 RBAC 角色间接性下会漏，见规格 §8）。

---

## 文件结构

包 `internal/sidecar/kernel`，模块 `github.com/nickZFZ/Sydom`：

| 文件 | 职责 | 任务 |
|---|---|---|
| `cache.go` | 有界 LRU，实现 `persist/cache.Cache` | 1 |
| `types.go` | 域类型 `Rule`/`Delta`/`PolicyChange`/`Snapshot`/`DataPolicy`/`ChangeOp` + `DataPolicyApplier` 接口 + `noopApplier` + 行助手 | 2 |
| `errors.go` | 哨兵错误 `ErrNotReady`/`ErrForeignDomain`/`ErrStaleVersion` | 2 |
| `model.go` | 内嵌 casbin model 常量 + `buildModel()` | 3 |
| `adapter.go` | `memoryAdapter`（Adapter+BatchAdapter+FilteredAdapter，只读 no-op 写） | 4 |
| `engine.go` | `Engine`：构造 + Enforce/BatchEnforce + ApplySnapshot/ApplyDelta + Version/Ready + GetImplicitRolesForUser | 5–8 |

每个 `*_test.go` 与被测文件同名同包（`package kernel`，白盒测试，可测未导出助手）。**全部纯单测，无 testcontainers、无 Docker。**

---

## 任务 1：有界 LRU 缓存（cache.go）

**文件：**
- 创建：`internal/sidecar/kernel/cache.go`
- 测试：`internal/sidecar/kernel/cache_test.go`

- [ ] **步骤 1：编写失败的测试**

`internal/sidecar/kernel/cache_test.go`：
```go
package kernel

import (
	"testing"

	"github.com/casbin/casbin/v3/persist/cache"
	"github.com/stretchr/testify/require"
)

func TestBoundedCache_SetGetDeleteClear(t *testing.T) {
	c := newBoundedCache(8)
	_, err := c.Get("missing")
	require.ErrorIs(t, err, cache.ErrNoSuchKey)

	require.NoError(t, c.Set("k", true))
	v, err := c.Get("k")
	require.NoError(t, err)
	require.True(t, v)

	require.NoError(t, c.Set("k", false)) // 覆盖
	v, _ = c.Get("k")
	require.False(t, v)

	require.NoError(t, c.Delete("k"))
	_, err = c.Get("k")
	require.ErrorIs(t, err, cache.ErrNoSuchKey)
	require.ErrorIs(t, c.Delete("k"), cache.ErrNoSuchKey)

	require.NoError(t, c.Set("a", true))
	require.NoError(t, c.Clear())
	_, err = c.Get("a")
	require.ErrorIs(t, err, cache.ErrNoSuchKey)
}

func TestBoundedCache_EvictsLRU(t *testing.T) {
	c := newBoundedCache(2)
	require.NoError(t, c.Set("a", true))
	require.NoError(t, c.Set("b", true))
	_, _ = c.Get("a")              // a 变最近使用
	require.NoError(t, c.Set("c", true)) // 容量满 → 淘汰最久未用 b

	_, err := c.Get("b")
	require.ErrorIs(t, err, cache.ErrNoSuchKey, "b 应被淘汰")
	va, errA := c.Get("a")
	require.NoError(t, errA)
	require.True(t, va)
	vc, errC := c.Get("c")
	require.NoError(t, errC)
	require.True(t, vc)
}

func TestBoundedCache_ImplementsInterface(t *testing.T) {
	var _ cache.Cache = newBoundedCache(1)
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/sidecar/kernel/ -run TestBoundedCache -v`
预期：FAIL（`newBoundedCache` 未定义）。

- [ ] **步骤 3：编写实现**

`internal/sidecar/kernel/cache.go`：
```go
package kernel

import (
	"container/list"
	"sync"

	"github.com/casbin/casbin/v3/persist/cache"
)

// boundedCache 是有界 LRU，实现 casbin persist/cache.Cache。
// 仅作决策缓存的内存上界（非一致性手段——一致性靠每次 apply 后 InvalidateCache 全量清）。
type boundedCache struct {
	mu       sync.Mutex
	capacity int
	ll       *list.List
	items    map[string]*list.Element
}

type cacheEntry struct {
	key string
	val bool
}

// newBoundedCache 构造容量为 capacity 的 LRU（capacity<=0 视为 1）。
func newBoundedCache(capacity int) *boundedCache {
	if capacity <= 0 {
		capacity = 1
	}
	return &boundedCache{
		capacity: capacity,
		ll:       list.New(),
		items:    make(map[string]*list.Element, capacity),
	}
}

func (c *boundedCache) Set(key string, value bool, _ ...interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		el.Value.(*cacheEntry).val = value
		c.ll.MoveToFront(el)
		return nil
	}
	el := c.ll.PushFront(&cacheEntry{key: key, val: value})
	c.items[key] = el
	if c.ll.Len() > c.capacity {
		if oldest := c.ll.Back(); oldest != nil {
			c.ll.Remove(oldest)
			delete(c.items, oldest.Value.(*cacheEntry).key)
		}
	}
	return nil
}

func (c *boundedCache) Get(key string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return false, cache.ErrNoSuchKey
	}
	c.ll.MoveToFront(el)
	return el.Value.(*cacheEntry).val, nil
}

func (c *boundedCache) Delete(key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return cache.ErrNoSuchKey
	}
	c.ll.Remove(el)
	delete(c.items, key)
	return nil
}

func (c *boundedCache) Clear() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ll.Init()
	c.items = make(map[string]*list.Element, c.capacity)
	return nil
}

var _ cache.Cache = (*boundedCache)(nil)
```

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/sidecar/kernel/ -run TestBoundedCache -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/sidecar/kernel/cache.go internal/sidecar/kernel/cache_test.go
git commit -m "feat(sidecar/kernel): 有界 LRU 决策缓存（实现 casbin cache.Cache）"
```

---

## 任务 2：域类型 + 错误 + 行助手（types.go / errors.go）

**文件：**
- 创建：`internal/sidecar/kernel/types.go`、`internal/sidecar/kernel/errors.go`
- 测试：`internal/sidecar/kernel/types_test.go`

- [ ] **步骤 1：编写失败的测试**

`internal/sidecar/kernel/types_test.go`：
```go
package kernel

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRule_Values_TrimsTrailingEmpty(t *testing.T) {
	p := Rule{Ptype: "p", V: [6]string{"manager", "dom1", "order", "read", "allow", ""}}
	require.Equal(t, []string{"manager", "dom1", "order", "read", "allow"}, p.values())

	g := Rule{Ptype: "g", V: [6]string{"alice", "manager", "dom1", "", "", ""}}
	require.Equal(t, []string{"alice", "manager", "dom1"}, g.values())
}

func TestRule_DomainValue_ByPtype(t *testing.T) {
	p := Rule{Ptype: "p", V: [6]string{"manager", "dom1", "order", "read", "allow", ""}}
	require.Equal(t, "dom1", p.domainValue()) // p 段 domain 在 V[1]

	g := Rule{Ptype: "g", V: [6]string{"alice", "manager", "dom1", "", "", ""}}
	require.Equal(t, "dom1", g.domainValue()) // g 段 domain 在 V[2]

	require.Equal(t, "", Rule{Ptype: "x"}.domainValue())
}

func TestNoopApplier_SatisfiesInterfaceAndNoPanic(t *testing.T) {
	var a DataPolicyApplier = noopApplier{}
	require.NotPanics(t, func() {
		a.ApplySnapshot([]DataPolicy{{ID: 1}})
		a.ApplyChange(ChangeAdd, DataPolicy{ID: 2})
	})
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/sidecar/kernel/ -run 'TestRule|TestNoopApplier' -v`
预期：FAIL（类型未定义）。

- [ ] **步骤 3：编写实现**

`internal/sidecar/kernel/types.go`：
```go
package kernel

// Rule 是一条 casbin_rule（ptype + v0..v5），空位用空串（casbin 惯例）。
type Rule struct {
	Ptype string
	V     [6]string
}

// values 返回去掉尾部空串的值切片（casbin []string 风格）。
func (r Rule) values() []string {
	n := len(r.V)
	for n > 0 && r.V[n-1] == "" {
		n--
	}
	out := make([]string, n)
	copy(out, r.V[:n])
	return out
}

// domainValue 取该行的 domain 列：p 段在 V[1]，g 段在 V[2]（对齐控制面 projection）。
func (r Rule) domainValue() string {
	switch r.Ptype {
	case "p":
		return r.V[1]
	case "g":
		return r.V[2]
	default:
		return ""
	}
}

// ChangeOp 是策略变更操作类型。
type ChangeOp int

const (
	ChangeAdd ChangeOp = iota
	ChangeUpdate
	ChangeRemove
)

// PolicyChange 是一条 casbin 策略行变更。
type PolicyChange struct {
	Op      ChangeOp
	Rule    Rule // ADD/UPDATE 的新行；REMOVE 的待删行
	OldRule Rule // 仅 UPDATE 用：旧行
}

// DataPolicy 是一条数据权限规则（Condition 为不透明 JSON 串，求值归 ④-2）。
type DataPolicy struct {
	ID          uint64
	SubjectType string
	SubjectID   string
	Resource    string
	Condition   string
}

// DataPolicyChange 是一条数据权限变更。
type DataPolicyChange struct {
	Op     ChangeOp
	Policy DataPolicy
}

// Delta 是一次策略变更（功能行 + 数据策略，共享统一版本号）。
type Delta struct {
	Version       uint64
	PolicyChanges []PolicyChange
	DataChanges   []DataPolicyChange
}

// Snapshot 是全量策略快照。
type Snapshot struct {
	Version      uint64
	Rules        []Rule
	DataPolicies []DataPolicy
}

// DataPolicyApplier 接收数据策略的全量/增量变更（④-2 实现，默认 no-op）。
type DataPolicyApplier interface {
	ApplySnapshot(policies []DataPolicy)
	ApplyChange(op ChangeOp, p DataPolicy)
}

// noopApplier 是默认空实现，便于内核独立单测。
type noopApplier struct{}

func (noopApplier) ApplySnapshot([]DataPolicy)       {}
func (noopApplier) ApplyChange(ChangeOp, DataPolicy) {}
```

`internal/sidecar/kernel/errors.go`：
```go
package kernel

import "errors"

var (
	// ErrNotReady 表示尚未成功应用过快照，鉴权 fail-close 拒绝。
	ErrNotReady = errors.New("kernel: enforcer not ready (no snapshot applied)")
	// ErrForeignDomain 表示规则/请求的 domain 不属于本 app 的固定域。
	ErrForeignDomain = errors.New("kernel: rule/request domain does not match pinned app domain")
	// ErrStaleVersion 表示 delta 版本未严格大于当前已应用版本（重放/乱序）。
	ErrStaleVersion = errors.New("kernel: delta version not greater than current applied version")
)
```

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/sidecar/kernel/ -run 'TestRule|TestNoopApplier' -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/sidecar/kernel/types.go internal/sidecar/kernel/errors.go internal/sidecar/kernel/types_test.go
git commit -m "feat(sidecar/kernel): 内核域类型 + 行助手 + DataPolicyApplier 接口 + 哨兵错误"
```

---

## 任务 3：casbin model（model.go）

**文件：**
- 创建：`internal/sidecar/kernel/model.go`
- 测试：`internal/sidecar/kernel/model_test.go`

- [ ] **步骤 1：编写失败的测试**

`internal/sidecar/kernel/model_test.go`：
```go
package kernel

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildModel_ParsesAndHasSections(t *testing.T) {
	m, err := buildModel()
	require.NoError(t, err)
	for _, sec := range []string{"r", "p", "g", "e", "m"} {
		require.Contains(t, m, sec, "model 缺少段 %q", sec)
	}
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/sidecar/kernel/ -run TestBuildModel -v`
预期：FAIL（`buildModel` 未定义）。

- [ ] **步骤 3：编写实现**

`internal/sidecar/kernel/model.go`：
```go
package kernel

import "github.com/casbin/casbin/v3/model"

// modelText 是锁定的 RBAC-with-domain model（架构 §6.2；与控制面 projection 落的行结构对齐）。
const modelText = `
[request_definition]
r = sub, dom, obj, act

[policy_definition]
p = sub, dom, obj, act, eft

[role_definition]
g = _, _, _

[policy_effect]
e = some(where (p.eft == allow)) && !some(where (p.eft == deny))

[matchers]
m = g(r.sub, p.sub, r.dom) && r.dom == p.dom && r.obj == p.obj && r.act == p.act
`

// buildModel 装配内嵌 model。
func buildModel() (model.Model, error) {
	return model.NewModelFromString(modelText)
}
```

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/sidecar/kernel/ -run TestBuildModel -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/sidecar/kernel/model.go internal/sidecar/kernel/model_test.go
git commit -m "feat(sidecar/kernel): 内嵌 RBAC-with-domain casbin model"
```

---

## 任务 4：memoryAdapter（adapter.go）

**文件：**
- 创建：`internal/sidecar/kernel/adapter.go`
- 测试：`internal/sidecar/kernel/adapter_test.go`

- [ ] **步骤 1：编写失败的测试**

`internal/sidecar/kernel/adapter_test.go`：
```go
package kernel

import (
	"testing"

	"github.com/casbin/casbin/v3/persist"
	"github.com/stretchr/testify/require"
)

func TestMemoryAdapter_ImplementsInterfaces(t *testing.T) {
	var (
		_ persist.Adapter         = (*memoryAdapter)(nil)
		_ persist.BatchAdapter    = (*memoryAdapter)(nil)
		_ persist.FilteredAdapter = (*memoryAdapter)(nil)
	)
}

func TestMemoryAdapter_LoadPolicy_LoadsAllHeldRules(t *testing.T) {
	rules := []Rule{
		{Ptype: "p", V: [6]string{"manager", "dom1", "order", "read", "allow", ""}},
		{Ptype: "g", V: [6]string{"alice", "manager", "dom1", "", "", ""}},
	}
	a := newMemoryAdapter(rules)
	m, err := buildModel()
	require.NoError(t, err)
	require.NoError(t, a.LoadPolicy(m))

	ok, _ := m.HasPolicy("p", "p", []string{"manager", "dom1", "order", "read", "allow"})
	require.True(t, ok)
	ok, _ = m.HasPolicy("g", "g", []string{"alice", "manager", "dom1"})
	require.True(t, ok)
	require.False(t, a.IsFiltered())
}

func TestMemoryAdapter_LoadFilteredPolicy_FiltersByDomain(t *testing.T) {
	rules := []Rule{
		{Ptype: "p", V: [6]string{"manager", "dom1", "order", "read", "allow", ""}},
		{Ptype: "p", V: [6]string{"manager", "dom2", "order", "read", "allow", ""}},
	}
	a := newMemoryAdapter(rules)
	m, err := buildModel()
	require.NoError(t, err)
	require.NoError(t, a.LoadFilteredPolicy(m, "dom1"))

	ok, _ := m.HasPolicy("p", "p", []string{"manager", "dom1", "order", "read", "allow"})
	require.True(t, ok)
	ok, _ = m.HasPolicy("p", "p", []string{"manager", "dom2", "order", "read", "allow"})
	require.False(t, ok, "外域行不应加载")
	require.True(t, a.IsFiltered())
}

func TestMemoryAdapter_WritesAreNoop(t *testing.T) {
	a := newMemoryAdapter([]Rule{{Ptype: "p", V: [6]string{"manager", "dom1", "order", "read", "allow", ""}}})
	require.NoError(t, a.AddPolicy("p", "p", []string{"x"}))
	require.NoError(t, a.RemovePolicy("p", "p", []string{"x"}))
	require.NoError(t, a.AddPolicies("p", "p", [][]string{{"x"}}))
	require.NoError(t, a.RemovePolicies("p", "p", [][]string{{"x"}}))
	require.NoError(t, a.RemoveFilteredPolicy("p", "p", 0, "x"))
	require.NoError(t, a.SavePolicy(nil))
	require.Len(t, a.rules, 1, "no-op 写不得改动持有快照")
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/sidecar/kernel/ -run TestMemoryAdapter -v`
预期：FAIL（`memoryAdapter` 未定义）。

- [ ] **步骤 3：编写实现**

`internal/sidecar/kernel/adapter.go`：
```go
package kernel

import (
	"github.com/casbin/casbin/v3/model"
	"github.com/casbin/casbin/v3/persist"
)

// memoryAdapter 是只读内存 Adapter：持本地快照，仅启动/重建时被加载。
// 运行期策略经 Engine 高层 API 改内存、不回写 adapter（autoSave=false）。
// 实现 Adapter+BatchAdapter+FilteredAdapter（FilteredAdapter 为架构 §3.2 强制契约 + ④-5 冷启动预留）。
type memoryAdapter struct {
	rules    []Rule
	filtered bool
}

func newMemoryAdapter(rules []Rule) *memoryAdapter {
	cp := make([]Rule, len(rules))
	copy(cp, rules)
	return &memoryAdapter{rules: cp}
}

func (a *memoryAdapter) loadInto(m model.Model, domainFilter string) error {
	for _, r := range a.rules {
		if domainFilter != "" && r.domainValue() != domainFilter {
			continue
		}
		line := append([]string{r.Ptype}, r.values()...)
		if err := persist.LoadPolicyArray(line, m); err != nil {
			return err
		}
	}
	return nil
}

// LoadPolicy 全量加载持有的行。
func (a *memoryAdapter) LoadPolicy(m model.Model) error {
	a.filtered = false
	return a.loadInto(m, "")
}

// LoadFilteredPolicy 按 domain 过滤加载；filter 期望为本域 domain 字符串，空/非字符串视为不过滤。
func (a *memoryAdapter) LoadFilteredPolicy(m model.Model, filter interface{}) error {
	dom, _ := filter.(string)
	a.filtered = dom != ""
	return a.loadInto(m, dom)
}

func (a *memoryAdapter) IsFiltered() bool { return a.filtered }

// 写路径一律 no-op（只读契约；运行期改内存走 Engine 高层 API）。
func (a *memoryAdapter) SavePolicy(model.Model) error                               { return nil }
func (a *memoryAdapter) AddPolicy(string, string, []string) error                  { return nil }
func (a *memoryAdapter) RemovePolicy(string, string, []string) error               { return nil }
func (a *memoryAdapter) RemoveFilteredPolicy(string, string, int, ...string) error { return nil }
func (a *memoryAdapter) AddPolicies(string, string, [][]string) error              { return nil }
func (a *memoryAdapter) RemovePolicies(string, string, [][]string) error           { return nil }

var (
	_ persist.Adapter         = (*memoryAdapter)(nil)
	_ persist.BatchAdapter    = (*memoryAdapter)(nil)
	_ persist.FilteredAdapter = (*memoryAdapter)(nil)
)
```

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/sidecar/kernel/ -run TestMemoryAdapter -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/sidecar/kernel/adapter.go internal/sidecar/kernel/adapter_test.go
git commit -m "feat(sidecar/kernel): 只读 memoryAdapter（Adapter+Batch+Filtered，no-op 写）"
```

---

## 任务 5：Engine 构造 + Enforce + fail-close（engine.go 第 1 部分）

**文件：**
- 创建：`internal/sidecar/kernel/engine.go`
- 测试：`internal/sidecar/kernel/engine_test.go`

- [ ] **步骤 1：编写失败的测试**

`internal/sidecar/kernel/engine_test.go`：
```go
package kernel

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEngine_New_NotReadyFailsClose(t *testing.T) {
	e, err := New("dom1", nil, nil) // cache=nil→默认有界；applier=nil→noop
	require.NoError(t, err)
	require.False(t, e.Ready())
	require.Equal(t, uint64(0), e.Version())

	allow, err := e.Enforce("alice", "dom1", "order", "read")
	require.ErrorIs(t, err, ErrNotReady)
	require.False(t, allow, "未就绪必须 fail-close 拒绝")
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/sidecar/kernel/ -run TestEngine_New -v`
预期：FAIL（`New` 未定义）。

- [ ] **步骤 3：编写实现**

`internal/sidecar/kernel/engine.go`：
```go
package kernel

import (
	"sync"
	"sync/atomic"

	"github.com/casbin/casbin/v3"
	"github.com/casbin/casbin/v3/persist/cache"
)

// Engine 是单 app（单 casbin domain）的纯内存功能权限内核。
type Engine struct {
	domain  string
	ce      *casbin.SyncedCachedEnforcer
	applier DataPolicyApplier

	applyMu sync.Mutex // 串行化 apply（validate→mutate→记版本）
	version atomic.Uint64
	ready   atomic.Bool
}

// New 构造内核：pin 本 app 的 domain；c 为决策缓存（nil 则内部建容量 1024 的有界 LRU）；
// applier 接收数据策略变更（nil 则退化 no-op，便于独立单测）。
func New(domain string, c cache.Cache, applier DataPolicyApplier) (*Engine, error) {
	m, err := buildModel()
	if err != nil {
		return nil, err
	}
	ce, err := casbin.NewSyncedCachedEnforcer(m, newMemoryAdapter(nil))
	if err != nil {
		return nil, err
	}
	ce.EnableAutoSave(false)           // 只读 adapter：运行期改内存不回写
	ce.EnableAutoNotifyWatcher(false)  // 纯订阅端：杜绝回播
	if c == nil {
		c = newBoundedCache(1024)
	}
	ce.SetCache(c)
	if applier == nil {
		applier = noopApplier{}
	}
	return &Engine{domain: domain, ce: ce, applier: applier}, nil
}

// Version 返回当前已应用版本（未就绪为 0）。
func (e *Engine) Version() uint64 { return e.version.Load() }

// Ready 表示是否已成功应用过一次快照。
func (e *Engine) Ready() bool { return e.ready.Load() }

// Enforce 判定 (sub,dom,obj,act)。未就绪/越域/出错一律 fail-close。
func (e *Engine) Enforce(sub, dom, obj, act string) (bool, error) {
	if !e.ready.Load() {
		return false, ErrNotReady
	}
	if dom != e.domain {
		return false, ErrForeignDomain
	}
	return e.ce.Enforce(sub, dom, obj, act)
}
```

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/sidecar/kernel/ -run TestEngine_New -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/sidecar/kernel/engine.go internal/sidecar/kernel/engine_test.go
git commit -m "feat(sidecar/kernel): Engine 构造 + Enforce + 未就绪 fail-close"
```

---

## 任务 6：ApplySnapshot（engine.go 第 2 部分）

**文件：**
- 修改：`internal/sidecar/kernel/engine.go`（追加方法）
- 测试：`internal/sidecar/kernel/engine_test.go`（追加用例）

> **已知可接受行为（写入实现注释）：** `ApplySnapshot` 走 `ClearPolicy`→`AddNamed*` 多次自锁调用，并发 Enforce 可能在重建中途读到「已清空未重建」的瞬时态 → 暂时拒绝（fail-close 的新鲜度滞后，非错误放行；快照重建罕见）。符合架构 §2.2/§5，不消窗，仅注释标注。

- [ ] **步骤 1：编写失败的测试**（追加到 `engine_test.go`）

```go
func mgrSnapshot(version uint64) Snapshot {
	return Snapshot{
		Version: version,
		Rules: []Rule{
			{Ptype: "p", V: [6]string{"manager", "dom1", "order", "read", "allow", ""}},
			{Ptype: "g", V: [6]string{"alice", "manager", "dom1", "", "", ""}},
		},
	}
}

func TestEngine_ApplySnapshot_EnforcesViaRole(t *testing.T) {
	e, _ := New("dom1", nil, nil)
	require.NoError(t, e.ApplySnapshot(mgrSnapshot(7)))
	require.True(t, e.Ready())
	require.Equal(t, uint64(7), e.Version())

	allow, err := e.Enforce("alice", "dom1", "order", "read") // alice 经 manager 角色
	require.NoError(t, err)
	require.True(t, allow)

	deny, err := e.Enforce("alice", "dom1", "order", "delete") // 无此权限
	require.NoError(t, err)
	require.False(t, deny)
}

func TestEngine_ApplySnapshot_DenyOverride(t *testing.T) {
	e, _ := New("dom1", nil, nil)
	s := Snapshot{Version: 1, Rules: []Rule{
		{Ptype: "g", V: [6]string{"alice", "manager", "dom1", "", "", ""}},
		{Ptype: "p", V: [6]string{"manager", "dom1", "order", "read", "allow", ""}},
		{Ptype: "p", V: [6]string{"manager", "dom1", "order", "read", "deny", ""}},
	}}
	require.NoError(t, e.ApplySnapshot(s))
	allow, err := e.Enforce("alice", "dom1", "order", "read")
	require.NoError(t, err)
	require.False(t, allow, "deny 覆盖 allow")
}

func TestEngine_ApplySnapshot_DomainIsolation(t *testing.T) {
	e, _ := New("dom1", nil, nil)
	require.NoError(t, e.ApplySnapshot(mgrSnapshot(1)))
	deny, err := e.Enforce("alice", "dom2", "order", "read") // 外域请求
	require.ErrorIs(t, err, ErrForeignDomain)
	require.False(t, deny)
}

func TestEngine_ApplySnapshot_ForeignDomainRejected(t *testing.T) {
	e, _ := New("dom1", nil, nil)
	s := Snapshot{Version: 1, Rules: []Rule{
		{Ptype: "p", V: [6]string{"manager", "dom2", "order", "read", "allow", ""}}, // 外域行
	}}
	require.ErrorIs(t, e.ApplySnapshot(s), ErrForeignDomain)
	require.False(t, e.Ready(), "越域快照整笔拒绝，状态不变")
}

func TestEngine_ApplySnapshot_RebuildNoResidue(t *testing.T) {
	e, _ := New("dom1", nil, nil)
	require.NoError(t, e.ApplySnapshot(mgrSnapshot(1)))
	// 二次快照只含一条无关 p、无 alice 的 g
	s2 := Snapshot{Version: 2, Rules: []Rule{
		{Ptype: "p", V: [6]string{"viewer", "dom1", "report", "read", "allow", ""}},
	}}
	require.NoError(t, e.ApplySnapshot(s2))
	require.Equal(t, uint64(2), e.Version())
	deny, err := e.Enforce("alice", "dom1", "order", "read") // 旧策略应已清除
	require.NoError(t, err)
	require.False(t, deny, "ClearPolicy 后旧策略不得残留")
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/sidecar/kernel/ -run TestEngine_ApplySnapshot -v`
预期：FAIL（`ApplySnapshot` 未定义）。

- [ ] **步骤 3：编写实现**（追加到 `engine.go`）

```go
// ApplySnapshot 全量重建内核状态：校验越域→ClearPolicy→分段灌入→路由数据策略→全量清缓存→记版本就绪。
// 越域行整笔拒绝（pre-clear，状态不变）；进入重建后任何失败一律 fail-close（ready=false），等 ④-3 重试。
//
// 已知可接受行为：重建经多次自锁调用，并发 Enforce 可能读到「已清空未重建」瞬时态→暂拒（fail-close
// 新鲜度滞后，非错误放行；快照罕见）。符合架构 §2.2/§5，不消窗。
func (e *Engine) ApplySnapshot(s Snapshot) error {
	e.applyMu.Lock()
	defer e.applyMu.Unlock()

	for _, r := range s.Rules { // 1. pre-clear 越域校验
		if r.domainValue() != e.domain {
			return ErrForeignDomain
		}
	}

	e.ce.ClearPolicy() // 2. 进入重建——此后任何失败 fail-close
	var pRules, gRules [][]string
	for _, r := range s.Rules {
		switch r.Ptype {
		case "p":
			pRules = append(pRules, r.values())
		case "g":
			gRules = append(gRules, r.values())
		}
	}
	if len(pRules) > 0 {
		if _, err := e.ce.AddNamedPolicies("p", pRules); err != nil {
			e.ready.Store(false)
			return err
		}
	}
	if len(gRules) > 0 {
		if _, err := e.ce.AddNamedGroupingPolicies("g", gRules); err != nil {
			e.ready.Store(false)
			return err
		}
	}

	e.applier.ApplySnapshot(s.DataPolicies) // 3. 路由数据策略
	if err := e.ce.InvalidateCache(); err != nil {
		e.ready.Store(false)
		return err
	}
	e.version.Store(s.Version)
	e.ready.Store(true)
	return nil
}
```

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/sidecar/kernel/ -run TestEngine_ApplySnapshot -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/sidecar/kernel/engine.go internal/sidecar/kernel/engine_test.go
git commit -m "feat(sidecar/kernel): ApplySnapshot 全量重建 + 越域拒绝 + fail-close"
```

---

## 任务 7：ApplyDelta + 缓存铁律（engine.go 第 3 部分）

**文件：**
- 修改：`internal/sidecar/kernel/engine.go`（追加方法）
- 测试：`internal/sidecar/kernel/engine_test.go`（追加用例）

- [ ] **步骤 1：编写失败的测试**（追加到 `engine_test.go`）

```go
// spyApplier 记录收到的数据策略变更，验证路由。
type spyApplier struct {
	snapshots [][]DataPolicy
	changes   []DataPolicyChange
}

func (s *spyApplier) ApplySnapshot(p []DataPolicy)        { s.snapshots = append(s.snapshots, p) }
func (s *spyApplier) ApplyChange(op ChangeOp, p DataPolicy) {
	s.changes = append(s.changes, DataPolicyChange{Op: op, Policy: p})
}

// 缓存铁律守门：撤权必须即时生效（按 key 删在角色间接性下会漏，全量清才正确）。
func TestEngine_ApplyDelta_RevokeTakesEffectImmediately(t *testing.T) {
	e, _ := New("dom1", nil, nil)
	require.NoError(t, e.ApplySnapshot(mgrSnapshot(1)))

	allow, err := e.Enforce("alice", "dom1", "order", "read") // 命中并入缓存 true
	require.NoError(t, err)
	require.True(t, allow)

	// 撤掉 manager 的 order:read 权限（delta REMOVE p）
	d := Delta{Version: 2, PolicyChanges: []PolicyChange{
		{Op: ChangeRemove, Rule: Rule{Ptype: "p", V: [6]string{"manager", "dom1", "order", "read", "allow", ""}}},
	}}
	require.NoError(t, e.ApplyDelta(d))
	require.Equal(t, uint64(2), e.Version())

	deny, err := e.Enforce("alice", "dom1", "order", "read")
	require.NoError(t, err)
	require.False(t, deny, "撤权后 alice（经 manager）必须立即被拒——证明全量清生效")
}

func TestEngine_ApplyDelta_AddGrant(t *testing.T) {
	e, _ := New("dom1", nil, nil)
	require.NoError(t, e.ApplySnapshot(mgrSnapshot(1)))
	d := Delta{Version: 2, PolicyChanges: []PolicyChange{
		{Op: ChangeAdd, Rule: Rule{Ptype: "p", V: [6]string{"manager", "dom1", "order", "delete", "allow", ""}}},
	}}
	require.NoError(t, e.ApplyDelta(d))
	allow, err := e.Enforce("alice", "dom1", "order", "delete")
	require.NoError(t, err)
	require.True(t, allow)
}

func TestEngine_ApplyDelta_UpdateRule(t *testing.T) {
	e, _ := New("dom1", nil, nil)
	require.NoError(t, e.ApplySnapshot(mgrSnapshot(1)))
	d := Delta{Version: 2, PolicyChanges: []PolicyChange{{
		Op:      ChangeUpdate,
		OldRule: Rule{Ptype: "p", V: [6]string{"manager", "dom1", "order", "read", "allow", ""}},
		Rule:    Rule{Ptype: "p", V: [6]string{"manager", "dom1", "invoice", "read", "allow", ""}},
	}}}
	require.NoError(t, e.ApplyDelta(d))
	old, _ := e.Enforce("alice", "dom1", "order", "read")
	require.False(t, old, "旧权限应被移除")
	neu, _ := e.Enforce("alice", "dom1", "invoice", "read")
	require.True(t, neu, "新权限应生效")
}

func TestEngine_ApplyDelta_StaleVersionRejected(t *testing.T) {
	e, _ := New("dom1", nil, nil)
	require.NoError(t, e.ApplySnapshot(mgrSnapshot(5)))
	d := Delta{Version: 5} // 非严格大于当前 5
	require.ErrorIs(t, e.ApplyDelta(d), ErrStaleVersion)
	require.Equal(t, uint64(5), e.Version(), "拒绝后版本不变")
}

func TestEngine_ApplyDelta_ForeignDomainRejected(t *testing.T) {
	e, _ := New("dom1", nil, nil)
	require.NoError(t, e.ApplySnapshot(mgrSnapshot(1)))
	d := Delta{Version: 2, PolicyChanges: []PolicyChange{
		{Op: ChangeAdd, Rule: Rule{Ptype: "p", V: [6]string{"manager", "dom2", "x", "y", "allow", ""}}},
	}}
	require.ErrorIs(t, e.ApplyDelta(d), ErrForeignDomain)
	require.Equal(t, uint64(1), e.Version())
}

func TestEngine_ApplyDelta_RoutesDataChanges(t *testing.T) {
	spy := &spyApplier{}
	e, _ := New("dom1", nil, spy)
	require.NoError(t, e.ApplySnapshot(Snapshot{Version: 1, DataPolicies: []DataPolicy{{ID: 9}}}))
	require.Len(t, spy.snapshots, 1)
	require.Equal(t, uint64(9), spy.snapshots[0][0].ID)

	d := Delta{Version: 2, DataChanges: []DataPolicyChange{
		{Op: ChangeAdd, Policy: DataPolicy{ID: 10, Resource: "order"}},
	}}
	require.NoError(t, e.ApplyDelta(d))
	require.Len(t, spy.changes, 1)
	require.Equal(t, ChangeAdd, spy.changes[0].Op)
	require.Equal(t, uint64(10), spy.changes[0].Policy.ID)
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/sidecar/kernel/ -run TestEngine_ApplyDelta -v`
预期：FAIL（`ApplyDelta` 未定义）。

- [ ] **步骤 3：编写实现**（追加到 `engine.go`）

```go
// ApplyDelta 增量应用一条变更：版本单调校验→越域校验→逐 PolicyChange 改 casbin→路由数据策略→
// 全量清缓存→记版本。版本未严格大于当前→ErrStaleVersion（拒重放/乱序）；越域→ErrForeignDomain（状态不变）；
// 进入变更后任何失败 fail-close（ready=false）。
func (e *Engine) ApplyDelta(d Delta) error {
	e.applyMu.Lock()
	defer e.applyMu.Unlock()

	if d.Version <= e.version.Load() {
		return ErrStaleVersion
	}
	for _, pc := range d.PolicyChanges { // 越域校验（pre-mutation）
		if pc.Rule.domainValue() != e.domain {
			return ErrForeignDomain
		}
		if pc.Op == ChangeUpdate && pc.OldRule.domainValue() != e.domain {
			return ErrForeignDomain
		}
	}

	for _, pc := range d.PolicyChanges { // 进入变更——失败 fail-close
		if err := e.applyPolicyChange(pc); err != nil {
			e.ready.Store(false)
			return err
		}
	}
	for _, dc := range d.DataChanges {
		e.applier.ApplyChange(dc.Op, dc.Policy)
	}
	if err := e.ce.InvalidateCache(); err != nil {
		e.ready.Store(false)
		return err
	}
	e.version.Store(d.Version)
	return nil
}

func (e *Engine) applyPolicyChange(pc PolicyChange) error {
	switch pc.Op {
	case ChangeAdd:
		return e.addRule(pc.Rule)
	case ChangeRemove:
		return e.removeRule(pc.Rule)
	case ChangeUpdate: // 防御性：删旧+加新（section-correct）。③ 不对功能行发 UPDATE，但内核兜住。
		if err := e.removeRule(pc.OldRule); err != nil {
			return err
		}
		return e.addRule(pc.Rule)
	default:
		return nil
	}
}

// addRule/removeRule 按 ptype 走 section-correct 的 casbin 高层 API（g 段自动 BuildIncrementalRoleLinks）。
func (e *Engine) addRule(r Rule) error {
	switch r.Ptype {
	case "p":
		_, err := e.ce.AddNamedPolicies("p", [][]string{r.values()})
		return err
	case "g":
		_, err := e.ce.AddNamedGroupingPolicies("g", [][]string{r.values()})
		return err
	default:
		return nil
	}
}

func (e *Engine) removeRule(r Rule) error {
	switch r.Ptype {
	case "p":
		_, err := e.ce.RemoveNamedPolicies("p", [][]string{r.values()})
		return err
	case "g":
		_, err := e.ce.RemoveNamedGroupingPolicies("g", [][]string{r.values()})
		return err
	default:
		return nil
	}
}
```

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/sidecar/kernel/ -run TestEngine_ApplyDelta -v`
预期：PASS（含撤权即时生效的缓存铁律守门用例）。

- [ ] **步骤 5：Commit**

```bash
git add internal/sidecar/kernel/engine.go internal/sidecar/kernel/engine_test.go
git commit -m "feat(sidecar/kernel): ApplyDelta 增量 apply + 缓存铁律全量清 + 版本单调 + 数据策略路由"
```

---

## 任务 8：GetImplicitRolesForUser + BatchEnforce（engine.go 第 4 部分）

**文件：**
- 修改：`internal/sidecar/kernel/engine.go`（追加方法）
- 测试：`internal/sidecar/kernel/engine_test.go`（追加用例）

- [ ] **步骤 1：编写失败的测试**（追加到 `engine_test.go`）

```go
func TestEngine_GetImplicitRolesForUser_Hierarchy(t *testing.T) {
	e, _ := New("dom1", nil, nil)
	// admin > manager > viewer（角色继承），alice 绑 admin
	s := Snapshot{Version: 1, Rules: []Rule{
		{Ptype: "g", V: [6]string{"alice", "admin", "dom1", "", "", ""}},
		{Ptype: "g", V: [6]string{"admin", "manager", "dom1", "", "", ""}},
		{Ptype: "g", V: [6]string{"manager", "viewer", "dom1", "", "", ""}},
	}}
	require.NoError(t, e.ApplySnapshot(s))

	roles, err := e.GetImplicitRolesForUser("alice", "dom1")
	require.NoError(t, err)
	require.Subset(t, roles, []string{"admin", "manager", "viewer"})
}

func TestEngine_GetImplicitRolesForUser_NotReady(t *testing.T) {
	e, _ := New("dom1", nil, nil)
	_, err := e.GetImplicitRolesForUser("alice", "dom1")
	require.ErrorIs(t, err, ErrNotReady)
}

func TestEngine_BatchEnforce(t *testing.T) {
	e, _ := New("dom1", nil, nil)
	require.NoError(t, e.ApplySnapshot(mgrSnapshot(1)))
	res, err := e.BatchEnforce([][]string{
		{"alice", "dom1", "order", "read"},   // true（经 manager）
		{"alice", "dom1", "order", "delete"}, // false
	})
	require.NoError(t, err)
	require.Equal(t, []bool{true, false}, res)
}

func TestEngine_BatchEnforce_NotReady(t *testing.T) {
	e, _ := New("dom1", nil, nil)
	_, err := e.BatchEnforce([][]string{{"a", "dom1", "o", "r"}})
	require.ErrorIs(t, err, ErrNotReady)
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/sidecar/kernel/ -run 'TestEngine_GetImplicit|TestEngine_BatchEnforce' -v`
预期：FAIL（方法未定义）。

- [ ] **步骤 3：编写实现**（追加到 `engine.go`）

```go
// GetImplicitRolesForUser 把 user 展开为隐式角色集（含继承），供 ④-2 数据权限主体解析。
// GetImplicitRolesForUser 提升自 *Enforcer（SyncedEnforcer 未重写、无锁），故自取读锁防与 apply 竞争。
func (e *Engine) GetImplicitRolesForUser(user, dom string) ([]string, error) {
	if !e.ready.Load() {
		return nil, ErrNotReady
	}
	if dom != e.domain {
		return nil, ErrForeignDomain
	}
	lock := e.ce.GetLock()
	lock.RLock()
	defer lock.RUnlock()
	return e.ce.GetImplicitRolesForUser(user, dom)
}

// BatchEnforce 批量鉴权。未就绪 fail-close。外域请求经 matcher 自然不命中任何策略→false（fail-close）。
func (e *Engine) BatchEnforce(reqs [][]string) ([]bool, error) {
	if !e.ready.Load() {
		return nil, ErrNotReady
	}
	casReqs := make([][]interface{}, len(reqs))
	for i, r := range reqs {
		row := make([]interface{}, len(r))
		for j, v := range r {
			row[j] = v
		}
		casReqs[i] = row
	}
	return e.ce.BatchEnforce(casReqs)
}
```

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/sidecar/kernel/ -run 'TestEngine_GetImplicit|TestEngine_BatchEnforce' -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/sidecar/kernel/engine.go internal/sidecar/kernel/engine_test.go
git commit -m "feat(sidecar/kernel): GetImplicitRolesForUser（供 ④-2）+ BatchEnforce"
```

---

## 收尾：全量验证

- [ ] 运行全部 kernel 测试 + race + 静态检查：

```bash
go build ./...
go test ./internal/sidecar/kernel/ -v -count=1
go test ./internal/sidecar/kernel/ -race -count=1   # 内核含并发读（Enforce）/写（apply），跑 race
go vet ./internal/sidecar/kernel/
gofmt -l internal/sidecar/kernel/                    # 应无输出
```
预期：全 PASS、race 干净、gofmt 干净。

- [ ] 完成后用 `superpowers:finishing-a-development-branch` 收尾分支。

---

## 自检结果

- **规格覆盖**：§3 包结构→任务 1-8 全覆盖（cache/types/errors/model/adapter/engine）；§4 model→任务 3；§5 MemoryAdapter→任务 4；§6 Engine API→任务 5(构造/Enforce)/8(BatchEnforce/GetImplicitRoles)；§7 apply 编排 + DataPolicyApplier→任务 6(snapshot)/7(delta)；§8 缓存铁律→任务 1(LRU)+6/7(InvalidateCache)，撤权回归→任务 7；§9 单域固定/fail-close/版本单调→任务 5/6/7；§10 域类型→任务 2；§11 错误语义→任务 2(定义)+5/6/7(触发)；§12 测试 9 用例→分布任务 1/6/7/8。
- **占位符扫描**：无 TODO/待定；每步含完整可编译代码。
- **类型一致性**：`Rule.values()/domainValue()`、`ChangeAdd/Update/Remove`、`Engine`/`New`/`ApplySnapshot`/`ApplyDelta`/`Enforce`/`BatchEnforce`/`GetImplicitRolesForUser`/`Version`/`Ready`、`memoryAdapter`/`newMemoryAdapter`、`boundedCache`/`newBoundedCache`、`noopApplier`、`spyApplier`、`mgrSnapshot` 跨任务一致。casbin 方法名（`AddNamedPolicies`/`AddNamedGroupingPolicies`/`RemoveNamed*`/`ClearPolicy`/`InvalidateCache`/`SetCache`/`EnableAutoSave`/`EnableAutoNotifyWatcher`/`GetLock`/`BatchEnforce`/`GetImplicitRolesForUser`）均已回源核实（见顶部）。
- **任务 6/7 修改同一文件 engine_test.go**：助手 `mgrSnapshot`（任务 6 定义）被任务 7/8 复用；`spyApplier` 任务 7 定义，无重复。

相关：规格 `2026-06-03-sydom-sidecar-kernel-design.md`；[[feedback-consistency-over-simplicity]]、[[feedback-verify-casbin-before-asserting]]。
