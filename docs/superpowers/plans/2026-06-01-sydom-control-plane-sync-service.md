# 司域 控制面 · 同步下发服务 (③-2 Sync Service) 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 实现控制面同步下发服务——PolicySync gRPC 服务端（Subscribe/PullSnapshot）+ Redis Pub/Sub 广播 + 领域 Delta→syncv1 翻译 + 版本对账续传，把 ③-1 产出的策略变更扩散给各副本持有的 Sidecar。

**架构：** 4 个新包（`translate` 纯翻译、`broadcast` Redis 发布/订阅、`policysync` gRPC 服务端 + Hub fan-out）+ `store` 只读快照扩展。单全局 Redis 频道 + 本地 app_id 路由；有界缓冲 fan-out，溢出转全量对账；fail-close。控制面不跑 casbin，快照从 `casbin_rule` 物化表 + `data_policy` 读出。

**技术栈：** Go 1.26.3、`google.golang.org/grpc`、`google.golang.org/protobuf/proto`、`github.com/redis/go-redis/v9`、`database/sql`+`lib/pq`、testcontainers-go（PG `postgres:17-alpine` + Redis）、testify、bufconn。module `github.com/nickZFZ/Sydom`。

**边界（spec §9，本计划不做）：** Sidecar 侧 apply（→④）；「PolicyManager 写完调 Publish」写编排 + status 拦截（→③-3）；主密钥轮转（→运维）。

---

## 文件结构

| 文件 | 职责 |
|---|---|
| `internal/controlplane/translate/translate.go` | `cp.Delta`↔syncv1 纯函数翻译 + 规则/数据策略转 proto |
| `internal/controlplane/translate/translate_test.go` | 翻译单测（无 Docker） |
| `internal/controlplane/store/read.go` | 只读快照：`ResolveAppIDByKey`/`ReadCurrentVersion`/`ReadAppDataPolicies` |
| `internal/controlplane/store/read_test.go` | 只读函数测试（testcontainers PG） |
| `internal/dbtest/dbtest.go`（扩） | 新增 `StartRedis(t) string` |
| `internal/controlplane/broadcast/envelope.go` | `{app_id, syncv1.Delta}` 编解码 + `Publisher`/`Subscriber` 接口 + 频道常量 |
| `internal/controlplane/broadcast/envelope_test.go` | 编解码往返单测（无 Docker） |
| `internal/controlplane/broadcast/redis.go` | go-redis/v9 实现 `RedisPublisher`/`RedisSubscriber` |
| `internal/controlplane/broadcast/redis_test.go` | 发布→订阅往返（testcontainers Redis） |
| `internal/controlplane/policysync/hub.go` | `Hub`：app_id→streams 注册表 + 有界非阻塞 fan-out + 溢出信号 |
| `internal/controlplane/policysync/hub_test.go` | Hub 单测（fake，无 grpc/Docker） |
| `internal/controlplane/policysync/server.go` | `Server`：PullSnapshot/Subscribe/对账/心跳 + gRPC 装配 + Subscriber→Hub 接线 |
| `internal/controlplane/policysync/server_test.go` | bufconn + PG（+ Redis 端到端） |

依赖方向（无环）：`policysync → {translate, store, broadcast, auth, dbtest(测试)}`；`broadcast → {translate, cp}`；`translate → {cp, gen/syncv1}`；`store`（扩 ③-1）。

---

## 任务 1：translate 包——cp.Delta ↔ syncv1 纯函数翻译

**背景：** `cp.Delta`（int64 版本、`cp.Rule.V[6]string`、RuleAdds/RuleRemoves、DataChanges）翻译为 syncv1 proto。casbin 行只有增/删（无 UPDATE）：RuleAdds→`PolicyChange{ADD, rule}`、RuleRemoves→`PolicyChange{REMOVE, old_rule}`。`cp.Rule.V` 裁掉尾部连续空串贴 casbin 变长风格。纯函数，无 DB/网络，快测。

**文件：**
- 创建：`internal/controlplane/translate/translate.go`
- 测试：`internal/controlplane/translate/translate_test.go`

- [ ] **步骤 1：编写失败的测试**

`internal/controlplane/translate/translate_test.go`：

```go
package translate

import (
	"testing"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
	"github.com/stretchr/testify/require"
)

func rule(ptype string, vs ...string) cp.Rule {
	var v [6]string
	copy(v[:], vs)
	return cp.Rule{Ptype: ptype, V: v}
}

func TestDeltaToProto_AddsRemovesData(t *testing.T) {
	d := cp.Delta{
		AppID:   7,
		Version: 5,
		RuleAdds: []cp.Rule{
			rule("p", "manager", "order-system", "order", "read", "allow"),
		},
		RuleRemoves: []cp.Rule{
			rule("g", "u-100", "manager", "order-system"),
		},
		DataChanges: []cp.DataPolicyChange{
			{Op: cp.ChangeAdd, Policy: cp.DataPolicy{ID: 9, SubjectType: "role", SubjectID: "manager", Resource: "order", Condition: `{"op":"ALL"}`}},
			{Op: cp.ChangeRemove, Policy: cp.DataPolicy{ID: 3}},
		},
	}
	got := DeltaToProto(d)

	require.Equal(t, uint64(5), got.Version)
	require.Len(t, got.PolicyChanges, 2)

	// ADD：rule 填充，old_rule 空；尾部空串被裁（5 个值，无第 6 个空串）
	add := got.PolicyChanges[0]
	require.Equal(t, syncv1.ChangeOp_CHANGE_OP_ADD, add.Op)
	require.NotNil(t, add.Rule)
	require.Nil(t, add.OldRule)
	require.Equal(t, "p", add.Rule.Ptype)
	require.Equal(t, []string{"manager", "order-system", "order", "read", "allow"}, add.Rule.Values)

	// REMOVE：old_rule 填充，rule 空；g 行 3 个值
	rem := got.PolicyChanges[1]
	require.Equal(t, syncv1.ChangeOp_CHANGE_OP_REMOVE, rem.Op)
	require.Nil(t, rem.Rule)
	require.NotNil(t, rem.OldRule)
	require.Equal(t, []string{"u-100", "manager", "order-system"}, rem.OldRule.Values)

	// data 变更：op 映射 + id
	require.Len(t, got.DataChanges, 2)
	require.Equal(t, syncv1.ChangeOp_CHANGE_OP_ADD, got.DataChanges[0].Op)
	require.Equal(t, uint64(9), got.DataChanges[0].Policy.Id)
	require.Equal(t, "manager", got.DataChanges[0].Policy.SubjectId)
	require.Equal(t, syncv1.ChangeOp_CHANGE_OP_REMOVE, got.DataChanges[1].Op)
	require.Equal(t, uint64(3), got.DataChanges[1].Policy.Id)
}

func TestRuleToProto_TrimsTrailingEmpty(t *testing.T) {
	// 全 6 位有值不裁
	full := RulesToProto([]cp.Rule{rule("p", "a", "b", "c", "d", "e", "f")})
	require.Equal(t, []string{"a", "b", "c", "d", "e", "f"}, full[0].Values)
	// 中间空串保留，仅裁尾部
	mid := RulesToProto([]cp.Rule{rule("p", "a", "", "c")})
	require.Equal(t, []string{"a", "", "c"}, mid[0].Values)
	// 全空 ptype 仍保留 ptype、values 为空切片
	empty := RulesToProto([]cp.Rule{rule("g")})
	require.Equal(t, "g", empty[0].Ptype)
	require.Empty(t, empty[0].Values)
}

func TestDataPoliciesToProto(t *testing.T) {
	got := DataPoliciesToProto([]cp.DataPolicy{
		{ID: 1, SubjectType: "user", SubjectID: "u-1", Resource: "doc", Condition: `{"op":"EQ"}`},
	})
	require.Len(t, got, 1)
	require.Equal(t, uint64(1), got[0].Id)
	require.Equal(t, "user", got[0].SubjectType)
	require.Equal(t, "u-1", got[0].SubjectId)
	require.Equal(t, "doc", got[0].Resource)
	require.Equal(t, `{"op":"EQ"}`, got[0].Condition)
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/translate/ -v`
预期：FAIL（package/函数未定义，编译失败）。

- [ ] **步骤 3：编写实现**

`internal/controlplane/translate/translate.go`：

```go
// Package translate 在控制面领域类型 cp.* 与 syncv1 proto 消息间做双向翻译。
// 纯函数，无 DB / 网络副作用。
package translate

import (
	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
	cp "github.com/nickZFZ/Sydom/internal/controlplane"
)

// DeltaToProto 把一次写事务的领域 Delta 翻译为 syncv1.Delta。
// casbin 行只有增/删：RuleAdds→ADD(rule)，RuleRemoves→REMOVE(old_rule)。
func DeltaToProto(d cp.Delta) *syncv1.Delta {
	out := &syncv1.Delta{Version: uint64(d.Version)}
	for _, r := range d.RuleAdds {
		out.PolicyChanges = append(out.PolicyChanges, &syncv1.PolicyChange{
			Op:   syncv1.ChangeOp_CHANGE_OP_ADD,
			Rule: ruleToProto(r),
		})
	}
	for _, r := range d.RuleRemoves {
		out.PolicyChanges = append(out.PolicyChanges, &syncv1.PolicyChange{
			Op:      syncv1.ChangeOp_CHANGE_OP_REMOVE,
			OldRule: ruleToProto(r),
		})
	}
	for _, c := range d.DataChanges {
		out.DataChanges = append(out.DataChanges, &syncv1.DataPolicyChange{
			Op:     opToProto(c.Op),
			Policy: dataPolicyToProto(c.Policy),
		})
	}
	return out
}

// RulesToProto 把全量规则翻译为 proto（供 PullSnapshot）。
func RulesToProto(rules []cp.Rule) []*syncv1.PolicyRule {
	out := make([]*syncv1.PolicyRule, 0, len(rules))
	for _, r := range rules {
		out = append(out, ruleToProto(r))
	}
	return out
}

// DataPoliciesToProto 把全量数据策略翻译为 proto（供 PullSnapshot）。
func DataPoliciesToProto(dps []cp.DataPolicy) []*syncv1.DataPolicy {
	out := make([]*syncv1.DataPolicy, 0, len(dps))
	for _, p := range dps {
		out = append(out, dataPolicyToProto(p))
	}
	return out
}

// ruleToProto 把 cp.Rule.V[6] 裁掉尾部连续空串后转 PolicyRule.values（贴 casbin 变长风格）。
func ruleToProto(r cp.Rule) *syncv1.PolicyRule {
	n := len(r.V)
	for n > 0 && r.V[n-1] == "" {
		n--
	}
	values := make([]string, n)
	copy(values, r.V[:n])
	return &syncv1.PolicyRule{Ptype: r.Ptype, Values: values}
}

func dataPolicyToProto(p cp.DataPolicy) *syncv1.DataPolicy {
	return &syncv1.DataPolicy{
		Id:          uint64(p.ID),
		SubjectType: p.SubjectType,
		SubjectId:   p.SubjectID,
		Resource:    p.Resource,
		Condition:   p.Condition,
	}
}

func opToProto(op cp.ChangeOp) syncv1.ChangeOp {
	switch op {
	case cp.ChangeAdd:
		return syncv1.ChangeOp_CHANGE_OP_ADD
	case cp.ChangeUpdate:
		return syncv1.ChangeOp_CHANGE_OP_UPDATE
	case cp.ChangeRemove:
		return syncv1.ChangeOp_CHANGE_OP_REMOVE
	default:
		return syncv1.ChangeOp_CHANGE_OP_UNSPECIFIED
	}
}
```

注：`RulesToProto` 对全空 `cp.Rule`（如 `rule("g")`）裁到 `n==0`，`values` 为 `make([]string,0)`（非 nil 空切片），`require.Empty` 通过。

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/controlplane/translate/ -v`
预期：PASS（DeltaToProto / 裁尾部空串 / DataPoliciesToProto）。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/translate/
git commit -m "feat(translate): cp.Delta↔syncv1 proto 翻译（增删映射/裁尾部空串）"
```

---

## 任务 2：store 只读快照扩展

**背景：** PullSnapshot 需读「app_id（由 app_key 解析）+ current_version（不加锁）+ 全量 data_policy」。③-1 的 `ReadAppRules` 已有；本任务补三个只读函数到 store 包。`LockAppVersion` 带 `FOR UPDATE` 不适合只读路径，故新增不加锁的 `ReadCurrentVersion`。

**文件：**
- 创建：`internal/controlplane/store/read.go`
- 测试：`internal/controlplane/store/read_test.go`

- [ ] **步骤 1：编写失败的测试**

`internal/controlplane/store/read_test.go`：

```go
package store_test

import (
	"context"
	"testing"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestResolveAppIDByKey(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	ctx := context.Background()

	got, err := store.ResolveAppIDByKey(ctx, db, dbtest.SeedAppKey)
	require.NoError(t, err)
	require.Equal(t, appID, got)

	_, err = store.ResolveAppIDByKey(ctx, db, "AK_nope")
	require.Error(t, err) // 未知 app_key → fail-close 报错
}

func TestReadCurrentVersion(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	ctx := context.Background()

	v, err := store.ReadCurrentVersion(ctx, db, appID)
	require.NoError(t, err)
	require.Equal(t, int64(0), v) // 种子 app 初始版本 0
}

func TestReadAppDataPolicies(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	ctx := context.Background()

	var id int64
	require.NoError(t, db.QueryRow(`
		INSERT INTO data_policy (app_id, subject_type, subject_id, resource, condition, version)
		VALUES ($1,'role','manager','order','{"op":"ALL"}'::jsonb,1) RETURNING id`, appID).Scan(&id))

	got, err := store.ReadAppDataPolicies(ctx, db, appID)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, cp.DataPolicy{
		ID: id, SubjectType: "role", SubjectID: "manager", Resource: "order", Condition: `{"op": "ALL"}`,
	}, got[0])
}
```

> 注：PostgreSQL `jsonb` 列回读会规范化（键值间补空格），故断言用 `{"op": "ALL"}`（含空格）。若实测规范化形式不同，以实测为准调整断言。

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/store/ -run 'TestResolveAppIDByKey|TestReadCurrentVersion|TestReadAppDataPolicies' -v`
预期：FAIL（函数未定义）。

- [ ] **步骤 3：编写实现**

`internal/controlplane/store/read.go`：

```go
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
)

// ResolveAppIDByKey 把外部 app_key（认证凭据标识）解析为 application.id。
// 无对应应用即报错（fail-close，不返回 0 让调用方误用）。
func ResolveAppIDByKey(ctx context.Context, q cp.DBTX, appKey string) (int64, error) {
	var id int64
	err := q.QueryRowContext(ctx,
		`SELECT id FROM application WHERE app_key=$1`, appKey).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("store: unknown app_key %q", appKey)
	}
	if err != nil {
		return 0, err
	}
	return id, nil
}

// ReadCurrentVersion 读取 app 当前版本号（只读路径，不加 FOR UPDATE，不串行化写）。
func ReadCurrentVersion(ctx context.Context, q cp.DBTX, appID int64) (int64, error) {
	var v int64
	err := q.QueryRowContext(ctx,
		`SELECT current_version FROM application WHERE id=$1`, appID).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("store: unknown app id=%d", appID)
	}
	return v, err
}

// ReadAppDataPolicies 读取某 app 全部数据策略（供全量快照）。
func ReadAppDataPolicies(ctx context.Context, q cp.DBTX, appID int64) ([]cp.DataPolicy, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT id, subject_type, subject_id, resource, condition FROM data_policy WHERE app_id=$1`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []cp.DataPolicy
	for rows.Next() {
		var p cp.DataPolicy
		if err := rows.Scan(&p.ID, &p.SubjectType, &p.SubjectID, &p.Resource, &p.Condition); err != nil {
			return nil, fmt.Errorf("scan data_policy: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
```

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/controlplane/store/ -v`
预期：PASS（含 ③-1 既有用例 + 本任务三个新用例）。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/store/read.go internal/controlplane/store/read_test.go
git commit -m "feat(store): 只读快照扩展 ResolveAppIDByKey/ReadCurrentVersion/ReadAppDataPolicies"
```

---

## 任务 3：broadcast 编解码 + 接口 + go-redis 依赖

**背景：** 单全局频道需在消息体携带 `app_id`（int64）+ 翻译后的 `syncv1.Delta`。无新 proto：用 8 字节大端 app_id 前缀 + `proto.Marshal(delta)` 字节，编解码为纯函数可单测。本任务还定义 `Publisher`/`Subscriber` 接口与频道常量，并引入 go-redis 依赖（任务 4 用）。

**文件：**
- 创建：`internal/controlplane/broadcast/envelope.go`
- 测试：`internal/controlplane/broadcast/envelope_test.go`

- [ ] **步骤 1：引入 go-redis 依赖**

运行：
```bash
go get github.com/redis/go-redis/v9@v9.7.0
```
预期：`go.mod`/`go.sum` 增加 go-redis。（若该版本拉取失败，用 `@latest` 取稳定版并在报告说明实际版本。）

- [ ] **步骤 2：编写失败的测试**

`internal/controlplane/broadcast/envelope_test.go`：

```go
package broadcast

import (
	"testing"

	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
	"github.com/stretchr/testify/require"
)

func TestEnvelopeRoundTrip(t *testing.T) {
	d := &syncv1.Delta{
		Version: 42,
		PolicyChanges: []*syncv1.PolicyChange{
			{Op: syncv1.ChangeOp_CHANGE_OP_ADD, Rule: &syncv1.PolicyRule{Ptype: "p", Values: []string{"a", "b"}}},
		},
	}
	blob, err := EncodeEnvelope(7, d)
	require.NoError(t, err)

	appID, got, err := DecodeEnvelope(blob)
	require.NoError(t, err)
	require.Equal(t, int64(7), appID)
	require.Equal(t, uint64(42), got.Version)
	require.Len(t, got.PolicyChanges, 1)
	require.Equal(t, "p", got.PolicyChanges[0].Rule.Ptype)
	require.Equal(t, []string{"a", "b"}, got.PolicyChanges[0].Rule.Values)
}

func TestDecodeEnvelope_TooShort(t *testing.T) {
	_, _, err := DecodeEnvelope([]byte{0x00, 0x01}) // < 8 字节前缀
	require.Error(t, err)
}
```

- [ ] **步骤 3：运行验证失败**

运行：`go test ./internal/controlplane/broadcast/ -run TestEnvelope -v` 与 `go test ./internal/controlplane/broadcast/ -run TestDecodeEnvelope -v`
预期：FAIL（package/函数未定义）。

- [ ] **步骤 4：编写实现**

`internal/controlplane/broadcast/envelope.go`：

```go
// Package broadcast 把控制面策略变更经 Redis Pub/Sub 扩散到各副本。
// 消息体 = 8 字节大端 app_id 前缀 + proto.Marshal(syncv1.Delta)。
package broadcast

import (
	"context"
	"encoding/binary"
	"fmt"

	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
	"google.golang.org/protobuf/proto"
)

// Channel 是控制面广播策略变更的单一全局 Redis 频道。
const Channel = "sydom:policy:delta"

// Publisher 把一次策略变更（领域 Delta，已翻译为 proto）发布到广播总线。
type Publisher interface {
	// Publish 发布 app 的一条 Delta。at-most-once：失败返错由调用方决策，不重试。
	Publish(ctx context.Context, appID int64, delta *syncv1.Delta) error
}

// Subscriber 订阅广播总线，对每条消息回调 handler。Run 阻塞直至 ctx 取消。
type Subscriber interface {
	Run(ctx context.Context, handler func(appID int64, delta *syncv1.Delta)) error
}

// EncodeEnvelope 把 {appID, delta} 编码为广播字节。
func EncodeEnvelope(appID int64, delta *syncv1.Delta) ([]byte, error) {
	body, err := proto.Marshal(delta)
	if err != nil {
		return nil, fmt.Errorf("broadcast: marshal delta: %w", err)
	}
	buf := make([]byte, 8+len(body))
	binary.BigEndian.PutUint64(buf[:8], uint64(appID))
	copy(buf[8:], body)
	return buf, nil
}

// DecodeEnvelope 从广播字节解出 {appID, delta}。
func DecodeEnvelope(blob []byte) (int64, *syncv1.Delta, error) {
	if len(blob) < 8 {
		return 0, nil, fmt.Errorf("broadcast: envelope too short (%d bytes)", len(blob))
	}
	appID := int64(binary.BigEndian.Uint64(blob[:8]))
	var d syncv1.Delta
	if err := proto.Unmarshal(blob[8:], &d); err != nil {
		return 0, nil, fmt.Errorf("broadcast: unmarshal delta: %w", err)
	}
	return appID, &d, nil
}
```

- [ ] **步骤 5：运行验证通过**

运行：`go test ./internal/controlplane/broadcast/ -v`
预期：PASS（往返 + too short）。

- [ ] **步骤 6：Commit**

```bash
git add go.mod go.sum internal/controlplane/broadcast/envelope.go internal/controlplane/broadcast/envelope_test.go
git commit -m "feat(broadcast): 广播信封编解码 + Publisher/Subscriber 接口 + go-redis 依赖"
```

---

## 任务 4：broadcast Redis 实现 + dbtest.StartRedis

**背景：** 用 go-redis/v9 实现 `Publisher`/`Subscriber`。`RedisPublisher.Publish` 编码后 `PUBLISH Channel`；`RedisSubscriber.Run` 订阅 Channel、解码每条消息回调 handler，ctx 取消时退出。测试用 testcontainers 起真实 Redis（在 `internal/dbtest` 加 `StartRedis` 共享 helper）。

**文件：**
- 修改：`internal/dbtest/dbtest.go`（新增 `StartRedis`）
- 创建：`internal/controlplane/broadcast/redis.go`
- 测试：`internal/controlplane/broadcast/redis_test.go`

- [ ] **步骤 1：在 dbtest 增加 Redis 容器 helper**

在 `internal/dbtest/dbtest.go` 追加（复用文件已有 import：`context`/`testing`/`time`/`require`/`testcontainers`/`wait`；新增 `github.com/testcontainers/testcontainers-go/modules/redis`）：

```go
// StartRedis 起一个临时 Redis 容器，返回 host:port 地址（无密码）。
func StartRedis(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	ctr, err := redis.RunContainer(ctx,
		testcontainers.WithImage("docker.io/redis:7-alpine"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("Ready to accept connections").WithStartupTimeout(60*time.Second)),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ctr.Terminate(ctx) })

	endpoint, err := ctr.Endpoint(ctx, "")
	require.NoError(t, err)
	return endpoint
}
```

import 块加入：`"github.com/testcontainers/testcontainers-go/modules/redis"`。运行 `go get github.com/testcontainers/testcontainers-go/modules/redis@latest`（与既有 testcontainers-go 同源）使其入 go.mod。

> 注：若该版本 `redis.RunContainer` API 名与实际不符（testcontainers-go 各版本曾改名 `Run`），以本机拉到的版本实际 API 为准对齐，并在报告说明。

- [ ] **步骤 2：编写失败的测试**

`internal/controlplane/broadcast/redis_test.go`：

```go
package broadcast_test

import (
	"context"
	"testing"
	"time"

	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/broadcast"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestRedisPublishSubscribe(t *testing.T) {
	addr := dbtest.StartRedis(t)
	pub := broadcast.NewRedisPublisher(redis.NewClient(&redis.Options{Addr: addr}))
	sub := broadcast.NewRedisSubscriber(redis.NewClient(&redis.Options{Addr: addr}))

	type recv struct {
		appID int64
		delta *syncv1.Delta
	}
	got := make(chan recv, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = sub.Run(ctx, func(appID int64, d *syncv1.Delta) { got <- recv{appID, d} })
	}()

	// 等订阅就绪后再发布（Redis Pub/Sub at-most-once，订阅前发会丢）
	require.Eventually(t, func() bool {
		err := pub.Publish(context.Background(), 7, &syncv1.Delta{Version: 99})
		require.NoError(t, err)
		select {
		case r := <-got:
			require.Equal(t, int64(7), r.appID)
			require.Equal(t, uint64(99), r.delta.Version)
			return true
		case <-time.After(100 * time.Millisecond):
			return false
		}
	}, 5*time.Second, 50*time.Millisecond)
}
```

- [ ] **步骤 3：运行验证失败**

运行：`go test ./internal/controlplane/broadcast/ -run TestRedisPublishSubscribe -v`
预期：FAIL（`NewRedisPublisher`/`NewRedisSubscriber` 未定义）。

- [ ] **步骤 4：编写实现**

`internal/controlplane/broadcast/redis.go`：

```go
package broadcast

import (
	"context"
	"fmt"

	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
	"github.com/redis/go-redis/v9"
)

// RedisPublisher 用 Redis PUBLISH 把 Delta 发布到全局频道。
type RedisPublisher struct {
	client *redis.Client
}

// NewRedisPublisher 构造 RedisPublisher。
func NewRedisPublisher(client *redis.Client) *RedisPublisher {
	return &RedisPublisher{client: client}
}

// Publish 编码 {appID, delta} 后 PUBLISH 到 Channel。
func (p *RedisPublisher) Publish(ctx context.Context, appID int64, delta *syncv1.Delta) error {
	blob, err := EncodeEnvelope(appID, delta)
	if err != nil {
		return err
	}
	if err := p.client.Publish(ctx, Channel, blob).Err(); err != nil {
		return fmt.Errorf("broadcast: redis publish: %w", err)
	}
	return nil
}

// RedisSubscriber 订阅全局频道，对每条消息解码后回调 handler。
type RedisSubscriber struct {
	client *redis.Client
}

// NewRedisSubscriber 构造 RedisSubscriber。
func NewRedisSubscriber(client *redis.Client) *RedisSubscriber {
	return &RedisSubscriber{client: client}
}

// Run 订阅 Channel，阻塞循环直至 ctx 取消。解码失败的消息跳过（记录由调用方决定），
// 不中断订阅——单条坏消息不应拖垮整条扩散链路。
func (s *RedisSubscriber) Run(ctx context.Context, handler func(appID int64, delta *syncv1.Delta)) error {
	ps := s.client.Subscribe(ctx, Channel)
	defer ps.Close()
	ch := ps.Channel()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-ch:
			if !ok {
				return nil
			}
			appID, delta, err := DecodeEnvelope([]byte(msg.Payload))
			if err != nil {
				continue // 坏消息跳过，不中断
			}
			handler(appID, delta)
		}
	}
}
```

- [ ] **步骤 5：运行验证通过**

运行：`go test ./internal/controlplane/broadcast/ -v`
预期：PASS（含信封单测 + Redis 往返）。

- [ ] **步骤 6：Commit**

```bash
git add go.mod go.sum internal/dbtest/dbtest.go internal/controlplane/broadcast/redis.go internal/controlplane/broadcast/redis_test.go
git commit -m "feat(broadcast): go-redis Publisher/Subscriber + dbtest.StartRedis"
```

---

## 任务 5：policysync Hub——注册表 + 有界 fan-out + 溢出信号

**背景：** 每副本一个 `Hub`，维护 `app_id → 本地 Subscribe 流` 注册表，把 `Dispatch` 来的事件非阻塞写入各流的有界缓冲；缓冲满则向该流的 size-1 overflow 信号通道投递一次（去重），由服务端 send-loop 转成 `SnapshotRequired`。慢流被隔离，不阻塞 Dispatch。Hub 不依赖 grpc，可纯单测。

**文件：**
- 创建：`internal/controlplane/policysync/hub.go`
- 测试：`internal/controlplane/policysync/hub_test.go`

- [ ] **步骤 1：编写失败的测试**

`internal/controlplane/policysync/hub_test.go`：

```go
package policysync

import (
	"testing"

	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
	"github.com/stretchr/testify/require"
)

func TestHub_DispatchDelivers(t *testing.T) {
	h := NewHub(4)
	sub := h.register(7)
	defer h.unregister(sub)

	ev := &syncv1.SyncEvent{Event: &syncv1.SyncEvent_Delta{Delta: &syncv1.Delta{Version: 1}}}
	h.Dispatch(7, ev)

	select {
	case got := <-sub.events:
		require.Equal(t, uint64(1), got.GetDelta().Version)
	default:
		t.Fatal("期望收到事件")
	}
}

func TestHub_DispatchToOtherAppIgnored(t *testing.T) {
	h := NewHub(4)
	sub := h.register(7)
	defer h.unregister(sub)

	h.Dispatch(999, &syncv1.SyncEvent{}) // 非本 app
	require.Empty(t, sub.events)
}

func TestHub_OverflowSignals(t *testing.T) {
	h := NewHub(2) // 缓冲仅 2
	sub := h.register(7)
	defer h.unregister(sub)

	ev := &syncv1.SyncEvent{Event: &syncv1.SyncEvent_Delta{Delta: &syncv1.Delta{Version: 1}}}
	for i := 0; i < 5; i++ { // 灌满并溢出
		h.Dispatch(7, ev)
	}
	require.Len(t, sub.events, 2) // 缓冲被填满
	select {
	case <-sub.overflow:
		// 溢出信号已投递
	default:
		t.Fatal("期望溢出信号")
	}
}

func TestHub_UnregisterStopsDelivery(t *testing.T) {
	h := NewHub(4)
	sub := h.register(7)
	h.unregister(sub)
	h.Dispatch(7, &syncv1.SyncEvent{})
	require.Empty(t, sub.events)
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/policysync/ -run TestHub -v`
预期：FAIL（package/类型未定义）。

- [ ] **步骤 3：编写实现**

`internal/controlplane/policysync/hub.go`：

```go
// Package policysync 实现控制面 PolicySync gRPC 服务端、本地 fan-out Hub 与版本对账。
package policysync

import (
	"sync"

	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
)

// subscriber 是一条本地 Subscribe 流的投递端点：有界事件缓冲 + size-1 溢出信号。
type subscriber struct {
	appID    int64
	events   chan *syncv1.SyncEvent // 有界数据缓冲
	overflow chan struct{}          // size-1，去重的"已落后，请全量对账"信号
}

// Hub 管理 app_id → 本地 Subscribe 流，并向其做有界非阻塞 fan-out。
type Hub struct {
	mu      sync.RWMutex
	streams map[int64]map[*subscriber]struct{}
	bufSize int
}

// NewHub 构造 Hub，bufSize 为每流事件缓冲容量。
func NewHub(bufSize int) *Hub {
	return &Hub{streams: map[int64]map[*subscriber]struct{}{}, bufSize: bufSize}
}

// register 为某 app 注册一个新流端点。
func (h *Hub) register(appID int64) *subscriber {
	s := &subscriber{
		appID:    appID,
		events:   make(chan *syncv1.SyncEvent, h.bufSize),
		overflow: make(chan struct{}, 1),
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.streams[appID] == nil {
		h.streams[appID] = map[*subscriber]struct{}{}
	}
	h.streams[appID][s] = struct{}{}
	return s
}

// unregister 注销一个流端点（流结束时调用），清掉空 app 桶。
func (h *Hub) unregister(s *subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if set := h.streams[s.appID]; set != nil {
		delete(set, s)
		if len(set) == 0 {
			delete(h.streams, s.appID)
		}
	}
}

// Dispatch 把事件非阻塞投递给某 app 的所有本地流；缓冲满则投递一次溢出信号（去重）。
func (h *Hub) Dispatch(appID int64, ev *syncv1.SyncEvent) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for s := range h.streams[appID] {
		select {
		case s.events <- ev:
		default:
			// 缓冲满：丢弃增量，投递溢出信号（size-1，已满则丢弃——信号已 pending）
			select {
			case s.overflow <- struct{}{}:
			default:
			}
		}
	}
}
```

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/controlplane/policysync/ -run TestHub -v`
预期：PASS（投递 / 隔离其他 app / 溢出信号 / 注销停投）。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/policysync/hub.go internal/controlplane/policysync/hub_test.go
git commit -m "feat(policysync): Hub 注册表 + 有界非阻塞 fan-out + 溢出信号"
```

---

## 任务 6：policysync Server——PullSnapshot（只读一致快照）

**背景：** `Server` 实现 `syncv1.PolicySyncServer`。本任务先实现 `PullSnapshot`：从 ctx 取 app_key（`auth.AppIDFromContext`）→ 在只读事务内 `ResolveAppIDByKey`→`ReadCurrentVersion`→`ReadAppRules`→`ReadAppDataPolicies`→翻译为 `Snapshot`。只读事务保证 version 与 rules/data 取自同一一致快照。

**文件：**
- 创建：`internal/controlplane/policysync/server.go`
- 测试：`internal/controlplane/policysync/server_test.go`

- [ ] **步骤 1：编写失败的测试**

`internal/controlplane/policysync/server_test.go`：

```go
package policysync_test

import (
	"context"
	"database/sql"
	"net"
	"testing"
	"time"

	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
	"github.com/nickZFZ/Sydom/internal/auth"
	"github.com/nickZFZ/Sydom/internal/controlplane/policysync"
	"github.com/nickZFZ/Sydom/internal/controlplane/secret"
	"github.com/nickZFZ/Sydom/internal/crypto"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// masterKey 固定 32 字节测试主密钥。
func masterKey() []byte {
	k := make([]byte, crypto.KeySize)
	for i := range k {
		k[i] = 0x2a
	}
	return k
}

// startServer 起一个带认证拦截器的 PolicySync 服务端（bufconn），返回连接与 app 的 secret。
func startServer(t *testing.T, db *sql.DB) (*grpc.ClientConn, []byte) {
	t.Helper()
	res, err := secret.NewResolver(db, masterKey())
	require.NoError(t, err)

	// 给种子 app 写入可解密的 secret（与下面 client 用同一份）
	plain := []byte("app-secret")
	enc, err := res.EncryptSecret(plain)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE application SET app_secret_enc=$1 WHERE app_key=$2`, enc, dbtest.SeedAppKey)
	require.NoError(t, err)

	srv := policysync.NewGRPCServer(policysync.NewServer(db, policysync.Config{
		HeartbeatInterval: 50 * time.Millisecond,
		BufSize:           8,
	}), res)

	lis := bufconn.Listen(1024 * 1024)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(auth.NewPerRPCCredentials(dbtest.SeedAppKey, plain, false)),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn, plain
}

func TestPullSnapshot(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)

	// 直接造一条 casbin_rule 与一条 data_policy + 推进版本
	_, err := db.Exec(`INSERT INTO casbin_rule (app_id, ptype, v0, v1, v2, v3, v4, v5, version)
		VALUES ($1,'p','manager','order-system','order','read','allow','',1)`, appID)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO data_policy (app_id, subject_type, subject_id, resource, condition, version)
		VALUES ($1,'role','manager','order','{"op":"ALL"}'::jsonb,1)`, appID)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE application SET current_version=1 WHERE id=$1`, appID)
	require.NoError(t, err)

	conn, _ := startServer(t, db)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	snap, err := syncv1.NewPolicySyncClient(conn).PullSnapshot(ctx, &syncv1.PullSnapshotRequest{})
	require.NoError(t, err)
	require.Equal(t, uint64(1), snap.Version)
	require.Len(t, snap.Rules, 1)
	require.Equal(t, "p", snap.Rules[0].Ptype)
	require.Equal(t, []string{"manager", "order-system", "order", "read", "allow"}, snap.Rules[0].Values)
	require.Len(t, snap.DataPolicies, 1)
	require.Equal(t, "manager", snap.DataPolicies[0].SubjectId)
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/policysync/ -run TestPullSnapshot -v`
预期：FAIL（`NewServer`/`NewGRPCServer`/`Config` 未定义）。

- [ ] **步骤 3：编写实现**

`internal/controlplane/policysync/server.go`：

```go
package policysync

import (
	"context"
	"database/sql"
	"time"

	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
	"github.com/nickZFZ/Sydom/internal/auth"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/controlplane/translate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const maxMsgSize = 64 * 1024 * 1024 // 64MB，容纳全量快照（gRPC spec §8）

// Config 配置 Server 行为。
type Config struct {
	HeartbeatInterval time.Duration // 心跳间隔（~30s 生产值）
	BufSize           int           // 每流事件缓冲容量
}

// Server 实现 syncv1.PolicySyncServer：PullSnapshot 全量快照 + Subscribe 流式下发。
type Server struct {
	syncv1.UnimplementedPolicySyncServer
	db  *sql.DB
	hub *Hub
	cfg Config
}

// NewServer 构造 Server。
func NewServer(db *sql.DB, cfg Config) *Server {
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = 30 * time.Second
	}
	if cfg.BufSize <= 0 {
		cfg.BufSize = 256
	}
	return &Server{db: db, hub: NewHub(cfg.BufSize), cfg: cfg}
}

// Hub 暴露给广播订阅循环 Dispatch（任务 8 接线）。
func (s *Server) Hub() *Hub { return s.hub }

// PullSnapshot 在只读事务内读全量策略，保证 version 与 rules/data 一致。
func (s *Server) PullSnapshot(ctx context.Context, _ *syncv1.PullSnapshotRequest) (*syncv1.Snapshot, error) {
	appKey, ok := auth.AppIDFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "missing app identity")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin read tx: %v", err)
	}
	defer tx.Rollback()

	appID, err := store.ResolveAppIDByKey(ctx, tx, appKey)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "resolve app: %v", err)
	}
	version, err := store.ReadCurrentVersion(ctx, tx, appID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read version: %v", err)
	}
	rules, err := store.ReadAppRules(ctx, tx, appID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read rules: %v", err)
	}
	dps, err := store.ReadAppDataPolicies(ctx, tx, appID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read data policies: %v", err)
	}
	return &syncv1.Snapshot{
		Version:      uint64(version),
		Rules:        translate.RulesToProto(rules),
		DataPolicies: translate.DataPoliciesToProto(dps),
	}, nil
}

// NewGRPCServer 组装带认证拦截器与 64MB 消息上限的 grpc.Server 并注册 PolicySync。
func NewGRPCServer(srv *Server, res auth.SecretResolver) *grpc.Server {
	g := grpc.NewServer(
		grpc.MaxRecvMsgSize(maxMsgSize),
		grpc.MaxSendMsgSize(maxMsgSize),
		grpc.UnaryInterceptor(auth.UnaryServerInterceptor(res)),
		grpc.StreamInterceptor(auth.StreamServerInterceptor(res)),
	)
	syncv1.RegisterPolicySyncServer(g, srv)
	return g
}
```

> 本任务的 `server.go` 只含 `Config`/`Server`/`NewServer`/`Hub()`/`PullSnapshot`/`NewGRPCServer`；`Subscribe` 在任务 7 追加、`RunDispatchLoop` 在任务 8 追加。

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/controlplane/policysync/ -run 'TestHub|TestPullSnapshot' -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/policysync/server.go internal/controlplane/policysync/server_test.go
git commit -m "feat(policysync): Server + PullSnapshot（只读一致快照）+ gRPC 装配"
```

---

## 任务 7：policysync Server——Subscribe（版本对账 + fan-out + 心跳）

**背景：** 实现 `Subscribe`：取 app_id→读 current_version→注册到 Hub→按 spec §5.3 对账（current>last 先发 SnapshotRequired）→进入 send-loop：从 `subscriber.events`（Hub 投递的 Delta）/`overflow`（溢出→SnapshotRequired）/心跳 ticker 三路 select 写 stream，ctx 取消时注销退出。心跳用内存维护的版本（send Delta 时更新），每 N 轮做一次轻量 `ReadCurrentVersion` 兑正。

**文件：**
- 修改：`internal/controlplane/policysync/server.go`（追加 `Subscribe`）
- 测试：`internal/controlplane/policysync/server_test.go`（追加用例）

- [ ] **步骤 1：编写失败的测试**

在 `internal/controlplane/policysync/server_test.go` 追加：

```go
func TestSubscribe_ColdStartSendsSnapshotRequired(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	_, err := db.Exec(`UPDATE application SET current_version=3 WHERE id=$1`, appID)
	require.NoError(t, err)

	conn, _ := startServer(t, db)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 冷启动 last_applied=0 < current=3 → 首事件应为 SnapshotRequired(behind)
	stream, err := syncv1.NewPolicySyncClient(conn).Subscribe(ctx, &syncv1.SubscribeRequest{LastAppliedVersion: 0})
	require.NoError(t, err)
	ev, err := stream.Recv()
	require.NoError(t, err)
	sr := ev.GetSnapshotRequired()
	require.NotNil(t, sr)
	require.Equal(t, uint64(3), sr.CurrentVersion)
	require.Equal(t, "behind", sr.Reason)
}

func TestSubscribe_InSyncReceivesDispatchedDelta(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db) // current_version=0

	srvHolder := make(chan *policysync.Server, 1)
	conn := startServerCapture(t, db, srvHolder)
	srv := <-srvHolder

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// last_applied==current==0 → 无 SnapshotRequired，注册后等 Dispatch
	stream, err := syncv1.NewPolicySyncClient(conn).Subscribe(ctx, &syncv1.SubscribeRequest{LastAppliedVersion: 0})
	require.NoError(t, err)

	// 等服务端注册完成后 Dispatch 一条 Delta（用 Eventually 容忍注册时序）
	require.Eventually(t, func() bool {
		srv.Hub().Dispatch(appID, &syncv1.SyncEvent{
			Event: &syncv1.SyncEvent_Delta{Delta: &syncv1.Delta{Version: 1}},
		})
		// 尝试非阻塞收一条；这里直接 Recv（带超时由外层 ctx 保证）
		return true
	}, 2*time.Second, 50*time.Millisecond)

	ev, err := stream.Recv()
	require.NoError(t, err)
	require.NotNil(t, ev.GetDelta())
	require.Equal(t, uint64(1), ev.GetDelta().Version)
}
```

并在测试文件加一个能拿到 `*policysync.Server` 的启动辅助（与 `startServer` 并存，复用其认证/seed 逻辑）：

```go
// startServerCapture 同 startServer，但把 *Server 经 holder 回传，便于测试直接 Dispatch。
func startServerCapture(t *testing.T, db *sql.DB, holder chan *policysync.Server) *grpc.ClientConn {
	t.Helper()
	res, err := secret.NewResolver(db, masterKey())
	require.NoError(t, err)
	plain := []byte("app-secret")
	enc, err := res.EncryptSecret(plain)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE application SET app_secret_enc=$1 WHERE app_key=$2`, enc, dbtest.SeedAppKey)
	require.NoError(t, err)

	srv := policysync.NewServer(db, policysync.Config{HeartbeatInterval: 50 * time.Millisecond, BufSize: 8})
	holder <- srv
	g := policysync.NewGRPCServer(srv, res)
	lis := bufconn.Listen(1024 * 1024)
	go func() { _ = g.Serve(lis) }()
	t.Cleanup(g.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(auth.NewPerRPCCredentials(dbtest.SeedAppKey, plain, false)),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}
```

> 重构提示：`startServer` 与 `startServerCapture` 的认证/seed 准备重复，实现时可把公共部分抽成一个 `prepAuth(t, db) (res, plain)` 小辅助，避免 DRY 违背（自行决定，不影响断言）。

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/policysync/ -run TestSubscribe -v`
预期：FAIL（`Subscribe` 未实现，`UnimplementedPolicySyncServer` 返回 `Unimplemented`）。

- [ ] **步骤 3：编写实现**

在 `internal/controlplane/policysync/server.go` 追加（复用已 import 的 store/translate/auth/syncv1/codes/status/sql/time/context；新增无）：

```go
// Subscribe 订阅策略变更流：对账续传 + fan-out + 心跳。
func (s *Server) Subscribe(req *syncv1.SubscribeRequest, stream syncv1.PolicySync_SubscribeServer) error {
	ctx := stream.Context()
	appKey, ok := auth.AppIDFromContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "missing app identity")
	}
	appID, err := store.ResolveAppIDByKey(ctx, s.db, appKey)
	if err != nil {
		return status.Errorf(codes.NotFound, "resolve app: %v", err)
	}
	current, err := store.ReadCurrentVersion(ctx, s.db, appID)
	if err != nil {
		return status.Errorf(codes.Internal, "read version: %v", err)
	}

	// 先注册到 Hub，避免读 current 与注册之间漏掉 Dispatch 的 Delta（重复由 Sidecar 版本去重兜底）。
	sub := s.hub.register(appID)
	defer s.hub.unregister(sub)

	// 对账（spec §5.3）：last_applied 落后于 current（含冷启动 0<current）→ 先发 SnapshotRequired。
	if uint64(current) != req.LastAppliedVersion {
		if err := stream.Send(&syncv1.SyncEvent{Event: &syncv1.SyncEvent_SnapshotRequired{
			SnapshotRequired: &syncv1.SnapshotRequired{CurrentVersion: uint64(current), Reason: "behind"},
		}}); err != nil {
			return err
		}
	}

	// send-loop：events / overflow / heartbeat 三路。
	lastVer := uint64(current)
	const reconcileEvery = 10 // 每 10 个心跳兑正一次内存版本（spec 决策 5）
	ticks := 0
	ticker := time.NewTicker(s.cfg.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil // Sidecar 断开 / 服务端关闭
		case ev := <-sub.events:
			if d := ev.GetDelta(); d != nil {
				lastVer = d.Version
			}
			if err := stream.Send(ev); err != nil {
				return err
			}
		case <-sub.overflow:
			if err := stream.Send(&syncv1.SyncEvent{Event: &syncv1.SyncEvent_SnapshotRequired{
				SnapshotRequired: &syncv1.SnapshotRequired{CurrentVersion: lastVer, Reason: "overflow"},
			}}); err != nil {
				return err
			}
		case <-ticker.C:
			ticks++
			if ticks%reconcileEvery == 0 {
				if v, err := store.ReadCurrentVersion(ctx, s.db, appID); err == nil {
					lastVer = uint64(v)
				}
			}
			if err := stream.Send(&syncv1.SyncEvent{Event: &syncv1.SyncEvent_Heartbeat{
				Heartbeat: &syncv1.Heartbeat{CurrentVersion: lastVer},
			}}); err != nil {
				return err
			}
		}
	}
}
```

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/controlplane/policysync/ -v`
预期：PASS（Hub + PullSnapshot + 冷启动 SnapshotRequired + 在线收 Dispatch Delta）。
**额外**：`go test ./internal/controlplane/policysync/ -race -count=1`（含并发 Dispatch/stream），确认无数据竞争。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/policysync/
git commit -m "feat(policysync): Subscribe 版本对账续传 + fan-out + 心跳"
```

---

## 任务 8：广播订阅接线 + 端到端

**背景：** 把 `broadcast.Subscriber` 的回调接到 `Server.Hub().Dispatch`——副本收到 Redis 广播的 Delta（已是 syncv1）就包成 `SyncEvent{Delta}` 投给本地对应 app 的流。提供 `RunDispatchLoop` 把订阅循环跑起来。端到端验证：`RedisPublisher.Publish → RedisSubscriber → Hub.Dispatch → Subscribe 流` 收到翻译后的 Delta。

**文件：**
- 修改：`internal/controlplane/policysync/server.go`（追加 `RunDispatchLoop`）
- 测试：`internal/controlplane/policysync/server_test.go`（追加端到端用例）

- [ ] **步骤 1：编写失败的测试**

在 `server_test.go` 追加（新增 import：`"github.com/nickZFZ/Sydom/internal/controlplane/broadcast"`、`"github.com/redis/go-redis/v9"`）：

```go
func TestEndToEnd_PublishToSubscribeStream(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db) // current_version=0
	addr := dbtest.StartRedis(t)

	srvHolder := make(chan *policysync.Server, 1)
	conn := startServerCapture(t, db, srvHolder)
	srv := <-srvHolder

	// 接线：RedisSubscriber → srv.Hub().Dispatch
	sub := broadcast.NewRedisSubscriber(redis.NewClient(&redis.Options{Addr: addr}))
	loopCtx, loopCancel := context.WithCancel(context.Background())
	defer loopCancel()
	go func() { _ = srv.RunDispatchLoop(loopCtx, sub) }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := syncv1.NewPolicySyncClient(conn).Subscribe(ctx, &syncv1.SubscribeRequest{LastAppliedVersion: 0})
	require.NoError(t, err)

	// 发布一条 Delta（用 Eventually 容忍订阅/注册时序）
	pub := broadcast.NewRedisPublisher(redis.NewClient(&redis.Options{Addr: addr}))
	require.Eventually(t, func() bool {
		return pub.Publish(context.Background(), appID, &syncv1.Delta{Version: 1}) == nil
	}, 2*time.Second, 50*time.Millisecond)

	// 流上应收到该 Delta（可能先有/无 SnapshotRequired，循环跳过非 Delta 事件）
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("超时未收到 Delta")
		default:
		}
		ev, err := stream.Recv()
		require.NoError(t, err)
		if d := ev.GetDelta(); d != nil {
			require.Equal(t, uint64(1), d.Version)
			return
		}
	}
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/policysync/ -run TestEndToEnd -v`
预期：FAIL（`RunDispatchLoop` 未定义）。

- [ ] **步骤 3：编写实现**

在 `internal/controlplane/policysync/server.go` 追加（新增 import：`"github.com/nickZFZ/Sydom/internal/controlplane/broadcast"`）：

```go
// RunDispatchLoop 跑广播订阅循环：收到的每条 Delta 包成 SyncEvent 投给本地对应 app 的流。
// 阻塞直至 ctx 取消。每副本启动一次。
func (s *Server) RunDispatchLoop(ctx context.Context, sub broadcast.Subscriber) error {
	return sub.Run(ctx, func(appID int64, delta *syncv1.Delta) {
		s.hub.Dispatch(appID, &syncv1.SyncEvent{
			Event: &syncv1.SyncEvent_Delta{Delta: delta},
		})
	})
}
```

- [ ] **步骤 4：运行验证通过**

运行：`go test ./internal/controlplane/policysync/ -v`
预期：PASS（含端到端 PG+Redis+bufconn）。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/policysync/
git commit -m "feat(policysync): 广播订阅接线 RunDispatchLoop + 端到端"
```

---

## 收尾：全量验证

- [ ] `go build ./...` —— 无错误。
- [ ] `go test ./... ` —— 全绿（translate 无 Docker；store/broadcast/policysync 需 Docker 起 PG+Redis；既有包不受影响）。
- [ ] `go test ./internal/controlplane/policysync/ -race -count=1` —— 并发 fan-out/stream 无数据竞争。
- [ ] `go vet ./...` 与 `gofmt -l internal/`（排除 gen/）—— 无告警、无未格式化。
- [ ] 调用 `superpowers:finishing-a-development-branch` 收尾合入。

---

## 自检结果

**1. 规格覆盖度（对照 spec 各节）：**
- §2 决策 1（③-2 出 Publisher、不碰 PolicyManager）→ `broadcast.Publisher` 接口 + Redis 实现（任务 3/4），无 PolicyManager 改动。决策 2（发完整翻译 Delta）→ `EncodeEnvelope` 带完整 `syncv1.Delta`（任务 3）。决策 3（单全局频道 + 本地路由）→ `Channel` 常量 + Hub `map[appID]`（任务 3/5）。决策 4（有界缓冲溢出转对账保流）→ Hub overflow 信号 + Subscribe send-loop 发 `SnapshotRequired(overflow)`（任务 5/7）。决策 5（内存版本 + 周期兑正）→ send-loop `lastVer` + `reconcileEvery`（任务 7）。
- §3 组件分解 → translate/store/broadcast/policysync 四块（任务 1-8），依赖无环。
- §4 数据流（写/扇出/拉取/订阅）→ Publish（任务 4）/RunDispatchLoop→Hub（任务 8/5）/PullSnapshot（任务 6）/Subscribe 对账（任务 7）。
- §5.1 翻译 → 任务 1（增删映射、裁尾部空串、op 映射、int64↔uint64）。§5.2 只读快照一致性 → 任务 6 只读事务内三读。§5.3 版本对账 → 任务 7（current!=last 发 SnapshotRequired）。§5.4 心跳 → 任务 7 ticker。§5.5 Hub 背压 → 任务 5。§5.6 认证复用 → 任务 6 `NewGRPCServer` + secret.Resolver。
- §6 容灾 → 未知 app fail-close（任务 2/6/7）、Publish 失败返错不影响 DB（任务 4）、坏消息跳过不中断订阅（任务 4）、ctx 取消注销不泄漏（任务 7）。
- §7 测试 → 各任务 TDD + 端到端（任务 8）。§8 依赖 → go-redis（任务 3）+ testcontainers redis（任务 4）。

**2. 占位符扫描：** 无 TODO/待定/占位 stub，每个代码步骤含完整可跑代码。

**3. 类型一致性：** `syncv1.*`（Delta/PolicyChange/PolicyRule/DataPolicy/SyncEvent_*/ChangeOp_CHANGE_OP_*/Snapshot/SnapshotRequired/Heartbeat 字段名均按生成代码核实）、`cp.Delta/Rule/DataPolicy/DataPolicyChange/ChangeOp`、`store.ResolveAppIDByKey/ReadCurrentVersion/ReadAppRules/ReadAppDataPolicies`、`translate.DeltaToProto/RulesToProto/DataPoliciesToProto`、`broadcast.Channel/EncodeEnvelope/DecodeEnvelope/Publisher/Subscriber/NewRedisPublisher/NewRedisSubscriber`、`policysync.Config/Server/NewServer/Hub()/PullSnapshot/Subscribe/NewGRPCServer/RunDispatchLoop`、`auth.AppIDFromContext/UnaryServerInterceptor/StreamServerInterceptor/NewPerRPCCredentials`、`secret.NewResolver/EncryptSecret`、`dbtest.SetupSchema/SeedApp/SeedAppKey/StartRedis` 跨任务一致。Hub 私有类型 `subscriber{events,overflow}` 在任务 5 定义、任务 7 引用一致。
