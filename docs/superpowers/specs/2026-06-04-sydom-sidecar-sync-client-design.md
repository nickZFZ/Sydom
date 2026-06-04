# 司域 · Sidecar 同步客户端 (④-3) 详细设计

> 版本：v0.1 | 日期：2026-06-04 | 状态：草稿

## 1. 范围与定位

Sidecar 内部结构（④）的第 3 个子项目。把 Sidecar 接成控制面 PolicySync 的 **gRPC 客户端**：持续订阅策略变更、做版本对账、必要时拉全量快照，并把结果喂给 ④-1 内核 `Engine`（内核再 fan-out：功能策略进 casbin、数据策略进 ④-2 dataperm 表）。

**上游契约**：③ 控制面 `api/proto/sydom/sync/v1/policy_sync.proto`（`PolicySync.Subscribe` server-streaming + `PullSnapshot` unary）。
**依赖**：④-1 `internal/sidecar/kernel`（`Engine` 的 `ApplySnapshot`/`ApplyDelta`/`Version`/`Ready` + 哨兵错误）；② `internal/auth`（HMAC 客户端凭据）。
**交付边界**：与 ③/④ 一致，**仅库/组件层，不出 cmd/binary**。进程装配（构造 Engine+Table+Filter+SyncClient、起 goroutine、配置/主密钥）留给后续 cmd 子项目。

**不在范围**：④-4 鉴权 API（调 `Enforce`/`FilterSQL` + fail-open/close 阈值策略）；TLS 证书管理（Config 留 `Secure` 开关，dial 凭据由调用方给）；分块流式快照（YAGNI，64MB unary 足够，对齐 gRPC spec §8）。

## 2. 设计决策（头脑风暴逐条确认）

1. **范围 = 同步协程**（数据面摄入），鉴权 API 归 ④-4。
2. **陈旧可用 + 暴露陈旧信号**：断连/apply 失败期间内核保留最后一份 good 状态继续服务（可用性优先，对齐 ③-2 spec「期间用旧快照鉴权」）；④-3 暴露 `engine.Ready()`/`engine.Version()` + 自身 `LastSyncAt()`/`Connected()`，fail-open/close 阈值策略交 ④-4/调用方（不在 ④-3）。
3. **引导顺序：先 PullSnapshot 再 Subscribe**。启动先显式拉快照建基线（`ApplySnapshot`→`Ready=true`，空策略 app 也达「已同步」而非永远 fail-close deny-all），再 `Subscribe(last=快照版本)`。窗口期 gap 由服务端版本对账（`current>last`→`SnapshotRequired`）重拉兜底，不依赖服务端冷启动推送。
4. **delta gap → 重拉快照**（一致性铁律）：仅当 `delta.Version == Version()+1`（连续）才增量 apply；任何 gap（`> Version()+1`）立即重拉全量快照，**绝不 apply 非连续 delta**（那会静默跳过中间版本的变更 = 不一致）。内核 `ApplyDelta` 本身容忍跳版（信任 ④-3 已决策），**版本连续性判定归 ④-3**（对齐 ④-1 spec §10）。
5. **fail-close**：所有 pull/translate/apply 错误一律不推进版本、退避后重拉，绝不部分应用、绝不放行。

## 3. 组件分解

包 `internal/sidecar/syncclient`（避开 stdlib `sync` 命名冲突；白盒测试同包）。

| 文件 | 职责 | 依赖 |
|---|---|---|
| `config.go` | `Config`（连接/认证/退避参数）+ 默认值 | — |
| `translate.go` | syncv1 proto → 内核域类型（**反向**于 cp translate）：snapshot/delta/rule/op/datapolicy（消费 effect） | gen/sydom/sync/v1、kernel |
| `client.go` | `SyncClient`：gRPC 拨号 + `Run(ctx)` 对账循环 + 重连退避 + 暴露状态 | gen、kernel、auth、translate |
| `backoff.go` | 有界指数退避 + 抖动（小工具） | — |

`SyncClient` 持：`cfg Config`、`engine *kernel.Engine`、连接态（`lastSyncAt atomic`、`connected atomic.Bool`）。**驱动注入的 `*kernel.Engine`**，不反向依赖内核内部。

## 4. 翻译层（translate，syncv1 → 域类型）

反向于 cp 的 `translate`（cp 是 域→syncv1，本包是 syncv1→域）。纯函数：

- `snapshotFromProto(*syncv1.Snapshot) (kernel.Snapshot, error)`：`Version`（uint64 直传）；`Rules` 由 `ruleFromProto` 逐条；`DataPolicies` 由 `dataPolicyFromProto` 逐条。
- `deltaFromProto(*syncv1.Delta) (kernel.Delta, error)`：`Version`；`PolicyChanges` 由 `policyChangeFromProto`；`DataChanges` 由 `dataPolicyChangeFromProto`。
- `ruleFromProto(*syncv1.PolicyRule) kernel.Rule`：`Ptype`；`values`（变长 ≤6）拷进 `Rule.V[6]`，缺位补 `""`（与 cp 裁尾空串互逆）。`len(values)>6` → 错误（fail-close）。
- `opFromProto(syncv1.ChangeOp) (kernel.ChangeOp, error)`：`ADD→ChangeAdd`/`REMOVE→ChangeRemove`/`UPDATE→ChangeUpdate`；`UNSPECIFIED`/未知 → 错误（fail-close）。
- `policyChangeFromProto`：`Op` + `Rule`(ADD/UPDATE) + `OldRule`(REMOVE/UPDATE)。
- `dataPolicyFromProto(*syncv1.DataPolicy) kernel.DataPolicy`：`ID/SubjectType/SubjectID/Resource/Condition` + **`Effect`**（刚打通；空串透传，内核/dataperm 兜底归一）。
- `dataPolicyChangeFromProto`：`Op` + `Policy`。

翻译错误（变长越界/未知 op）一律返 error，由对账循环当作"该笔不可用 → 重拉快照"处理（绝不部分应用）。

## 5. 对账状态机（核心，client.go）

`Run(ctx) error`：阻塞式循环，调用方 goroutine 之；内部自管重连。`ctx` 取消即干净退出。

### 5.1 引导（每次连接/重连）
```
1. bootstrap: snap = PullSnapshot(ctx)              // 失败 → 退避重试
2. ks, err = snapshotFromProto(snap)                // 失败 → 退避重拉
3. engine.ApplySnapshot(ks)                         // 失败(含 ErrForeignDomain) → 退避重拉
4. 记 lastSyncAt = now; connected = true
5. stream = Subscribe(ctx, last=engine.Version())   // 失败 → 重连(退避)
6. 进入事件循环 §5.2
```

### 5.2 事件循环（`for { ev = stream.Recv() }`）
- **Recv 错误**（流断/连接错）：`connected=false`；退避后回 §5.1 重连。`ctx` 取消则返回 nil。
- **Delta(d)**：
  - `d.Version ≤ engine.Version()` → 丢弃（重放/重复），刷新 lastSyncAt。
  - `d.Version == engine.Version()+1` → `deltaFromProto` + `engine.ApplyDelta`；成功记 lastSyncAt；`ApplyDelta` 返 `ErrStaleVersion` → 丢弃（竞态，已被并发推进）；其它 apply/translate 错误 → 重拉(§5.3)。
  - `d.Version > engine.Version()+1` → **gap → 重拉(§5.3)**。
- **Heartbeat(cv)**（反熵）：`cv > engine.Version()` → 漏包 → 重拉(§5.3)；否则刷新 lastSyncAt（流活性证明）。
- **SnapshotRequired** → 重拉(§5.3)。

### 5.3 重拉 helper（resync）
`PullSnapshot → snapshotFromProto → engine.ApplySnapshot → 记 lastSyncAt`。任一步失败 → 退避后再试（仍在当前连接的事件循环里；若是连接错则升级为 §5.1 重连）。重拉成功后继续消费同一流（后续 delta 经版本单调自然对齐）。

### 5.4 重连退避
有界指数退避 + 全抖动（`backoff.go`）：`initial=500ms, factor=2, max=30s`（Config 可覆盖）。断连期间内核状态不变（陈旧可用），`lastSyncAt` 冻结 → 陈旧度随时间增长，供 ④-4 读。

## 6. 错误处理与 fail-close（对齐 gRPC spec §6 + 铁律）

| 场景 | 处理 |
|---|---|
| PullSnapshot RPC 失败 | 退避重试；内核状态不变（冷启动则仍 `Ready=false`→Enforce fail-close deny） |
| snapshot/delta 翻译失败 | 当作该笔不可用 → 重拉；绝不部分应用 |
| ApplySnapshot/ApplyDelta 失败（含 ErrForeignDomain） | 内核已 fail-close（不改 version/ready）→ ④-3 重拉对齐 |
| ApplyDelta ErrStaleVersion | 丢弃该 delta（重放/竞态），非错误 |
| delta 版本 gap | 重拉快照（§5.3），不 apply 非连续 delta |
| Heartbeat 报告版本超前 | 漏包 → 重拉 |
| 流断 / Recv 错 | `connected=false` → 退避重连 → 重新引导 |
| ctx 取消 | 干净退出 Run，返回 nil |

SyncClient **不做任何鉴权决策**，只喂内核；最终 fail-close 由 ④-4 在 `!Ready()` 时拒绝。

## 7. 暴露状态（供 ④-4 自定阈值）

- 透传内核：`engine.Ready()`（至少同步过一次）、`engine.Version()`。
- 自身：`LastSyncAt() time.Time`（最后一次成功 apply 或 heartbeat 的时刻，原子读）、`Connected() bool`（订阅流是否在线）。

④-4/调用方据 `now - LastSyncAt()` 与 `Connected()` 自定 fail-open/close 阈值——**阈值策略不在 ④-3**（决策 2）。

## 8. 配置

```go
type Config struct {
	Endpoint string        // 控制面 PolicySync 地址
	AppID    string        // app_key（HMAC 认证标识 + 流路由）
	Secret   []byte        // HMAC 密钥（调用方从配置/解密提供）
	Secure   bool          // 传输层是否 TLS（false=insecure，证书 dial 选项由调用方给）
	DialOptions []grpc.DialOption // 可选附加 dial 选项（TLS 凭据等）
	BackoffInitial, BackoffMax time.Duration // 退避参数（零值用默认 500ms/30s）
}
```
认证：`auth.NewPerRPCCredentials(cfg.AppID, cfg.Secret, cfg.Secure)` 经 `grpc.WithPerRPCCredentials` 注入；`maxRecvMsgSize` 设 64MB（容纳全量快照，对齐 gRPC spec §8）。请求体不带 app_id（服务端从认证上下文取，架构强隔离）。

## 9. 测试策略（纯内存，无 Docker）

- **translate 纯单测**：syncv1→域 各方向（snapshot/delta/rule 变长补齐/op 映射/datapolicy 含 effect）；未知 op、values 越界 → 错误。
- **client 用 bufconn + fake PolicySync 服务端**（仿 ③-2 bufconn 套路），脚本化发 SyncEvent，配真实 `kernel.Engine`（注入真实 `dataperm.Table`）：
  - 引导：PullSnapshot→Ready/Version 推进；空快照也 Ready=true。
  - 稳态连续 delta → 增量 apply、Version 单调。
  - **gapped delta → 断言触发重拉**（PullSnapshot 被再次调用，Version 跳到快照版本）。
  - **heartbeat 超前 → 断言重拉**；heartbeat 平 → 仅刷新 lastSyncAt。
  - SnapshotRequired → 重拉。
  - 流错 → 重连退避 → 重新引导。
  - ErrStaleVersion delta → 丢弃不报错。
  - **端到端**：快照带一条 `effect=deny` 数据策略 → 经内核 fan-out 到 dataperm → `Filter.FilterSQL` 反映 deny（打通 ④-2/④-3/effect 链）。
  - ctx 取消 → Run 干净返回。
- **退避**：单测退避序列有界 + 抖动范围。
- 全程 `-race`（对账写 vs 暴露状态读并发）。

## 10. 自检结果

- **占位符扫描**：无 TODO/待定；组件/状态机/错误矩阵均具体。
- **内部一致性**：决策 3（pull-first）贯穿 §5.1；决策 4（gap→重拉）贯穿 §5.2/§6；决策 2（暴露状态）贯穿 §7；effect 消费（§4）对齐刚合并的 effect 链。
- **范围检查**：单一关注点（同步客户端），单计划可covered；④-4/cmd 明确划出。
- **模糊性检查**：引导顺序、gap 判定、heartbeat 反熵、重拉 helper 复用点、lastSyncAt 更新时机均已明确。

相关：gRPC 协议 `2026-05-31-sydom-grpc-sync-protocol-design.md`；③-2 同步服务 `2026-06-01-sydom-control-plane-sync-service-design.md`（服务端对账语义）；④-1 内核 `2026-06-03-sydom-sidecar-kernel-design.md`（域类型/apply 语义/版本判定归属）；④-2 dataperm + effect 专项（effect 消费端）；[[feedback-consistency-over-simplicity]]。
