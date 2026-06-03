# 司域 Sidecar 鉴权内核（④-1）详细设计

> 子项目 ④「Sidecar 内部结构」第 1 切片。日期 2026-06-03。
> 上游契约：③ 控制面 PolicySync gRPC（`api/proto/sydom/sync/v1/policy_sync.proto`）。
> 架构前提：`docs/superpowers/specs/2026-05-30-sydom-architecture-design.md` §3/§5/§6/§7。

## 0. ④ 的切分与本切片定位

「Sidecar 内部结构」横跨 5 个职责清晰、可独立测试的层，各走独立 spec→plan→实现周期：

| 切片 | 职责 | 网络 |
|---|---|---|
| **④-1 鉴权内核（本文）** | casbin model + MemoryAdapter + SyncedCachedEnforcer + 有界缓存 + applyDelta/applySnapshot 原子编排 + InvalidateCache 铁律 + fail-close | 无（纯内存，纯单测） |
| ④-2 数据权限引擎（库） | 条件树求值 + 主体角色展开 + 参数化渲染（sql/orm/raw）+ DataPolicy 内存表 | 无 |
| ④-3 同步客户端（传输） | gRPC PolicySync 客户端 + Subscribe 流 + 版本对账 + PullSnapshot + HMAC + proto→域翻译 | 接 ③ |
| ④-4 鉴权 API 服务端（入站） | /check /filter /batch-check | localhost |
| ④-5 进程装配（二进制） | 配置 + wiring + 优雅关闭 + metrics | — |

④-1 是 ④-2/3/4 的共同依赖根。与 ③ 一致，本切片**仅交付库/组件层，不出 cmd/binary**。

## 1. 目标与非目标

**目标**：把「单 app（单 casbin domain）的功能权限内存状态 + 鉴权计算 + 原子策略 apply」封装为一个纯内存、可完全单测的内核 `Engine`，供上层（④-3 驱动 apply、④-4 调 Enforce、④-2 取主体角色与接收 DataPolicy）使用。

**非目标（明确不在本切片）**：
- gRPC 客户端 / Subscribe 流 / 版本对账 / PullSnapshot / HMAC（→ ④-3）。
- syncv1 proto ↔ 域类型翻译（→ ④-3；本切片只定义内核域类型）。
- 条件树求值与方言渲染（→ ④-2；本切片只定义 `DataPolicyApplier` 注入接口并原子路由）。
- 鉴权 API server / HTTP / 序列化（→ ④-4）。
- 配置解析、进程生命周期、metrics（→ ④-5）。

## 2. casbin 能力对照（回源核实，见 §13）

| 能力 | casbin 实现 | ④-1 用法 |
|---|---|---|
| 决策执行 + 线程安全 + 决策缓存 | `SyncedCachedEnforcer`（内嵌 `*SyncedEnforcer` + `cache.Cache`） | 内核核心，`Enforce(sub,dom,obj,act)` |
| 批量鉴权 | `SyncedEnforcer.BatchEnforce([][]interface{})` | `Engine.BatchEnforce` |
| 增量改策略 | `AddPolicies/RemovePolicies/UpdatePolicy`（g 段自动 `BuildIncrementalRoleLinks`） | apply delta |
| 全量重建 | `ClearPolicy()` + `AddPolicies` 批量灌入 | apply snapshot |
| 主体角色展开 | `GetImplicitRolesForUser(user, dom)` | 供 ④-2 |
| 决策缓存注入 | `SetCache(cache.Cache)`（缓存为裸 map，无容量上界） | 注入有界 LRU |
| 缓存失效 | `InvalidateCache()` 全量清 | **每次 apply 后必调** |
| 启动分片加载 | `persist.FilteredAdapter.LoadFilteredPolicy(model, filter)` | MemoryAdapter 按本域加载 |
| 策略存储 | `persist.Adapter` / `BatchAdapter` / `FilteredAdapter` | MemoryAdapter 实现 |

**绝不修改 casbin 源码**（架构 §2 铁律）。

## 3. 模块与包结构

包 `internal/sidecar/kernel`，单一职责，文件按职责拆分：

| 文件 | 职责 |
|---|---|
| `model.go` | 内嵌 casbin model 常量（架构 §6.2）+ `NewModelFromString` 装配 |
| `adapter.go` | `MemoryAdapter`（实现 `persist.Adapter + BatchAdapter + FilteredAdapter`） |
| `cache.go` | 有界 LRU，实现 `persist/cache.Cache`（Set/Get/Delete/Clear） |
| `types.go` | 内核域类型：`Rule` / `Delta` / `PolicyChange` / `Snapshot` / `DataPolicy` / `ChangeOp` / `DataPolicyApplier` |
| `engine.go` | `Engine`：构造 + Enforce/BatchEnforce + ApplySnapshot/ApplyDelta + Version/Ready + GetImplicitRolesForUser |
| `errors.go` | 哨兵错误（`ErrNotReady` / `ErrForeignDomain` / `ErrStaleVersion`） |

## 4. casbin model（内嵌常量）

锁定架构 §6.2 的 RBAC-with-domain（数据库 spec §「单一 model」已固化，不支持每 app 自定义 `.conf`）：

```ini
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
```

经 `model.NewModelFromString(modelText)` 装配，与控制面 projection 落的行结构对齐：`p = {role, domain, obj, act, eft}`、`g = {user|child, role|parent, domain}`（casbin domain = `application.domain` 字符串，已回源核实 `projection/project.go`）。

## 5. MemoryAdapter 设计

实现三接口，持本地快照 `[]Rule`，运行期 `autoSave=false`（构造后 `EnableAutoSave(false)`，写不回 adapter）：

- `persist.Adapter`：`LoadPolicy(model)`（灌入持有的全量行）、`SavePolicy`/`AddPolicy`/`RemovePolicy`/`RemoveFilteredPolicy`（**no-op**，只读契约，fail-loud 注释；运行期改内存走 enforcer 高层 API，不经 adapter）。
- `persist.BatchAdapter`：`AddPolicies`/`RemovePolicies`（同上 no-op）。
- `persist.FilteredAdapter`：`LoadFilteredPolicy(model, filter)` 只灌入本域行（filter 锁定 domain 列）、`IsFiltered() bool`。

> casbin `loadFilteredPolicy` 会强制类型断言 adapter 为 `FilteredAdapter`，否则报错（架构 §3.2 已核实 `enforcer.go:476`）——故 MemoryAdapter 必须实现 `FilteredAdapter`。

**职责边界**：MemoryAdapter 只服务**启动初次加载**与（可选的）从 adapter 重建路径；运行期 delta 与对账重建一律走 `Engine` 的高层 API 改 enforcer 内存模型（§7），不依赖 adapter 持有的快照保持最新。本切片中 `Engine.ApplySnapshot` 走高层 `ClearPolicy`+`AddPolicies`（不经 adapter reload），MemoryAdapter 的快照在构造时给定（空或冷启动种子均可）。

## 6. Engine 公开 API

```go
// New 构造内核：pin 到单 app 的 domain；cache 为有界实现；dataApplier 可为 nil（退化 no-op）。
func New(domain string, cache cachepkg.Cache, dataApplier DataPolicyApplier) (*Engine, error)

func (e *Engine) Enforce(sub, dom, obj, act string) (bool, error)
func (e *Engine) BatchEnforce(reqs [][]string) ([]bool, error)
func (e *Engine) GetImplicitRolesForUser(user, dom string) ([]string, error)

func (e *Engine) ApplySnapshot(s Snapshot) error // 全量重建 + 路由 data + InvalidateCache + 记版本
func (e *Engine) ApplyDelta(d Delta) error       // 增量 apply + 路由 data + InvalidateCache + 记版本

func (e *Engine) Version() uint64 // 当前已应用版本（未就绪为 0）
func (e *Engine) Ready() bool     // 是否已成功应用过一次 Snapshot
```

内部持 `*casbin.SyncedCachedEnforcer`、`domain string`、`version uint64`、`ready bool`、`dataApplier DataPolicyApplier`、一把保护 version/ready 的轻量 `sync.Mutex`（enforcer 自身 RWMutex 已保护策略/缓存）。构造时：`EnableAutoSave(false)`、`EnableAutoNotifyWatcher(false)`、**不 SetWatcher**（架构 §7：纯订阅端，杜绝回播）、`SetCache(cache)`。

## 7. apply 编排（DataPolicyApplier 注入）

一条 delta 是原子整体（功能行 + DataPolicy + **统一版本号**），编排在内核（§职责边界决策）：

```go
type ChangeOp int
const ( ChangeAdd ChangeOp = iota; ChangeUpdate; ChangeRemove )

type DataPolicyApplier interface {
    ApplySnapshot(policies []DataPolicy) // 全量替换
    ApplyChange(op ChangeOp, p DataPolicy)
}
// 默认 no-op 实现，便于 ④-1 独立单测；④-2 提供真实实现。
```

**ApplySnapshot(s)**：① 校验 s 全部 Rules 的 domain==本域（否则 `ErrForeignDomain`，fail-close）；② `ClearPolicy()`；③ 按 ptype 分组 `AddPolicies`（g 段自动 `BuildIncrementalRoleLinks`）；④ `dataApplier.ApplySnapshot(s.DataPolicies)`；⑤ `InvalidateCache()`；⑥ 记 `version=s.Version`、`ready=true`。任一步失败整体不改 version/ready（fail-close，由 ④-3 触发重试/再拉快照）。

**ApplyDelta(d)**：① 版本单调校验 `d.Version > version`（否则 `ErrStaleVersion`，拒绝重放/乱序）；② 校验 PolicyChanges 的 domain==本域；③ 逐 PolicyChange 转高层调用：`ADD→AddPolicies`、`REMOVE→RemovePolicies`、`UPDATE→UpdatePolicy(OldRule, Rule)`；④ `dataApplier.ApplyChange(op, p)` 路由 DataChanges；⑤ `InvalidateCache()`；⑥ 记 `version=d.Version`。

> **版本连续性判定不在内核**：内核只保证「严格单调、拒 stale」。「版本跳变（中间丢包）→ 该不该改去拉快照」属对账逻辑，归 ④-3（架构 §7）。内核 `ApplyDelta` 对 `d.Version > version+1` 的跳变**仍照常 apply**（信任 ④-3 已决定增量可用），仅暴露 `Version()` 供 ④-3 比对决策。

> 与 ③-1 同构：`PolicyManager` 拥有写事务模板、`DeltaSink` 注入；这里 `Engine` 拥有 apply 编排、`DataPolicyApplier` 注入。版本/原子性/缓存铁律集中一处。

## 8. 缓存铁律落地（架构 §5）

`SyncedCachedEnforcer` 自带缓存为裸 map、无容量上界，且 `UpdatePolicy`/`ClearPolicy`/`BuildIncrementalRoleLinks` **未被 cached 层重写、不碰缓存**（回源核实，§13）。故：

1. **有界 LRU**：`cache.go` 实现 `persist/cache.Cache` 四方法，固定容量淘汰，仅作**内存上界**（非一致性手段）。经 `SetCache` 注入。
2. **每次 apply 后全量清**：`ApplySnapshot`/`ApplyDelta` 结尾必调 `InvalidateCache()`。**不按 key 删**——缓存 key 由**请求**主体构成（`alice$$…`），而 `RemovePolicies` 的内置按 key 删用**规则**主体（`manager$$…`），RBAC 角色间接性下撤权缓存对不上、会漏（架构 §5 已回源核实）。**不用 TTL 做一致性**（TTL=N 秒 ≡ 容忍 N 秒不一致，违反底线）。
3. **apply→invalidate 微窗**：`SyncedCachedEnforcer.Enforce` 命中缓存时只走 cache 锁、不取策略锁，故「已 apply 但未 InvalidateCache」的微秒级窗口内并发鉴权可能读到旧 true。属架构 §2.2 可容忍新鲜度滞后（同协程顺序 apply→InvalidateCache 收口，无长期不一致），**不消窗**，文档标注。

## 9. 单域固定 + fail-close + 版本单调

- **单域固定**：`Engine` 构造 pin 本 app 的 `domain`。`ApplySnapshot`/`ApplyDelta` 遇 domain≠本域的行 → `ErrForeignDomain`，整笔拒绝（防跨 app 策略泄漏，一致性铁律）。`Enforce(sub, dom, …)` 的 dom≠本域 → 直接 `(false, ErrForeignDomain)`。
- **就绪前 fail-close**：`ready==false`（未成功 ApplySnapshot）时 `Enforce`/`BatchEnforce` 返回 `(false, ErrNotReady)`，绝不放行。对齐 casbin `enforce()` 出错即 `(false, err)` 的内核语义（架构 §2.1）。
- **版本单调**：`ApplyDelta` 拒 `version ≤ current`（`ErrStaleVersion`），防重放/乱序。`ApplySnapshot` 无条件接受（全量对齐语义，可回退或前进版本）。

## 10. 内核域类型（syncv1 挡在外）

内核定义自有最小域类型，**不依赖 `gen/sydom/sync/v1`**（与 ③ 把 syncv1 挡在 policy core 外一致；proto→域翻译由 ④-3 实现）：

```go
type Rule struct { Ptype string; V [6]string } // casbin-native，空位空串

type PolicyChange struct { Op ChangeOp; Rule Rule; OldRule Rule } // REMOVE 用 Rule 定位；UPDATE 用 OldRule→Rule

type Delta struct {
    Version       uint64
    PolicyChanges []PolicyChange
    DataChanges   []DataPolicyChange
}

type DataPolicy struct { ID uint64; SubjectType, SubjectID, Resource, Condition string } // Condition 为不透明 JSON 串
type DataPolicyChange struct { Op ChangeOp; Policy DataPolicy }

type Snapshot struct {
    Version      uint64
    Rules        []Rule
    DataPolicies []DataPolicy
}
```

语义对齐 syncv1（§上游契约）：`PolicyChange.Op/Rule/OldRule` ↔ proto `PolicyChange`；`DataPolicy.Condition` 不透明透传（求值归 ④-2）。本切片不引入 cp 控制面类型（`internal/controlplane`），保持 Sidecar 侧独立。

## 11. 错误语义

| 错误 | 触发 | 调用方处置 |
|---|---|---|
| `ErrNotReady` | 未就绪即 Enforce | ④-4 返回 deny（fail-close） |
| `ErrForeignDomain` | 越域行/请求 | apply 失败→④-3 触发再拉快照；Enforce→deny |
| `ErrStaleVersion` | delta version≤current | ④-3 丢弃该 delta（重放/乱序），不报错升级 |
| casbin 内部 err | Enforce 计算失败 | 透传，调用方 fail-close |

apply 失败一律「整笔不改 version/ready」，由 ④-3 决定重试或全量对齐。

## 12. 测试策略（纯单测，无 Docker）

④-1 无任何 I/O，全部 `go test` 纯单测、毫秒级。关键用例：

1. **RBAC 撤权即时生效（缓存铁律回归，最关键）**：snapshot 灌入 `g(alice,manager,dom)` + `p(manager,dom,order,read,allow)`；`Enforce(alice,…)=true` 并入缓存；ApplyDelta 撤 `p(manager,…)`；断言 `Enforce(alice,…)=false`。证明全量清在角色间接性下正确（按 key 删会漏，故该用例即是对铁律的守门）。
2. **有界缓存淘汰**：注入小容量 LRU，灌入超容量不同请求，断言不超界 + 命中正确。
3. **版本单调**：ApplyDelta 拒 version≤current（`ErrStaleVersion`）；ApplySnapshot 可回退版本。
4. **就绪前 fail-close**：New 后未 ApplySnapshot，`Enforce` 返回 `(false, ErrNotReady)`。
5. **越域拒绝**：snapshot/delta 含外域行 → `ErrForeignDomain` 且状态不变；`Enforce` 外域 dom → deny。
6. **snapshot 全量重建**：二次 ApplySnapshot 后旧策略不残留（ClearPolicy 生效）。
7. **DataPolicyApplier 路由**：用 spy applier 断言 snapshot/delta 的 DataPolicies/DataChanges 被原子转发；nil applier 退化 no-op 不 panic。
8. **GetImplicitRolesForUser**：多级角色继承下展开正确（供 ④-2 主体解析）。
9. **deny override**：同时命中 allow 与 deny → deny（effect 正确）。

## 13. 与下游切片的接口契约（移交）

- **④-3 同步客户端**：实现 proto→域翻译（`syncv1.Delta→kernel.Delta`、`syncv1.Snapshot→kernel.Snapshot`），驱动 `Engine.ApplyDelta/ApplySnapshot`；依据 `Engine.Version()` 做版本对账（gap/心跳领先/重连→PullSnapshot→ApplySnapshot）；翻译时裁剪尾部空串、proto `uint64`↔域 `uint64`。
- **④-2 数据权限引擎**：实现 `kernel.DataPolicyApplier`（维护 `subject→[]DataPolicy` 内存表）；求值时调 `Engine.GetImplicitRolesForUser` 展开主体。
- **④-4 鉴权 API**：调 `Engine.Enforce/BatchEnforce`；未就绪/出错一律 deny。

## 14. casbin 回源核实记录（遵循「论断先回源」铁律）

均核对 `casbin/`（v3.10.0 克隆）：
- `enforcer_cached_synced.go`：`NewSyncedCachedEnforcer`(:35)、`Enforce`(:60)、`AddPolicies`(:101)、`RemovePolicies`(:115)、`SetCache(cache.Cache)`(:133)、`InvalidateCache`(:148)。
- `enforcer_synced.go`：`BatchEnforce([][]interface{})`(:219)、`UpdatePolicy`(:427)。
- `enforcer.go`：`ClearPolicy`(:352)、`EnableAutoNotifyWatcher`(:616)、`EnableAutoSave`(:626)、`BuildIncrementalRoleLinks`(:657)。
- `rbac_api.go`：`GetImplicitRolesForUser(name, domain...)`(:233)。
- `persist/`：`Adapter`(adapter.go:64)、`BatchAdapter`(batch_adapter.go:18)、`FilteredAdapter`(adapter_filtered.go:22)、`cache.Cache`(cache/cache.go:21，Set/Get/Delete/Clear)。
- **关键论断**：`SyncedCachedEnforcer` 重写的增删走「按 key 删」（key 由规则主体构成），`UpdatePolicy`/`ClearPolicy`/`BuildIncrementalRoleLinks` 未被 cached 层重写、不碰缓存——故必须显式 `InvalidateCache()` 全量清（架构 §5 同源结论）。

相关：架构总纲 §3/§5/§6/§7；③ PolicySync 契约；[[feedback-consistency-over-simplicity]]、[[feedback-verify-casbin-before-asserting]]。
