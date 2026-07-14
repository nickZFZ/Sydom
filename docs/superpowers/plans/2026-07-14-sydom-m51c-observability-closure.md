# M5.1c 观测收尾 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 把 M5.1 遗留的 3 处 stubbed 可观测 TODO 接线到已存在的 obs 基座——authz.go Enforce 内部错误日志、relay drain 错误日志、sidecar snapshot_applied 指标。

**架构：** 全部 additive 可观测，授权决策/fail-close/apply 逻辑/循环行为/签名逐字不变。用 `obs.From(ctx)`（拦截器已注入 request-logger，缺省回退 slog.Default）加日志；用 syncclient Config 可选回调触发既有 `m.SnapshotApplied()` 指标。测试双证行为不变（既有全绿 + 强制内部错误仍 PermissionDenied）。

**技术栈：** Go、slog、`internal/obs`（`With`/`From`/`Metrics`）、testify、dbtest、bufconn。

规格：`docs/superpowers/specs/2026-07-14-sydom-m51c-observability-closure-design.md`

---

## 文件结构

- **修改** `internal/controlplane/mgmt/authz.go` — Enforce err 分支加 warn（+import obs）
- **创建** `internal/controlplane/mgmt/authz_obs_test.go` — 关 DB 触发 Enforce 内部错误 → 仍 PermissionDenied + 有日志
- **修改** `internal/controlplane/outbox/relay.go` — drain err 分支加 warn（+import obs）
- **修改** `internal/controlplane/app/run.go` — onElected 注入 logger 到 lctx
- **创建** `internal/controlplane/outbox/relay_obs_test.go` — failing pub → drain warn 被记
- **修改** `internal/sidecar/syncclient/config.go` — Config 加 `OnSnapshotApplied func()`
- **修改** `internal/sidecar/syncclient/client.go` — resync apply 成功后触发回调
- **修改** `internal/sidecar/app/run.go` — 接线 `scCfg.OnSnapshotApplied = m.SnapshotApplied`
- **修改** `internal/sidecar/syncclient/client_test.go` — 加 snapshot-hook 触发测试

---

### 任务 1：authz.go Enforce 内部错误日志（授权核心，行为双证）

**文件：**
- 修改：`internal/controlplane/mgmt/authz.go`
- 测试：`internal/controlplane/mgmt/authz_obs_test.go`

- [ ] **步骤 1：写失败的测试**

创建 `internal/controlplane/mgmt/authz_obs_test.go`：

```go
package mgmt_test

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/nickZFZ/Sydom/internal/obs"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Enforce 内部错误（关 DB → ReadPolicyVersion 失败）须【仍 fail-close 为 PermissionDenied】（行为不变）
// 且记一条 warn（新日志有齿）。用 scopeTenant 规则(ListApplications)——域由 tenant_id 纯字符串算出，
// Enforce 前不碰 DB，故错误确由 Enforce 内部触发。
func TestAuthorizeRule_EnforceInternalError_FailsClosedAndLogs(t *testing.T) {
	db := dbtest.SetupSchema(t)
	enf, err := adminauthz.NewEnforcer(db)
	require.NoError(t, err)
	require.NoError(t, db.Close()) // 关 DB → Enforce 内 ReadPolicyVersion 必失败

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil)) // 默认 Info 级，Warn 会输出
	ctx := obs.With(context.Background(), logger)

	msg := &adminv1.ListApplicationsRequest{TenantId: 1}
	_, err = mgmt.AuthorizeRule(ctx, enf, "/sydom.admin.v1.AdminService/ListApplications", "someprincipal", msg)
	require.Equal(t, codes.PermissionDenied, status.Code(err), "内部错误须仍 fail-close 为 PermissionDenied（行为不变）")
	require.Contains(t, buf.String(), "authz enforce internal error", "内部错误须记 warn（新日志有齿）")
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/mgmt/ -run TestAuthorizeRule_EnforceInternalError -v`
预期：FAIL（`buf` 不含 "authz enforce internal error"——日志未加）；`PermissionDenied` 断言应已通过（行为本就 fail-close）。

- [ ] **步骤 3：加日志（authz.go）**

`authz.go` import 块加 `"github.com/nickZFZ/Sydom/internal/obs"`。

把 `enf.Enforce(...)` 后的 TODO 注释替换为条件日志（返回逻辑逐字不变）：

```go
	allow, err := enf.Enforce(ctx, principal, domain, tdom, rule.resource, rule.action)
	if err != nil { // 仅内部错误(DB/策略加载故障)记日志；合法拒绝(!allow)是正常结果不记，避噪声
		obs.From(ctx).Warn("authz enforce internal error (fail-closed as permission denied)",
			"method", fullMethod, "principal", principal, "err", err)
	}
	if err != nil || !allow {
		return nil, status.Error(codes.PermissionDenied, "permission denied")
	}
	return cp.WithOperator(ctx, principal), nil
```

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/controlplane/mgmt/ -run TestAuthorizeRule_EnforceInternalError -v`
预期：PASS（PermissionDenied + 日志命中）。

- [ ] **步骤 5：行为不变回归（既有 authz 全套）**

运行：`go test ./internal/controlplane/mgmt/ -run 'Authz|Authorize|Scope|Isolation'`
预期：全 PASS（deny/allow/fail-close 路径逐字不变）。

- [ ] **步骤 6：Commit**

```bash
git add internal/controlplane/mgmt/authz.go internal/controlplane/mgmt/authz_obs_test.go
git commit -m "feat(cp): M5.1c authz.go Enforce 内部错误记 warn(obs.From(ctx),仅 err!=nil;返回逐字不变仍 fail-close PermissionDenied;关 DB 触发测试双证行为不变+日志有齿;授权决策零改)"
```

---

### 任务 2：relay drain 错误日志（relay.go + run.go）

**文件：**
- 修改：`internal/controlplane/outbox/relay.go`
- 修改：`internal/controlplane/app/run.go`
- 测试：`internal/controlplane/outbox/relay_obs_test.go`

- [ ] **步骤 1：写失败的测试**

创建 `internal/controlplane/outbox/relay_obs_test.go`：

```go
package outbox_test

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/outbox"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/nickZFZ/Sydom/internal/obs"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// drain 出错（pub 恒失败）须记一条 warn；循环随 ctx 超时正常退出（不 panic）。
func TestRelay_DrainError_Logged(t *testing.T) {
	db := dbtest.SetupSchema(t)
	blob, _ := proto.Marshal(&syncv1.Delta{Version: 1})
	_, err := db.Exec(`INSERT INTO policy_outbox (app_id, version, delta_proto) VALUES (1,1,$1)`, blob)
	require.NoError(t, err)

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	ctx, cancel := context.WithTimeout(obs.With(context.Background(), logger), 200*time.Millisecond)
	defer cancel()

	pub := &recordingPub{fail: true} // Publish 恒返回 assertErr → drainOnce error
	_ = outbox.RunRelayLoop(ctx, db, pub, 20*time.Millisecond) // 同 goroutine 阻塞至 ctx 超时

	require.Contains(t, buf.String(), "relay drain error", "drain 出错须记 warn")
}
```

（复用同包既有 `recordingPub{fail}` 与 `assertErr`。）

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/outbox/ -run TestRelay_DrainError_Logged -v`
预期：FAIL（buf 不含 "relay drain error"）。

- [ ] **步骤 3：加日志（relay.go）**

`relay.go` import 块加 `"github.com/nickZFZ/Sydom/internal/obs"`。drain err 分支加 warn：

```go
		n, err := drainOnce(ctx, db, pub)
		if err != nil && ctx.Err() == nil {
			// 记录但不中断循环（DB 抖动等）；下轮重试。
			obs.From(ctx).Warn("relay drain error (retrying next tick)", "err", err)
			n = 0
		}
```

- [ ] **步骤 4：run.go 注入 logger 到 lctx**

`internal/controlplane/app/run.go` 的 relay onElected 回调（`outbox.RunRelayLoop(lctx, ...)` 之前）加一行：

```go
			func(lctx context.Context) error {
				m.SetRelayLeader(true)
				defer m.SetRelayLeader(false)
				lctx = obs.With(lctx, logger.With("component", "relay"))
				return outbox.RunRelayLoop(lctx, db, pub, cfg.RelayPollInterval)
			})
```

（run.go 已 import obs〔用 `m.SetRelayLeader`〕与 slog〔`logger`〕。）

- [ ] **步骤 5：运行验证通过 + relay 回归**

运行：`go test ./internal/controlplane/outbox/`
预期：新测试 + 既有 relay 测试全 PASS（循环行为不变）。

- [ ] **步骤 6：Commit**

```bash
git add internal/controlplane/outbox/relay.go internal/controlplane/app/run.go internal/controlplane/outbox/relay_obs_test.go
git commit -m "feat(cp): M5.1c relay drain 错误记 warn(obs.From(ctx);run.go onElected 注入 component=relay logger 到 lctx;RunRelayLoop 签名不变,循环行为不变;failing pub 测试证有齿)"
```

---

### 任务 3：sidecar snapshot_applied 指标（syncclient 回调 + 接线）

**文件：**
- 修改：`internal/sidecar/syncclient/config.go`
- 修改：`internal/sidecar/syncclient/client.go`
- 修改：`internal/sidecar/app/run.go`
- 测试：`internal/sidecar/syncclient/client_test.go`

- [ ] **步骤 1：写失败的测试**

在 `client_test.go` 末尾加（内部测试包，可直接设私有 `c.cfg`）：

```go
// OnSnapshotApplied 回调须在 bootstrap 成功 apply 后触发一次。
func TestSyncClient_Bootstrap_FiresSnapshotHook(t *testing.T) {
	f := &fakeServer{snapFn: func(int) (*syncv1.Snapshot, error) {
		return &syncv1.Snapshot{Version: 1}, nil
	}}
	c, _, _ := startFake(t, f)
	var n int
	c.cfg.OnSnapshotApplied = func() { n++ }
	require.NoError(t, c.bootstrap(context.Background()))
	require.Equal(t, 1, n, "OnSnapshotApplied 应在成功 apply 后触发一次")
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/sidecar/syncclient/ -run TestSyncClient_Bootstrap_FiresSnapshotHook -v`
预期：编译失败（`Config` 无 `OnSnapshotApplied` 字段）。

- [ ] **步骤 3：Config 加回调字段**

`internal/sidecar/syncclient/config.go` 的 `Config` 结构末尾加：

```go
	// OnSnapshotApplied 可选：每次全量快照成功 apply 后触发（观测 hook，nil=no-op）。
	OnSnapshotApplied func()
```

- [ ] **步骤 4：resync 成功 apply 后触发回调**

`internal/sidecar/syncclient/client.go` 的 `resync`，`ApplySnapshot` 成功后、`markSync` 前加：

```go
	if err := c.engine.ApplySnapshot(ks); err != nil {
		return err
	}
	if c.cfg.OnSnapshotApplied != nil {
		c.cfg.OnSnapshotApplied()
	}
	c.markSync()
	return nil
```

- [ ] **步骤 5：运行验证通过**

运行：`go test ./internal/sidecar/syncclient/ -run TestSyncClient_Bootstrap_FiresSnapshotHook -v`
预期：PASS（n==1）。

- [ ] **步骤 6：sidecar run.go 接线指标**

`internal/sidecar/app/run.go` 的 `scCfg, err := buildSyncConfig(cfg)` 之后、`syncclient.New(scCfg, engine)` 之前加：

```go
	scCfg, err := buildSyncConfig(cfg)
	if err != nil {
		return err
	}
	scCfg.OnSnapshotApplied = m.SnapshotApplied // M5.1c: 全量快照 apply 计入 sydom_sidecar_snapshot_applied_total
	syncCli, err := syncclient.New(scCfg, engine)
```

（`m := obs.New()` 在 run.go:30 已在 scope；`m.SnapshotApplied` 是绑定方法值 `func()`，匹配 `OnSnapshotApplied` 字段类型。）

- [ ] **步骤 7：运行验证通过（syncclient + sidecar 全套）**

运行：`go test ./internal/sidecar/...`
预期：全 PASS（既有 bootstrap/delta 测试不回归；新 hook 测试过；sidecar app 编译+测试过）。

- [ ] **步骤 8：Commit**

```bash
git add internal/sidecar/syncclient/config.go internal/sidecar/syncclient/client.go internal/sidecar/app/run.go internal/sidecar/syncclient/client_test.go
git commit -m "feat(sidecar): M5.1c snapshot_applied 指标接线(syncclient.Config.OnSnapshotApplied 回调,resync 成功 apply 后触发单一 hook,apply 逻辑/New 签名不变;sidecar run.go 接 m.SnapshotApplied〔既有指标此前无调用方〕;bootstrap 触发测试证有齿)"
```

---

### 任务 4：全量验证 + 零行为改动核验

**文件：** 无（仅验证）

- [ ] **步骤 1：import 环 + 编译**

运行：`go build ./...`
预期：EXIT 0（无 import 环：obs 不依赖 outbox/mgmt，已核）。

- [ ] **步骤 2：全量测试**

运行：`go test ./...`
预期：EXIT 0（全绿）。

- [ ] **步骤 3：零行为改动核验（diff 逐行是纯 additive 可观测）**

运行：
```bash
git diff b5732b6..HEAD -- internal/controlplane/mgmt/authz.go internal/controlplane/outbox/relay.go internal/sidecar/syncclient/client.go | cat
```
预期：authz.go/relay.go/client.go 的改动**仅**为：加 import obs、加 `obs.From(ctx).Warn(...)` 日志行、加 `if OnSnapshotApplied != nil { ... }` 回调——**无任何返回值/控制流/决策/apply 逻辑改动**。人工确认返回语句、`if err != nil || !allow`、`return err` 等逐字未变。

- [ ] **步骤 4：无 commit（本任务仅验证）**

---

## 验收对照（M51C-1..8）

| # | 验收项 | 覆盖任务 |
|---|---|---|
| 1 | authz.go Enforce 内部错误记 warn，返回/控制流逐字不变 | 任务 1 步骤 3 |
| 2 | authz 行为双证（既有全绿 + 强制内部错误仍 PermissionDenied + 有日志） | 任务 1 步骤 4-5 |
| 3 | relay.go drain 记 warn + run.go 注入 lctx logger；循环不变 | 任务 2 |
| 4 | syncclient Config.OnSnapshotApplied 回调，resync apply 成功触发；apply 不变 | 任务 3 步骤 3-5 |
| 5 | sidecar run.go 接线 m.SnapshotApplied；bootstrap 触发测试 | 任务 3 步骤 6-7 |
| 6 | 三处签名不变；无 import 环 | 任务 4 步骤 1 |
| 7 | server.go 错误语义排除（未触碰） | 全程不碰 server.go writeResp |
| 8 | `go test ./...` EXIT 0 | 任务 4 步骤 2 |

## 自检

**1. 规格覆盖度：** authz 日志(任务1)、relay 日志+run.go(任务2)、syncclient 回调+sidecar 接线(任务3)、验证+零行为核验(任务4)。server.go 明确排除。无遗漏。

**2. 占位符扫描：** 无 TODO/待定；每步含完整代码。

**3. 类型一致性：** `obs.From(ctx)`（返 `*slog.Logger`）在 authz/relay 一致；`obs.With(ctx, logger)`（run.go）注入；`Config.OnSnapshotApplied func()`（任务3步3）与 client.go 调用点(步4)、sidecar 接线 `m.SnapshotApplied`(步6，`func (m *Metrics) SnapshotApplied()` 绑定方法值)、测试(步1) 一致；`AuthorizeRule`/`RunRelayLoop`/`SyncClient.New`/`resync` 签名均不变；`dbtest.SetupSchema`/`adminauthz.NewEnforcer`/`recordingPub{fail}`/`fakeServer{snapFn}`/`startFake`/`c.bootstrap` 均已核实存在。
