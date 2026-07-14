# M5.1c 观测收尾（stubbed 错误路径接线可观测）— 设计规格

**日期**：2026-07-14
**里程碑**：M5.1 可观测性 · 收尾（把 M5.1 遗留的 stubbed 可观测 TODO 接线）
**前序**：M5.1 obs 基座（`internal/obs`：Registry + 10 低基数指标 + /metrics + 结构化日志 + request_id + `obs.With`/`obs.From` ctx-logger）

## 目标

把 M5.1 当时 stubbed（"待接入日志/metric hook 后接入"）的 3 处错误/事件路径接线到已存在的 obs 基座。**铁律：纯加日志/指标，授权决策与 fail-close 行为逐字不变，用测试双证行为不变。** 触碰授权核心（`mgmt/authz.go`）已获用户明确 greenlit（一次性放宽"零触碰授权核心"姿态，仅限本片纯 additive 可观测）。

## 范围

三处，全部 additive（零行为改动）：

1. `mgmt/authz.go` — Enforce 内部错误日志（授权核心）
2. `outbox/relay.go` + `controlplane/app/run.go` — relay drain 错误日志
3. `sidecar/syncclient` + `sidecar/app/run.go` — snapshot_applied 指标

## 非目标（YAGNI / 排除）

- `mgmt/server.go:43` **错误语义细化**：是错误码语义（需 PolicyManager 先暴露领域 sentinel error），非可观测。本片不做，保留其 TODO。
- 新增 obs 指标（relay drain 只加日志，不扩既有 10 指标集；snapshot_applied 指标已存在，只是接线）。
- 改动任一处的控制流/决策/apply 逻辑。

## 详细设计

### 1. `mgmt/authz.go` — Enforce 内部错误日志

`AuthorizeRule` 里 `enf.Enforce(...)` 之后，仅当 `err != nil`（内部错误：DB/策略加载故障）记一条 warn，随后**返回值逐字不变**：

```go
allow, err := enf.Enforce(ctx, principal, domain, tdom, rule.resource, rule.action)
if err != nil { // 仅内部错误记日志；合法拒绝(!allow)不记（避噪声，拒绝是正常结果非错误）
	obs.From(ctx).Warn("authz enforce internal error (fail-closed as permission denied)",
		"method", fullMethod, "principal", principal, "err", err)
}
if err != nil || !allow {
	return nil, status.Error(codes.PermissionDenied, "permission denied")
}
```

- `obs.From(ctx)`：mgmt gRPC 拦截器（`server.go:241` `m.UnaryServerInterceptor(logger)`）已在 `grpc.go:24` 注入 `obs.With(ctx, logger.With("request_id",rid))`；故 gRPC 路径拿到带 request_id 的 logger；Console/REST 等无注入处回退 `slog.Default()`（`obs.From` 保证非 nil）。**到处安全，无签名改动**（`AuthorizeRule` 13 调用方零改）。
- **决策零改**：内部错误仍 fail-close 为 PermissionDenied；客户端响应逐字不变（仍只见 `permission denied`）；差别仅在运维日志能看见被吞的真实内部错误。
- import 加 `internal/obs`（`obs` 依赖 `cp`；`mgmt` 已依赖 `obs`〔server.go 用 `m.UnaryServerInterceptor`〕与 `cp`，无新循环）。

### 2. `outbox/relay.go` + `app/run.go` — relay drain 错误日志

`relay.go` 的 drain 出错分支（当前静默 `n = 0`）加日志：

```go
n, err := drainOnce(ctx, db, pub)
if err != nil && ctx.Err() == nil {
	obs.From(ctx).Warn("relay drain error (retrying next tick)", "err", err)
	n = 0
}
```

`RunRelayLoop` 签名不变（用 `obs.From(ctx)`）。`app/run.go` 的 onElected 回调注入 logger 到 lctx：

```go
func(lctx context.Context) error {
	m.SetRelayLeader(true)
	defer m.SetRelayLeader(false)
	lctx = obs.With(lctx, logger.With("component", "relay"))
	return outbox.RunRelayLoop(lctx, db, pub, cfg.RelayPollInterval)
}
```

- `outbox` import 加 `internal/obs`（`obs`→`cp`，`outbox`→`obs` 不成环）。
- 循环行为不变（出错仍续跑重试，只多一条日志）。

### 3. `sidecar/syncclient` + sidecar `app/run.go` — snapshot_applied 指标

`syncclient.Config` 加可选回调：

```go
type Config struct {
	// ...既有字段...
	OnSnapshotApplied func() // 可选：每次全量快照成功 apply 后触发（观测 hook，nil=no-op）
}
```

`bootstrap`（`client.go:130`）成功 apply 后触发（**唯一** snapshot-apply 站点；`ApplyDelta` 非快照不计）：

```go
if err := c.engine.ApplySnapshot(ks); err != nil {
	return err // 逐字不变
}
if c.cfg.OnSnapshotApplied != nil {
	c.cfg.OnSnapshotApplied()
}
```

sidecar `app/run.go` 构造 syncclient Config 时接线：`OnSnapshotApplied: m.SnapshotApplied`（`obs.Metrics.SnapshotApplied()` 已存在于 `metrics.go:115`，此前无调用方）。

- `SyncClient.New` 签名不变（回调经 Config 传入）。apply 逻辑逐字不变（只在成功后多一次 no-op-able 回调）。

## 测试（证明行为不变是核心）

### authz（授权核心，双证）
- **行为不变（既有全绿）**：既有 `authz_scope_test`/`account_isolation_test` 等全部通过 = deny/allow 路径逐字不变。
- **内部错误仍 fail-close（新，有齿不变量）**：构造 Enforce 内部错误（如关闭 enforcer 底层 `*sql.DB` 后调 `AuthorizeRule`）→ 断言 **仍返回 `codes.PermissionDenied`**（不变量），且经 `obs.With(ctx, 捕获logger)` 断言 warn 被记（新日志有齿）。若 Enforce 因缓存不触 DB 而不报错，则改用一个必然报错的构造（如未初始化/已关闭 enforcer）；断言核心是"内部错误→仍 PermissionDenied + 有日志"。

### relay
- failing `pub`（Publish 恒错）→ `RunRelayLoop` 短跑（ctx 超时取消）→ 经捕获 logger 断言 drain warn 被记；循环不 panic、正常随 ctx 退出（既有 relay 测试不回归）。

### sidecar 指标
- 起 in-memory/stub PolicySync 或复用既有 syncclient 测试夹具，bootstrap 成功 apply 一次 → 断言 `OnSnapshotApplied` 被调用（用计数闭包）或 `m.SnapshotApplied` 计数 +1。

### 全量
- `go test ./...` EXIT 0。

## 不变量

- **授权决策逻辑逐字不变**：`authz.go` diff 仅 `err != nil` 分支加一条 log，返回值/控制流不动；deny/allow/fail-close 语义零改
- `AuthorizeRule` / `RunRelayLoop` / `SyncClient.New` **签名不变**
- apply 逻辑 / 循环行为 / 决策 零改
- 机器 diff 会显示 `authz.go`/`relay.go` 改动（用户 greenlit），但可逐行证明为纯 additive 可观测
- 无新 import 循环（obs→cp；mgmt/outbox→obs 不成环）
- `go test ./...` EXIT 0

## 验收（M51C-1..8）

1. authz.go：Enforce 内部错误记 warn（obs.From(ctx)），返回值/控制流逐字不变
2. authz 行为不变双证：既有 authz 全绿 + 强制内部错误仍 PermissionDenied + 有日志
3. relay.go：drain 错误记 warn；run.go onElected 注入 logger 到 lctx；循环行为不变
4. syncclient：Config.OnSnapshotApplied 回调，bootstrap apply 成功后触发；apply 逻辑不变
5. sidecar run.go：接线 `OnSnapshotApplied: m.SnapshotApplied`；指标被触发（测试）
6. 三处签名均不变；无 import 循环
7. server.go 错误语义排除（TODO 保留，spec 记明）
8. `go test ./...` EXIT 0
