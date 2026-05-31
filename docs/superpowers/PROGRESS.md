# 司域 (Sydom) 详细设计进度

> 更新：2026-05-31 | 阶段：架构设计已完成，详细设计进行中

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
| 3 | 控制面 | ⏳ 未开始 | — |
| 4 | Sidecar 内部结构 | ⏳ 未开始 | — |
| 5 | SDK 接口规范 | ⏳ 未开始 | — |

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

**后续（子项目 ③④⑤）：**
- [ ] 控制面：管理 API + Policy Manager + DB BatchAdapter + delta 生成 + Redis Pub/Sub 广播；从 `app_secret_enc` 解密实现 `auth.SecretResolver`
- [ ] Sidecar：同步协程 + MemoryAdapter + SyncedCachedEnforcer 封装 + 数据权限引擎 + 鉴权 API
- [ ] SDK：middleware + ORM hook + 权限点上报

## 环境与技术栈

- **语言**：Go 1.26.3（本机装于 `~/.local/go`，软链到 `~/.local/bin/go`）
- **数据库**：PostgreSQL 优先（MySQL 方言后续补）；migration 用 golang-migrate
- **测试**：testcontainers-go 起真实 PG（`postgres:17-alpine`），**依赖 Docker**
- **module**：`github.com/nickZFZ/Sydom`
- **协议**：gRPC（控制面 ↔ Sidecar）；REST + gRPC（控制面对外）

---

*本文件随详细设计推进持续更新。*
