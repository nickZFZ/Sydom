---
name: feedback-consistency-over-simplicity
description: "For Sydom permission system, permission inconsistency is never acceptable — always favor correctness over simplification in any design tradeoff"
metadata: 
  node_type: memory
  type: feedback
  originSessionId: db698ca2-37fc-4821-8130-b63a6c379204
---

在司域（Sydom）权限系统设计中，**"权限不一致绝不可接受"是用户明确定下的底线**。

**Why:** 用户在 2026-05-30 的架构评审中明确说"权限不一致绝不可接受"。权限系统里偶发的不一致（撤权后还能访问、授权后不生效）是致命的，比性能/简化重要得多。

**How to apply:** 凡是遇到"简化/性能 vs 一致性"的取舍，一律倒向一致性。具体已落地的几条硬性规则：
- 异常路径默认 **fail-close**（无可用策略快照即拒绝），对齐 casbin `enforce()` 出错返回 `(false, err)` 的语义。
- 策略广播用 Redis Pub/Sub 提速，但 **DB（带单调版本号）是唯一真相源**，Redis at-most-once 丢消息由版本号对账兜底（Sidecar 发现版本不连续就 `LoadFilteredPolicy` 对齐）。绝不拿 Redis 当真相源。
- **决策缓存失效只能全量 `InvalidateCache()`**，不能依赖 casbin `CachedEnforcer` 的按 key 删（RBAC 角色间接性下 key 对不上，撤权不生效），也不能依赖 TTL 做一致性（TTL=N 秒 ≡ 允许 N 秒不一致）。TTL 仅作内存上界。

相关：[[feedback-verify-casbin-before-asserting]]
