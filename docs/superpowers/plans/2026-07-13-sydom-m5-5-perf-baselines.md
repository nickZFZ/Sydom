# M5.5 授权决策性能基准 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 给内核决策热路径立 Go benchmark（缓存命中/冷 matcher 伸缩/批量）+ 捕获真实基线写入手册，供容量规划与回归对照。

**架构：** 基准在独立包 `test/bench`（经公开 `kernel.New`/`ApplySnapshot`/`Enforce`/`BatchEnforce` 构建+种子）→ **内核字节不变（零触碰）**；手册记跑法 + 实测基线 + 容量估算 + 回归对照法。

**技术栈：** Go `testing.B` benchmark、`internal/sidecar/kernel` 公开 API、`go test -bench -benchmem`。

**BASE：** `feat/m5-5-perf-baselines` @ 含设计规格提交；规格 `docs/superpowers/specs/2026-07-13-sydom-m5-5-perf-baselines-design.md`。

**零触碰铁律：** `git diff 5b9d36d..HEAD -- casbin/ adminauthz/ internal/sidecar/kernel internal/sidecar/dataperm internal/sidecar/authz internal/auth internal/obs` 必须为空。

---

## 任务 1：决策基准（缓存命中 / 冷 matcher 伸缩 / 批量）

**文件：**
- 创建：`test/bench/kernel_bench_test.go`

- [ ] **步骤 1：写基准文件**

`test/bench/kernel_bench_test.go`：
```go
package bench

import (
	"fmt"
	"testing"

	"github.com/nickZFZ/Sydom/internal/sidecar/kernel"
)

const benchDom = "dom"

// seedEngine 建 rules 条 p(role_i,dom,obj_i,read,allow) + 一条 g(user,role_0,dom)，就绪。
func seedEngine(tb testing.TB, rules int) *kernel.Engine {
	tb.Helper()
	e, err := kernel.New(benchDom, nil, nil)
	if err != nil {
		tb.Fatal(err)
	}
	rs := make([]kernel.Rule, 0, rules+1)
	for i := 0; i < rules; i++ {
		rs = append(rs, kernel.Rule{Ptype: "p", V: [6]string{
			fmt.Sprintf("role_%d", i), benchDom, fmt.Sprintf("obj_%d", i), "read", "allow", "",
		}})
	}
	rs = append(rs, kernel.Rule{Ptype: "g", V: [6]string{"user", "role_0", benchDom, "", "", ""}})
	if err := e.ApplySnapshot(kernel.Snapshot{Version: 1, Rules: rs}); err != nil {
		tb.Fatal(err)
	}
	return e
}

// 缓存命中热路径：同元组重复 Enforce（生产主路径）。
func BenchmarkEnforce_CacheHit(b *testing.B) {
	e := seedEngine(b, 100)
	if allow, err := e.Enforce("user", benchDom, "obj_0", "read"); err != nil || !allow {
		b.Fatalf("预热应 allow：allow=%v err=%v", allow, err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = e.Enforce("user", benchDom, "obj_0", "read")
	}
}

// 冷 matcher 随策略规模伸缩：唯一 sub 每次未命中，对每条 p 评 g() → 全遍历。
func BenchmarkEnforce_MissScaling(b *testing.B) {
	for _, n := range []int{10, 100, 1000} {
		b.Run(fmt.Sprintf("rules=%d", n), func(b *testing.B) {
			e := seedEngine(b, n)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _ = e.Enforce(fmt.Sprintf("u%d", i), benchDom, "obj_0", "read")
			}
		})
	}
}

// 批量吞吐：一次 BatchEnforce 50 条混合元组。
func BenchmarkBatchEnforce(b *testing.B) {
	e := seedEngine(b, 100)
	reqs := make([][]string, 0, 50)
	for i := 0; i < 50; i++ {
		reqs = append(reqs, []string{"user", benchDom, fmt.Sprintf("obj_%d", i%100), "read"})
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = e.BatchEnforce(reqs)
	}
}
```

- [ ] **步骤 2：跑基准，捕获真实数**

运行：`go test -run=^$ -bench=. -benchmem ./test/bench/ | tee /tmp/bench.txt`
预期：三基准产出 ns/op（`BenchmarkEnforce_CacheHit`、`BenchmarkEnforce_MissScaling/rules=10|100|1000`、`BenchmarkBatchEnforce`）；无 FAIL。记下各 ns/op（步骤 3 手册用）。

- [ ] **步骤 3：核验缓存有效 + 伸缩可见**

从 `/tmp/bench.txt` 观察：
- `CacheHit` ns/op **远低于** `MissScaling/rules=100`（数量级差 → 内建 LRU 生效、生产热路径快）。
- `MissScaling` ns/op 随 `rules` 10→100→1000 **单调增**（O(rules) 遍历）。
若 CacheHit ≈ Miss 或伸缩不单调，停下排查（基准无效/缓存未生效）。

- [ ] **步骤 4：零触碰核验**

运行：`git diff 5b9d36d..HEAD -- casbin/ adminauthz/ internal/sidecar/kernel internal/sidecar/dataperm internal/sidecar/authz internal/auth internal/obs`
预期：**空**（基准在 test/bench 独立包）。

- [ ] **步骤 5：Commit**

```bash
git add test/bench/kernel_bench_test.go
git commit -m "test(perf): M5.5 内核决策基准(缓存命中热路径/冷 matcher 随 rules 10·100·1000 伸缩/批量 50;独立 test/bench 包经公开 API 保内核零触碰)"
```

---

## 任务 2：性能基线手册 + 最终验收

**文件：**
- 创建：`docs/runbooks/performance-baselines.md`

- [ ] **步骤 1：写手册（填入步骤 2 的实测数）**

`docs/runbooks/performance-baselines.md`（用任务 1 步骤 2 的真实 ns/op 填「基线」表；下方为骨架，`<...>` 换实测值）：
````markdown
# 授权决策性能基线

数据面每次 Check 经 sidecar → `kernel.Engine.Enforce`（casbin `SyncedCachedEnforcer` + 有界 LRU 1024）。本手册记决策内核基线，供容量规划与回归对照。

## 跑法

```
go test -run=^$ -bench=. -benchmem ./test/bench/
```

## 基线（本机实测，仅数量级参考）

> 环境：`<CPU>` / Go `<ver>` / 单机非隔离。绝对值随机器变，**用 benchstat 做相对对照，勿追绝对值**。

| 基准 | ns/op | B/op | allocs/op | 含义 |
|---|---|---|---|---|
| Enforce 缓存命中 | `<..>` | `<..>` | `<..>` | 生产主路径（同元组复命中 LRU） |
| Enforce 未命中 rules=10 | `<..>` | | | 冷 matcher，小策略 |
| Enforce 未命中 rules=100 | `<..>` | | | 冷 matcher，中策略 |
| Enforce 未命中 rules=1000 | `<..>` | | | 冷 matcher，大策略（O(rules)） |
| BatchEnforce 50 | `<..>` | | | 批量 50 条/次 |

**观察**：缓存命中比未命中(100) 快 `<N>` 倍 → 生产在高命中率下走快路径；未命中随 rules 线性增 → 策略规模是冷成本主因。

## 容量估算（粗略）

单核缓存命中 ≈ `<ns>` ns/op → ≈ `1e9/<ns>` Check/s/core（**上界**：真实含 gRPC 编解码 + freshness + 网络，pod 多核并行）。容量规划以此为量级起点，按实测下修。

## 回归对照

改动后：
```
go test -run=^$ -bench=. -benchmem -count=10 ./test/bench/ > new.txt
benchstat old.txt new.txt     # 看 delta 与显著性
```

## 改授权核心的边界

任何改 casbin/kernel/dataperm/authz 的优化，**须先有本基线（old.txt）**，改后 `benchstat` 证不劣化，并对基准做变异实验证其能捕获劣化（如临时增/减策略遍历看 ns/op 是否随之变）。基准本身不改核（`test/bench` 独立包，内核字节不变）。
````

- [ ] **步骤 2：最终验收**

运行：
```bash
go test -run=^$ -bench=. -benchmem ./test/bench/    # 复跑确认稳定产数
go build ./... && go vet ./test/bench/
git diff 5b9d36d..HEAD -- casbin/ adminauthz/ internal/sidecar/kernel internal/sidecar/dataperm internal/sidecar/authz internal/auth internal/obs | head; echo "ZERO-TOUCH-DONE(空)"
go test ./test/bench/ 2>&1 | tail -2    # 常规 go test：无 Benchmark 跑，包应 ok(no test)
```
预期：基准产数；build/vet 干净；零触碰空；`test/bench` 常规 `go test` 通过（ok/无测试函数）。

- [ ] **步骤 3：Commit**

```bash
git add docs/runbooks/performance-baselines.md
git commit -m "docs(runbook): M5.5 授权决策性能基线手册(实测 ns/op 基线表+缓存命中 vs 未命中数量级+容量估算+benchstat 回归对照+改核须先基线边界;M5 收官)"
```

---

## 自检

**1. 规格覆盖度：** §4.1 文件→任务1(基准)+任务2(手册)；§4.2 基准集→任务1步1；§4.3 手册→任务2步1；§5 验证→任务1步2/3/4+任务2步2；§6 M55-1..6→M55-1 任务1步4、M55-2 任务1步2、M55-3/4 任务1步3、M55-5 任务2步1、M55-6 任务2步2。全覆盖。

**2. 占位符扫描：** 基准/命令为实代码；手册 `<..>` 是**待实测填入的基线数占位**（任务2步1明确「填入步骤2实测数」），非计划缺陷。

**3. 类型一致性：** `kernel.New(domain string, cache.Cache, DataPolicyApplier)(*Engine,error)`（传 nil,nil）、`kernel.Rule{Ptype string; V [6]string}`、`kernel.Snapshot{Version uint64; Rules []Rule}`、`Engine.ApplySnapshot(Snapshot)error`、`Engine.Enforce(sub,dom,obj,act string)(bool,error)`、`Engine.BatchEnforce([][]string)([]bool,error)` 均与实查一致；`seedEngine(tb testing.TB, rules int)*kernel.Engine` 任务1定义、三基准一致调用。
