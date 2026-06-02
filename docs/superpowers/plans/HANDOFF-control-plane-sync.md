# Handoff — 司域 控制面 ③-2 同步下发服务（子代理驱动执行中）

> 执行计划：`docs/superpowers/plans/2026-06-01-sydom-control-plane-sync-service.md`（8 个 TDD 任务）
> 执行方式：subagent-driven-development（每任务：全新实现子代理 → 规格审查 → 代码质量审查 → 修复回归）
> 快照时间：2026-06-01

---

## 当前状态：任务 8/8 全部完成 —— 待最终审查 + 收尾合入

### 工作区（关键，恢复前务必确认）
- **隔离 worktree**：`/home/tongyu/codes/Sydom/.claude/worktrees/control-plane-sync`
- **分支**：`worktree-control-plane-sync`
- **基线**：从本地 HEAD `1767f89` 切出 —— **不是** `origin/main`。
  - ⚠️ 本地 `main` 领先 `origin/main` **24 个提交**；③-1 等依赖只存在于本地。
  - 为此在主仓库设了 `git config worktree.baseRef head`，使 worktree 从本地 HEAD 切而非 origin/main。恢复 / 新建 worktree 时若用默认 `fresh` 会拉到过时的 origin/main 而缺依赖。
- 工作树干净（无未提交改动），`go build ./...` 通过。

### 已完成任务（均含 实现 + 规格审查✅ + 质量审查✅ + 修复，全绿）
| 任务 | 内容 | 提交 |
|---|---|---|
| 1 | `translate` 包：cp.Delta↔syncv1 翻译（增删映射/裁尾部空串/op 映射） | `a3b7528` + `fbc01cf`（补 opToProto 全分支测试 + 修正包注释为"单向"） |
| 2 | `store` 只读快照扩展：ResolveAppIDByKey / ReadCurrentVersion（不加 FOR UPDATE）/ ReadAppDataPolicies | `9802fcf` + `0ef224e`（补未知 app id fail-close 分支） |
| 3 | `broadcast` 编解码 + Publisher/Subscriber 接口 + go-redis 依赖（8 字节大端 app_id 前缀 + proto） | `4a50cff` + `03b9602`（补空 body/损坏 body 边界用例 + 明确 Run 取消语义） |
| 4 | `broadcast` Redis 实现 RedisPublisher/RedisSubscriber + `dbtest.StartRedis`（testcontainers redis v0.31.0） | `44fe296` + `93740cd`（修正 Eventually 内 require 误用 + 关闭 redis 客户端） |
| 5 | `policysync` Hub — app_id→streams 注册表 + 有界非阻塞 fan-out + 溢出信号（纯单测） | `a77d90d` + `5de86d7`（补 overflow 去重断言 + 多订阅者 fan-out + 并发 -race 压测） |
| 6 | `policysync` Server + PullSnapshot（只读一致事务）+ NewGRPCServer 装配（bufconn + PG） | `fe3c45a`（含 grpc v1.59→v1.64，因测试用 grpc.NewClient 需 ≥v1.63） + `9ff39d2`（补未认证负路径用例 + go mod tidy，顺带剔除 superseded golang/protobuf indirect） |

> ⚠️ **任务 6 遗留（已知、刻意不修）**：`PullSnapshot` 把 `store.ResolveAppIDByKey` 的**所有**错误映射为 `codes.NotFound`；理论上瞬时 DB 故障会被误标为 NotFound（而非 Internal）。未修原因：①auth 拦截器已先按 app_key 解析 secret，故进入 handler 时 app_key 必然刚存在过，ErrNoRows 仅为删除竞态、几乎不触发；②正确修法需在 `store/read.go` 用 `%w`/哨兵包装 ErrNoRows——属范围外既有文件，按纪律不动；③计划已显式指定 NotFound。若日后要修：store 包统一错误哨兵时一并处理。

| 7 | `policysync` Subscribe — 版本对账续传 + send-loop（events/overflow/heartbeat 三路）+ `startServerCapture` 辅助 | `a5d5652` + `a626d8b`（修 InSyncReceivesDispatchedDelta 注册时序 flaky：原计划 Eventually 闭包首轮即 return true，Delta 早于 hub.register 投递而丢失 → 改后台续投 + Recv 跳心跳，-count=10 稳定、-race 干净） |

> 📝 **任务 7 收尾时顺手处理的次要项（质量审查 PASS，非阻塞）**：①`server.go` Subscribe 里 "先注册到 Hub" 注释措辞不准——真正受保护窗口是 register→send-loop 之间，register 前两次 DB 读的窄窗由心跳反熵兜底，建议改注释；②可补 `TestSubscribe_Unauthenticated` 与 PullSnapshot 对称（Stream 拦截器路径）；③`startServer`/`startServerCapture` seed/auth 重复可抽 `prepAuth`（计划已注明可选）。均放最终 cleanup。

| 8 | `RunDispatchLoop` 接 broadcast.Subscriber→Hub.Dispatch + 端到端（PG+Redis+bufconn） | `5a73751`（端到端测试用后台续发布+Recv 跳非 Delta 的健壮写法，规避 Redis pub/sub 无缓冲时序窗口；质量审查 PASS，3 个次要测试注释/风格项未修，非阻塞） |

### ~~待办任务~~ —— 全部完成
- **收尾**：`go build ./...` / `go test ./...`（需 Docker 起 PG+Redis）/ `go test ./internal/controlplane/policysync/ -race -count=1` / `go vet ./...` / `gofmt -l internal/`（排除 gen/）→ 调用 `finishing-a-development-branch` 收尾合入。

---

## 已验证可用的 ③-1 依赖（实现子代理可直接用）
- `cp` (`internal/controlplane/types.go`)：`Rule{Ptype string; V [6]string}`、`ChangeOp`(Add=0/Update=1/Remove=2)、`DataPolicy{ID int64; SubjectType; SubjectID; Resource; Condition string}`、`DataPolicyChange{Op; Policy}`、`Delta{AppID,Version int64; RuleAdds,RuleRemoves []Rule; DataChanges []DataPolicyChange}`、`DBTX` 接口。
- `syncv1` (`gen/sydom/sync/v1/`)：`Delta{Version uint64; PolicyChanges; DataChanges}`、`PolicyChange{Op; Rule; OldRule}`、`PolicyRule{Ptype; Values}`、`DataPolicy{Id uint64; SubjectType; SubjectId; Resource; Condition}`、`SyncEvent{oneof Delta/Heartbeat/SnapshotRequired}`、`Snapshot{Version; Rules; DataPolicies}`、`SnapshotRequired{CurrentVersion; Reason}`、`Heartbeat{CurrentVersion}`、`ChangeOp_CHANGE_OP_*`(0-3)。`PolicySyncServer`/`UnimplementedPolicySyncServer`/`RegisterPolicySyncServer`/`PolicySync_SubscribeServer`。
- `store`：`ReadAppRules(ctx, q cp.DBTX, appID) ([]cp.Rule, error)`、`LockAppVersion`(带 FOR UPDATE，**只读路径勿用**)、加上本次新增的三个只读函数。
- `auth`：`AppIDFromContext(ctx) (string, bool)`、`UnaryServerInterceptor(SecretResolver)`、`StreamServerInterceptor(SecretResolver)`、`NewPerRPCCredentials(appID string, secret []byte, secure bool)`、接口 `SecretResolver{ResolveSecret(ctx, appID string) ([]byte, error)}`。
- `secret`：`NewResolver(db *sql.DB, masterKey []byte) (*Resolver, error)`、`(*Resolver).EncryptSecret([]byte)([]byte,error)`、`(*Resolver).ResolveSecret(...)`（满足 auth.SecretResolver）。
- `dbtest`：`SetupSchema(t) *sql.DB`、`SeedApp(t, db) int64`、常量 `SeedAppKey="AK_order"`、`SeedDomain="order-system"`、本次新增 `StartRedis(t) string`（裸 host:port）。测试需本机 **Docker**。
- 依赖：`testcontainers-go` 全家桶锁 **v0.31.0**（redis 模块 API 为 `redis.RunContainer`）；`go-redis/v9 v9.7.0`。

---

## 流程要点（恢复执行时遵循）
1. 用 `subagent-driven-development` skill。每任务派**全新** general-purpose 实现子代理（机械任务用 sonnet），**附完整任务文本**（不要让子代理读计划文件），并说明工作目录是上面的 worktree 路径。
2. 实现 DONE 后：派**规格审查**子代理（独立读代码核对，不信报告）→ 通过后派**代码质量审查**子代理。
3. 审查发现问题 → 派修复子代理（无 SendMessage 工具，只能新派）→ 重跑验证 → 绿了再进下一任务。
4. 审查关注点对照 `~/.claude/skills/subagent-driven-development/` 三个 prompt 模板。
5. 已知复发模式：测试里 `assert.Eventually` 的 condition 闭包内**不要用 `require.*`**（会 Goexit 错 goroutine，吞诊断），用 `assert.*`+`return false`——任务 7/8 的 Eventually 用例注意。
6. 范围纪律：不重构任务范围外的既有文件（如 store.go 的错误包装风格不一致是已知项，不动）。

## 任务追踪
TaskList 中任务 1-4=completed，5-8=pending（本会话内存态；新会话需重建或以本文件为准）。

## 恢复命令速记
```bash
cd /home/tongyu/codes/Sydom/.claude/worktrees/control-plane-sync
git branch --show-current   # 应为 worktree-control-plane-sync
git log --oneline 1767f89..HEAD   # 应见 8 个提交，最新 93740cd
go build ./...              # 应通过
```
