# 司域 控制面 · 同步下发服务 (③-2 Sync Service) 详细设计

> 厘定辖域，权归其位
>
> 版本：v0.1 | 日期：2026-06-01 | 状态：草稿
>
> 上游：[整体架构设计](2026-05-30-sydom-architecture-design.md) | 关联：[gRPC 同步协议契约](2026-05-31-sydom-grpc-sync-protocol-design.md)、[控制面策略核心引擎 ③-1](2026-06-01-sydom-control-plane-policy-core-design.md)

---

## 1. 范围与定位

本文档是控制面第 3 个子项目（控制面）的第 2 个子模块 **③-2 同步下发服务**的详细设计。它在每个控制面副本内：① 实现已定义的 gRPC `PolicySync` 服务端（`gen/sydom/sync/v1`）；② 经 Redis Pub/Sub 把策略变更扩散给各副本持有的 Sidecar 订阅流；③ 把 ③-1 产出的领域 `cp.Delta` 翻译为 syncv1 proto 消息；④ 实现版本号对账续传与全量快照拉取。

**进程拓扑**：控制面多副本无状态进程。每副本内同时运行 ③-1 写引擎、③-2 同步服务（、将来 ③-3 管理 API）。③-2 在每副本内跑 PolicySync gRPC 服务端，持有一批 Sidecar 的 `Subscribe` 长连，并订阅 Redis 全局频道把 Delta 路由到本地持有该 app 流的 Sidecar。

**不在本文档范围内**（留给后续 spec）：
- Sidecar 侧 apply：delta → casbin 高层 API、MemoryAdapter、`InvalidateCache`、本地快照降级鉴权（→ ④ Sidecar spec）。
- 写编排：「PolicyManager 写成功后调用 Publisher 发布」的串联（→ ③-3 管理 API）。
- 管理 API / 管理鉴权 / `application.status` 生命周期拦截（→ ③-3）。
- 主密钥轮转 / KMS 选型（→ 运维 spec）。
- 分块流式快照（YAGNI，64MB unary 快照足够，见 gRPC spec §8）。

---

## 2. 设计决策（头脑风暴逐条确认）

1. **广播集成边界**：③-2 提供 `broadcast.Publisher`（`cp.Delta → Redis`），**不触碰 ③-1 的 PolicyManager**。「写成功后调用 `Publish`」的编排由 ③-3 管理 API 负责（它持有 PolicyManager）。③-1 保持零网络 / 纯 DB，单元可独立测试。
2. **广播消息内容**：发布**完整 Delta**（翻译为 syncv1），不发"仅通知"。依据：③-1 决策②"无独立 delta 重放表"，DB 无逐版本变更历史，"仅通知 + 副本回查 DB"无法算出单版本增量，只能退化为每次拉全量快照。完整自包含 Delta：副本收到直接转发；丢包（at-most-once）由版本对账 + 心跳 + 全量快照兜底，与"DB 真相源"不冲突（Delta 即刚落库的真相）。
3. **Redis 频道拓扑**：**单一全局频道** `sydom:policy:delta` + 副本本地按 `app_id` 路由。消息体携带 `{app_id, 翻译后的 syncv1.Delta}`。副本维护 `app_id → 本地 streams` 注册表，只转给持有该 app 流的本地 Sidecar，无本地流则丢弃。简单、无动态订阅管理；代价是每副本解码所有 app 的消息（MVP 规模可接受，分片/每-app 频道为后续优化）。
4. **慢消费者背压**：每流一个**有界缓冲** channel。溢出时不阻塞：丢弃后续增量、流内推 `SnapshotRequired(reason="overflow")` 让该 Sidecar 主动 `PullSnapshot` 全量对齐，**连接保留**。发完 SnapshotRequired 后 hub 不维护"降级态"，照常 best-effort 继续喂后续 Delta——一致性权威判定在 Sidecar 侧版本号校验。慢流被隔离，不阻塞 Redis 读协程与其他流。
5. **心跳版本来源**：每 app 在内存维护最后下发的 version（收到 Delta 时更新），心跳直接用；为防漏广播偏差，心跳跳满时或每 N 轮做一次轻量 `SELECT current_version` 兑正。

---

## 3. 组件分解

均在 `internal/controlplane/` 下，依赖 ③-1 既有包。依赖方向无环：`policysync → {translate, store, broadcast, auth}`；`broadcast → {translate, cp}`；`translate → {cp, gen/syncv1}`；`store`（③-1 扩只读函数）。

| 包 | 职责 | 依赖 |
|---|---|---|
| `internal/controlplane/translate` | `cp.Delta` ↔ syncv1 proto 双向翻译（纯函数，无 DB/网络） | cp、gen/sydom/sync/v1 |
| `internal/controlplane/store`（扩 ③-1） | 新增**只读**快照函数：`ReadAppDataPolicies`、`ReadCurrentVersion`（不加锁）、`ResolveAppIDByKey`（app_key→id） | cp |
| `internal/controlplane/broadcast` | `Publisher`（Delta→Redis 发布）+ `Subscriber`（订阅→解码→回调）。Redis 客户端用 `github.com/redis/go-redis/v9` | translate、cp |
| `internal/controlplane/policysync` | PolicySync gRPC 服务端实现 + `Hub`（app_id→streams 注册表 + 有界 fan-out）+ 版本对账 + 心跳 ticker + 服务端组装（拦截器/maxRecvMsgSize） | translate、store、broadcast、auth、gen/sydom/sync/v1 |

---

## 4. 数据流

```
写路径（③-3 编排，本文档不实现编排）：
  PolicyManager.Write → cp.Delta → broadcast.Publisher.Publish(ctx, delta)
                                     → Redis PUBLISH sydom:policy:delta  {app_id, syncv1.Delta}

扇出路径（每副本常驻）：
  broadcast.Subscriber 收消息 → 解码 {app_id, syncv1.Delta}
                              → Hub.Dispatch(app_id, syncEvent) → 本地持有该 app 的各 Subscribe 流（有界缓冲）→ Sidecar

冷启动 / 拉取路径：
  Sidecar PullSnapshot → 只读事务读 current_version + casbin_rule + data_policy
                       → translate.RulesToProto / DataPoliciesToProto → Snapshot 返回

订阅路径：
  Sidecar Subscribe(last_applied) → 解析 app_id → 读 current_version
    current == last_applied → 注册流，续推后续 Delta
    current >  last_applied → 流内先发 SnapshotRequired(reason="behind"/"reconnect")，再注册续推
```

---

## 5. 关键机制

### 5.1 翻译层（translate，纯函数）

- `DeltaToProto(d cp.Delta) *syncv1.Delta`：
  - `version int64 → uint64`（③-1 用 int64 对齐 DB BIGINT，proto 用 uint64）。
  - `d.RuleAdds[] → PolicyChange{op: CHANGE_OP_ADD, rule: PolicyRule}`。
  - `d.RuleRemoves[] → PolicyChange{op: CHANGE_OP_REMOVE, old_rule: PolicyRule}`（casbin 行只有增/删，无 UPDATE）。
  - `cp.Rule.V [6]string → PolicyRule.values`：裁掉尾部连续空串（贴 casbin 变长 `[]string` 风格），`ptype` 直传。
  - `d.DataChanges[] → DataPolicyChange{op, policy}`：`cp.ChangeAdd/ChangeUpdate/ChangeRemove → CHANGE_OP_ADD/UPDATE/REMOVE`；`cp.DataPolicy{ID,SubjectType,SubjectID,Resource,Condition} → syncv1.DataPolicy`（id int64→uint64）。
- `RulesToProto([]cp.Rule) []*syncv1.PolicyRule`、`DataPoliciesToProto([]cp.DataPolicy) []*syncv1.DataPolicy`：供 `PullSnapshot` 用。

### 5.2 快照只读（store 扩展）

`PullSnapshot` 在**单个只读事务**（`BeginTx(ctx, &sql.TxOptions{ReadOnly:true})`）内依次读，保证 version 与 rules/data_policies 取自同一一致快照（避免读到"半个新版本"）：

1. `ResolveAppIDByKey(ctx, tx, appKey) (int64, error)`：auth 注入的是 app_key 字符串，须先解析为 `application.id`；无行返错（fail-close）。
2. `ReadCurrentVersion(ctx, tx, appID) (int64, error)`：普通 `SELECT current_version`，**不加 FOR UPDATE**（只读路径不串行化写）。
3. `ReadAppRules(ctx, tx, appID)`（③-1 已有）。
4. `ReadAppDataPolicies(ctx, tx, appID) ([]cp.DataPolicy, error)`：新增，`SELECT id, subject_type, subject_id, resource, condition FROM data_policy WHERE app_id=$1`。

### 5.3 版本对账续传（policysync，对齐 gRPC spec §5）

- `Subscribe(SubscribeRequest{last_applied_version})`：
  1. 取 app_id（`auth.AppIDFromContext`）→ `ResolveAppIDByKey` → `ReadCurrentVersion`。
  2. `current == last_applied`：注册流到 Hub，续推后续 Delta。
  3. `current > last_applied`（含冷启动 last=0 而 current>0）：流内先发 `SnapshotRequired(reason="behind"，重连场景用 "reconnect")`，再注册续推。
  4. `current < last_applied`：异常（Sidecar 领先于控制面），按 `SnapshotRequired(reason="behind")` 兜底纠偏（fail toward reconcile）。
- 副本不校验 Delta 序号——Sidecar 侧负责检测 `version` 跳变并自行 `PullSnapshot`（spec §5 丢包检测）。副本只负责"尽量送达 + 落后提示"。

### 5.4 心跳

每个 Subscribe 流一个 ~30s ticker，推 `Heartbeat{current_version}`（反熵，spec §8）。`current_version` 取自该 app 的内存维护值（收到 Delta 时更新）；为防漏广播偏差，跳满或每 N 轮触发一次轻量 `ReadCurrentVersion` 兑正。

### 5.5 Hub（注册表 + 有界 fan-out）

- 结构：`map[int64]map[*subStream]struct{}`（appID → 该 app 的本地流集合）+ `sync.RWMutex`。每 `subStream` 持一个**有界缓冲** channel + 一个写协程把 channel 内事件写入 gRPC stream。
- `Register(appID, stream)` / `Unregister(appID, stream)`：随 Subscribe 流的建立/结束（ctx 取消、Sidecar 断开）调用，释放缓冲与写协程，不泄漏 goroutine。
- `Dispatch(appID, event)`：`RLock` 取该 app 流集合，对每流的缓冲非阻塞 `select { case ch <- event: default: 溢出处理 }`。溢出 → 推 `SnapshotRequired(reason="overflow")`，丢弃后续增量但保流（见决策 4）。
- Redis 读协程单线程 `Dispatch`，慢流不回压它（非阻塞写）。

### 5.6 认证

复用 ② 的 `auth.StreamServerInterceptor(resolver)` / `auth.UnaryServerInterceptor(resolver)`，`resolver` 注入 ③-1 的 `secret.Resolver`（解密 `app_secret_enc`）。app_id 强制取自 `auth.AppIDFromContext`，请求体无 app_id（架构 I2 强隔离：业务系统无法越权订阅/拉取其他 app）。服务端 `maxRecvMsgSize`/`maxSendMsgSize` 设 64MB（spec §8，容纳全量快照）。

---

## 6. 错误处理与容灾（对齐 gRPC spec §6 + fail-close 铁律）

| 场景 | ③-2 服务端行为 |
|---|---|
| 认证失败 | 拦截器返 `UNAUTHENTICATED`（② 已实现统一错误消息，防 app 注册枚举） |
| Redis 发布失败（Publisher） | 返错给调用方（③-3）；**不影响 DB 写**（写已提交）。丢的广播由 Sidecar 心跳/对账兜底；记日志告警 |
| Redis 订阅断连（Subscriber） | 自动重连；重连期间漏的消息由各 Sidecar 心跳对账补（at-most-once 允许丢） |
| `PullSnapshot` DB 读失败 | 返 `Unavailable`/`Internal`；Sidecar 退避重试，期间用旧快照鉴权（④ 侧降级） |
| Subscribe 流 ctx 取消 / Sidecar 断开 | Hub 注销该流、释放缓冲与写协程，不泄漏 |
| 未知 app_key（`ResolveAppIDByKey` 无行） | 返错（fail-close，不返回空快照/空流） |
| Hub 缓冲溢出 | 流内发 `SnapshotRequired(reason="overflow")`，保流，best-effort 续推（决策 4） |

fail-close 是默认安全姿态：异常路径默认不放行/不返回空集，对齐 casbin 内核 `enforce()` 出错返回 `(false, err)`。

---

## 7. 测试策略

- **translate**：纯函数单测（无 Docker）——增删映射、尾部空串裁剪、int64↔uint64、各 `ChangeOp`、空 Delta。
- **store 新读函数 + 快照一致性**：testcontainers PG（复用 `internal/dbtest`）——`ReadAppDataPolicies`、`ReadCurrentVersion` 不加锁、`ResolveAppIDByKey` 命中/未命中、只读事务内三读一致。
- **broadcast**：testcontainers Redis（go-redis/v9）——发布→订阅往返、消息编解码、断连重连。
- **policysync**：bufconn 起服务端（复用 ② bufconn 套路）+ 内存 fake broadcast——冷启动发 SnapshotRequired、稳态推 Delta、心跳、溢出转对账、并发多流、流注销不泄漏、认证强制 app_id。
- **端到端**：testcontainers PG+Redis，`Publish → Subscribe → Hub → bufconn Subscribe 流` 收到翻译后的 Delta；`PullSnapshot` 返回一致快照。

---

## 8. 新增依赖

- `github.com/redis/go-redis/v9`：Redis 客户端（Pub/Sub）。
- 测试：testcontainers-go redis 模块（复用既有 testcontainers-go）。

---

## 9. 边界与未决（留给后续 spec）

- **Sidecar 侧 apply**：delta → casbin 高层 API、`autoNotifyWatcher=false`、`InvalidateCache`、本地快照降级 → ④ Sidecar spec。
- **写编排**：「PolicyManager 写完调 Publisher」串联 + `application.status` 停用态写拦截 → ③-3 管理 API。
- **`DataPolicyChange` REMOVE 的载荷**：③-1 的 `cp.Delta` 中 `ChangeRemove` 仅携带 ID；若 Sidecar 数据权限引擎需按 (subject,resource) 索引定位，translate/③-1 需在删前回查补全 subject/resource。本文档按"id 定位删除"（proto `DataPolicy.id` 语义）实现，待 ④ 数据权限引擎定稿后回看。
- **频道分片 / 每-app 频道**：单全局频道的规模优化，YAGNI。
- **主密钥轮转 / KMS 选型** → 运维 spec。

---

*下一步：本子模块进入实现计划（writing-plans）；之后是 ③-3 管理 API、④ Sidecar、⑤ SDK。*
