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
| 2 | gRPC 同步协议契约 | 🔄 spec 已完成，待实现 | spec（`348ae57`） |
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

## 待办

**近期（子项目 ②）：**
- [ ] gRPC 协议 `writing-plans` → 实现计划
- [ ] 实现：proto 工具链（protoc/buf）+ `PolicySync` service + HMAC 拦截器 + 测试
- [ ] **跨子项目回改**：新增 migration `000011`，`application.app_secret_hash` → `app_secret_enc`（AES-GCM 可逆加密，HMAC 验签需密钥原文）；同步更新数据库 spec §4.1 application 表说明

**后续（子项目 ③④⑤）：**
- [ ] 控制面：管理 API + Policy Manager + DB BatchAdapter + delta 生成 + Redis Pub/Sub 广播
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
