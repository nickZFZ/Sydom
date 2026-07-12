# M5.5 授权决策性能基准 — 设计规格

> M5「运维就绪 + 生产硬化」末子项目（M5.5 性能）。BASE=main `5b9d36d`（M5.3 收官）。部署链已就绪，本切片给数据面决策热路径立性能基准 + 容量指导 + 回归对照手段。

## 1. 背景与目标

司域数据面每次 SDK 授权都经 sidecar：gRPC `Server.Check` → `Authorizer.Check` → **`kernel.Engine.Enforce`（casbin `SyncedCachedEnforcer`）**。`Enforce` 是决策主成本（matcher 求值 + 角色图 `g()` 遍历），且内建有界 LRU 决策缓存（`New` 默认容量 1024）。sidecar 容量/延迟由此路径决定。当前**无任何性能基准**——无从做容量规划或回归对照。

**目标**：
1. 给内核决策热路径立 Go benchmark：**缓存命中**（生产主路径）、**冷 matcher 随策略规模伸缩**（10/100/1000 rules）、**批量**。
2. 捕获真实基线数（ns/op、B/op、allocs/op）写入手册，给容量规划（Check/s per core）。
3. 提供可复跑的 benchmark 供将来回归对照（`go test -bench` / benchstat）。

**非目标（明确排除）**：
- **改授权核心做优化**：本切片只**测量+立基线**，不动 casbin/kernel 逻辑（零触碰铁律）。若基线暴露热点，另开优化切片（届时有基准护体）。
- 自动 CI 性能门（benchstat 阈值 gate）：需 CI 集成，留后续；本切片交付可复跑基准 + 手册对照法。
- 端到端 gRPC/网络压测（wrk/ghz 打 sidecar 进程）：受机器/网络噪声大，留后续；本切片测**决策内核**（确定性、可复现、定位主成本）。
- `Authorizer.Check`/`dataperm` 过滤基准：`Enforce` 是主成本，freshness/filter 是薄层；本切片聚焦内核，Authorizer 层留后续纵深。

## 2. 现状（实查）

- `kernel.New(domain, c cache.Cache, applier DataPolicyApplier) (*Engine, error)`：`c=nil`→内部有界 LRU 1024；`applier=nil`→noop。
- 种子：`Engine.ApplySnapshot(kernel.Snapshot{Version, Rules:[]kernel.Rule{{Ptype, V:[6]string}}})`。
- 决策：`Engine.Enforce(sub,dom,obj,act)(bool,error)`、`Engine.BatchEnforce(reqs [][]string)([]bool,error)`。
- `Rule{Ptype string; V [6]string}`、`Snapshot{Version uint64; Rules []Rule; DataPolicies []DataPolicy}` 均导出。
- 内建缓存：`SyncedCachedEnforcer` 以请求元组为 key；同元组二次 Enforce 命中缓存。

## 3. 方案

**基准放独立包 `test/bench`（`package bench`），经公开 API 构建+种子+跑**——**内核包字节不变**（保零触碰铁律：`git diff -- internal/sidecar/kernel ...` 空）。`internal/sidecar/kernel` 在模块内可被 `test/bench` import。

## 4. 设计

### 4.1 文件

| 文件 | 职责 |
|---|---|
| `test/bench/kernel_bench_test.go`（新） | 决策基准：缓存命中 / 冷 matcher 伸缩 / 批量 + 种子助手 |
| `docs/runbooks/performance-baselines.md`（新） | 跑法 + 真实基线数 + 容量规划 + 回归对照法 |

### 4.2 基准集（`test/bench/kernel_bench_test.go`）

种子助手 `seedEngine(rules int) *kernel.Engine`：建 `rules` 条 `p`（`role_i,dom,obj_i,read,allow`）+ 一条 `g`（`user,role_0,dom`）；`ApplySnapshot` 就绪。

- `BenchmarkEnforce_CacheHit`：`seedEngine(100)`，`b.N` 次 `Enforce("user","dom","obj_0","read")` 同元组 → **缓存命中热路径**（生产主路径）。
- `BenchmarkEnforce_MissScaling`：子基准 `rules=10/100/1000`，每次 `Enforce("u"+i, "dom","obj_0","read")`（唯一 sub → **缓存未命中**，matcher 对每条 p 评 `g()` → 全遍历）→ 显 O(rules) 伸缩与未命中成本。
- `BenchmarkBatchEnforce`：`seedEngine(100)`，`BatchEnforce` 50 条混合元组 → 批量吞吐。

各基准 `b.ReportAllocs()`；`b.ResetTimer()` 在种子后。

### 4.3 手册（`docs/runbooks/performance-baselines.md`）

跑法（`go test -bench=. -benchmem ./test/bench/`）、**真实基线表**（本机实测 ns/op·B/op·allocs/op，注明 CPU/Go 版本/非严格隔离仅数量级参考）、容量估算（缓存命中 ns/op → 单核 Check/s ≈ 1e9/ns_per_op，注 pod 多核 + gRPC/网络开销另计）、回归对照法（`go test -bench -count=10 > new.txt; benchstat old.txt new.txt`）、优化边界声明（改核前须先有基线，改后 benchstat 证不劣化 + 变异实验证基准有齿）。

## 5. 验证

- `go test -bench=. -benchmem ./test/bench/` 跑通、产 ns/op（含缓存命中 << 未命中、未命中随 rules 增长）。
- **基准有意义（缓存确实生效）**：断言/观察 CacheHit ns/op 显著低于 Miss（rules=100）——若相近则缓存未生效或基准无效。
- **零触碰**：`git diff 5b9d36d..HEAD -- casbin/ adminauthz/ internal/sidecar/{kernel,dataperm,authz}/ internal/auth/ internal/obs/` = 空。
- `go test ./...` EXIT 0（新增 bench 包不破坏；benchmark 不随普通 `go test` 跑，`-run=^$ -bench` 才跑）。

## 6. 验收标准（M55-1..6）

- **M55-1** 零触碰授权核心：上述 diff = 空（基准在 `test/bench` 独立包）。
- **M55-2** 三基准（缓存命中 / 未命中伸缩 10·100·1000 / 批量）`go test -bench` 跑通产数。
- **M55-3** 缓存有效：CacheHit ns/op 显著低于 Miss(100)（数量级差），佐证内建 LRU 生效、生产热路径快。
- **M55-4** 伸缩可见：Miss ns/op 随 rules 10→1000 单调增（O(rules) 遍历）。
- **M55-5** 手册：跑法 + 真实基线表 + 容量估算 + 回归对照法 + 改核前须基线的边界声明。
- **M55-6** `go test ./...` EXIT 0（bench 包合法、不干扰常规测试）。

## 7. 风险

- **基准噪声**：单机非隔离，绝对值仅数量级参考；手册注明用 benchstat + `-count` 做相对对照，不追绝对值。
- **未命中基准的 deny 短路**：唯一 sub 无 `g` 绑定 → 每条 p 仍评 `g()`（无链返 false）后 deny，仍全遍历（非首条短路），O(rules) 成立；手册注明测的是「未命中 + 全策略遍历」worst-case。
- **缓存伸缩干扰**：唯一 sub 保证每次未命中；有界 LRU churn 的插入成本远小于 matcher 评估，不失真。
