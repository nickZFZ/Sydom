# M5.5 BatchEnforce 决策缓存复用 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 让 `kernel.BatchEnforce` 复用 casbin 决策缓存，补上实测 ~300× 差距（缓存命中 137ns vs 批量 ~42µs/决策）。

**架构：** `kernel.BatchEnforce` 内改为逐行调 `e.ce.Enforce`（casbin `SyncedCachedEnforcer` 的缓存 `Enforce` 路径），
替代当前的 `e.ce.BatchEnforce`（经嵌入落到 `Enforcer.BatchEnforce`，直调底层 `enforce` 绕过缓存）。
零新增缓存逻辑——复用 casbin 自身的 get/set/键推导/失效。

**技术栈：** Go 1.26、casbin v3.10.0（`SyncedCachedEnforcer`）、testify。

**规格：** `docs/superpowers/specs/2026-07-16-sydom-m55-batchenforce-cache-design.md`

---

## 文件结构

| 文件 | 职责 | 动作 |
|---|---|---|
| `internal/sidecar/kernel/engine.go` | **唯一生产改动**：`BatchEnforce` 函数体 + 其文档注释 | 修改（`:226-245`） |
| `internal/sidecar/kernel/engine_test.go` | T1–T5 五条有齿测试 + `countingCache` 测试构件 | 修改（追加） |
| `docs/runbooks/performance-baselines.md` | 基准实测数更新 + §4 撕裂取舍的运维说明 | 修改（`:21`, `:26`） |

**零触碰**：`cache.go`、`Enforce`、`EnforceEx`、`ApplySnapshot`、`ApplyDelta`、`authorizer.go`、`effperm`。

## TDD 结构说明（重要——决定任务顺序）

本片只有 **T1 是 RED→GREEN**（现行实现不读缓存 → 命中 0 → 失败）。

**T2/T3/T4/T5 是契约刻画测试，对现行实现就应 GREEN**，改完实现后**须仍 GREEN**——
这正是「行为不变」的双证（沿用 M5.1c 的双证模式）。它们的牙来自**变异验证**，不是来自初始红。

故任务顺序：**先落 T2–T5 刻画现行契约（GREEN）→ 再落 T1（RED）→ 改实现（T1 转 GREEN 且 T2–T5 仍 GREEN）**。

---

### 任务 M55B-1：契约刻画测试 T2/T3/T5（对现行实现须 GREEN）

**文件：**
- 测试：`internal/sidecar/kernel/engine_test.go`（追加）

- [ ] **步骤 1：追加三条契约测试**

追加到 `internal/sidecar/kernel/engine_test.go` 末尾：

```go
// T2: 批量判定须与逐条 Enforce 逐一相等（allow/deny 混合）——改写循环不得改判定。
// 刻意用两个独立引擎：同一引擎上批量会先填缓存、单条再读同一条目 → 两边"串供"会掩盖分歧。
func TestEngine_BatchEnforce_MatchesSingleEnforce(t *testing.T) {
	reqs := [][]string{
		{"alice", "dom1", "order", "read"},   // 经 manager 继承
		{"alice", "dom1", "order", "delete"}, // 无此 act
		{"bob", "dom1", "order", "read"},     // 非 manager
		{"manager", "dom1", "order", "read"}, // 直接主体（casbin HasLink 自反，role_manager.go:310）
	}

	eb, err := New("dom1", nil, nil)
	require.NoError(t, err)
	require.NoError(t, eb.ApplySnapshot(mgrSnapshot(1)))
	batch, err := eb.BatchEnforce(reqs)
	require.NoError(t, err)
	require.Len(t, batch, len(reqs))

	es, err := New("dom1", nil, nil)
	require.NoError(t, err)
	require.NoError(t, es.ApplySnapshot(mgrSnapshot(1)))
	for i, r := range reqs {
		single, serr := es.Enforce(r[0], r[1], r[2], r[3])
		require.NoError(t, serr)
		require.Equal(t, single, batch[i], "第 %d 行：批量与单条判定必须一致（req=%v）", i, r)
	}
}

// T3: 批量对外域行返 false 且不报错——与单条 Enforce 的 ErrForeignDomain 刻意不同（engine.go 契约）。
// 钉死陷阱：实现须调 e.ce.Enforce（casbin），若误写 e.Enforce（内核）→ 外域行变报错 → 本测试红。
func TestEngine_BatchEnforce_ForeignDomainRowReturnsFalseNotError(t *testing.T) {
	e, err := New("dom1", nil, nil)
	require.NoError(t, err)
	require.NoError(t, e.ApplySnapshot(mgrSnapshot(1)))

	res, err := e.BatchEnforce([][]string{
		{"alice", "dom1", "order", "read"}, // 本域
		{"alice", "dom2", "order", "read"}, // 外域：须 false，不得报错
	})
	require.NoError(t, err, "批量不得对外域行返错（契约：外域以 false 表达拒绝，不回传越域信号）")
	require.Equal(t, []bool{true, false}, res)

	// 对照：单条 Enforce 对同一外域请求显式报 ErrForeignDomain——两者刻意不对称
	_, serr := e.Enforce("alice", "dom2", "order", "read")
	require.ErrorIs(t, serr, ErrForeignDomain, "单条须保留越域信号（与批量的刻意差异）")
}

// T5: 空输入返 nil（非空切片）——逐字对齐 casbin BatchEnforce 现行行为（authorizer.go:88 直接透传）。
func TestEngine_BatchEnforce_EmptyInputReturnsNil(t *testing.T) {
	e, err := New("dom1", nil, nil)
	require.NoError(t, err)
	require.NoError(t, e.ApplySnapshot(mgrSnapshot(1)))

	res, err := e.BatchEnforce(nil)
	require.NoError(t, err)
	require.Nil(t, res, "空输入须返 nil（实现须用 var results []bool + append，非 make([]bool,0,n)）")

	res, err = e.BatchEnforce([][]string{})
	require.NoError(t, err)
	require.Nil(t, res)
}
```

- [ ] **步骤 2：运行验证「对现行实现即 GREEN」**

运行：
```bash
go test ./internal/sidecar/kernel/ -run 'TestEngine_BatchEnforce_(MatchesSingleEnforce|ForeignDomainRowReturnsFalseNotError|EmptyInputReturnsNil)' -v
```
预期：**3 条全 PASS**。它们刻画的是现行契约。

> 若 T2 有行 FAIL：说明我对 matcher 的预期有误——**停下来核实 model.go 与 casbin 源码，不要改测试去迁就**。

- [ ] **步骤 3：Commit**

```bash
git add internal/sidecar/kernel/engine_test.go
git commit -m "test(m55b): BatchEnforce 契约刻画测试 T2/T3/T5（对现行实现即 GREEN，为缓存复用改写立双证基线：T2 批量与单条判定逐一相等〔两独立引擎避免共享缓存串供〕/T3 外域行返 false 非报错〔钉死 e.ce.Enforce vs e.Enforce 陷阱〕/T5 空输入返 nil〔钉死 var results []bool + append〕）"
```

---

### 任务 M55B-2：一致性证明测试 T4（撤权后批量立即翻 false）

**文件：**
- 测试：`internal/sidecar/kernel/engine_test.go`（追加）

- [ ] **步骤 1：追加 T4**

本测试是 `TestEngine_ApplyDelta_RevokeTakesEffectImmediately`（`engine_test.go:102`）的**批量孪生**。
追加到 `engine_test.go` 末尾：

```go
// T4: 版本变更后批量判定须立即反映撤权——本片的核心一致性证明。
// 缓存复用绝不可喂陈旧决策：ApplyDelta 的 InvalidateCache 全量清 → 后续探测必 miss → 按新策略重算。
// 是 TestEngine_ApplyDelta_RevokeTakesEffectImmediately 的批量孪生。
func TestEngine_BatchEnforce_RevokeTakesEffectImmediately(t *testing.T) {
	e, err := New("dom1", nil, nil)
	require.NoError(t, err)
	require.NoError(t, e.ApplySnapshot(mgrSnapshot(1)))

	reqs := [][]string{{"alice", "dom1", "order", "read"}}
	res, err := e.BatchEnforce(reqs)
	require.NoError(t, err)
	require.Equal(t, []bool{true}, res, "撤权前应 allow（并填充决策缓存）")

	// 撤掉 manager 的 order:read（delta REMOVE p）
	d := Delta{Version: 2, PolicyChanges: []PolicyChange{
		{Op: ChangeRemove, Rule: Rule{Ptype: "p", V: [6]string{"manager", "dom1", "order", "read", "allow", ""}}},
	}}
	require.NoError(t, e.ApplyDelta(d))
	require.Equal(t, uint64(2), e.Version())

	res, err = e.BatchEnforce(reqs)
	require.NoError(t, err)
	require.Equal(t, []bool{false}, res, "撤权后批量必须立即翻 false——绝不可从缓存喂旧决策")
}
```

- [ ] **步骤 2：运行验证 GREEN**

运行：`go test ./internal/sidecar/kernel/ -run TestEngine_BatchEnforce_RevokeTakesEffectImmediately -v`
预期：PASS（现行实现绕缓存，撤权自然生效）。

- [ ] **步骤 3：变异验证——证明 T4 有牙**

此测试的牙来自变异，不是初始红。临时把 `engine.go` 的 `ApplyDelta` 中：
```go
	if err := e.ce.InvalidateCache(); err != nil {
```
改为（**临时**，验证后必须还原）：
```go
	if err := error(nil); err != nil {
```
即摘掉全量清缓存。

运行：`go test ./internal/sidecar/kernel/ -run TestEngine_BatchEnforce_RevokeTakesEffectImmediately -v`

预期（改写实现**之后**）：**FAIL**——批量从缓存读到陈旧 `true`。

> ⚠️ **注意时序**：在**当前**（未改写）实现下此变异**不会**让 T4 红，因为现行批量根本不读缓存。
> 故本步骤须在任务 M55B-4 改写实现**之后**补做。此处先记录，M55B-4 步骤 5 执行。

**还原**：`git checkout internal/sidecar/kernel/engine.go`（若已改写实现则用 `git diff` 核对仅还原变异行）。

- [ ] **步骤 4：Commit**

```bash
git add internal/sidecar/kernel/engine_test.go
git commit -m "test(m55b): BatchEnforce 撤权一致性测试 T4（ApplyDelta REMOVE 后批量须立即翻 false，是 TestEngine_ApplyDelta_RevokeTakesEffectImmediately 的批量孪生；本片核心一致性证明——缓存复用绝不喂陈旧决策；变异验证〔摘 InvalidateCache〕延到实现改写后做，因现行实现绕缓存该变异无感）"
```

---

### 任务 M55B-3：T1 缓存复用测试（对现行实现须 RED）

**文件：**
- 测试：`internal/sidecar/kernel/engine_test.go`（追加 `countingCache` + T1）

- [ ] **步骤 1：追加 import**

`engine_test.go` 现有 import 块为：
```go
import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)
```
改为：
```go
import (
	"sync"
	"testing"

	"github.com/casbin/casbin/v3/persist/cache"
	"github.com/stretchr/testify/require"
)
```

- [ ] **步骤 2：追加 countingCache + T1**

追加到 `engine_test.go` 末尾：

```go
// countingCache 包 boundedCache 计 Get 的命中/未命中，供缓存复用断言。
// 只在 Get 上计数（Set/Delete/Clear 透传）——不改任何缓存策略。
type countingCache struct {
	cache.Cache
	mu     sync.Mutex
	hits   int
	misses int
}

func newCountingCache() *countingCache { return &countingCache{Cache: newBoundedCache(1024)} }

func (c *countingCache) Get(key string) (bool, error) {
	v, err := c.Cache.Get(key)
	c.mu.Lock()
	if err == nil {
		c.hits++
	} else {
		c.misses++
	}
	c.mu.Unlock()
	return v, err
}

func (c *countingCache) stats() (hits, misses int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits, c.misses
}

func (c *countingCache) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hits, c.misses = 0, 0
}

// T1: 批量判定须复用决策缓存——同一批连跑两次，第二次须每行命中、零未命中。
// 现行实现（e.ce.BatchEnforce 直调底层 enforce）根本不读缓存 → 命中 0 → 本测试红。
func TestEngine_BatchEnforce_ReusesDecisionCache(t *testing.T) {
	c := newCountingCache()
	e, err := New("dom1", c, nil)
	require.NoError(t, err)
	require.NoError(t, e.ApplySnapshot(mgrSnapshot(1)))

	reqs := [][]string{
		{"alice", "dom1", "order", "read"},   // true
		{"alice", "dom1", "order", "delete"}, // false
	}
	_, err = e.BatchEnforce(reqs) // 第一次：冷，填充缓存
	require.NoError(t, err)

	c.reset()
	res, err := e.BatchEnforce(reqs) // 第二次：须全命中
	require.NoError(t, err)
	require.Equal(t, []bool{true, false}, res, "缓存命中不得改变判定")

	hits, misses := c.stats()
	require.Equal(t, len(reqs), hits, "第二次批量须每行命中决策缓存")
	require.Equal(t, 0, misses, "第二次批量不得有未命中")
}
```

- [ ] **步骤 3：运行验证 RED**

运行：`go test ./internal/sidecar/kernel/ -run TestEngine_BatchEnforce_ReusesDecisionCache -v`

预期：**FAIL**，形如 `Error: Not equal: expected: 2, actual: 0`（hits=0）——
证明现行批量路径完全不碰缓存。

> 这一红就是本片存在的理由。若它意外绿了，**停下来**：说明我对 casbin 的判断有误，回源重核。

- [ ] **步骤 4：Commit（红测试单独提交，留下证据）**

```bash
git add internal/sidecar/kernel/engine_test.go
git commit -m "test(m55b): BatchEnforce 缓存复用测试 T1（RED：注入 countingCache 计 Get 命中/未命中，同批连跑两次断言第二次每行命中零未命中；现行 e.ce.BatchEnforce 经嵌入落 Enforcer.BatchEnforce 直调底层 enforce 完全不读缓存故 hits=0 红——这一红即本片理由）"
```

---

### 任务 M55B-4：改写 BatchEnforce 复用缓存（T1 转 GREEN + T2–T5 双证）

**文件：**
- 修改：`internal/sidecar/kernel/engine.go:226-245`

- [ ] **步骤 1：改写函数体 + 注释**

把 `engine.go` 的整段（含注释）：

```go
// BatchEnforce 批量鉴权。未就绪 fail-close。
// 注意语义差异（刻意取舍）：单条 Enforce 对外域请求显式返回 ErrForeignDomain；批量接口不逐条校验越域，
// 外域请求经 matcher 自然不命中任何本域策略→false。两者 fail-close 等价（都不放行），但批量以 false
// 表达拒绝、不回传越域信号——调用方需要区分「越域」与「域内无权」时应走单条 Enforce。
// 另：casbin 的 BatchEnforce 直调底层 enforce、绕过决策缓存（与单条 Enforce 走缓存不同），
// 故批量鉴权不享缓存命中，高频批量调用需自行权衡。
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

替换为：

```go
// BatchEnforce 批量鉴权。未就绪 fail-close。
// 注意语义差异（刻意取舍）：单条 Enforce 对外域请求显式返回 ErrForeignDomain；批量接口不逐条校验越域，
// 外域请求经 matcher 自然不命中任何本域策略→false。两者 fail-close 等价（都不放行），但批量以 false
// 表达拒绝、不回传越域信号——调用方需要区分「越域」与「域内无权」时应走单条 Enforce。
//
// 实现逐行调 e.ce.Enforce（casbin SyncedCachedEnforcer 的缓存 Enforce），而非 e.ce.BatchEnforce：
// 后者经嵌入落到 Enforcer.BatchEnforce（enforcer.go:951）直调底层 enforce，完全绕过决策缓存
// （casbin 的缓存只挂在 Enforce 上，SyncedCachedEnforcer 未覆写 BatchEnforce）。两者 matcher 同为 ""
// （Enforce 与 BatchEnforce 都走 e.enforce("", nil, ...)），故缓存键逐字相同——批量与单条共享缓存
// 条目、互相暖缓存，且语义正确。实测 50 条批量 ~2.1ms → ~µs 量级（见 docs/runbooks/performance-baselines.md）。
//
// ⚠️ 此处必须调 e.ce.Enforce（casbin 的），绝不可调 e.Enforce（内核的）——后者会对外域行返回
// ErrForeignDomain，破坏上述「批量以 false 表达拒绝」的契约（TestEngine_BatchEnforce_
// ForeignDomainRowReturnsFalseNotError 钉死此点）。
//
// 已知取舍：逐行 Enforce 各自取放 RLock，故一批不再共享单一 RLock——一次 apply 可能插在行间，
// 使批次跨版本撕裂（前几行旧版本、其余新版本）。可接受：每行答案都是某个近期版本的正确答案；
// BatchCheck 语义上等价于 N 次 Check（客户端发 N 次单条本就如此撕裂），批量只是省 RTT 的传输层优化；
// 原子性从非承诺契约，而 §2.2/§5 已明文接受新鲜度滞后。撤权及时性不受影响：apply 的 InvalidateCache
// 全量清同一缓存 → 之后探测必 miss → 按新策略重算（TestEngine_BatchEnforce_
// RevokeTakesEffectImmediately 钉死此点）。
func (e *Engine) BatchEnforce(reqs [][]string) ([]bool, error) {
	if !e.ready.Load() {
		return nil, ErrNotReady
	}
	// 逐字对齐 casbin Enforcer.BatchEnforce 的现行外部行为：空输入返 nil（非空切片）、
	// 出错返回已算出的部分结果 + err（authorizer.go:88 直接透传，本片不顺手改错误语义）。
	var results []bool
	for _, r := range reqs {
		row := make([]interface{}, len(r))
		for j, v := range r {
			row[j] = v
		}
		res, err := e.ce.Enforce(row...) // casbin 的 Enforce（走缓存），绝非 e.Enforce
		if err != nil {
			return results, err
		}
		results = append(results, res)
	}
	return results, nil
}
```

- [ ] **步骤 2：运行 T1 验证转 GREEN**

运行：`go test ./internal/sidecar/kernel/ -run TestEngine_BatchEnforce_ReusesDecisionCache -v`
预期：**PASS**（hits=2, misses=0）。

- [ ] **步骤 3：运行全部 BatchEnforce 测试验证 T2–T5 仍 GREEN（双证：行为不变）**

运行：`go test ./internal/sidecar/kernel/ -run TestEngine_BatchEnforce -v`
预期：**全 PASS**（含既有 `TestEngine_BatchEnforce`、`TestEngine_BatchEnforce_NotReady`）。

- [ ] **步骤 4：运行内核全量包测试**

运行：`go test ./internal/sidecar/kernel/ -v`
预期：EXIT 0，零 FAIL。

- [ ] **步骤 5：变异验证 ×2（补做 M55B-2 步骤 3，并证 T1 有牙）**

**变异 A——证 T4 有牙（缓存不喂陈旧决策）：**
临时把 `ApplyDelta` 中 `if err := e.ce.InvalidateCache(); err != nil {` 整个 if 块注释掉（摘掉全量清缓存）。

运行：`go test ./internal/sidecar/kernel/ -run TestEngine_BatchEnforce_RevokeTakesEffectImmediately -v`
预期：**FAIL**（批量从缓存读到陈旧 `true`）。
→ 证明 T4 真在守护「版本变更下缓存正确」。**还原变异。**

**变异 B——证 T1 有牙（确实复用了缓存）：**
临时把 `res, err := e.ce.Enforce(row...)` 一行还原为旧的整批 `return e.ce.BatchEnforce(casReqs)` 写法
（或直接 `git stash` 本任务改动）。

运行：`go test ./internal/sidecar/kernel/ -run TestEngine_BatchEnforce_ReusesDecisionCache -v`
预期：**FAIL**（hits=0）。**还原变异。**

**还原后必跑**：`go test ./internal/sidecar/kernel/` 预期 EXIT 0。

- [ ] **步骤 6：Commit**

```bash
git add internal/sidecar/kernel/engine.go
git commit -m "feat(kernel): M5.5 BatchEnforce 复用决策缓存（逐行调 e.ce.Enforce 走 casbin SyncedCachedEnforcer 缓存路径，替代绕缓存的 e.ce.BatchEnforce；零新增缓存逻辑复用 casbin 自身 get/set/键推导/失效；单条与批量同 matcher \"\" 故缓存键逐字相同可共享互暖；逐字保留 casbin 外部行为=空输入返 nil+出错返部分结果+err；T1 转绿证复用,T2-T5 仍绿双证行为不变;两变异证有牙:摘 InvalidateCache→T4 红、还原 BatchEnforce→T1 红)"
```

---

### 任务 M55B-5：基准前后对照 + 更新 performance-baselines.md

**文件：**
- 修改：`docs/runbooks/performance-baselines.md:21`、`:26`

- [ ] **步骤 1：跑基准取实测数**

运行：
```bash
go test ./test/bench/ -bench BenchmarkBatchEnforce -benchtime 2s -run '^$' | tee /tmp/claude-1000/-home-tongyu-codes-Sydom/3ff8e11e-797c-4f6e-b74c-358d395d54d2/scratchpad/bench-after.txt
```
预期：`BenchmarkBatchEnforce-N` 的 ns/op 从基线 **2,130,430** 掉到 **µs 量级**。

> 现成基准每轮跑同一批 `reqs`，第一轮后即全缓存命中——正好测暖路径，无需改基准。

- [ ] **步骤 2：把实测数填进表格**

把 `docs/runbooks/performance-baselines.md:21` 一行：
```markdown
| BatchEnforce 50 | 2,130,430 | 1,503,118 | 41,061 | 批量 50 条/次 |
```
替换为（**用步骤 1 的真实实测数**填 `<ns>`/`<B>`/`<allocs>`，不得臆造）：
```markdown
| BatchEnforce 50（缓存命中） | <ns> | <B> | <allocs> | 批量 50 条/次；M5.5 优化后复用决策缓存 |
```

- [ ] **步骤 3：改写观察条目**

把 `:26` 一行：
```markdown
- **BatchEnforce 50 ≈ 2.13ms（~42µs/决策）远高于缓存命中单次（137ns）** → 批量路径未充分复用决策缓存（casbin `BatchEnforce` 走底层枚举）。**非阻断观察，留后续优化候选**（本切片只测量不改核）。
```
替换为（`<ns>`/`<倍数>` 同样用实测数）：
```markdown
- **BatchEnforce 50 已复用决策缓存**（M5.5 优化，见 `docs/superpowers/specs/2026-07-16-sydom-m55-batchenforce-cache-design.md`）：原 2.13ms（~42µs/决策，casbin `BatchEnforce` 直调底层 enforce 绕过缓存）→ 现 <ns> ns/op（**~<倍数>× 提升**）。内核逐行调 casbin 缓存 `Enforce`，与单条**共享缓存键**（同 matcher `""`）→ 批量与单条互相暖缓存。
- **运维注意（批次跨版本撕裂）**：批量不再共享单一 RLock，一次策略 apply 可能插在行间，使一批中前后行分属相邻两个版本。**这不是不一致**——每行都是某个近期版本的正确答案，且 `BatchCheck` 语义上等价于 N 次 `Check`。**撤权及时性不受影响**（apply 全量清缓存 → 后续必按新策略重算）。
```

- [ ] **步骤 4：Commit**

```bash
git add docs/runbooks/performance-baselines.md
git commit -m "docs(m55b): 基准手册更新 BatchEnforce 优化后实测数（表格改缓存命中态 + 观察条目从"留优化候选"改为已优化并记提升倍数 + 新增批次跨版本撕裂的运维说明：非不一致、撤权及时性不受影响）"
```

---

### 任务 M55B-6：零触碰核验 + 全量回归

**文件：** 无（纯验证）

- [ ] **步骤 1：机器 diff 核验 footprint**

运行：
```bash
git diff --stat 5f59fb8..HEAD -- internal/ docs/
```
预期：**仅** `internal/sidecar/kernel/engine.go`、`internal/sidecar/kernel/engine_test.go`、
`docs/runbooks/performance-baselines.md` 三个文件（`docs/superpowers/` 的 spec/plan 另计）。

- [ ] **步骤 2：核验内核敏感面逐字未变**

运行：
```bash
git diff 5f59fb8..HEAD -- internal/sidecar/kernel/cache.go
git diff 5f59fb8..HEAD -- internal/sidecar/authz/ internal/controlplane/effperm/
```
预期：**两条均输出空**（缓存实现、批量既有调用方零改）。

运行：
```bash
git diff 5f59fb8..HEAD -- internal/sidecar/kernel/engine.go | grep -E '^[-+]' | grep -vE '^(\+\+\+|---)'
```
预期：改动**全部落在 `BatchEnforce` 函数体与其注释内**；
`Enforce`/`EnforceEx`/`ApplySnapshot`/`ApplyDelta`/`applyPolicyChange`/`addRule`/`removeRule`/
`GetImplicitRolesForUser`/`New` **均无一行出现在 diff 中**。

- [ ] **步骤 3：全量回归**

运行：
```bash
go build ./... && go test ./... 2>&1 | tail -30; echo "EXIT=${PIPESTATUS[0]}"
```
预期：`EXIT=0`，零 FAIL。**`internal/sidecar/authz`（BatchCheck 调用方）与
`internal/controlplane/effperm`（BatchEnforce 调用方）必须全绿**——它们是本改动的真实下游。

- [ ] **步骤 4：Commit（若前三步无改动则跳过）**

本任务纯验证，通常无文件改动 → 无 commit。若核验中发现问题，修复后单独 commit。

---

### 任务 M55B-7：收尾

**文件：** 无（汇报）

- [ ] **步骤 1：汇总实测提升**

对照 `docs/runbooks/performance-baselines.md` 的新旧数，用**真实数字**汇报：
- 优化前：2,130,430 ns/op（~42µs/决策）
- 优化后：`<实测>` ns/op（`<实测>` /决策）
- 提升倍数：`<实测算得>`

- [ ] **步骤 2：确认 M55B-1..6 全部复选框已勾**

- [ ] **步骤 3：向用户汇报，并请示是否 push origin**

> 铁律：push origin 须每次取得用户显式授权。**不得自行 push。**

---

## 自检

**1. 规格覆盖度**（逐节对照 `2026-07-16-sydom-m55-batchenforce-cache-design.md`）：

| 规格章节 | 落地任务 |
|---|---|
| §2 根因（源码核实） | 已在 M55B-4 注释中落为代码内文档 |
| §5 实现 | M55B-4 步骤 1 |
| §5.1 陷阱（`e.ce.Enforce` vs `e.Enforce`） | M55B-1 的 T3 + M55B-4 注释 ⚠️ 段 |
| §5.2 逐字保留行为（空输入 nil / 部分结果+err） | M55B-1 的 T5 + M55B-4 注释与代码 |
| §5.3 注释更新 | M55B-4 步骤 1 |
| §6 T1–T5 + 变异验证 | T2/T3/T5→M55B-1；T4→M55B-2；T1→M55B-3；变异 A/B→M55B-4 步骤 5 |
| §7 验证（go test / 零触碰 diff / 基准 / 手册） | M55B-5、M55B-6 |
| §8 明确排除 | M55B-6 步骤 2 以机器 diff 强制 |

无遗漏。

**2. 占位符扫描**：
`<ns>`/`<B>`/`<allocs>`/`<倍数>`/`<实测>` 是**待实测填入的槽位**，非占位符缺陷——
每处均配了取数命令（M55B-5 步骤 1）并明写「不得臆造」。**这是刻意的**：基准数必须来自真实运行。

**3. 类型一致性**：
- `countingCache` 定义于 M55B-3 步骤 2，仅由同任务 T1 使用；方法 `stats()`/`reset()`/`newCountingCache()` 命名前后一致。
- `mgrSnapshot(version uint64)`、`Delta`/`PolicyChange`/`ChangeRemove`/`Rule`、`New(domain, cache.Cache, applier)`、
  `ErrForeignDomain`/`ErrNotReady` 均为**既有**构件（已读源核实，见 `types.go`/`engine.go`/`engine_test.go:21`）。
- M55B-2 步骤 3 的时序陷阱（变异在改写前无感）已显式标注并改派到 M55B-4 步骤 5 执行——
  **这是自检抓到的真问题**：若按朴素 TDD 在 M55B-2 就做变异验证，会得到「变异了但测试仍绿」的假象，
  并可能被误读为「T4 没牙」而错误地删掉这条最重要的测试。
