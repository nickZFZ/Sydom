# M5.5 BatchEnforce 决策缓存复用 — 设计规格

日期：2026-07-16
状态：已批准，待实现
里程碑：M5.5（性能）收尾片

## 1. 背景与动机

M5.5 基准（`ccab3a7`）实测出数据面热路径的一个量化缺口（Ryzen 9 7900X / Go 1.26.3）：

| 路径 | 耗时/决策 |
|---|---|
| `Enforce` 缓存命中 | **137 ns** |
| `BatchEnforce`（50 条） | **~42 µs**（整批 ~2.1 ms） |

差距约 **300×**。当时那一片的范围是「只测量不改核」，故记为非阻断的优化候选。本片兑现它。

用户已显式放行触碰授权核心（`internal/sidecar/kernel`）。

## 2. 根因（casbin v3.10.0 源码核实）

铁律「casbin 论断先回源核实」——以下每条均已读源码确认，非推测：

1. **`SyncedCachedEnforcer` 未覆写 `BatchEnforce`**（`enforcer_cached_synced.go` 全文无此方法）。
   故 `e.ce.BatchEnforce(...)` 经嵌入提升解析为
   `SyncedEnforcer.BatchEnforce`（`enforcer_synced.go:219`）→ `e.m.RLock()` → `Enforcer.BatchEnforce`（`enforcer.go:951`）
   → 循环 `e.enforce("", nil, request...)`。**全程不读缓存、不写缓存。**

2. **缓存只挂在 `Enforce` 上**（`enforcer_cached_synced.go:60`）：
   `getKey` → `getCachedResult` → miss → `SyncedEnforcer.Enforce`（真实求值）→ `setCachedResult`。

3. **缓存键在单条与批量之间可安全共享**：
   - `Enforcer.Enforce(rvals...)` = `e.enforce("", nil, rvals...)`
   - `Enforcer.BatchEnforce` 每行 = `e.enforce("", nil, request...)`

   **同一 matcher `""`、同一求值路径**。`GetCacheKey(params...)` 仅把字符串参数以 `$$` 拼接
   （非字符串参数返回 `ok=false`；本内核入参恒为 `[]string` → 恒 ok）。
   故同一四元组 (sub,dom,obj,act) 在批量与单条下产生**逐字相同的键** → 两者互相暖缓存，且语义正确。

## 3. 方案选型

### 选定：方案 A — `kernel.BatchEnforce` 内循环调 `e.ce.Enforce`

复用 casbin 自身的缓存 `Enforce` 路径，**零新增缓存逻辑**。

**理由（决定性的一条）**：此处是授权内核，最好的代码是没写的代码。方案 A 把批量路径接到
生产单条 Check 每天在跑的同一条 casbin 路径上——缓存的 get/set/键推导/失效全部复用既有实现。

### 否决：方案 B — 外层持一次 RLock + 手写缓存 + 调底层不加锁 Enforce

- **直接踩 `engine.go:212-215` 已文档化的死锁地雷**：`GetLock()` 返回的正是同一把 `e.m`，
  嵌套读锁在 apply 写锁到来时触发 Go RWMutex 递归读锁死锁。
- 需穿透两层嵌入破坏库封装。

### 否决：方案 C — 探缓存 → 未命中子集走一次 BatchEnforce → 合并

- 需内核自持 cache 引用 + 手写 casbin 键协议（平行实现 = 微妙不一致的经典来源）。
- **并不兑现看似买到的原子性**：命中来自探测时刻、未命中来自求值时刻，照样撕裂。
  更多代码换一个不存在的保证。

### 否决：方案 D — version 前后比对的乐观并发重试

同样是**假原子性**：`ApplySnapshot` 是 `ClearPolicy → 重建 → InvalidateCache → version.Store`，
重建期间 version 仍为旧值但策略已撕裂——该窗口 `engine.go:84-85` 已明文接受。
拿复杂度换心理安慰。YAGNI。

## 4. 关键取舍：批次原子性（已与用户明确确认）

**方案 A 放弃一个现存属性**：当前整批在一把 RLock 下求值，一批 50 行看到同一策略版本。
改为逐行 `Enforce` 后，一次 apply 可能插在行间 → 批次跨版本撕裂（前 3 行 v7、后 47 行 v8）。

**判定：可接受。** 依据：

1. **撕裂 ≠ 不一致**：每行答案都是「某个近期版本的正确答案」，无任何一行拿到伪造结果。
2. **`BatchCheck` 语义上等价于 50 次 `Check`**（`authorizer.go:80-89` 仅是用 pin 域组装四元组）。
   客户端今天发 50 次单独 Check 本就这样撕裂。批量只是省 49 个 RTT 的**传输层优化**，
   为它坚持一个**语义层保证**是在保护从未许诺过的东西。
3. **原子性从不是承诺的契约**：`engine.go:226-231` 详述了批量语义（含越域差异），
   通篇未提原子性——它是从 casbin 实现偶然继承的。而 `engine.go:84-85` 已明文接受「新鲜度滞后」。
4. **撤权及时性一字未动**（安全相关的真不变量）：apply → `InvalidateCache()` 全量清 →
   之后任何探测必然 miss → 按新策略重算。方案 A 用的是**同一个缓存、同一次清空**。

> 上述第 4 条不以论证结案，以**行为测试**证明（见 §6 测试 T4）。

## 5. 实现

**生产改动 footprint：`internal/sidecar/kernel/engine.go` 一个函数 + 其文档注释。**

```go
func (e *Engine) BatchEnforce(reqs [][]string) ([]bool, error) {
	if !e.ready.Load() {
		return nil, ErrNotReady
	}
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

### 5.1 必须绕开的陷阱（钉死在测试里）

`engine.go:227-229` 明文契约：批量接口**不逐条校验越域**，外域行经 matcher 自然不命中 → `false`；
而单条 `kernel.Engine.Enforce`（engine.go:59-61）显式返回 `ErrForeignDomain`。

⚠️ **必须调 `e.ce.Enforce`（casbin 的），绝不能调 `e.Enforce`（内核的）**——
后者会让外域行从 `false` 变成报错，破坏已文档化的契约。测试 T3 专钉此点。

### 5.2 刻意逐字保留的行为

对齐 casbin `Enforcer.BatchEnforce` 现行行为，因 `authorizer.go:88` 直接透传，本片不顺手改错误语义：

- **空输入返回 `nil`**（非 `[]bool{}`）：故用 `var results []bool` + `append`，不用 `make([]bool, 0, n)`。
- **出错返回已算出的部分结果 + err**（非 `nil, err`）。

### 5.3 文档注释更新

`engine.go:230-231` 现有表述「casbin 的 BatchEnforce 直调底层 enforce、绕过决策缓存……
故批量鉴权不享缓存命中，高频批量调用需自行权衡」在本片后**成为假话**，须改写为新事实
（含批次可跨版本撕裂的明示，及与单条共享缓存键的说明）。

## 6. 测试（TDD，每条须有齿）

测试落 `internal/sidecar/kernel/engine_test.go`（`package kernel` 内部包，可直接用未导出构件）。

T1 的计数 cache：测试本地定义 `countingCache`，内嵌 `newBoundedCache(1024)` 并在 `Get` 上累加
`hits`/`misses`（`cache.ErrNoSuchKey` 即 miss），经 `New(dom, c, nil)` 注入——
`New` 的 `c cache.Cache` 形参既有，无需改签名。

| # | 测试 | 变异验证（证明有齿） |
|---|---|---|
| T1 | **缓存被复用**：注入 `countingCache`，同一批 N 条连跑两次；第二次须恰产生 N 次命中、0 次未命中 | 还原为 `e.ce.BatchEnforce` → 命中 0 → FAIL |
| T2 | **决策不变**：batch 结果 == 同元组逐条 `Enforce` 结果（allow/deny 混合） | 保证循环改写未改判定 |
| T3 | **越域行仍返 `false` 而非报错**（钉 §5.1 陷阱） | 把 `e.ce.Enforce` 写成 `e.Enforce` → FAIL |
| T4 | **版本变更下缓存正确**：batch → `ApplyDelta` 撤规则 → batch → **须翻 false** | 摘掉 `InvalidateCache` → FAIL |
| T5 | **空输入返回 `nil`**（钉 §5.2） | 改用 `make([]bool,0,n)` → FAIL |

T4 是本片的核心证明——「缓存正确性在版本变更下必须严格证明」。

未就绪 fail-close 由既有 `TestEngine_BatchEnforce_NotReady` 覆盖（不动）。

## 7. 验证

- `go test ./...` EXIT 0（全库回归；`authz`/`effperm` 为 `BatchEnforce` 既有调用方，须全绿）。
- **零触碰核验**：机器 diff 确认改动仅限 `engine.go` 的 `BatchEnforce` 函数体 + 其注释；
  `Enforce`/`ApplySnapshot`/`ApplyDelta`/`cache.go` 逐字不变。
- **基准对照**：现成 `test/bench/kernel_bench_test.go` 的 `BenchmarkBatchEnforce`
  每轮跑同一批 reqs，正好测暖路径。预期 ~2.1 ms → ~10 µs 量级。
- 实测数更新进 `docs/runbooks/performance-baselines.md`（含 M5.5 原始基线的对照，
  及 §4 撕裂取舍的运维说明）。

## 8. 明确排除

- 不改 `authorizer.go`（透传不动）、不改 `effperm`（瞬态引擎、非共享，无并发 apply）。
- 不改缓存实现 `cache.go`（容量/LRU 策略不动）。
- 不改 `Enforce`/`EnforceEx`/apply 路径。
- 不引入新的缓存层、不改缓存容量 1024。

## 9. 任务编号

M55B-1..7（详见实现计划）。
