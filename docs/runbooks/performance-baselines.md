# 授权决策性能基线

数据面每次 Check 经 sidecar → `kernel.Engine.Enforce`（casbin `SyncedCachedEnforcer` + 有界 LRU 1024）。本手册记决策内核基线，供容量规划与回归对照。

## 跑法

```
go test -run=^$ -bench=. -benchmem ./test/bench/
```

## 基线（本机实测，仅数量级参考）

> 环境：AMD Ryzen 9 7900X（12 核/24 线程）/ Go 1.26.3 linux/amd64 / 单机非隔离。绝对值随机器变，**用 benchstat 做相对对照，勿追绝对值**。

| 基准 | ns/op | B/op | allocs/op | 含义 |
|---|---|---|---|---|
| Enforce 缓存命中 | 137 | 184 | 8 | 生产主路径（同元组复命中 LRU） |
| Enforce 未命中 rules=10 | 23,110 | 21,564 | 359 | 冷 matcher，小策略 |
| Enforce 未命中 rules=100 | 203,768 | 207,361 | 3,455 | 冷 matcher，中策略 |
| Enforce 未命中 rules=1000 | 2,297,526 | 2,063,118 | 34,407 | 冷 matcher，大策略（O(rules)） |
| BatchEnforce 50（缓存命中） | 7,522 | 9,324 | 404 | 批量 50 条/次；M5.5 优化后复用决策缓存 |

**观察**：
- 缓存命中（137 ns）比未命中 rules=100（203,768 ns）快 **~1,487 倍** → 生产在高命中率下走快路径；**性能高度依赖缓存命中率**。
- 未命中随 rules 10→100→1000 近线性增（23µs→204µs→2.3ms）→ 策略规模是冷成本主因；未命中成本高（rules=100 时 204µs/3,455 allocs）。
- **BatchEnforce 50 已复用决策缓存**（M5.5 优化，见 `docs/superpowers/specs/2026-07-16-sydom-m55-batchenforce-cache-design.md`）：原 2.13ms（~42µs/决策，casbin `BatchEnforce` 直调底层 enforce 绕过缓存）→ 现 7,522 ns/op（**~283× 提升**）。内核逐行调 casbin 缓存 `Enforce`，与单条**共享缓存键**（同 matcher `""`）→ 批量与单条互相暖缓存。
- **运维注意（批次跨版本撕裂）**：批量不再共享单一 RLock，一次策略 apply 可能插在行间，使一批中前后行分属相邻两个版本。**这不是不一致**——每行都是某个近期版本的正确答案，且 `BatchCheck` 语义上等价于 N 次 `Check`。**撤权及时性不受影响**（apply 全量清缓存 → 后续必按新策略重算）。

## 容量估算（粗略）

单核缓存命中 ≈ 137 ns/op → ≈ **7.3M Check/s/core**（**理论上界**：真实含 gRPC 编解码 + freshness 检查 + 网络往返，且 pod 多核并行；实际以端到端压测下修）。**关键**：容量强依赖缓存命中率——低命中率（大量唯一 subject/object）会跌到未命中量级（rules=100 时 ~4.9K Check/s/core）。运维应监控 `sydom_*cache_hits/miss`（M5.1 obs）保命中率。

## 回归对照

改动后：
```
go test -run=^$ -bench=. -benchmem -count=10 ./test/bench/ > new.txt
benchstat old.txt new.txt     # 看 delta 与显著性
```

## 改授权核心的边界

任何改 casbin/kernel/dataperm/authz 的优化，**须先有本基线（old.txt）**，改后 `benchstat` 证不劣化，并对基准做变异实验证其能捕获劣化（如临时增/减策略遍历看 ns/op 是否随之变）。基准本身不改核（`test/bench` 独立包，内核字节不变）。
