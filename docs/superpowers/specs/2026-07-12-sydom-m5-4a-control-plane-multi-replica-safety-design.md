# M5.4a 控制面多副本安全（Relay 选主）设计规格

**里程碑：** M5.4 高可用（HA）· 第一片（M5.4a）

**BASE：** `main` @ `272a806`（M5.3b 完结 + demo/gitignore 卫生清理之后）。

---

## 1. 目标与范围

让控制面可安全横向扩到 **N 个副本**（HA + 水平扩展的地基），只补齐当前唯一的多副本正确性硬缺口：**outbox relay 无协调**。

**唯一改动面 = 新增选主组件 + 控制面装配一行接线 + 集成测试（+ 一条低基数 obs gauge）。** 授权求值核心（`casbin/`、`adminauthz/`、`internal/sidecar/{kernel,dataperm,authz}/`）**零触碰**。`outbox.RunRelayLoop` / `drainOnce` 的 drain 逻辑**逐字不改**。

**明确不在本片范围**（留 M5.4 其它切片）：
- **M5.4b**：优雅故障转移 —— SIGTERM 优雅关闭 drain policysync 流、sidecar 断连重连到另一副本、滚动升级就绪门控。
- **M5.4c**：PG/Redis 基础设施 HA —— 连接池 failover 感知、Redis pub/sub 断线重订阅、PG failover 重试、托管 HA（sentinel/patroni/operator）文档。
- **K8s/Helm 多副本清单**（属 TODO-M5.3-k8s 切片）。
- 单 drainer 吞吐优化 / relay 并行分担（YAGNI：策略写低频，drain 非瓶颈）。

---

## 2. 现状与缺口（代码实查）

控制面的多副本机器**已大体就绪**，实查结论：

| 面 | 现状 | 多副本安全？ |
|---|---|---|
| 写路径版本 bump | `policy/manager.go:runVersionedWrite` 首步 `store.LockAppVersion`（`SELECT current_version FROM application WHERE id=$1 FOR UPDATE`）**行锁串行化本 app**；outbox delta 经 `sink.Persist` 落**同一事务** | ✅ 跨副本并发写由 DB 行锁串行、delta 事务性，无内存写协调假设 |
| 快照服务 | `policysync/server.go:PullSnapshot` 全部从 DB/store 读（`ResolveAppIDByKey`/`ReadCurrentVersion`/`ReadAppRules`/`ReadAppDataPolicies`） | ✅ 无状态、任一副本服务正确快照 |
| 实时增量扇出 | `app/run.go:131` 每副本 `RunDispatchLoop` 订阅 Redis pub/sub（`broadcast` 包）→ 各自 `hub.Dispatch` 给本副本连着的 sidecar | ✅ 每副本只扇出给自己的连接，Redis 广播到全副本 |
| 持久投递 | `outbox` 事务性 outbox：写事务内落 delta，独立 relay drain→publish→标记 `published_at` | ✅ at-least-once 持久 |
| **outbox relay drain** | `app/run.go:130` **每个副本都** `launch("relay", RunRelayLoop(...))` drain 同一张 `policy_outbox`（`ORDER BY id ASC`，无 leader / 无 `FOR UPDATE SKIP LOCKED` / 无 advisory lock） | ❌ **N 副本 = 每条 delta 发布 N 次 + 并发 drainer 破坏顺序投递** |

**唯一缺口** = relay drain 在多副本下无协调。「relay 单点」的真身不是缺副本，而是**多副本下无协调**：现每副本都跑 drain，扩容即重复/乱序发布。

---

## 3. 目标设计（advisory-lock 选主）

### 3.1 机制

只让**一个**副本在任一时刻跑 outbox drain 循环，用 **PostgreSQL 会话级 advisory lock 选主**：

- 从连接池取**一条专用长连接** `conn, _ := db.Conn(ctx)`（**不可**用普通池连接——池会回收/归还该连接从而释放会话锁）。
- 在该连接上 `SELECT pg_try_advisory_lock(<key>)`：
  - 返回 `true` ⟹ 本副本成为 **leader**，跑现有 drain 循环。
  - 返回 `false` ⟹ 本副本是 **follower**，按 `relay_poll_interval` 空转重试 `pg_try_advisory_lock`。
- leader 进程崩溃 / 网络断 / 连接死 ⟹ 该会话结束，PG **自动释放** advisory lock ⟹ 某 follower 下次 `pg_try_advisory_lock` 抢到 ⟹ 接管（自动 failover，无需显式租约续期）。
- 专用连接自身探测：若连接 `Ping`/`pg_try_advisory_lock` 报错（连接失效），leader 主动放弃领导权（取消 drain 子 ctx）、丢弃该连接、重建后重新参选。

### 3.2 key 选择

固定常量 advisory-lock key（全局单 leader，与现有单 drainer 语义一致）。取 `pg_try_advisory_lock(bigint)` 的 key = 一个写死的 64 位常量（如 `hashtext('sydom:policy_outbox_relay')` 的结果值写死为字面量，或直接选一个仓库内唯一的固定整数并注释其含义）。同一 PG 实例内本 key 专属 relay 选主，不与他用冲突。

### 3.3 组件与接口（隔离清晰）

**新增小包 `internal/controlplane/leader`**，单一职责 = 「持有 advisory lock 期间运行回调」：

```
// Run 参选 advisory-lock 领导权；抢到锁后以一个「领导期有效」的子 ctx 调用 onElected，
// 失去锁 / 连接失效则取消该子 ctx 并重新参选。阻塞至 ctx 取消。
func Run(ctx context.Context, db *sql.DB, key int64, poll time.Duration,
        onElected func(leaderCtx context.Context) error) error
```

- `onElected` 里跑的就是**原样** `outbox.RunRelayLoop(leaderCtx, db, pub, poll)`——**drain 逻辑逐字不改**（`ORDER BY id ASC` / break-on-failure / at-least-once / `published_at` 标记全保留）；leader 只决定它**何时**跑。
- `leader` 包只依赖 `database/sql` 与标准库，不依赖 outbox/broadcast（可独立测试）。

**装配接线（`internal/controlplane/app/run.go`）**：把
```
launch("relay", func() error { return outbox.RunRelayLoop(runCtx, db, pub, cfg.RelayPollInterval) })
```
改为
```
launch("relay", func() error {
    return leader.Run(runCtx, db, relayLeaderKey, cfg.RelayPollInterval,
        func(lctx context.Context) error { return outbox.RunRelayLoop(lctx, db, pub, cfg.RelayPollInterval) })
})
```
`dispatch`/`subscribe`/写路径/快照服务**全副本照跑不变**——领导权只门控 relay drain。

### 3.4 可观测性

M5.1 `internal/obs` registry 加**低基数** gauge `sydom_relay_leader`（本副本为 leader=1，否则 0）；leader 变更时更新。运维可断言「全集群恰好一个 leader」。经既有 obs 注入缝，不新增全局态。

---

## 4. 关键决策与默认

- **会话级 `pg_try_advisory_lock`（非事务级 `pg_advisory_xact_lock`）**：领导权须跨多次 drain 持续持有，事务级锁事务结束即释放不适用。会话级锁随连接生命周期，进程死即自动释放 = 天然 failover。
- **专用连接持锁**：绝不从共享池借连接持长命锁（池回收即释放锁 → 静默双 leader）。`db.Conn` 独占一条并长持。
- **`pg_try_advisory_lock`（非阻塞 try）而非 `pg_advisory_lock`（阻塞）**：follower 用非阻塞 try 轮询，避免一条连接永久阻塞在锁等待里、且轮询间隔可控、可随时响应 ctx 取消。
- **复用 `relay_poll_interval`**：follower 参选轮询与 leader drain 轮询同一配置项，不新增配置。
- **drain 逻辑零改**：`outbox.RunRelayLoop`/`drainOnce` 逐字不动（M54A-5 diff 证），杜绝把「多副本协调」与「投递语义」耦合。
- **fail-close 优先一致性**：无 leader 时宁可积压不投，绝不重复/乱序（见 §5）。

---

## 5. 故障语义与 fail-close

- **正常**：写在任一副本（行锁串行 bump 版本 + 同事务落 outbox delta）→ leader drain outbox → Redis pub → **所有**副本 dispatch → 各自 hub → 其 sidecar。
- **leader 崩溃**：会话锁自动释放 → follower 抢到接管，从 `published_at IS NULL` 继续 drain 积压 → **无丢**；已标记 published 的不再发 → **无重**。
- **全副本崩溃 / 无人持锁**：无 drain，delta **积压在 `policy_outbox`（持久）**；leader 回归后 at-least-once 补投。**绝不丢、绝不乱序**——最坏是延迟投递。sidecar 按自身 `max_staleness` 继续服务上次快照（既有行为），执行路径 fail-close 不变。
- **恰好一次投递**由「唯一 leader drain + `published_at` 幂等标记」共同保证：即便 failover 瞬间新旧 leader 极短重叠，`UPDATE … SET published_at=now() WHERE id=$1` 幂等 + 单 leader 稳态使重复窗口趋零；下游 sidecar 版本对账对偶发重复亦幂等吸收（纵深防御）。
- 与 carry-forward 约束一致：**DB 真相源**（outbox 是投递真相）、**fail-close 全贯穿**（HA 分区下宁可延迟不放行/不乱序）。

---

## 6. 验证策略

Go 集成测试为权威（复用 `internal/dbtest`），可复现胜过一次性演示：

1. **争锁 → 恰一个 leader**：两个 `leader.Run` 并发参选同 key，断言任一时刻恰好一个回调在跑（leader gauge / 计数）。
2. **恰好一次投递（有齿）**：两个 relay（各经 `leader.Run` 包裹）+ 共享 outbox，写若干 delta，断言 Redis（或 fake Publisher）**每条 delta 恰收一次**（非 2×）。**变异实验**：临时撤去 leader 门（两 relay 都直接 drain）→ 测试须见 2× FAIL，证测试有齿。
3. **failover 连续性**：leader 持锁 drain，关闭其持锁连接（模拟崩溃）→ follower 抢到接管 drain 积压，断言**无丢无重、顺序不变**。
4. **并发写安全**：两「副本」并发对同一 app 写 → 版本严格单调无碰撞、每版本恰一条 delta（证 `LockAppVersion` 行锁串行有效）。
5. （可选）把 compose demo 控制面 `scale=2` 作端到端演示（非权威）。

`go test ./...` EXIT 0。

---

## 7. 不变量 / 验收关卡 M54A-1..7

- **M54A-1 零触碰授权核心**：`git diff 272a806..HEAD -- casbin/ adminauthz/ internal/sidecar/{kernel,dataperm,authz}/` = 空。
- **M54A-2 恰好一次投递**：多 relay 争锁下每 delta 发布一次；变异实验（撤 leader 门）证测试有齿见 2× FAIL。
- **M54A-3 failover 连续性**：杀 leader → follower 接管 drain 积压，无丢、无重、顺序不变。
- **M54A-4 并发写安全**：2 副本并发写同 app → 版本单调无碰撞、每版本恰一条 delta。
- **M54A-5 单 drainer 语义保留**：`outbox.RunRelayLoop` / `drainOnce` 内容 diff = 0（drain 逻辑逐字未改）。
- **M54A-6 leader 可观测**：`sydom_relay_leader` gauge 存在、低基数、随领导权变更。
- **M54A-7 全绿**：`go test ./...` EXIT 0。

---

## 8. 文件清单

| 文件 | 改动 |
|---|---|
| `internal/controlplane/leader/leader.go`（新增） | advisory-lock 选主：`Run(ctx, db, key, poll, onElected)`，专用连接 `pg_try_advisory_lock`、失锁取消领导期 ctx、重建重选。 |
| `internal/controlplane/leader/leader_test.go`（新增） | 争锁单 leader、failover 接管、（配合）恰好一次投递的选主侧断言。 |
| `internal/controlplane/app/run.go`（修改） | `launch("relay", …)` 由裸 `RunRelayLoop` 改为 `leader.Run(…, RunRelayLoop)`；定义 `relayLeaderKey` 常量；leader gauge 接线。 |
| `internal/controlplane/outbox/relay_test.go`（新增/扩充） | 恰好一次投递（有齿变异实验）、failover 连续 drain 积压。 |
| `internal/obs/*`（微调） | 加 `sydom_relay_leader` gauge 向量 + 类型化助手（低基数，nil-safe）。 |
| `internal/controlplane/app/run.go` 或 config（若需） | 复用 `relay_poll_interval`，预期无新配置项。 |

> 注：`outbox/relay.go` 的 `RunRelayLoop`/`drainOnce` **不改逻辑**（仅可能被测试新增覆盖）；authz 求值核心零触碰。
