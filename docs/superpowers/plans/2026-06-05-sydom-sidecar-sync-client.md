# 司域 · Sidecar 同步客户端 (④-3) 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 把 Sidecar 接成控制面 `PolicySync` 的 gRPC 订阅客户端：先 `PullSnapshot` 建基线、再 `Subscribe` 持续对账，把翻译后的快照/增量喂给已注入的 `*kernel.Engine`（内核再 fan-out 到 casbin 与 ④-2 dataperm）。

**架构：** 新建包 `internal/sidecar/syncclient`（避开 stdlib `sync` 命名冲突，白盒测试同包）。纯库层，不出 cmd/binary。`SyncClient` 持注入的 `*kernel.Engine` + gRPC 连接 + 原子连接态。`Run(ctx)` 阻塞式对账循环，内部自管退避重连；ctx 取消干净退出。一切 pull/translate/apply 错误一律 fail-close：不推进版本、退避后重拉，绝不部分应用、绝不放行。

**技术栈：** Go 1.26、gRPC（`google.golang.org/grpc`）、生成桩 `gen/sydom/sync/v1`（`syncv1`）、`internal/auth`（HMAC PerRPC 凭据）、`internal/sidecar/kernel`（域类型 + apply）、`internal/sidecar/dataperm`（端到端断言）、`bufconn` + `testify/require`（测试）。

---

## 关键设计澄清（动手前必读）

**REMOVE 变更的字段搬运 —— 这是本计划最易踩的坑，务必照 Task 3 实现：**

- **线上（proto）约定**（cp `translate.go:20-25`）：`PolicyChange` 对 REMOVE 把待删行放进 `old_rule`，`rule` 留空（nil）。
- **内核约定**（`kernel/engine.go`）：`ApplyDelta` 对 **所有** op 先做越域校验 `pc.Rule.domainValue() != e.domain`（engine.go:120-127），且 REMOVE 分支执行 `removeRule(pc.Rule)`（engine.go:146-163）——内核读的是 `pc.Rule`，**不是** `pc.OldRule`。
- **结论**：④-3 翻译层必须把 proto 的 `old_rule` 搬进内核 `Rule`（REMOVE 场景）。否则内核 `Rule` 为零值 → `domainValue()==""` ≠ 域 → 整条 delta 被判 `ErrForeignDomain` 拒绝 → 每次删除都强制重拉，甚至静默不生效 → **陈旧授权删不掉 = 扩权 = 一致性事故**（见 [[feedback-consistency-over-simplicity]]）。

正确映射（Task 3 `policyChangeFromProto` 落地）：

| proto op | 内核 `Rule` 来源 | 内核 `OldRule` 来源 |
|---|---|---|
| ADD | `pc.rule` | —（零值） |
| REMOVE | `pc.old_rule` | —（零值） |
| UPDATE | `pc.rule` | `pc.old_rule` |

> 规格 §4 原文「`OldRule`(REMOVE/UPDATE)」与内核读 `pc.Rule` 相矛盾，本计划按内核契约修正（已回源核实 engine.go:120-163、translate.go:20-25）。Task 3 的 `TestPolicyChangeFromProto_Remove_RuleGoesToRule` + Task 5 的「REMOVE 真删」端到端断言共同守住此不变量。

**已核实的生成桩签名**（`gen/sydom/sync/v1`，勿凭记忆改）：
- `syncv1.NewPolicySyncClient(cc) PolicySyncClient`；`PolicySyncClient.Subscribe(ctx, *SubscribeRequest, opts) (PolicySync_SubscribeClient, error)`、`.PullSnapshot(ctx, *PullSnapshotRequest, opts) (*Snapshot, error)`。
- `PolicySync_SubscribeClient.Recv() (*SyncEvent, error)`。
- `syncv1.RegisterPolicySyncServer(s, srv)`；`PolicySyncServer` 接口 + `UnimplementedPolicySyncServer`；`PolicySync_SubscribeServer.Send(*SyncEvent) error` + 内嵌 `grpc.ServerStream`（含 `Context()`）。
- `SyncEvent.GetEvent()` 返回 oneof 包装 `*SyncEvent_Delta{Delta}` / `*SyncEvent_Heartbeat{Heartbeat}` / `*SyncEvent_SnapshotRequired{SnapshotRequired}`；另有 `GetDelta()/GetHeartbeat()/GetSnapshotRequired()`。
- 消息字段（proto3 getter 风格）：`Snapshot{GetVersion()uint64, GetRules()[]*PolicyRule, GetDataPolicies()[]*DataPolicy}`、`Delta{GetVersion()uint64, GetPolicyChanges()[]*PolicyChange, GetDataChanges()[]*DataPolicyChange}`、`PolicyRule{GetPtype()string, GetValues()[]string}`、`PolicyChange{GetOp()ChangeOp, GetRule()*PolicyRule, GetOldRule()*PolicyRule}`、`DataPolicy{GetId()uint64, GetSubjectType/SubjectId/Resource/Condition/Effect()string}`、`DataPolicyChange{GetOp(), GetPolicy()*DataPolicy}`、`Heartbeat{GetCurrentVersion()uint64}`、`SnapshotRequired{GetCurrentVersion()uint64, GetReason()string}`、`SubscribeRequest{LastAppliedVersion uint64}`、`PullSnapshotRequest{}`、枚举 `ChangeOp_CHANGE_OP_UNSPECIFIED/ADD/REMOVE/UPDATE`。

**已核实的依赖签名：**
- `kernel.New(domain string, c cache.Cache, applier DataPolicyApplier) (*Engine, error)`；`(*Engine).ApplySnapshot(Snapshot) error`、`.ApplyDelta(Delta) error`、`.Version() uint64`、`.Ready() bool`、`.GetImplicitRolesForUser(user,dom) ([]string,error)`。哨兵：`kernel.ErrNotReady/ErrForeignDomain/ErrStaleVersion`。
- 域类型见 `kernel/types.go`：`Rule{Ptype string; V [6]string}`、`ChangeAdd/ChangeUpdate/ChangeRemove`、`PolicyChange{Op,Rule,OldRule}`、`DataPolicy{ID,SubjectType,SubjectID,Resource,Condition,Effect}`、`DataPolicyChange{Op,Policy}`、`Delta{Version,PolicyChanges,DataChanges}`、`Snapshot{Version,Rules,DataPolicies}`。
- `*dataperm.Table` 满足 `kernel.DataPolicyApplier`；`dataperm.NewTable()`、`dataperm.NewFilter(roles RoleResolver, table *Table) *Filter`、`(*Filter).FilterSQL(user,dom,resource string, attrs map[string]any) (SQLResult, error)`，`SQLResult{SQL string; Args []any}`。`*kernel.Engine` 满足 `dataperm.RoleResolver`。
- `auth.NewPerRPCCredentials(appID string, secret []byte, secure bool) credentials.PerRPCCredentials`。

---

## 文件结构

包 `internal/sidecar/syncclient`：

| 文件 | 职责 |
|---|---|
| `config.go` | `Config`（连接/认证/退避参数）+ 默认值常量（`defaultBackoffInitial/Max`、`maxRecvMsgSize`） |
| `backoff.go` | 有界指数退避 + 全抖动小工具（`backoff`，`capFor`/`next`/`reset`） |
| `translate.go` | syncv1 proto → 内核域类型（**反向**于 cp `translate`）；纯函数 |
| `client.go` | `SyncClient`：gRPC 拨号 + `Run(ctx)` 对账循环 + 重连退避 + 暴露状态 |
| `backoff_test.go` | 退避序列有界 + 抖动范围 + reset（白盒） |
| `translate_test.go` | snapshot/delta/rule 变长补齐/op 映射/datapolicy 含 effect/REMOVE 字段搬运/错误路径（白盒） |
| `client_test.go` | bufconn + fake `PolicySync` 服务端，配真实 `kernel.Engine`+`dataperm.Table` 的对账行为矩阵（白盒） |

---

## Task 1：包骨架与 Config

**文件：**
- 创建：`internal/sidecar/syncclient/config.go`

`config.go` 是纯数据声明（结构体 + 常量），无行为逻辑，故以 `go build` 作为验证（TDD 针对行为，不针对声明）。

- [ ] **步骤 1：写 config.go**

```go
// Package syncclient 把 Sidecar 接成控制面 PolicySync 的 gRPC 订阅客户端：
// 先 PullSnapshot 建基线、再 Subscribe 持续对账，把翻译后的策略喂给注入的 *kernel.Engine。
// 一切 pull/translate/apply 错误一律 fail-close：不推进版本、退避后重拉，绝不部分应用、绝不放行。
package syncclient

import (
	"time"

	"google.golang.org/grpc"
)

const (
	defaultBackoffInitial = 500 * time.Millisecond
	defaultBackoffMax     = 30 * time.Second
	// maxRecvMsgSize 容纳全量快照（对齐 gRPC spec §8 的 64MB unary）。
	maxRecvMsgSize = 64 * 1024 * 1024
)

// Config 是 SyncClient 的连接/认证/退避参数。
type Config struct {
	Endpoint    string            // 控制面 PolicySync 地址
	AppID       string            // app_key：HMAC 认证标识 + 流路由
	Secret      []byte            // HMAC 密钥（调用方从配置/解密提供）
	Secure      bool              // 传输层是否 TLS（false=insecure；true 时由 DialOptions 提供传输凭据）
	DialOptions []grpc.DialOption // 附加 dial 选项（TLS 凭据等）

	BackoffInitial time.Duration // 退避初值（零值用 500ms）
	BackoffMax     time.Duration // 退避上限（零值用 30s）
}
```

- [ ] **步骤 2：验证编译通过**

运行：`go build ./internal/sidecar/syncclient/`
预期：成功，无输出（包内暂只有 config.go）。

- [ ] **步骤 3：Commit**

```bash
git add internal/sidecar/syncclient/config.go
git commit -m "feat(sidecar/syncclient): Config 与默认参数骨架（④-3）"
```

---

## Task 2：退避工具 backoff

**文件：**
- 创建：`internal/sidecar/syncclient/backoff.go`
- 测试：`internal/sidecar/syncclient/backoff_test.go`

把指数退避拆成纯函数 `capFor(attempt)`（确定性、可断言序列）与 `next()`（在 cap 上叠全抖动）。便于无 sleep 单测。

- [ ] **步骤 1：写失败的测试**

`internal/sidecar/syncclient/backoff_test.go`：

```go
package syncclient

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBackoff_CapForSequence(t *testing.T) {
	b := newBackoff(500*time.Millisecond, 30*time.Second)
	want := []time.Duration{
		500 * time.Millisecond, // 2^0
		1 * time.Second,        // 2^1
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		30 * time.Second, // 32s 截顶到 max
		30 * time.Second, // 此后恒为 max
	}
	for i, w := range want {
		require.Equal(t, w, b.capFor(i), "attempt %d 的 cap 不符", i)
	}
}

func TestBackoff_NextJitterWithinCap(t *testing.T) {
	b := newBackoff(500*time.Millisecond, 30*time.Second)
	// 注入确定性 rng：返回 n-1（cap 上界附近），断言 next 落在 [0, cap)。
	b.rng = func(n int64) int64 { return n - 1 }
	for i := 0; i < 10; i++ {
		cap := b.capFor(b.attempt)
		d := b.next()
		require.GreaterOrEqual(t, d, time.Duration(0))
		require.Less(t, d, cap, "全抖动必须严格小于 cap")
	}
}

func TestBackoff_Reset(t *testing.T) {
	b := newBackoff(500*time.Millisecond, 30*time.Second)
	_ = b.next()
	_ = b.next()
	b.reset()
	require.Equal(t, 500*time.Millisecond, b.capFor(b.attempt), "reset 后 attempt 归零")
}

func TestBackoff_DefaultsOnZero(t *testing.T) {
	b := newBackoff(0, 0)
	require.Equal(t, defaultBackoffInitial, b.capFor(0))
	require.Equal(t, defaultBackoffMax, b.capFor(100))
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/sidecar/syncclient/ -run TestBackoff -v`
预期：编译失败 `undefined: newBackoff`。

- [ ] **步骤 3：写 backoff.go 实现**

```go
package syncclient

import (
	"math/rand"
	"time"
)

// backoff 是有界指数退避 + 全抖动状态机。须经 newBackoff 构造。
type backoff struct {
	initial time.Duration
	max     time.Duration
	attempt int
	rng     func(n int64) int64 // 返回 [0,n)，注入便于测试
}

func newBackoff(initial, max time.Duration) *backoff {
	if initial <= 0 {
		initial = defaultBackoffInitial
	}
	if max <= 0 {
		max = defaultBackoffMax
	}
	if initial > max {
		initial = max
	}
	return &backoff{initial: initial, max: max, rng: rand.Int63n}
}

// capFor 返回第 attempt 次重试的退避上界 min(max, initial*2^attempt)，防溢出。
func (b *backoff) capFor(attempt int) time.Duration {
	capped := b.initial
	for i := 0; i < attempt; i++ {
		if capped >= b.max/2 {
			return b.max
		}
		capped *= 2
	}
	if capped > b.max {
		return b.max
	}
	return capped
}

// next 返回下一次退避时长（全抖动：[0, cap)）并推进 attempt。
func (b *backoff) next() time.Duration {
	c := b.capFor(b.attempt)
	b.attempt++
	if c <= 0 {
		return 0
	}
	return time.Duration(b.rng(int64(c)))
}

// reset 清零退避（连接健康后调用）。
func (b *backoff) reset() { b.attempt = 0 }
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/sidecar/syncclient/ -run TestBackoff -v`
预期：4 个测试 PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/sidecar/syncclient/backoff.go internal/sidecar/syncclient/backoff_test.go
git commit -m "feat(sidecar/syncclient): 有界指数退避 + 全抖动（④-3）"
```

---

## Task 3：翻译层 translate（syncv1 → 域类型）

**文件：**
- 创建：`internal/sidecar/syncclient/translate.go`
- 测试：`internal/sidecar/syncclient/translate_test.go`

反向于 cp `translate`。纯函数。**核心不变量见「关键设计澄清」：REMOVE 的 `old_rule` 必须搬进内核 `Rule`。** 翻译错误（变长越界 / 未知 op）一律返 error，由对账循环当作「该笔不可用 → 重拉」处理。

- [ ] **步骤 1：写失败的测试**

`internal/sidecar/syncclient/translate_test.go`：

```go
package syncclient

import (
	"testing"

	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
	"github.com/nickZFZ/Sydom/internal/sidecar/kernel"
	"github.com/stretchr/testify/require"
)

func TestRuleFromProto_PadsShortValues(t *testing.T) {
	r, err := ruleFromProto(&syncv1.PolicyRule{Ptype: "g", Values: []string{"alice", "manager", "dom1"}})
	require.NoError(t, err)
	require.Equal(t, kernel.Rule{Ptype: "g", V: [6]string{"alice", "manager", "dom1", "", "", ""}}, r)
}

func TestRuleFromProto_RejectsTooManyValues(t *testing.T) {
	_, err := ruleFromProto(&syncv1.PolicyRule{
		Ptype:  "p",
		Values: []string{"1", "2", "3", "4", "5", "6", "7"}, // 7 > 6
	})
	require.Error(t, err, "变长越界必须 fail-close 报错")
}

func TestOpFromProto(t *testing.T) {
	for _, tc := range []struct {
		in   syncv1.ChangeOp
		want kernel.ChangeOp
	}{
		{syncv1.ChangeOp_CHANGE_OP_ADD, kernel.ChangeAdd},
		{syncv1.ChangeOp_CHANGE_OP_REMOVE, kernel.ChangeRemove},
		{syncv1.ChangeOp_CHANGE_OP_UPDATE, kernel.ChangeUpdate},
	} {
		got, err := opFromProto(tc.in)
		require.NoError(t, err)
		require.Equal(t, tc.want, got)
	}
}

func TestOpFromProto_RejectsUnspecifiedAndUnknown(t *testing.T) {
	_, err := opFromProto(syncv1.ChangeOp_CHANGE_OP_UNSPECIFIED)
	require.Error(t, err)
	_, err = opFromProto(syncv1.ChangeOp(99))
	require.Error(t, err)
}

// REMOVE 的待删行在线上躺在 old_rule，必须搬进内核 Rule（否则内核越域校验拒绝整条 delta）。
func TestPolicyChangeFromProto_Remove_RuleGoesToRule(t *testing.T) {
	pc, err := policyChangeFromProto(&syncv1.PolicyChange{
		Op:      syncv1.ChangeOp_CHANGE_OP_REMOVE,
		OldRule: &syncv1.PolicyRule{Ptype: "p", Values: []string{"manager", "dom1", "order", "read", "allow"}},
	})
	require.NoError(t, err)
	require.Equal(t, kernel.ChangeRemove, pc.Op)
	require.Equal(t, "p", pc.Rule.Ptype)
	require.Equal(t, "dom1", pc.Rule.V[1], "REMOVE 待删行必须落在 Rule（内核读 pc.Rule）")
	require.Equal(t, kernel.Rule{}, pc.OldRule, "REMOVE 不用 OldRule")
}

func TestPolicyChangeFromProto_Add_RuleGoesToRule(t *testing.T) {
	pc, err := policyChangeFromProto(&syncv1.PolicyChange{
		Op:   syncv1.ChangeOp_CHANGE_OP_ADD,
		Rule: &syncv1.PolicyRule{Ptype: "p", Values: []string{"manager", "dom1", "order", "read", "allow"}},
	})
	require.NoError(t, err)
	require.Equal(t, kernel.ChangeAdd, pc.Op)
	require.Equal(t, "dom1", pc.Rule.V[1])
	require.Equal(t, kernel.Rule{}, pc.OldRule)
}

func TestPolicyChangeFromProto_Update_BothRules(t *testing.T) {
	pc, err := policyChangeFromProto(&syncv1.PolicyChange{
		Op:      syncv1.ChangeOp_CHANGE_OP_UPDATE,
		Rule:    &syncv1.PolicyRule{Ptype: "p", Values: []string{"manager", "dom1", "order", "write"}},
		OldRule: &syncv1.PolicyRule{Ptype: "p", Values: []string{"manager", "dom1", "order", "read"}},
	})
	require.NoError(t, err)
	require.Equal(t, "write", pc.Rule.V[3])
	require.Equal(t, "read", pc.OldRule.V[3])
}

func TestDataPolicyFromProto_CarriesEffect(t *testing.T) {
	dp := dataPolicyFromProto(&syncv1.DataPolicy{
		Id: 7, SubjectType: "role", SubjectId: "manager",
		Resource: "order", Condition: `{"field":"a","op":"EQ","value":1}`, Effect: "deny",
	})
	require.Equal(t, kernel.DataPolicy{
		ID: 7, SubjectType: "role", SubjectID: "manager",
		Resource: "order", Condition: `{"field":"a","op":"EQ","value":1}`, Effect: "deny",
	}, dp)
}

func TestSnapshotFromProto_RoundTripsRulesAndData(t *testing.T) {
	ks, err := snapshotFromProto(&syncv1.Snapshot{
		Version: 9,
		Rules: []*syncv1.PolicyRule{
			{Ptype: "p", Values: []string{"manager", "dom1", "order", "read", "allow"}},
			{Ptype: "g", Values: []string{"alice", "manager", "dom1"}},
		},
		DataPolicies: []*syncv1.DataPolicy{
			{Id: 1, SubjectType: "role", SubjectId: "manager", Resource: "order", Condition: "{}", Effect: "allow"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, uint64(9), ks.Version)
	require.Len(t, ks.Rules, 2)
	require.Len(t, ks.DataPolicies, 1)
	require.Equal(t, "allow", ks.DataPolicies[0].Effect)
}

func TestSnapshotFromProto_PropagatesRuleError(t *testing.T) {
	_, err := snapshotFromProto(&syncv1.Snapshot{
		Rules: []*syncv1.PolicyRule{{Ptype: "p", Values: []string{"1", "2", "3", "4", "5", "6", "7"}}},
	})
	require.Error(t, err, "快照内任一条非法 → 整快照不可用")
}

func TestDeltaFromProto_RoundTripsChanges(t *testing.T) {
	kd, err := deltaFromProto(&syncv1.Delta{
		Version: 12,
		PolicyChanges: []*syncv1.PolicyChange{
			{Op: syncv1.ChangeOp_CHANGE_OP_ADD, Rule: &syncv1.PolicyRule{Ptype: "p", Values: []string{"x", "dom1", "o", "a", "allow"}}},
		},
		DataChanges: []*syncv1.DataPolicyChange{
			{Op: syncv1.ChangeOp_CHANGE_OP_REMOVE, Policy: &syncv1.DataPolicy{Id: 3}},
		},
	})
	require.NoError(t, err)
	require.Equal(t, uint64(12), kd.Version)
	require.Equal(t, kernel.ChangeAdd, kd.PolicyChanges[0].Op)
	require.Equal(t, kernel.ChangeRemove, kd.DataChanges[0].Op)
	require.Equal(t, uint64(3), kd.DataChanges[0].Policy.ID)
}

func TestDeltaFromProto_PropagatesOpError(t *testing.T) {
	_, err := deltaFromProto(&syncv1.Delta{
		Version:       1,
		PolicyChanges: []*syncv1.PolicyChange{{Op: syncv1.ChangeOp_CHANGE_OP_UNSPECIFIED}},
	})
	require.Error(t, err)
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/sidecar/syncclient/ -run 'TestRule|TestOp|TestPolicyChange|TestDataPolicy|TestSnapshot|TestDelta' -v`
预期：编译失败 `undefined: ruleFromProto`（及其余翻译函数）。

- [ ] **步骤 3：写 translate.go 实现**

```go
package syncclient

import (
	"fmt"

	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
	"github.com/nickZFZ/Sydom/internal/sidecar/kernel"
)

// snapshotFromProto 把 syncv1.Snapshot 翻译为内核 Snapshot；任一条目非法 → error（fail-close）。
func snapshotFromProto(s *syncv1.Snapshot) (kernel.Snapshot, error) {
	out := kernel.Snapshot{Version: s.GetVersion()}
	for _, pr := range s.GetRules() {
		r, err := ruleFromProto(pr)
		if err != nil {
			return kernel.Snapshot{}, err
		}
		out.Rules = append(out.Rules, r)
	}
	for _, dp := range s.GetDataPolicies() {
		out.DataPolicies = append(out.DataPolicies, dataPolicyFromProto(dp))
	}
	return out, nil
}

// deltaFromProto 把 syncv1.Delta 翻译为内核 Delta。
func deltaFromProto(d *syncv1.Delta) (kernel.Delta, error) {
	out := kernel.Delta{Version: d.GetVersion()}
	for _, pc := range d.GetPolicyChanges() {
		c, err := policyChangeFromProto(pc)
		if err != nil {
			return kernel.Delta{}, err
		}
		out.PolicyChanges = append(out.PolicyChanges, c)
	}
	for _, dc := range d.GetDataChanges() {
		c, err := dataPolicyChangeFromProto(dc)
		if err != nil {
			return kernel.Delta{}, err
		}
		out.DataChanges = append(out.DataChanges, c)
	}
	return out, nil
}

// ruleFromProto 把变长 values（≤6）拷进 Rule.V[6]，缺位补 ""（与 cp 裁尾空串互逆）。>6 → error。
func ruleFromProto(pr *syncv1.PolicyRule) (kernel.Rule, error) {
	vals := pr.GetValues()
	if len(vals) > 6 {
		return kernel.Rule{}, fmt.Errorf("syncclient: policy rule has %d values, max 6", len(vals))
	}
	r := kernel.Rule{Ptype: pr.GetPtype()}
	copy(r.V[:], vals)
	return r, nil
}

// opFromProto 映射 ChangeOp；UNSPECIFIED/未知 → error（fail-close）。
func opFromProto(op syncv1.ChangeOp) (kernel.ChangeOp, error) {
	switch op {
	case syncv1.ChangeOp_CHANGE_OP_ADD:
		return kernel.ChangeAdd, nil
	case syncv1.ChangeOp_CHANGE_OP_REMOVE:
		return kernel.ChangeRemove, nil
	case syncv1.ChangeOp_CHANGE_OP_UPDATE:
		return kernel.ChangeUpdate, nil
	default:
		return 0, fmt.Errorf("syncclient: unknown change op %v", op)
	}
}

// policyChangeFromProto 按「关键设计澄清」搬运字段：
// ADD→Rule(rule)；REMOVE→Rule(old_rule)；UPDATE→Rule(rule)+OldRule(old_rule)。
// 内核 ApplyDelta 对所有 op 读 pc.Rule 做越域校验与 add/remove，故 REMOVE 的待删行必须落在 Rule。
func policyChangeFromProto(pc *syncv1.PolicyChange) (kernel.PolicyChange, error) {
	op, err := opFromProto(pc.GetOp())
	if err != nil {
		return kernel.PolicyChange{}, err
	}
	out := kernel.PolicyChange{Op: op}
	switch op {
	case kernel.ChangeAdd:
		if out.Rule, err = ruleFromProto(pc.GetRule()); err != nil {
			return kernel.PolicyChange{}, err
		}
	case kernel.ChangeRemove:
		if out.Rule, err = ruleFromProto(pc.GetOldRule()); err != nil {
			return kernel.PolicyChange{}, err
		}
	case kernel.ChangeUpdate:
		if out.Rule, err = ruleFromProto(pc.GetRule()); err != nil {
			return kernel.PolicyChange{}, err
		}
		if out.OldRule, err = ruleFromProto(pc.GetOldRule()); err != nil {
			return kernel.PolicyChange{}, err
		}
	}
	return out, nil
}

// dataPolicyFromProto 直传各字段，含刚打通的 Effect（空串透传，内核/dataperm 兜底归一）。
func dataPolicyFromProto(dp *syncv1.DataPolicy) kernel.DataPolicy {
	return kernel.DataPolicy{
		ID:          dp.GetId(),
		SubjectType: dp.GetSubjectType(),
		SubjectID:   dp.GetSubjectId(),
		Resource:    dp.GetResource(),
		Condition:   dp.GetCondition(),
		Effect:      dp.GetEffect(),
	}
}

func dataPolicyChangeFromProto(dc *syncv1.DataPolicyChange) (kernel.DataPolicyChange, error) {
	op, err := opFromProto(dc.GetOp())
	if err != nil {
		return kernel.DataPolicyChange{}, err
	}
	return kernel.DataPolicyChange{Op: op, Policy: dataPolicyFromProto(dc.GetPolicy())}, nil
}
```

> 注：`ruleFromProto(nil)` 安全——proto3 getter 对 nil 返回零值（`GetPtype()=""`、`GetValues()=nil`），但本实现对每个 op 只取该 op 实际带的字段，不会把 nil 行误塞进 Rule。

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/sidecar/syncclient/ -run 'TestRule|TestOp|TestPolicyChange|TestDataPolicy|TestSnapshot|TestDelta' -v`
预期：全部 PASS（含 `TestPolicyChangeFromProto_Remove_RuleGoesToRule`）。

- [ ] **步骤 5：Commit**

```bash
git add internal/sidecar/syncclient/translate.go internal/sidecar/syncclient/translate_test.go
git commit -m "feat(sidecar/syncclient): syncv1→域类型翻译层，REMOVE 待删行归一到 Rule（④-3）"
```

---

## Task 4：SyncClient 核心 + 引导（PullSnapshot → Ready）

**文件：**
- 创建：`internal/sidecar/syncclient/client.go`
- 测试：`internal/sidecar/syncclient/client_test.go`

本任务落地 `SyncClient` 拨号、状态暴露、`Run` 对账循环骨架（引导 + Subscribe + Recv 循环，事件分派先用 no-op `handleEvent` 占位，后续任务逐个事件类型填充）。同时建立可复用的 bufconn fake 服务端测试夹具。

- [ ] **步骤 1：写失败的测试（含夹具）**

`internal/sidecar/syncclient/client_test.go`：

```go
package syncclient

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
	"github.com/nickZFZ/Sydom/internal/sidecar/dataperm"
	"github.com/nickZFZ/Sydom/internal/sidecar/kernel"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

// fakeServer 是脚本化 PolicySync 服务端：
// snapFn 决定第 N 次 PullSnapshot 的返回；subscribeFn 决定第 M 次 Subscribe 的行为。
type fakeServer struct {
	syncv1.UnimplementedPolicySyncServer

	mu          sync.Mutex
	pullCalls   int
	subCalls    int
	snapFn      func(call int) (*syncv1.Snapshot, error)
	subscribeFn func(call int, s syncv1.PolicySync_SubscribeServer) error
}

func (f *fakeServer) PullSnapshot(_ context.Context, _ *syncv1.PullSnapshotRequest) (*syncv1.Snapshot, error) {
	f.mu.Lock()
	f.pullCalls++
	n, fn := f.pullCalls, f.snapFn
	f.mu.Unlock()
	return fn(n)
}

func (f *fakeServer) Subscribe(_ *syncv1.SubscribeRequest, s syncv1.PolicySync_SubscribeServer) error {
	f.mu.Lock()
	f.subCalls++
	n, fn := f.subCalls, f.subscribeFn
	f.mu.Unlock()
	return fn(n, s)
}

func (f *fakeServer) pullCount() int { f.mu.Lock(); defer f.mu.Unlock(); return f.pullCalls }

// sendThenBlock 依次 Send evs，然后阻塞到 stream ctx 取消（模拟长连保持，避免触发重连）。
func sendThenBlock(evs ...*syncv1.SyncEvent) func(int, syncv1.PolicySync_SubscribeServer) error {
	return func(_ int, s syncv1.PolicySync_SubscribeServer) error {
		for _, ev := range evs {
			if err := s.Send(ev); err != nil {
				return err
			}
		}
		<-s.Context().Done()
		return s.Context().Err()
	}
}

func deltaEv(d *syncv1.Delta) *syncv1.SyncEvent {
	return &syncv1.SyncEvent{Event: &syncv1.SyncEvent_Delta{Delta: d}}
}
func heartbeatEv(v uint64) *syncv1.SyncEvent {
	return &syncv1.SyncEvent{Event: &syncv1.SyncEvent_Heartbeat{Heartbeat: &syncv1.Heartbeat{CurrentVersion: v}}}
}
func snapshotRequiredEv() *syncv1.SyncEvent {
	return &syncv1.SyncEvent{Event: &syncv1.SyncEvent_SnapshotRequired{SnapshotRequired: &syncv1.SnapshotRequired{}}}
}

// startFake 起 bufconn fake 服务端，构造真实 Engine+Table，返回拨号好的 SyncClient。
func startFake(t *testing.T, f *fakeServer) (*SyncClient, *kernel.Engine, *dataperm.Table) {
	t.Helper()
	g := grpc.NewServer()
	syncv1.RegisterPolicySyncServer(g, f)
	lis := bufconn.Listen(1024 * 1024)
	go func() { _ = g.Serve(lis) }()
	t.Cleanup(g.Stop)

	table := dataperm.NewTable()
	engine, err := kernel.New("dom1", nil, table)
	require.NoError(t, err)

	c, err := New(Config{
		Endpoint: "passthrough:///bufnet",
		AppID:    "app-1",
		Secret:   []byte("secret"),
		Secure:   false,
		DialOptions: []grpc.DialOption{
			grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		},
		BackoffInitial: time.Millisecond,
		BackoffMax:     5 * time.Millisecond,
	}, engine)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c, engine, table
}

// runClient 在后台跑 Run，返回 cancel（测试结束自动取消）。
func runClient(t *testing.T, c *SyncClient) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = c.Run(ctx) }()
}

func snapV5() *syncv1.Snapshot {
	return &syncv1.Snapshot{
		Version: 5,
		Rules:   []*syncv1.PolicyRule{{Ptype: "g", Values: []string{"alice", "manager", "dom1"}}},
	}
}

func TestSyncClient_Bootstrap_PullsSnapshotAndBecomesReady(t *testing.T) {
	f := &fakeServer{
		snapFn:      func(int) (*syncv1.Snapshot, error) { return snapV5(), nil },
		subscribeFn: sendThenBlock(),
	}
	c, engine, _ := startFake(t, f)
	runClient(t, c)

	require.Eventually(t, func() bool {
		return engine.Ready() && engine.Version() == 5
	}, time.Second, 5*time.Millisecond, "引导后应 Ready 且 Version=5")
	require.Eventually(t, func() bool { return c.Connected() }, time.Second, 5*time.Millisecond)
	require.False(t, c.LastSyncAt().IsZero(), "引导成功应刷新 LastSyncAt")
}

// 空策略 app 也应达「已同步」（Ready=true），而非永远 fail-close deny-all。
func TestSyncClient_Bootstrap_EmptySnapshotStillReady(t *testing.T) {
	f := &fakeServer{
		snapFn:      func(int) (*syncv1.Snapshot, error) { return &syncv1.Snapshot{Version: 0}, nil },
		subscribeFn: sendThenBlock(),
	}
	c, engine, _ := startFake(t, f)
	runClient(t, c)

	require.Eventually(t, func() bool {
		return engine.Ready() && engine.Version() == 0
	}, time.Second, 5*time.Millisecond, "空快照也应 Ready=true、Version=0")
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/sidecar/syncclient/ -run TestSyncClient_Bootstrap -v`
预期：编译失败 `undefined: New`（`SyncClient` 尚未定义）。

- [ ] **步骤 3：写 client.go 实现（核心 + 引导 + 事件循环骨架）**

```go
package syncclient

import (
	"context"
	"sync/atomic"
	"time"

	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
	"github.com/nickZFZ/Sydom/internal/auth"
	"github.com/nickZFZ/Sydom/internal/sidecar/kernel"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// SyncClient 把 Sidecar 接成控制面 PolicySync 的订阅客户端：持续对账并把结果喂给注入的
// *kernel.Engine。不做任何鉴权决策；最终 fail-close 由 ④-4 在 !Ready() 时拒绝。
type SyncClient struct {
	cfg    Config
	engine *kernel.Engine

	conn   *grpc.ClientConn
	client syncv1.PolicySyncClient

	lastSyncAt atomic.Int64 // UnixNano；0 表示从未成功同步
	connected  atomic.Bool  // 订阅流是否在线
}

// New 拨号控制面并构造 SyncClient（不启动对账，调用方另起 goroutine 调 Run）。
// Secure=false 时注入 insecure 传输凭据；Secure=true 时传输凭据须由 cfg.DialOptions 提供。
func New(cfg Config, engine *kernel.Engine) (*SyncClient, error) {
	opts := []grpc.DialOption{
		grpc.WithPerRPCCredentials(auth.NewPerRPCCredentials(cfg.AppID, cfg.Secret, cfg.Secure)),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(maxRecvMsgSize)),
	}
	if !cfg.Secure {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}
	opts = append(opts, cfg.DialOptions...)

	conn, err := grpc.NewClient(cfg.Endpoint, opts...)
	if err != nil {
		return nil, err
	}
	return &SyncClient{
		cfg:    cfg,
		engine: engine,
		conn:   conn,
		client: syncv1.NewPolicySyncClient(conn),
	}, nil
}

// Version/Ready 透传内核；Connected/LastSyncAt 是自身连接态，供 ④-4 自定 fail-open/close 阈值。
func (c *SyncClient) Version() uint64 { return c.engine.Version() }
func (c *SyncClient) Ready() bool     { return c.engine.Ready() }
func (c *SyncClient) Connected() bool { return c.connected.Load() }

func (c *SyncClient) LastSyncAt() time.Time {
	ns := c.lastSyncAt.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

// Close 关闭底层连接（Run 退出后调用）。
func (c *SyncClient) Close() error { return c.conn.Close() }

func (c *SyncClient) markSync() { c.lastSyncAt.Store(time.Now().UnixNano()) }

// Run 阻塞式对账循环：引导 → 订阅消费 → 断连退避重连。ctx 取消即干净退出返回 nil。
func (c *SyncClient) Run(ctx context.Context) error {
	bo := newBackoff(c.cfg.BackoffInitial, c.cfg.BackoffMax)
	for {
		if ctx.Err() != nil {
			return nil
		}
		if err := c.connectAndServe(ctx, bo); err != nil {
			if !c.sleep(ctx, bo.next()) {
				return nil
			}
			continue
		}
		return nil // connectAndServe 返回 nil = ctx 取消
	}
}

// connectAndServe 引导 + 订阅消费，直到流断（返回 err 触发重连）或 ctx 取消（返回 nil）。
func (c *SyncClient) connectAndServe(ctx context.Context, bo *backoff) error {
	if err := c.bootstrap(ctx); err != nil {
		return err
	}
	stream, err := c.client.Subscribe(ctx, &syncv1.SubscribeRequest{
		LastAppliedVersion: c.engine.Version(),
	})
	if err != nil {
		return err
	}
	c.connected.Store(true)
	defer c.connected.Store(false)

	for {
		ev, err := stream.Recv()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err // 流断 → 退避重连
		}
		bo.reset() // 收到事件 = 流健康，退避归零
		if err := c.handleEvent(ctx, ev); err != nil {
			return err // resync 连接级失败 → 升级为重连
		}
	}
}

// bootstrap 显式拉全量快照建基线（PullSnapshot→ApplySnapshot），使空策略 app 也达 Ready=true。
func (c *SyncClient) bootstrap(ctx context.Context) error { return c.resync(ctx) }

// resync 重拉全量快照对齐内核：成功刷新 lastSyncAt 返回 nil（继续消费同一流）；失败返回 err。
func (c *SyncClient) resync(ctx context.Context) error {
	snap, err := c.client.PullSnapshot(ctx, &syncv1.PullSnapshotRequest{})
	if err != nil {
		return err
	}
	ks, err := snapshotFromProto(snap)
	if err != nil {
		return err
	}
	if err := c.engine.ApplySnapshot(ks); err != nil {
		return err
	}
	c.markSync()
	return nil
}

// handleEvent 按事件类型分派；后续任务逐类型填充。
func (c *SyncClient) handleEvent(_ context.Context, _ *syncv1.SyncEvent) error {
	return nil
}

// sleep 睡 d，期间 ctx 取消则返回 false（应退出）。
func (c *SyncClient) sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/sidecar/syncclient/ -run TestSyncClient_Bootstrap -v`
预期：2 个测试 PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/sidecar/syncclient/client.go internal/sidecar/syncclient/client_test.go
git commit -m "feat(sidecar/syncclient): SyncClient 拨号 + 引导 + 对账循环骨架（④-3）"
```

---

## Task 5：稳态增量 + REMOVE 真删（连续 delta 应用）

**文件：**
- 修改：`internal/sidecar/syncclient/client.go`（新增 `handleDelta`，`handleEvent` 接 Delta 分支）
- 修改：`internal/sidecar/syncclient/client_test.go`（新增测试）

填充连续增量路径：`d.Version == Version()+1` 才翻译 + `ApplyDelta`；非连续一律重拉（gap 兜底，落后情形在 Task 7 细化）。同时用一条 REMOVE delta 端到端证明「待删行经翻译落在 Rule、内核真把授权删掉」。

- [ ] **步骤 1：写失败的测试**

在 `client_test.go` 追加：

```go
func TestSyncClient_SteadyDeltas_AppliedMonotonically(t *testing.T) {
	addP := func(act string) *syncv1.PolicyChange {
		return &syncv1.PolicyChange{
			Op:   syncv1.ChangeOp_CHANGE_OP_ADD,
			Rule: &syncv1.PolicyRule{Ptype: "p", Values: []string{"manager", "dom1", "order", act, "allow"}},
		}
	}
	f := &fakeServer{
		snapFn: func(int) (*syncv1.Snapshot, error) { return snapV5(), nil },
		subscribeFn: sendThenBlock(
			deltaEv(&syncv1.Delta{Version: 6, PolicyChanges: []*syncv1.PolicyChange{addP("read")}}),
			deltaEv(&syncv1.Delta{Version: 7, PolicyChanges: []*syncv1.PolicyChange{addP("write")}}),
			deltaEv(&syncv1.Delta{Version: 8, PolicyChanges: []*syncv1.PolicyChange{addP("delete")}}),
		),
	}
	c, engine, _ := startFake(t, f)
	runClient(t, c)

	require.Eventually(t, func() bool { return engine.Version() == 8 }, time.Second, 5*time.Millisecond)
	require.Equal(t, 1, f.pullCount(), "连续 delta 不应触发任何重拉")

	allow, err := engine.Enforce("alice", "dom1", "order", "write") // 经 manager 角色
	require.NoError(t, err)
	require.True(t, allow)
}

// REMOVE delta 端到端：内核读 pc.Rule 删行，证明翻译层把 old_rule 搬进了 Rule。
func TestSyncClient_RemoveDelta_RevokesGrant(t *testing.T) {
	snap := &syncv1.Snapshot{
		Version: 5,
		Rules: []*syncv1.PolicyRule{
			{Ptype: "g", Values: []string{"alice", "manager", "dom1"}},
			{Ptype: "p", Values: []string{"manager", "dom1", "order", "read", "allow"}},
		},
	}
	f := &fakeServer{
		snapFn: func(int) (*syncv1.Snapshot, error) { return snap, nil },
		subscribeFn: sendThenBlock(
			deltaEv(&syncv1.Delta{Version: 6, PolicyChanges: []*syncv1.PolicyChange{{
				Op:      syncv1.ChangeOp_CHANGE_OP_REMOVE,
				OldRule: &syncv1.PolicyRule{Ptype: "p", Values: []string{"manager", "dom1", "order", "read", "allow"}},
			}}}),
		),
	}
	c, engine, _ := startFake(t, f)
	runClient(t, c)

	require.Eventually(t, func() bool { return engine.Version() == 6 }, time.Second, 5*time.Millisecond)
	require.Equal(t, 1, f.pullCount(), "REMOVE 不应被误判越域而触发重拉")

	allow, err := engine.Enforce("alice", "dom1", "order", "read")
	require.NoError(t, err)
	require.False(t, allow, "REMOVE 后该授权必须被真正撤销")
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/sidecar/syncclient/ -run 'TestSyncClient_SteadyDeltas|TestSyncClient_RemoveDelta' -v`
预期：FAIL —— `handleEvent` 当前 no-op，Version 停在 5、Enforce("write") 为 false（`TestSyncClient_SteadyDeltas` 失败）；REMOVE 测试 Version 停在 5。

- [ ] **步骤 3：实现 handleDelta 并接入 handleEvent**

把 `client.go` 的 `handleEvent` 替换为分派 + 新增 `handleDelta`：

```go
// handleEvent 按事件类型分派。
func (c *SyncClient) handleEvent(ctx context.Context, ev *syncv1.SyncEvent) error {
	switch e := ev.GetEvent().(type) {
	case *syncv1.SyncEvent_Delta:
		return c.handleDelta(ctx, e.Delta)
	default:
		return nil // 其它事件类型在后续任务填充
	}
}

// handleDelta 仅当版本连续（==Version()+1）才增量 apply；非连续 → 重拉（gap 兜底）。
// 落后/重放（≤Version()）与 ErrStaleVersion 的精细处理见 Task 7。
func (c *SyncClient) handleDelta(ctx context.Context, d *syncv1.Delta) error {
	if d.GetVersion() == c.engine.Version()+1 {
		kd, err := deltaFromProto(d)
		if err != nil {
			return c.resync(ctx) // 翻译失败 → 重拉，绝不部分应用
		}
		if err := c.engine.ApplyDelta(kd); err != nil {
			return c.resync(ctx) // apply 失败 → 重拉
		}
		c.markSync()
		return nil
	}
	return c.resync(ctx) // 非连续（含 gap） → 重拉
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/sidecar/syncclient/ -run 'TestSyncClient_SteadyDeltas|TestSyncClient_RemoveDelta' -v`
预期：2 个测试 PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/sidecar/syncclient/client.go internal/sidecar/syncclient/client_test.go
git commit -m "feat(sidecar/syncclient): 连续 delta 增量应用 + REMOVE 撤销贯通（④-3）"
```

---

## Task 6：gap / heartbeat / SnapshotRequired → 重拉

**文件：**
- 修改：`internal/sidecar/syncclient/client.go`（`handleEvent` 补 Heartbeat / SnapshotRequired 分支）
- 修改：`internal/sidecar/syncclient/client_test.go`（新增测试）

补全反熵与显式重拉触发：心跳超前 → 漏包重拉；心跳持平 → 仅刷新活性；SnapshotRequired → 重拉；delta gap → 重拉（Task 5 默认分支已覆盖，这里加显式断言锁住）。所有重拉断言 `pullCount` 自增、Version 跳到新快照版本。

- [ ] **步骤 1：写失败的测试**

在 `client_test.go` 追加：

```go
// snapStep 第 1 次 PullSnapshot 返回 v5，第 2 次起返回 vHi（模拟重拉对齐到更高版本）。
func snapStep(hi uint64) func(int) (*syncv1.Snapshot, error) {
	return func(call int) (*syncv1.Snapshot, error) {
		if call == 1 {
			return snapV5(), nil
		}
		return &syncv1.Snapshot{Version: hi, Rules: snapV5().Rules}, nil
	}
}

func TestSyncClient_HeartbeatAhead_TriggersResync(t *testing.T) {
	f := &fakeServer{
		snapFn:      snapStep(9),
		subscribeFn: sendThenBlock(heartbeatEv(9)), // 9 > 引导版本 5 → 漏包
	}
	c, engine, _ := startFake(t, f)
	runClient(t, c)

	require.Eventually(t, func() bool { return engine.Version() == 9 }, time.Second, 5*time.Millisecond)
	require.GreaterOrEqual(t, f.pullCount(), 2, "心跳超前应触发重拉")
}

func TestSyncClient_HeartbeatLevel_NoResync(t *testing.T) {
	addP := &syncv1.PolicyChange{
		Op:   syncv1.ChangeOp_CHANGE_OP_ADD,
		Rule: &syncv1.PolicyRule{Ptype: "p", Values: []string{"manager", "dom1", "order", "read", "allow"}},
	}
	f := &fakeServer{
		snapFn: func(int) (*syncv1.Snapshot, error) { return snapV5(), nil },
		subscribeFn: sendThenBlock(
			heartbeatEv(5), // 持平，不重拉
			deltaEv(&syncv1.Delta{Version: 6, PolicyChanges: []*syncv1.PolicyChange{addP}}),
		),
	}
	c, engine, _ := startFake(t, f)
	runClient(t, c)

	require.Eventually(t, func() bool { return engine.Version() == 6 }, time.Second, 5*time.Millisecond)
	require.Equal(t, 1, f.pullCount(), "持平心跳不应触发重拉，流照常消费后续 delta")
}

func TestSyncClient_SnapshotRequired_TriggersResync(t *testing.T) {
	f := &fakeServer{
		snapFn:      snapStep(8),
		subscribeFn: sendThenBlock(snapshotRequiredEv()),
	}
	c, engine, _ := startFake(t, f)
	runClient(t, c)

	require.Eventually(t, func() bool { return engine.Version() == 8 }, time.Second, 5*time.Millisecond)
	require.GreaterOrEqual(t, f.pullCount(), 2, "SnapshotRequired 应触发重拉")
}

func TestSyncClient_GapDelta_TriggersResync(t *testing.T) {
	f := &fakeServer{
		snapFn:      snapStep(10),
		subscribeFn: sendThenBlock(deltaEv(&syncv1.Delta{Version: 12})), // 12 > 5+1 → gap
	}
	c, engine, _ := startFake(t, f)
	runClient(t, c)

	require.Eventually(t, func() bool { return engine.Version() == 10 }, time.Second, 5*time.Millisecond)
	require.GreaterOrEqual(t, f.pullCount(), 2, "非连续 delta 必须重拉，绝不 apply 跳版变更")
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/sidecar/syncclient/ -run 'TestSyncClient_Heartbeat|TestSyncClient_SnapshotRequired|TestSyncClient_GapDelta' -v`
预期：`HeartbeatAhead` / `SnapshotRequired` FAIL（当前 default 分支忽略这两类事件，Version 停在 5、pullCount=1）。`GapDelta` 与 `HeartbeatLevel` 可能已 PASS（gap 走 Task 5 default 重拉；持平心跳被忽略后流继续消费 delta v6）。

- [ ] **步骤 3：补全 handleEvent 的 Heartbeat / SnapshotRequired 分支**

把 `client.go` 的 `handleEvent` 替换为：

```go
// handleEvent 按事件类型分派。
func (c *SyncClient) handleEvent(ctx context.Context, ev *syncv1.SyncEvent) error {
	switch e := ev.GetEvent().(type) {
	case *syncv1.SyncEvent_Delta:
		return c.handleDelta(ctx, e.Delta)
	case *syncv1.SyncEvent_Heartbeat:
		if e.Heartbeat.GetCurrentVersion() > c.engine.Version() {
			return c.resync(ctx) // 漏包 → 重拉
		}
		c.markSync() // 流活性证明
		return nil
	case *syncv1.SyncEvent_SnapshotRequired:
		return c.resync(ctx)
	default:
		return nil // 未知事件忽略（前向兼容）
	}
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/sidecar/syncclient/ -run 'TestSyncClient_Heartbeat|TestSyncClient_SnapshotRequired|TestSyncClient_GapDelta' -v`
预期：4 个测试全部 PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/sidecar/syncclient/client.go internal/sidecar/syncclient/client_test.go
git commit -m "feat(sidecar/syncclient): 心跳反熵/SnapshotRequired/gap 统一重拉对齐（④-3）"
```

---

## Task 7：落后/重放 delta 丢弃 + ErrStaleVersion 容忍

**文件：**
- 修改：`internal/sidecar/syncclient/client.go`（细化 `handleDelta`）
- 修改：`internal/sidecar/syncclient/client_test.go`（新增测试）

Task 5 的 `handleDelta` 把任何非连续（含 `≤Version()` 的重放/重复）都当 gap 重拉——这对重放是浪费且会无谓打快照。本任务区分：`≤Version()` → 丢弃刷新活性（非错误）；`==Version()+1` → apply，其中 `ApplyDelta` 返 `ErrStaleVersion`（与并发重拉竞态）→ 丢弃；其它 apply/翻译错误 → 重拉；`>Version()+1` → 重拉。

> 说明：`ErrStaleVersion` 仅在「检查 `==cur+1` 后、`ApplyDelta` 前」版本被并发重拉推进时出现，单 goroutine 顺序流无法确定性构造，故不写易碎的并发专测——该分支由 Task 11 的 `-race` 全程覆盖，此处仅以代码 + 注释固化容忍语义。重放丢弃（现实可达路径）则用确定性测试锁死。

- [ ] **步骤 1：写失败的测试**

在 `client_test.go` 追加：

```go
// 重放/重复 delta（version ≤ 当前）必须丢弃，绝不触发重拉。
func TestSyncClient_ReplayDelta_Discarded(t *testing.T) {
	addP := func(act string) *syncv1.PolicyChange {
		return &syncv1.PolicyChange{
			Op:   syncv1.ChangeOp_CHANGE_OP_ADD,
			Rule: &syncv1.PolicyRule{Ptype: "p", Values: []string{"manager", "dom1", "order", act, "allow"}},
		}
	}
	f := &fakeServer{
		snapFn: func(int) (*syncv1.Snapshot, error) { return snapV5(), nil },
		subscribeFn: sendThenBlock(
			deltaEv(&syncv1.Delta{Version: 6, PolicyChanges: []*syncv1.PolicyChange{addP("read")}}),
			deltaEv(&syncv1.Delta{Version: 5}),                                                     // 重放（==引导版本）
			deltaEv(&syncv1.Delta{Version: 6}),                                                     // 重复（==当前）
			deltaEv(&syncv1.Delta{Version: 7, PolicyChanges: []*syncv1.PolicyChange{addP("write")}}), // 继续推进
		),
	}
	c, engine, _ := startFake(t, f)
	runClient(t, c)

	require.Eventually(t, func() bool { return engine.Version() == 7 }, time.Second, 5*time.Millisecond)
	require.Equal(t, 1, f.pullCount(), "重放/重复 delta 必须静默丢弃，绝不重拉")
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/sidecar/syncclient/ -run TestSyncClient_ReplayDelta -v`
预期：FAIL —— 当前 `handleDelta` 把 v5、v6（≤当前）走 default 重拉，`pullCount` 涨到 2/3，断言 `==1` 失败。

- [ ] **步骤 3：细化 handleDelta**

把 `client.go` 的 `handleDelta` 替换为：

```go
import "errors" // 文件顶部 import 补 errors

// handleDelta 按版本关系分派：
//   ≤Version()         → 重放/重复，丢弃刷新活性（非错误）
//   ==Version()+1      → 翻译 + ApplyDelta；ErrStaleVersion（并发竞态）→ 丢弃；其它错误 → 重拉
//   >Version()+1       → gap → 重拉（绝不 apply 跳版变更）
func (c *SyncClient) handleDelta(ctx context.Context, d *syncv1.Delta) error {
	cur := c.engine.Version()
	switch {
	case d.GetVersion() <= cur:
		c.markSync()
		return nil
	case d.GetVersion() == cur+1:
		kd, err := deltaFromProto(d)
		if err != nil {
			return c.resync(ctx)
		}
		if err := c.engine.ApplyDelta(kd); err != nil {
			if errors.Is(err, kernel.ErrStaleVersion) {
				c.markSync() // 已被并发重拉推进，丢弃非错误
				return nil
			}
			return c.resync(ctx)
		}
		c.markSync()
		return nil
	default:
		return c.resync(ctx)
	}
}
```

> 注：`client.go` 顶部 import 块需加入 `"errors"`（与 `"context"` 等并列）。

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/sidecar/syncclient/ -run TestSyncClient_ReplayDelta -v`
预期：PASS。再跑 `go test ./internal/sidecar/syncclient/ -run TestSyncClient -v` 确认 Task 5/6 既有测试不回归。

- [ ] **步骤 5：Commit**

```bash
git add internal/sidecar/syncclient/client.go internal/sidecar/syncclient/client_test.go
git commit -m "feat(sidecar/syncclient): 重放 delta 丢弃 + ErrStaleVersion 竞态容忍（④-3）"
```

---

## Task 8：流断 → 退避重连 → 重新引导

**文件：**
- 修改：`internal/sidecar/syncclient/client_test.go`（新增测试）

`Run` 循环已具备重连能力（`connectAndServe` 返 err → 退避 → 重入）。本任务用 fake 在第 1 次 Subscribe 立即返错、第 2 次起正常推送，验证：重连后重新 `PullSnapshot`（`pullCount≥2`）并消费新流的 delta。属行为锁定测试（无新增产品代码；若不通过则暴露重连缺陷需修）。

- [ ] **步骤 1：写测试**

在 `client_test.go` 追加（顶部 import 需含 `"google.golang.org/grpc/codes"`、`"google.golang.org/grpc/status"`）：

```go
func TestSyncClient_StreamError_ReconnectsAndRebootstraps(t *testing.T) {
	addP := &syncv1.PolicyChange{
		Op:   syncv1.ChangeOp_CHANGE_OP_ADD,
		Rule: &syncv1.PolicyRule{Ptype: "p", Values: []string{"manager", "dom1", "order", "read", "allow"}},
	}
	f := &fakeServer{
		snapFn: func(int) (*syncv1.Snapshot, error) { return snapV5(), nil },
		subscribeFn: func(call int, s syncv1.PolicySync_SubscribeServer) error {
			if call == 1 {
				return status.Error(codes.Unavailable, "boom") // 首连立即断
			}
			// 重连后正常推送一条连续 delta，然后保持
			if err := s.Send(deltaEv(&syncv1.Delta{Version: 6, PolicyChanges: []*syncv1.PolicyChange{addP}})); err != nil {
				return err
			}
			<-s.Context().Done()
			return s.Context().Err()
		},
	}
	c, engine, _ := startFake(t, f)
	runClient(t, c)

	require.Eventually(t, func() bool { return engine.Version() == 6 }, 2*time.Second, 5*time.Millisecond,
		"重连后应重新引导并消费新流 delta")
	require.GreaterOrEqual(t, f.pullCount(), 2, "重连必然重新 PullSnapshot 引导")
	require.True(t, c.Connected(), "稳态后 Connected 应为 true")
}
```

- [ ] **步骤 2：运行测试验证通过**

运行：`go test ./internal/sidecar/syncclient/ -run TestSyncClient_StreamError -v`
预期：PASS（`Run` 退避后重连、重新引导、消费 v6）。若 FAIL，按 systematic-debugging 排查 `connectAndServe`/`Run` 重连分支再修。

- [ ] **步骤 3：Commit**

```bash
git add internal/sidecar/syncclient/client_test.go
git commit -m "test(sidecar/syncclient): 流断重连重新引导行为锁定（④-3）"
```

---

## Task 9：端到端 —— effect=deny 经内核 fan-out 到 dataperm 反映在 FilterSQL

**文件：**
- 修改：`internal/sidecar/syncclient/client_test.go`（新增测试）

打通 ④-2/④-3/effect 链的头条断言：快照带「一条 allow + 一条 deny」数据策略 → 翻译 → `ApplySnapshot` → fan-out 到 `dataperm.Table` → `Filter.FilterSQL` 渲染出 `deny override`。期望 SQL 复用 dataperm 既有用例口径 `(dept = ? AND NOT (status IN (?, ?)))`。

- [ ] **步骤 1：写测试**

在 `client_test.go` 追加：

```go
func TestSyncClient_EndToEnd_DenyEffectReachesFilterSQL(t *testing.T) {
	snap := &syncv1.Snapshot{
		Version: 5,
		Rules:   []*syncv1.PolicyRule{{Ptype: "g", Values: []string{"alice", "manager", "dom1"}}},
		DataPolicies: []*syncv1.DataPolicy{
			{Id: 1, SubjectType: "role", SubjectId: "manager", Resource: "order",
				Condition: `{"field":"dept","op":"EQ","value":"$user.department"}`, Effect: "allow"},
			{Id: 2, SubjectType: "role", SubjectId: "manager", Resource: "order",
				Condition: `{"field":"status","op":"IN","value":["locked","void"]}`, Effect: "deny"},
		},
	}
	f := &fakeServer{
		snapFn:      func(int) (*syncv1.Snapshot, error) { return snap, nil },
		subscribeFn: sendThenBlock(),
	}
	c, engine, table := startFake(t, f)
	runClient(t, c)

	require.Eventually(t, func() bool { return engine.Ready() && engine.Version() == 5 },
		time.Second, 5*time.Millisecond)

	filter := dataperm.NewFilter(engine, table)
	res, err := filter.FilterSQL("alice", "dom1", "order", map[string]any{"department": "HR"})
	require.NoError(t, err)
	require.Equal(t, "(dept = ? AND NOT (status IN (?, ?)))", res.SQL,
		"effect=deny 必须经内核 fan-out 反映为 FilterSQL 的 NOT 段")
	require.Equal(t, []any{"HR", "locked", "void"}, res.Args)
}
```

- [ ] **步骤 2：运行测试验证通过**

运行：`go test ./internal/sidecar/syncclient/ -run TestSyncClient_EndToEnd -v`
预期：PASS。若 SQL 不符，先用 `go test ./internal/sidecar/dataperm/ -run TestFilterSQL_ParamizedAndDenyOverrides` 核对 dataperm 渲染口径，再定位翻译/fan-out。

- [ ] **步骤 3：Commit**

```bash
git add internal/sidecar/syncclient/client_test.go
git commit -m "test(sidecar/syncclient): effect=deny 端到端贯通 FilterSQL（④-2/④-3/effect）"
```

---

## Task 10：ctx 取消 → Run 干净返回

**文件：**
- 修改：`internal/sidecar/syncclient/client_test.go`（新增测试）

验证 `Run(ctx)` 在 ctx 取消后干净退出返回 nil（无论处于引导、订阅消费还是退避睡眠）。

- [ ] **步骤 1：写测试**

在 `client_test.go` 追加：

```go
func TestSyncClient_ContextCancel_RunReturnsNil(t *testing.T) {
	f := &fakeServer{
		snapFn:      func(int) (*syncv1.Snapshot, error) { return snapV5(), nil },
		subscribeFn: sendThenBlock(), // 引导后长连保持
	}
	c, engine, _ := startFake(t, f)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()

	require.Eventually(t, func() bool { return engine.Ready() }, time.Second, 5*time.Millisecond)
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err, "ctx 取消应使 Run 干净返回 nil")
	case <-time.After(2 * time.Second):
		t.Fatal("Run 未在 ctx 取消后及时退出")
	}
}
```

- [ ] **步骤 2：运行测试验证通过**

运行：`go test ./internal/sidecar/syncclient/ -run TestSyncClient_ContextCancel -v`
预期：PASS。

- [ ] **步骤 3：Commit**

```bash
git add internal/sidecar/syncclient/client_test.go
git commit -m "test(sidecar/syncclient): ctx 取消 Run 干净退出（④-3）"
```

---

## Task 11：全量 -race 验证与收尾

**文件：** 无新增；全包验证。

- [ ] **步骤 1：全包竞态测试**

运行：`go test -race ./internal/sidecar/syncclient/...`
预期：`ok`，无 DATA RACE 报告（对账写 lastSyncAt/connected vs 暴露状态读并发安全）。

- [ ] **步骤 2：vet + 全仓回归**

运行：`go vet ./internal/sidecar/syncclient/... && go build ./... && go test ./internal/sidecar/...`
预期：vet 无输出；build 成功；sidecar 各包测试 `ok`（确认未破坏 kernel/dataperm）。

- [ ] **步骤 3：收尾**

调用 finishing-a-development-branch 技能决定集成方式（合并 / PR / 清理）。最终提交信息建议：

```bash
git add docs/superpowers/plans/2026-06-05-sydom-sidecar-sync-client.md
git commit -m "docs: Sidecar 同步客户端（④-3）TDD 实现计划"
```

更新进度记忆 `project_detailed_design_progress.md`：④-3 同步客户端已实现并入 main；下一步 ④-4 鉴权 API。

---

## 自检结果

**1. 规格覆盖度**（对照 spec 各节）：
- §3 组件分解：`config.go`(Task 1)、`backoff.go`(Task 2)、`translate.go`(Task 3)、`client.go`(Task 4-7)，文件职责一一对应。✅
- §4 翻译层：`snapshotFromProto`/`deltaFromProto`/`ruleFromProto`(变长补齐+越界报错)/`opFromProto`(UNSPECIFIED/未知报错)/`policyChangeFromProto`/`dataPolicyFromProto`(含 Effect)/`dataPolicyChangeFromProto` 全部在 Task 3，并修正了 REMOVE 字段搬运。✅
- §5.1 引导（pull-first）：Task 4 `bootstrap`→`resync`→Subscribe(last=Version())。✅
- §5.2 事件循环：Delta(Task 5/7)、Heartbeat(Task 6)、SnapshotRequired(Task 6)、Recv 错(Task 8)。✅
- §5.3 重拉 helper：`resync`(Task 4)，gap/翻译失败/apply 失败/心跳超前/SnapshotRequired 复用(Task 5/6/7)。✅
- §5.4 重连退避：`backoff`(Task 2) + `Run` 退避(Task 4) + 流断重连(Task 8)。✅
- §6 错误矩阵：PullSnapshot 失败(resync 返 err→重连)、翻译失败(resync)、Apply 失败(resync)、ErrStaleVersion(丢弃 Task 7)、gap(重拉 Task 5/6)、心跳超前(Task 6)、流断(Task 8)、ctx 取消(Task 10)。✅
- §7 暴露状态：`Version/Ready/Connected/LastSyncAt`(Task 4)。✅
- §8 配置：`Config` + `maxRecvMsgSize` 64MB + PerRPC 凭据 + Secure 分支(Task 1/4)。✅
- §9 测试策略：translate 纯单测(Task 3)、bufconn+fake(Task 4 夹具)、连续 delta(Task 5)、gap 重拉(Task 6)、心跳(Task 6)、SnapshotRequired(Task 6)、流错重连(Task 8)、ErrStaleVersion(Task 7 注释+race)、端到端 effect(Task 9)、ctx 取消(Task 10)、退避(Task 2)、`-race`(Task 11)。✅

**2. 占位符扫描**：无 TODO/待定；每个代码步骤均给出完整可编译代码与确切命令/预期。Task 7 的 ErrStaleVersion「不写并发专测」是经论证的诚实取舍（单 goroutine 不可确定性构造），非占位。✅

**3. 类型一致性**：跨任务符号统一——`newBackoff/capFor/next/reset`(Task 2)、`snapshotFromProto/deltaFromProto/policyChangeFromProto`(Task 3)、`New/Run/connectAndServe/bootstrap/resync/handleEvent/handleDelta/markSync/sleep`(Task 4-7)、夹具 `fakeServer/startFake/runClient/sendThenBlock/deltaEv/heartbeatEv/snapshotRequiredEv/snapV5/snapStep`(Task 4-6)。`handleEvent`/`handleDelta` 在 Task 5→6→7 三次替换，签名 `(ctx, *SyncEvent)`/`(ctx, *Delta)` 全程一致。已核实所有生成桩/依赖签名（见「关键设计澄清」），无引用未定义符号。✅
