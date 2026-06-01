# 司域 (Sydom) 详细设计进度

> 更新：2026-06-01 | 阶段：架构设计已完成，详细设计进行中（控制面 ③-1 已实现）

## 阶段总览

```
架构设计 ✅  →  详细设计（5 个子项目，按依赖排序）  →  集成联调
```

架构设计已定稿并评审（R1–R10 回源核实 casbin v3.10.0）：
`docs/superpowers/specs/2026-05-30-sydom-architecture-design.md`

## 子项目进度

| # | 子项目 | 状态 | 产出 |
|---|--------|------|------|
| 1 | 数据库 Schema | ✅ 设计+实现，已并入 main | spec + plan + 代码（`d2b78d6`） |
| 2 | gRPC 同步协议契约 | ✅ 设计+实现，已并入 main（5 个 TDD 任务） | spec（`348ae57`）+ plan（`f258956`）+ 代码（见下） |
| 3 | 控制面 | 🔵 拆 3 子模块；③-1 策略核心引擎已实现（8 个 TDD 任务，待合并） | spec（`2f193e3`）+ plan（`5432f65`）+ 代码（8 commit） |
| 4 | Sidecar 内部结构 | ⏳ 未开始 | — |
| 5 | SDK 接口规范 | ⏳ 未开始 | — |

控制面（③）拆为 3 子模块：**③-1 策略核心引擎**（纯 DB 写引擎：投影/diff/版本写事务/SecretResolver）✅ 已实现；**③-2 同步下发服务**（Redis 广播 + PolicySync gRPC 服务端 + Delta→syncv1 翻译）⏳；**③-3 管理 API**（REST/gRPC CRUD + 管理鉴权）⏳。

## 关键产出索引

| 类型 | 路径 | commit |
|------|------|--------|
| 架构设计 | `specs/2026-05-30-sydom-architecture-design.md` | `ed84b72` |
| ① Schema 设计 | `specs/2026-05-31-sydom-database-schema-design.md` | `ef24084` |
| ① Schema 实现计划 | `plans/2026-05-31-sydom-database-schema.md` | `70eaad7` |
| ① Schema 代码 | `db/migrations/000001-000010`、`internal/db/`、`Makefile` | `d2b78d6` |
| ② gRPC 协议设计 | `specs/2026-05-31-sydom-grpc-sync-protocol-design.md` | `348ae57` |
| ② gRPC 实现计划 | `plans/2026-05-31-sydom-grpc-sync-protocol.md` | `f258956` |
| ② migration 000011 + crypto | `db/migrations/000011_*`、`internal/crypto/` | `1f39a56`/`adacfe4` |
| ② proto 契约 + 生成代码 | `api/proto/sydom/sync/v1/`、`buf.yaml`、`gen/sydom/sync/v1/` | `cc334d2` |
| ② HMAC 认证 + 拦截器 | `internal/auth/`（signature/拦截器/凭据/bufconn 集成） | `cab2341`/`2c0506b` |

## 待办

**子项目 ②（已完成）：**
- [x] gRPC 协议 `writing-plans` → 实现计划（`f258956`，5 个 TDD 任务）
- [x] 实现：migration 000011 + AES-GCM + buf 工具链/proto 生成 + HMAC 拦截器 + bufconn 集成测试（5 commit，全程两阶段审查；全量 `go test -race ./...` 通过）
- [x] **跨子项目回改**：migration `000011` 落地 `app_secret_enc`；数据库 spec §4.1 已同步更新

**子项目 ② 实现后浮现的待跟进项（留给后续 spec）：**
- [ ] 控制面 DB 扫描：proto `version`/`id` 为 `uint64`、DB 为 `BIGINT`(int64)，扫描时统一用 int64 接收再转 uint64（最终审查 M1）
- [ ] 测试代码 `grpc.DialContext` 已 deprecated，后续 patch 换 `grpc.NewClient`（最终审查 I1，仅测试、不阻塞）

**子项目 ③-1（策略核心引擎，已实现，待合并）：**
- [x] spec（`2f193e3`）+ plan（`5432f65`，8 个 TDD 任务）
- [x] 实现：`internal/dbtest`(共享 testcontainers 基建) + `internal/controlplane/{types,projection,store,policy,secret}` + migration `000012`(audit.action 16→32)；8 commit，全程两阶段审查 + opus 最终整体审查（结论：可合并）。全量 `go test ./...` 通过（含 -race 并发版本串行化）。
- [x] **跨子项目回改**：数据库 spec policy_audit_log.action 列同步更新为 varchar(32)、注明 migration 000012。
- [x] 已实现 `auth.SecretResolver`（secret 包，解密 `app_secret_enc`、主密钥 fail-close）。

**子项目 ③-1 实现后浮现的待跟进项（留给 ③-2/③-3）：**
- [ ] **③-3**：`application.status`（停用态）的写拦截——③-1 写引擎不 enforce status（属生命周期/鉴权闸门），由 ③-3 接入层拦截（最终审查建议）。
- [ ] **③-2**：`DeleteDataPolicy` 产出的 `ChangeRemove` 仅携带 ID（subject/resource 空）；若 ③-2 下发协议按 (subject,resource) 索引则需删前回查补全，待 ③-2 协议定稿对齐。
- [ ] **③-2**：审计 `policy_audit_log.diff` 列当前恒 NULL；若需变更回放再补 InsertAudit 写 diff。

**后续（子项目 ③-2/③-3、④、⑤）：**
- [ ] ③-2 同步下发服务：Redis Pub/Sub 广播 + PolicySync gRPC 服务端（Subscribe/PullSnapshot）+ 领域 Delta→syncv1 翻译。
- [ ] ③-3 管理 API：REST/gRPC CRUD（租户/应用/角色/权限/继承/绑定/数据策略）+ 管理鉴权 + status 生命周期拦截。
- [ ] Sidecar：同步协程 + MemoryAdapter + SyncedCachedEnforcer 封装 + 数据权限引擎 + 鉴权 API。
- [ ] SDK：middleware + ORM hook + 权限点上报。

## 环境与技术栈

- **语言**：Go 1.26.3（本机装于 `~/.local/go`，软链到 `~/.local/bin/go`）
- **数据库**：PostgreSQL 优先（MySQL 方言后续补）；migration 用 golang-migrate
- **测试**：testcontainers-go 起真实 PG（`postgres:17-alpine`），**依赖 Docker**
- **module**：`github.com/nickZFZ/Sydom`
- **协议**：gRPC（控制面 ↔ Sidecar）；REST + gRPC（控制面对外）

---

*本文件随详细设计推进持续更新。*
