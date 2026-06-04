# 司域 · DataPolicy effect 端到端贯通 详细设计

> 版本：v0.1 | 日期：2026-06-04 | 状态：草稿

## 1. 范围与定位

这是一个**专项跨层子项目**，单一关注点：把数据策略的 `effect`（allow/deny）字段从管理 API 创建一路贯通到 Sidecar 同步下发的 wire proto。

**背景缺口**：④-2 数据权限引擎已实现 `kernel.DataPolicy.Effect` 与 dataperm 的 allow/deny deny-overrides 合并，但回溯发现整条上游链都没有 effect——`data_policy` 表（migration `000008`）无 `effect` 列、`cp.DataPolicy` 域类型无 Effect、`store.UpsertDataPolicy`/`ReadAppDataPolicies` 不读写 effect、管理 proto `UpsertDataPolicyRequest` 与同步 proto `sync.v1.DataPolicy` 均无 effect 字段、cp `translate` 不携带 effect。结果：deny 数据策略**无法被创建、也无法下发**，dataperm 的 deny 能力悬空。本子项目补齐这条链。

**为何独立成专项**：effect 链横跨 ①DB、②proto 契约、③-1 写引擎、③-2 读/翻译、③-3 管理 API 六处。塞进 ④-3（Sidecar 同步客户端）会破坏其"干净的传输层"边界，且混淆两个不相关的关注点。按本项目"一个子项目一个明确职责"的纪律独立成片，自走 spec → plan → 实现周期。

**终点与边界**：本子项目终点是 **wire `sync.v1.DataPolicy` 携带 effect**（快照 + 增量两条出口都带）。Sidecar 侧 `syncv1 → kernel` 的翻译消费 effect 归 **④-3**（本片不碰 `internal/sidecar`）。④-2 dataperm 已能消费 `Effect`，无需改动。

## 2. 设计决策（头脑风暴逐条确认）

1. **effect 链独立成专项子项目、排在 ④-3 之前做**。理由见 §1。
2. **proto `effect` 用 string，不用 enum**。`""`/`"allow"`/`"deny"` 与 `condition`（也是 string）、DB 列、④-2 `kernel.DataPolicy.Effect`/`normalizeEffect` 全链 string 一致；空串自然映射默认 allow；translate 无需 enum↔string 映射。取值约束交给 **DB CHECK 约束 + ④-2 dataperm `normalizeEffect`** 双重兜底，proto 层不自带约束是有意取舍（换取全链一致与最小改动）。
3. **空串语义 = allow**。对齐 DB 默认值与 ④-2 `normalizeEffect("")→allow`。存量行、未显式设值的请求一律按 allow。
4. **fail-close 取值校验分两道**：① DB `CHECK (effect IN ('allow','deny'))` 是硬保证（任何非法值落库即被拒）；② 管理 API 层把空串归一为 `allow`、非 {allow,deny} 直接返 `InvalidArgument`（给调用方干净错误码，不依赖 DB 报错冒泡）。
5. **本片不动 data_policy 投影逻辑**：data_policy 本就不投影到 `casbin_rule`（③-1 决策），effect 只是随 data_policy 行透传的一个普通字段，不参与功能权限计算。

## 3. 组件分解与改动清单

| 层 | 文件 | 改动 |
|---|---|---|
| ① DB | `db/migrations/000015_data_policy_effect.{up,down}.sql` | 新 migration：ALTER ADD COLUMN effect + CHECK 约束 |
| ① DB spec | `docs/superpowers/specs/2026-05-31-sydom-database-schema-design.md` | §data_policy 同步加 effect 列说明 |
| ③-1 域类型 | `internal/controlplane/types.go` | `cp.DataPolicy` 加 `Effect string` |
| ③-1 写 | `internal/controlplane/store/write.go`（UpsertDataPolicy） | INSERT/UPDATE 带 effect 列 |
| ③-2 读 | `internal/controlplane/store/read.go`（ReadAppDataPolicies） | SELECT 加 effect |
| ②/③-2 同步 proto | `api/proto/sydom/sync/v1/policy_sync.proto` | `DataPolicy` 加 `string effect = 6` + regenerate |
| ③-2 翻译 | `internal/controlplane/translate/translate.go`（dataPolicyToProto） | 带 `Effect`（增量 DeltaToProto 随之自动带上） |
| ③-3 管理 proto | `api/proto/sydom/admin/v1/admin.proto` | `UpsertDataPolicyRequest` 加 `string effect = 7` + regenerate |
| ③-3 管理 server | `internal/controlplane/mgmt/server.go`（UpsertDataPolicy） | 透传 effect + 归一/校验 |

## 4. 详细设计

### 4.1 DB migration（000015）

```sql
-- up
ALTER TABLE data_policy
    ADD COLUMN effect VARCHAR(8) NOT NULL DEFAULT 'allow'
    CONSTRAINT data_policy_effect_chk CHECK (effect IN ('allow', 'deny'));
```
```sql
-- down
ALTER TABLE data_policy DROP COLUMN effect;
```

- `NOT NULL DEFAULT 'allow'`：存量行平滑获默认值，无需数据回填脚本。
- `VARCHAR(8)`：与 `subject_type VARCHAR(8)` 同宽度风格，足容 "allow"/"deny"。
- 具名 CHECK 约束 `data_policy_effect_chk`：与本库既有具名约束（如 `fk_data_policy_application`）风格一致，便于排错与 down 迁移。
- DB schema spec `§4.x data_policy` 同步补 effect 列定义。

### 4.2 控制面域类型与写路径

`cp.DataPolicy`（`internal/controlplane/types.go`）追加末尾字段：
```go
type DataPolicy struct {
	ID          int64
	SubjectType string
	SubjectID   string
	Resource    string
	Condition   string
	Effect      string // "allow" | "deny"；空串按 "allow"（对齐 DB 默认）
}
```

`store.UpsertDataPolicy`：INSERT 列与 VALUES、以及 UPDATE 的 SET 子句加入 `effect`。effect 直接取自传入的 `cp.DataPolicy.Effect`（管理层已归一为 allow/deny，DB CHECK 兜底）。注意保持既有 `RowsAffected` 校验（命中 0 行报错回滚——③-1 一致性加固，勿回退）。

### 4.3 读路径与翻译

`store.ReadAppDataPolicies`：`SELECT id, subject_type, subject_id, resource, condition, effect FROM data_policy ...`，Scan 多收一个 `&p.Effect`。

同步 proto `sync.v1.DataPolicy` 加字段：
```proto
message DataPolicy {
  uint64 id = 1;
  string subject_type = 2;
  string subject_id = 3;
  string resource = 4;
  string condition = 5;
  string effect = 6; // "allow" / "deny"；空串按 "allow"（协议层不约束，DB/dataperm 兜底）
}
```

cp `translate.dataPolicyToProto` 加 `Effect: p.Effect`。**增量路径无需单独改**：`DeltaToProto` 的 `DataChanges` 已复用 `dataPolicyToProto`，快照 `DataPoliciesToProto` 同理——一处加字段，快照与增量两条出口同时带上 effect。

### 4.4 管理 API

同步 proto `admin.v1.UpsertDataPolicyRequest` 加 `string effect = 7;`。

`mgmt/server.go UpsertDataPolicy`：构造 `cp.DataPolicy` 时透传并归一：
```go
eff := r.Effect
if eff == "" {
	eff = "allow"
}
if eff != "allow" && eff != "deny" {
	return nil, status.Errorf(codes.InvalidArgument, "invalid effect %q (want allow|deny)", r.Effect)
}
d, err := s.mgr.UpsertDataPolicy(ctx, int64(r.AppId), cp.DataPolicy{
	... // 既有字段
	Effect: eff,
})
```
`DeleteDataPolicy` 按 ID 删，与 effect 无关，不改。

## 5. 数据流（端到端）

```
管理员 UpsertDataPolicy(effect="deny")
  → mgmt.server 归一/校验 effect
  → PolicyManager.UpsertDataPolicy（runVersionedWriteData：bump 版本 + 写 data_policy.effect）
  → 产出 cp.Delta（DataChanges[].Policy.Effect="deny"）
  → ③-2 broadcast/translate.DeltaToProto → SyncEvent.Delta（wire 带 effect）  ┐
  → 或 PullSnapshot → store.ReadAppDataPolicies(读 effect) → translate.DataPoliciesToProto → Snapshot（wire 带 effect）  ┘
  → 【边界】Sidecar ④-3 翻译消费 effect → kernel.DataPolicy.Effect → ④-2 dataperm deny-overrides
```

## 6. 错误处理（fail-close 铁律）

| 场景 | 处理 |
|---|---|
| 管理 API effect 为空串 | 归一为 "allow"（默认语义，非错误） |
| 管理 API effect ∉ {allow,deny} | 返 `InvalidArgument`（不落库） |
| 绕过管理层的非法 effect 落库尝试 | DB CHECK 约束拒绝（硬兜底） |
| 存量行无 effect | DB DEFAULT 'allow' 自动填充 |
| down 迁移 | DROP COLUMN，回到无 effect 态（不破坏既有读写——读路径加列前后兼容由实现保证仅在 up 后启用） |

## 7. 测试策略

全部复用既有基建（`internal/dbtest` testcontainers PG；mgmt bufconn）：

- **migration**：000015 up/down 可逆；up 后 `\d data_policy` 含 effect 列且 CHECK 生效（插入 'bogus' 被拒）；存量行默认 'allow'。
- **store 往返**：`UpsertDataPolicy(effect="deny")` → `ReadAppDataPolicies` 读回 effect="deny"；effect="" 写入后读回 'allow'（DB 默认）；UPDATE 改 effect 生效。
- **translate**：`DataPoliciesToProto`（快照）与 `DeltaToProto`（增量）输出的 `sync.v1.DataPolicy.Effect` 与输入 `cp.DataPolicy.Effect` 一致。
- **mgmt**：`UpsertDataPolicy(effect="deny")` 透传至 store 并读回；`effect=""` 归一为 allow；`effect="bogus"` 返 `InvalidArgument` 且未落库。
- **proto 漂移**：`make proto-check`（buf generate 后无 diff）。
- **回归**：`go test ./...` 全绿（加列不破既有 data_policy 读写用例；既有用例不设 effect 时读回默认 'allow'，按需断言）。

## 8. 不在范围

- Sidecar `syncv1 → kernel` 翻译消费 effect → ④-3。
- ④-2 dataperm（已能消费 Effect）。
- data_policy 投影到 casbin_rule（本就不投影）。
- effect 之外的 data_policy 字段/语义变更。

## 9. 自检结果

- **占位符扫描**：无 TODO/待定；改动清单 §3 与详细设计 §4 一一对应。
- **内部一致性**：proto string 决策（§2.2）贯穿 §4.3/4.4；空串→allow 语义在 DB(§4.1)/管理层(§4.4)/错误表(§6) 一致。
- **范围检查**：单一关注点（穿 effect 一字段），跨层但每处改动小，可单计划覆盖。
- **模糊性检查**：effect 取值校验位置（DB CHECK 硬保证 + 管理层软校验）已明确二选其责；增量是否单独改 translate 已澄清（复用 dataPolicyToProto，无需）。

相关：④-2 `docs/superpowers/specs/2026-06-04-sydom-sidecar-data-policy-engine-design.md`（effect 消费端）；DB schema `2026-05-31-sydom-database-schema-design.md`；同步协议 `2026-05-31-sydom-grpc-sync-protocol-design.md`；[[feedback-consistency-over-simplicity]]。
