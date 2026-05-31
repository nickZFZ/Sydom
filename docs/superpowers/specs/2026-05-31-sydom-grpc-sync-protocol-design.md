# 司域 (Sydom) gRPC 同步协议契约 详细设计

> 厘定辖域，权归其位
>
> 版本：v0.1 | 日期：2026-05-31 | 状态：草稿
>
> 上游：[整体架构设计](2026-05-30-sydom-architecture-design.md) | 关联：[数据库 Schema](2026-05-31-sydom-database-schema-design.md)

---

## 1. 范围与定位

本文档是司域**控制面 ↔ Sidecar 之间策略下发协议**的详细设计，是详细设计阶段的第 2 个子项目。它定义 gRPC service `PolicySync` 的 proto 契约、认证机制、交互时序与容灾语义。

**不在本文档范围内**（留给后续 spec）：
- 控制面如何生成 delta、Policy Manager 内部结构（→ 控制面 spec）
- Sidecar 如何 apply delta 到 casbin 内存、MemoryAdapter（→ Sidecar spec）
- Redis Pub/Sub 广播总线的内部实现（→ 控制面 spec）
- 权限点上报 API（SDK → 控制面，那是另一条链路，见架构第 8 节）

本文档只回答："控制面与 Sidecar 之间，用什么 RPC、什么消息、什么时序、什么认证来同步策略。"

**数据来源**：下发内容来自第 1 个子项目的 `casbin_rule` 表（app_id/ptype/v0-v5/version）与 `data_policy` 表。

---

## 2. 设计决策（本设计的关键主线）

以下决策已在头脑风暴中逐条确认：

1. **stream 形态**：`Subscribe`（server-streaming 推送 delta + 心跳）+ 独立 unary `PullSnapshot`（按需查全量）。不用双向流——架构已定 at-most-once + 版本号对账，bidi 的 ack 能力多余。
2. **认证**：metadata + HMAC 签名，控制面拦截器校验并**强制 app_id**（架构 I2）。app_id 不出现在任何请求体。
3. **重连对齐**：版本号对账续传——`Subscribe` 带 `last_applied_version`，控制面对比 app 当前版本，一致则无缝续推、落后则发 `SnapshotRequired` 触发全量拉取。
4. **全量快照**：unary `PullSnapshot` 一次性返回 + 调大 `maxRecvMsgSize`（64MB）。分块流式为大型规模的后续优化（YAGNI）。
5. **schema 回改**：HMAC 需服务端持有 AppSecret 原文，故 `application.app_secret_hash` 改为 `app_secret_enc`（可逆加密存储）。这是对第 1 个子项目 schema 的修正，随本子项目交付（见 §7）。

---

## 3. Proto 契约

文件位置约定：`api/proto/sync/v1/policy_sync.proto`。

```protobuf
syntax = "proto3";

package sydom.sync.v1;

option go_package = "github.com/nickZFZ/Sydom/gen/sync/v1;syncv1";

// PolicySync 是控制面向 Sidecar 下发策略的同步服务。
// 控制面实现，Sidecar 调用。所有 RPC 均需 HMAC 认证（见 §4）。
service PolicySync {
  // Subscribe 订阅策略变更流。Sidecar 启动后调一次并长连保持，
  // 控制面持续推送 delta / heartbeat / snapshot_required。
  rpc Subscribe(SubscribeRequest) returns (stream SyncEvent);

  // PullSnapshot 拉取该 app 全量策略快照。
  // 冷启动、版本断档、收到 SnapshotRequired 时调用。
  rpc PullSnapshot(PullSnapshotRequest) returns (Snapshot);
}

// ── 请求 ──

message SubscribeRequest {
  // Sidecar 已应用到的版本号；冷启动为 0。app_id 由认证凭据强制，不在此处。
  uint64 last_applied_version = 1;
}

message PullSnapshotRequest {
  // 空——app_id 由认证凭据强制，快照总是返回该 app 的全量当前态。
}

// ── 流事件 ──

message SyncEvent {
  oneof event {
    Delta delta = 1;                        // 实时增量变更
    Heartbeat heartbeat = 2;                // ~30s 版本心跳（反熵）
    SnapshotRequired snapshot_required = 3; // 控制面要求 Sidecar 全量对齐
  }
}

// Delta 对应控制面一次策略变更事务的产物（一个新版本号，原子整体 apply）。
message Delta {
  uint64 version = 1;                        // 本次变更后的 app 版本号（单调递增）
  repeated PolicyChange policy_changes = 2;  // casbin_rule 行的增删改
  repeated DataPolicyChange data_changes = 3; // data_policy 的增删改
}

enum ChangeOp {
  CHANGE_OP_UNSPECIFIED = 0;  // proto3 要求首值为 0
  ADD = 1;
  REMOVE = 2;
  UPDATE = 3;
}

// casbin 策略行变更（对应 casbin_rule 表，Sidecar 喂给 casbin 高层 API）。
message PolicyChange {
  ChangeOp op = 1;
  PolicyRule rule = 2;      // ADD/UPDATE 的新行
  PolicyRule old_rule = 3;  // UPDATE 的旧行；REMOVE 时为待删行
}

message PolicyRule {
  string ptype = 1;            // "p" / "g"
  repeated string values = 2;  // v0..v5，变长（casbin []string 风格，尾部空串可省）
}

// 数据权限变更（对应 data_policy 表）。
message DataPolicyChange {
  ChangeOp op = 1;
  DataPolicy policy = 2;
}

message DataPolicy {
  uint64 id = 1;            // data_policy.id；REMOVE/UPDATE 靠它定位（无自然唯一键）
  string subject_type = 2;  // "role" / "user"
  string subject_id = 3;
  string resource = 4;
  string condition = 5;     // 条件树 JSON 字符串（协议层不透明传输）
}

message Heartbeat {
  uint64 current_version = 1;  // 该 app 当前 max 版本号
}

message SnapshotRequired {
  uint64 current_version = 1;  // 当前版本，供 Sidecar 日志/决策
  string reason = 2;           // "behind" / "reconnect" 等，便于排查
}

// ── 全量快照 ──

message Snapshot {
  uint64 version = 1;                    // 快照对应的 app 当前版本号
  repeated PolicyRule rules = 2;         // 全量 casbin 策略行（p + g）
  repeated DataPolicy data_policies = 3; // 全量 data_policy
}
```

**字段设计要点：**
- **Delta = 版本号 + 一批变更**：对应控制面一次写入事务（数据库 spec §6 时序），多条变更原子地一起 apply。
- **`PolicyRule.values` 变长**：贴合 casbin `[]string`，Sidecar 直接喂 `AddPolicies/RemovePolicies`；删除按值匹配（casbin 语义），无需 id。
- **`DataPolicy.id` 必带**：data_policy 无自然唯一键（同 subject 对同 resource 可多条），删除/更新必须按 DB id 定位。
- **`condition` 传 JSON 字符串**：条件树 schema 归 Sidecar 数据权限引擎，协议层当不透明载荷，避免协议与条件树语法耦合。
- **`UPDATE` 带 `rule` + `old_rule`**：对应 casbin `UpdatePolicy(old, new)` 签名。

---

## 4. 认证机制（HMAC 签名）

所有 RPC（`Subscribe` 与 `PullSnapshot`）在 gRPC metadata 携带：

| metadata key | 内容 |
|---|---|
| `x-sydom-app-id` | AppID（明文，标识哪个 app） |
| `x-sydom-timestamp` | Unix 秒时间戳（防重放） |
| `x-sydom-signature` | `HMAC-SHA256(AppSecret, "<app_id>\n<timestamp>\n<rpc_method>")` 的 hex |

**控制面拦截器**（unary + stream 各一个）校验流程：
1. 取 `app_id`，查其 `AppSecret`（从 `app_secret_enc` 解密，见 §7）。
2. 用相同规则重算 HMAC，与 `x-sydom-signature` 常量时间比对（防时序攻击）。
3. 校验 `timestamp` 在 **±5 分钟**窗口内（容忍时钟偏移，超窗拒绝，防重放）。
4. 校验通过 → 把 `app_id` 注入 context，**后续一切操作强制使用此 app_id**（架构 I2：业务系统无法越权订阅/拉取其他 app）。
5. 任一步失败 → 返回 gRPC `UNAUTHENTICATED`。

**为什么 HMAC 而非传密钥原文**：HMAC 不在链路上传输 AppSecret 原文，且签名含时间戳天然防重放——对齐架构 I2"AppSecret 签名"与司域安全优先取向。

---

## 5. 交互时序

```
【冷启动】
  Sidecar(配置注入 AppID/AppSecret) → Subscribe(last_applied=0)
  控制面: current_version > 0 → 流内发 SnapshotRequired(reason="behind")
  Sidecar → PullSnapshot → 重建内存(全量 rules + data_policies) → last_applied=current
  → 继续消费同一订阅流的后续 delta

【稳态】
  控制面策略变更 → 推 Delta(version=N, changes...)
  Sidecar: 校验 N == last_applied+1 → 顺序 apply → last_applied=N
  每 ~30s: 控制面推 Heartbeat(current_version)
  Sidecar: current_version == last_applied → 无操作

【丢包检测】
  Sidecar 收到 Delta.version 跳变（> last_applied+1）
    或 Heartbeat.current_version > last_applied
  → 调 PullSnapshot 全量对齐 → last_applied=snapshot.version → 继续消费流

【断线重连】
  流中断 → Sidecar 指数退避重连 → Subscribe(last_applied)
  控制面比对:
    current_version == last_applied → 无缝续推后续 delta
    current_version >  last_applied → 发 SnapshotRequired → Sidecar PullSnapshot → 续推
```

---

## 6. 错误与容灾语义

| 场景 | 行为 |
|---|---|
| 认证失败 | 返回 `UNAUTHENTICATED`；Sidecar 告警 + 退避重试（多为配置错，需人工介入） |
| 控制面不可达 / 流中断 | Sidecar 用**本地策略快照继续鉴权**（架构 C3 降级）；指数退避重连 |
| 冷启动连不上且无本地快照 | **鉴权一律拒绝（fail-close，架构默认姿态）** |
| `PullSnapshot` 失败 | 退避重试；期间用旧快照鉴权 |
| 控制面优雅关闭流 | Sidecar 视为正常断开，重连（带 last_applied） |
| Delta apply 失败（内存操作异常） | Sidecar 回退到 `PullSnapshot` 全量重建，不带病前进 |

fail-close 是默认安全姿态：异常路径默认不放行，对齐 casbin `enforce()` 出错返回 `(false, err)` 的内核语义。

---

## 7. Schema 回改（对第 1 个子项目的修正）

HMAC 认证要求控制面持有 AppSecret 原文来验签，而数据库 spec 原先存 `app_secret_hash`（不可逆，无法验 HMAC）。本子项目交付一个修正 migration：

- **新增** `db/migrations/000011_application_secret_enc.up.sql`：
  - `application.app_secret_hash`（VARCHAR(255)）→ `app_secret_enc`（BYTEA，存 AES-GCM 加密后的 AppSecret，含 nonce）
- **主密钥管理**：对称主密钥经环境变量 / KMS 注入控制面进程，**绝不入库**；轮转策略留待运维 spec。
- schema 尚未上线，可直接加修正迁移，无需考虑数据迁移兼容。
- 数据库 spec 的 §4.1 `application` 表定义需同步更新该列说明。

---

## 8. 关键参数

| 参数 | 值 | 依据 |
|---|---|---|
| 心跳间隔 | ~30s | 架构 R3 反熵 |
| 签名时间戳防重放窗口 | ±5 分钟 | 容忍时钟偏移 |
| gRPC `maxRecvMsgSize` | 64 MB | 容纳中型基线全量快照（~10万规则 5–10MB） |
| 重连退避 | 指数退避（如 1s→最大 30s） | 避免控制面恢复时惊群 |

---

## 9. 边界与未决（留给后续 spec）

- **delta 生成逻辑**：控制面如何从一次写入事务算出 delta（add/remove 行集）→ 控制面 spec。
- **Redis Pub/Sub 扇出**：控制面多副本如何经 Redis 把变更扩散给各自持有的 Sidecar stream → 控制面 spec。
- **Sidecar apply 细节**：delta → casbin 高层 API、autoNotifyWatcher=false、InvalidateCache → Sidecar spec。
- **主密钥轮转 / KMS 选型** → 运维 spec。
- **proto 代码生成工具链**（protoc / buf）→ 实现计划中确定。

---

*下一步：按头脑风暴排序推进——本子项目可进入实现计划；之后是控制面、Sidecar、SDK。*
