# 司域 · Sidecar 鉴权 API (④-4) 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 在 Sidecar 内构建数据面对外的鉴权出口——Go `Authorizer` 门面（组合 ④-1 内核 + ④-2 数据权限 + ④-3 陈旧信号，落地 fail-close + 可配陈旧上限）加一层 gRPC `AuthService`（Check/BatchCheck/FilterSQL）。

**架构：** 新建包 `internal/sidecar/authz`。`Authorizer` 持注入的 `*kernel.Engine`、`*dataperm.Filter`、窄接口 `Freshness`（`*syncclient.SyncClient` 满足），pin 域取自 `engine.Domain()`（单一真相源）。每个公开方法先过陈旧守卫：`!Ready()`→`ErrNotReady`，`now-LastSyncAt > MaxStaleness`→`ErrTooStale`，均上抛为 gRPC `Unavailable`（"无法判定"信号，调用方自定 fail-open/close），绝不伪装成 `allowed=false`。纯库层，不出 cmd。

**技术栈：** Go 1.26、gRPC、生成桩 `gen/sydom/auth/v1`（`authv1`，含 WKT `google.protobuf.Struct/Value`）、`google.golang.org/protobuf/types/known/structpb`、buf 工具链、`bufconn`+`testify/require`。

---

## 关键事实（动手前必读，均已回源核实）

**下游公共签名：**
- `kernel.New(domain string, c cache.Cache, applier DataPolicyApplier) (*Engine, error)`；`(*Engine).Enforce(sub,dom,obj,act string) (bool,error)`、`.BatchEnforce(reqs [][]string) ([]bool,error)`、`.Ready() bool`、`.ApplySnapshot(Snapshot) error`。哨兵 `kernel.ErrNotReady`/`ErrForeignDomain`。**`Engine` 当前无 `Domain()` getter —— Task 1 新增。**
- 域类型 `kernel.Rule{Ptype string; V [6]string}`、`kernel.Snapshot{Version uint64; Rules []Rule; DataPolicies []DataPolicy}`、`kernel.DataPolicy{ID,SubjectType,SubjectID,Resource,Condition,Effect}`。
- `dataperm.NewTable() *Table`（满足 `kernel.DataPolicyApplier`）；`dataperm.NewFilter(roles RoleResolver, table *Table) *Filter`；`(*Filter).FilterSQL(user,dom,resource string, attrs map[string]any) (SQLResult,error)`、`.FilterRaw(...) (RawResult,error)`。`SQLResult{SQL string; Args []any}`、`RawResult{Match string; Tree *Condition}`、常量 `dataperm.MatchAll/MatchNone/MatchConditional`。哨兵 `dataperm.ErrMissingVar`/`dataperm.ErrInvalidPolicy`。`*kernel.Engine` 满足 `dataperm.RoleResolver`。
- `syncclient.SyncClient.Ready() bool`、`.LastSyncAt() time.Time`（满足本包 `Freshness` 接口，构造期由 cmd 注入，本计划测试用 fake）。

**工具链事实：**
- proto 头部：`syntax="proto3"; package sydom.auth.v1; option go_package="github.com/nickZFZ/Sydom/gen/sydom/auth/v1;authv1";`。WKT 用 `import "google/protobuf/struct.proto";`（buf 自带 protocompile 支持）。
- `buf.yaml` 已全局豁免 `RPC_REQUEST_STANDARD_NAME` 与 `RPC_REQUEST_RESPONSE_UNIQUE`——故 `FilterRequest`（非 `FilterSQLRequest`）命名、`CheckRequest` 被 `BatchCheckRequest` 嵌入复用，都不触 lint。
- `make proto-gen`（= buf lint + buf generate，输出 `gen/`，`paths=source_relative`）；`make proto-check` 检生成代码漂移。前置 `make proto-tools` 装 buf（若未装）。
- `structpb`（protobuf v1.33.0）：`structpb.NewStruct(map[string]any) (*Struct,error)`、`(*Struct).AsMap() map[string]any`（nil 安全，返回空 map）、`structpb.NewValue(any) (*Value,error)`（含 nil→NullValue、float64/string/bool 直转）、`(*Value).AsInterface() any`。

**gRPC 错误码映射（§6 契约）：** not-ready/too-stale→`Unavailable`；`ErrMissingVar`→`InvalidArgument`；`ErrInvalidPolicy`→`FailedPrecondition`；`ErrForeignDomain`/其它→`Internal`。

---

## 文件结构

| 文件 | 职责 |
|---|---|
| `internal/sidecar/kernel/engine.go`（改） | 加 `Engine.Domain() string` getter |
| `internal/sidecar/authz/errors.go` | 哨兵 `ErrTooStale` |
| `internal/sidecar/authz/authorizer.go` | `Authorizer` 门面 + `Config` + `Freshness` + `CheckReq` + `New` + `checkFresh` + Check/BatchCheck/FilterSQL/FilterRaw |
| `internal/sidecar/authz/server.go` | gRPC `Server`（包装 Authorizer）+ `NewServer`/`NewGRPCServer` + `toStatus` 错误映射 + structpb 译码 |
| `api/proto/sydom/auth/v1/auth.proto` | `AuthService`（Check/BatchCheck/FilterSQL）契约 |
| `gen/sydom/auth/v1/*`（生成） | buf generate 产物 |
| `internal/sidecar/kernel/engine_test.go`（改） | `Engine.Domain()` 测试 |
| `internal/sidecar/authz/authorizer_test.go` | 门面单测 + 共享夹具（fakeFresh/appliedEngine/newAuthorizer） |
| `internal/sidecar/authz/server_test.go` | bufconn + AuthService RPC 往返 + 错误码映射 |

---

## 任务 1：Engine.Domain() getter（④-1 唯一改动）

**文件：**
- 修改：`internal/sidecar/kernel/engine.go`
- 测试：`internal/sidecar/kernel/engine_test.go`

- [ ] **步骤 1：编写失败的测试**

在 `engine_test.go` 末尾追加：

```go
func TestEngine_Domain_ReturnsPinnedDomain(t *testing.T) {
	e, err := New("dom1", nil, nil)
	require.NoError(t, err)
	require.Equal(t, "dom1", e.Domain(), "Domain() 应返回构造时 pin 的域")
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/sidecar/kernel/ -run TestEngine_Domain -v`
预期：编译失败 `e.Domain undefined`。

- [ ] **步骤 3：编写实现**

在 `engine.go` 的 `Ready()` 方法之后追加：

```go
// Domain 返回构造时 pin 的 casbin 域（供上层组合者取单一真相源的域，避免平行配置漂移）。
func (e *Engine) Domain() string { return e.domain }
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/sidecar/kernel/ -run TestEngine_Domain -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/sidecar/kernel/engine.go internal/sidecar/kernel/engine_test.go
git commit -m "feat(sidecar/kernel): Engine.Domain() 暴露 pin 域（④-4 依赖，任务 1/8）"
```

---

## 任务 2：Authorizer 核心 + 陈旧守卫 + Check

**文件：**
- 创建：`internal/sidecar/authz/errors.go`
- 创建：`internal/sidecar/authz/authorizer.go`
- 测试：`internal/sidecar/authz/authorizer_test.go`

本任务建立门面骨架、陈旧守卫、Check，以及供后续任务复用的测试夹具（`fakeFresh`/`appliedEngine`/`newAuthorizer`）。

- [ ] **步骤 1：编写失败的测试（含共享夹具）**

`internal/sidecar/authz/authorizer_test.go`：

```go
package authz

import (
	"testing"
	"time"

	"github.com/nickZFZ/Sydom/internal/sidecar/dataperm"
	"github.com/nickZFZ/Sydom/internal/sidecar/kernel"
	"github.com/stretchr/testify/require"
)

// fakeFresh 注入可控的就绪/陈旧信号。
type fakeFresh struct {
	ready bool
	last  time.Time
}

func (f fakeFresh) Ready() bool            { return f.ready }
func (f fakeFresh) LastSyncAt() time.Time  { return f.last }

// appliedEngine 构造已应用快照（alice→manager；manager 可 read order；allow+deny 两条数据策略）的内核与表。
func appliedEngine(t *testing.T) (*kernel.Engine, *dataperm.Table) {
	t.Helper()
	table := dataperm.NewTable()
	engine, err := kernel.New("dom1", nil, table)
	require.NoError(t, err)
	snap := kernel.Snapshot{
		Version: 5,
		Rules: []kernel.Rule{
			{Ptype: "g", V: [6]string{"alice", "manager", "dom1", "", "", ""}},
			{Ptype: "p", V: [6]string{"manager", "dom1", "order", "read", "allow", ""}},
		},
		DataPolicies: []kernel.DataPolicy{
			{ID: 1, SubjectType: "role", SubjectID: "manager", Resource: "order",
				Condition: `{"field":"dept","op":"EQ","value":"$user.department"}`, Effect: "allow"},
			{ID: 2, SubjectType: "role", SubjectID: "manager", Resource: "order",
				Condition: `{"field":"status","op":"IN","value":["locked","void"]}`, Effect: "deny"},
		},
	}
	require.NoError(t, engine.ApplySnapshot(snap))
	return engine, table
}

// newAuthorizer 组装 Authorizer（真实 Engine+Filter + 注入的 fresh）。
func newAuthorizer(t *testing.T, cfg Config, fresh Freshness) *Authorizer {
	t.Helper()
	engine, table := appliedEngine(t)
	filter := dataperm.NewFilter(engine, table)
	return New(engine, filter, fresh, cfg)
}

func TestAuthorizer_Check_AllowViaRole(t *testing.T) {
	a := newAuthorizer(t, Config{}, fakeFresh{ready: true, last: time.Now()})
	allow, err := a.Check("alice", "order", "read") // alice 经 manager 角色
	require.NoError(t, err)
	require.True(t, allow)
}

func TestAuthorizer_Check_DenyUnconfigured(t *testing.T) {
	a := newAuthorizer(t, Config{}, fakeFresh{ready: true, last: time.Now()})
	allow, err := a.Check("alice", "order", "delete") // 无此策略
	require.NoError(t, err)
	require.False(t, allow)
}

func TestAuthorizer_Check_NotReady_FailClose(t *testing.T) {
	a := newAuthorizer(t, Config{}, fakeFresh{ready: false})
	allow, err := a.Check("alice", "order", "read")
	require.ErrorIs(t, err, kernel.ErrNotReady)
	require.False(t, allow, "未就绪必须 fail-close")
}

func TestAuthorizer_Check_TooStale_FailClose(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	a := newAuthorizer(t, Config{MaxStaleness: 10 * time.Second},
		fakeFresh{ready: true, last: now.Add(-10*time.Second - time.Nanosecond)})
	a.now = func() time.Time { return now }
	allow, err := a.Check("alice", "order", "read")
	require.ErrorIs(t, err, ErrTooStale)
	require.False(t, allow)
}

func TestAuthorizer_Check_AtStalenessBoundary_Passes(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	a := newAuthorizer(t, Config{MaxStaleness: 10 * time.Second},
		fakeFresh{ready: true, last: now.Add(-10 * time.Second)}) // 恰好等于阈值
	a.now = func() time.Time { return now }
	allow, err := a.Check("alice", "order", "read")
	require.NoError(t, err, "恰好等于阈值应放行（用 > 比较）")
	require.True(t, allow)
}

func TestAuthorizer_Check_MaxStalenessZero_DisablesGuard(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	a := newAuthorizer(t, Config{MaxStaleness: 0}, // 关闭陈旧守卫
		fakeFresh{ready: true, last: now.Add(-9999 * time.Hour)})
	a.now = func() time.Time { return now }
	allow, err := a.Check("alice", "order", "read")
	require.NoError(t, err, "MaxStaleness=0 时陈旧不拦截")
	require.True(t, allow)
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/sidecar/authz/ -run TestAuthorizer_Check -v`
预期：编译失败 `undefined: Authorizer`（及 `New`/`Config`/`Freshness`/`ErrTooStale`）。

- [ ] **步骤 3：编写实现**

`internal/sidecar/authz/errors.go`：

```go
// Package authz 是 Sidecar 数据面对外的鉴权出口：组合内核功能鉴权 + 数据权限下推 + 陈旧守卫。
// 一切"无法判定"（未就绪/太陈旧）以独立错误上抛，绝不伪装成 allowed=false——让调用方自定 fail-open/close。
package authz

import "errors"

// ErrTooStale 表示快照陈旧度超过 Config.MaxStaleness，fail-close 拒绝判定。
var ErrTooStale = errors.New("authz: snapshot too stale (exceeds max staleness)")
```

`internal/sidecar/authz/authorizer.go`：

```go
package authz

import (
	"time"

	"github.com/nickZFZ/Sydom/internal/sidecar/dataperm"
	"github.com/nickZFZ/Sydom/internal/sidecar/kernel"
)

// Freshness 暴露同步新鲜度信号；*syncclient.SyncClient 满足之。窄接口便于测试注入。
type Freshness interface {
	Ready() bool
	LastSyncAt() time.Time
}

// Config 是 Authorizer 的策略参数。
type Config struct {
	// MaxStaleness 为 0 关闭陈旧守卫（Ready 即服务）；>0 时 now-LastSyncAt 超阈 fail-close。
	MaxStaleness time.Duration
}

// CheckReq 是一条批量鉴权请求（域由 Authorizer pin，不在请求内）。
type CheckReq struct {
	Subject string
	Object  string
	Action  string
}

// Authorizer 组合内核 + 数据权限 + 陈旧守卫，是数据面鉴权门面。不做任何持久化/网络副作用。
type Authorizer struct {
	engine *kernel.Engine
	filter *dataperm.Filter
	fresh  Freshness
	domain string // = engine.Domain()，构造时取，单一真相源
	cfg    Config
	now    func() time.Time // 注入便于测试
}

// New 组装 Authorizer；pin 域取自内核（engine.Domain()），避免平行配置漂移成 deny-all。
func New(engine *kernel.Engine, filter *dataperm.Filter, fresh Freshness, cfg Config) *Authorizer {
	return &Authorizer{
		engine: engine,
		filter: filter,
		fresh:  fresh,
		domain: engine.Domain(),
		cfg:    cfg,
		now:    time.Now,
	}
}

// checkFresh 是陈旧守卫：未就绪 → ErrNotReady；超阈（含从未同步）→ ErrTooStale；否则放行。
func (a *Authorizer) checkFresh() error {
	if !a.fresh.Ready() {
		return kernel.ErrNotReady
	}
	if a.cfg.MaxStaleness > 0 {
		last := a.fresh.LastSyncAt()
		if last.IsZero() || a.now().Sub(last) > a.cfg.MaxStaleness {
			return ErrTooStale
		}
	}
	return nil
}

// Check 判定 (subject, object, action)；域由 pin。守卫不通过即 fail-close。
func (a *Authorizer) Check(subject, object, action string) (bool, error) {
	if err := a.checkFresh(); err != nil {
		return false, err
	}
	return a.engine.Enforce(subject, a.domain, object, action)
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/sidecar/authz/ -run TestAuthorizer_Check -v`
预期：6 个测试 PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/sidecar/authz/errors.go internal/sidecar/authz/authorizer.go internal/sidecar/authz/authorizer_test.go
git commit -m "feat(sidecar/authz): Authorizer 门面 + 陈旧守卫 + Check（④-4，任务 2/8）"
```

---

## 任务 3：BatchCheck

**文件：**
- 修改：`internal/sidecar/authz/authorizer.go`
- 测试：`internal/sidecar/authz/authorizer_test.go`

- [ ] **步骤 1：编写失败的测试**

在 `authorizer_test.go` 追加：

```go
func TestAuthorizer_BatchCheck_PreservesOrderAndLength(t *testing.T) {
	a := newAuthorizer(t, Config{}, fakeFresh{ready: true, last: time.Now()})
	got, err := a.BatchCheck([]CheckReq{
		{Subject: "alice", Object: "order", Action: "read"},   // 命中
		{Subject: "alice", Object: "order", Action: "delete"}, // 不命中
		{Subject: "bob", Object: "order", Action: "read"},     // bob 无角色
	})
	require.NoError(t, err)
	require.Equal(t, []bool{true, false, false}, got)
}

func TestAuthorizer_BatchCheck_NotReady_FailClose(t *testing.T) {
	a := newAuthorizer(t, Config{}, fakeFresh{ready: false})
	got, err := a.BatchCheck([]CheckReq{{Subject: "alice", Object: "order", Action: "read"}})
	require.ErrorIs(t, err, kernel.ErrNotReady)
	require.Nil(t, got)
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/sidecar/authz/ -run TestAuthorizer_BatchCheck -v`
预期：编译失败 `a.BatchCheck undefined`。

- [ ] **步骤 3：编写实现**

在 `authorizer.go` 的 `Check` 方法之后追加：

```go
// BatchCheck 批量判定；用 pin 域组装 casbin 四元请求，等长同序返回。守卫不通过即 fail-close。
func (a *Authorizer) BatchCheck(reqs []CheckReq) ([]bool, error) {
	if err := a.checkFresh(); err != nil {
		return nil, err
	}
	rows := make([][]string, len(reqs))
	for i, r := range reqs {
		rows[i] = []string{r.Subject, a.domain, r.Object, r.Action}
	}
	return a.engine.BatchEnforce(rows)
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/sidecar/authz/ -run TestAuthorizer_BatchCheck -v`
预期：2 个测试 PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/sidecar/authz/authorizer.go internal/sidecar/authz/authorizer_test.go
git commit -m "feat(sidecar/authz): BatchCheck 批量鉴权（④-4，任务 3/8）"
```

---

## 任务 4：FilterSQL + FilterRaw

**文件：**
- 修改：`internal/sidecar/authz/authorizer.go`
- 测试：`internal/sidecar/authz/authorizer_test.go`

- [ ] **步骤 1：编写失败的测试**

在 `authorizer_test.go` 追加：

```go
func TestAuthorizer_FilterSQL_DenyOverride(t *testing.T) {
	a := newAuthorizer(t, Config{}, fakeFresh{ready: true, last: time.Now()})
	res, err := a.FilterSQL("alice", "order", map[string]any{"department": "HR"})
	require.NoError(t, err)
	require.Equal(t, "(dept = ? AND NOT (status IN (?, ?)))", res.SQL)
	require.Equal(t, []any{"HR", "locked", "void"}, res.Args)
}

func TestAuthorizer_FilterSQL_MissingVar(t *testing.T) {
	a := newAuthorizer(t, Config{}, fakeFresh{ready: true, last: time.Now()})
	_, err := a.FilterSQL("alice", "order", map[string]any{}) // 缺 department
	require.ErrorIs(t, err, dataperm.ErrMissingVar)
}

func TestAuthorizer_FilterSQL_NotReady_FailClose(t *testing.T) {
	a := newAuthorizer(t, Config{}, fakeFresh{ready: false})
	_, err := a.FilterSQL("alice", "order", map[string]any{"department": "HR"})
	require.ErrorIs(t, err, kernel.ErrNotReady)
}

func TestAuthorizer_FilterRaw_MergedTree(t *testing.T) {
	a := newAuthorizer(t, Config{}, fakeFresh{ready: true, last: time.Now()})
	res, err := a.FilterRaw("alice", "order", map[string]any{"department": "HR"})
	require.NoError(t, err)
	require.Equal(t, dataperm.MatchConditional, res.Match)
	require.NotNil(t, res.Tree, "命中应返回合并条件树")
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/sidecar/authz/ -run 'TestAuthorizer_FilterSQL|TestAuthorizer_FilterRaw' -v`
预期：编译失败 `a.FilterSQL undefined` / `a.FilterRaw undefined`。

- [ ] **步骤 3：编写实现**

在 `authorizer.go` 的 `BatchCheck` 之后追加：

```go
// FilterSQL 渲染数据权限的参数化 SQL 片段（值全进 Args）。守卫不通过即 fail-close。
func (a *Authorizer) FilterSQL(subject, resource string, attrs map[string]any) (dataperm.SQLResult, error) {
	if err := a.checkFresh(); err != nil {
		return dataperm.SQLResult{}, err
	}
	return a.filter.FilterSQL(subject, a.domain, resource, attrs)
}

// FilterRaw 返回变量已解析的合并条件树，交 ORM/SDK 自渲染。守卫不通过即 fail-close。
func (a *Authorizer) FilterRaw(subject, resource string, attrs map[string]any) (dataperm.RawResult, error) {
	if err := a.checkFresh(); err != nil {
		return dataperm.RawResult{}, err
	}
	return a.filter.FilterRaw(subject, a.domain, resource, attrs)
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/sidecar/authz/ -run 'TestAuthorizer_FilterSQL|TestAuthorizer_FilterRaw' -v`
预期：4 个测试 PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/sidecar/authz/authorizer.go internal/sidecar/authz/authorizer_test.go
git commit -m "feat(sidecar/authz): FilterSQL/FilterRaw 数据权限下推（④-4，任务 4/8）"
```

---

## 任务 5：auth.proto 契约 + 生成代码

**文件：**
- 创建：`api/proto/sydom/auth/v1/auth.proto`
- 生成：`gen/sydom/auth/v1/auth.pb.go`、`gen/sydom/auth/v1/auth_grpc.pb.go`

proto 是声明，验证靠 `make proto-gen`（lint + 生成）+ `go build ./gen/...`（生成代码编译）。

- [ ] **步骤 1：编写 auth.proto**

`api/proto/sydom/auth/v1/auth.proto`：

```proto
syntax = "proto3";

package sydom.auth.v1;

option go_package = "github.com/nickZFZ/Sydom/gen/sydom/auth/v1;authv1";

import "google/protobuf/struct.proto";

// AuthService 是 Sidecar 对同机业务进程暴露的本地鉴权服务（回环调用，v1 不加 HMAC）。
// 域由 Sidecar 按所属 app pin，不在请求体中传递（强隔离）。
service AuthService {
  // Check 判定单条功能权限。
  rpc Check(CheckRequest) returns (CheckResponse);
  // BatchCheck 批量判定，结果与请求等长同序。
  rpc BatchCheck(BatchCheckRequest) returns (BatchCheckResponse);
  // FilterSQL 返回数据权限的参数化 SQL 片段（值在 args，绝不进 SQL 文本）。
  rpc FilterSQL(FilterRequest) returns (FilterSQLResponse);
}

message CheckRequest {
  string subject = 1;
  string object = 2;
  string action = 3;
}

message CheckResponse {
  bool allowed = 1;
}

message BatchCheckRequest {
  repeated CheckRequest requests = 1;
}

message BatchCheckResponse {
  repeated bool allowed = 1; // 与 requests 等长同序
}

message FilterRequest {
  string subject = 1;
  string resource = 2;
  google.protobuf.Struct attrs = 3; // $user.xxx 变量取值（JSON 标量）
}

message FilterSQLResponse {
  string sql = 1;                          // 无过滤=空串；deny-all="1=0"；否则参数化片段
  repeated google.protobuf.Value args = 2; // 占位符实参（JSON 标量）
}
```

- [ ] **步骤 2：生成代码并验证 lint/编译**

运行：`make proto-gen`
预期：buf lint 无报错（`FilterRequest` 命名与 `CheckRequest` 复用被 `buf.yaml` 既有豁免覆盖）；生成 `gen/sydom/auth/v1/auth.pb.go` 与 `auth_grpc.pb.go`。
> 若报 `buf: command not found`，先 `make proto-tools`。

运行：`go build ./gen/sydom/auth/v1/`
预期：成功（生成代码可编译）。

- [ ] **步骤 3：核对生成符号齐备**

运行：`grep -lE 'NewAuthServiceClient|RegisterAuthServiceServer|UnimplementedAuthServiceServer' gen/sydom/auth/v1/auth_grpc.pb.go`
预期：输出该文件路径（client/server/Unimplemented 三件套都在）。

- [ ] **步骤 4：Commit（proto + 生成代码一起入库，保持无漂移）**

```bash
git add api/proto/sydom/auth/v1/auth.proto gen/sydom/auth/v1/
git commit -m "feat(proto): AuthService v1 契约 + 生成代码（④-4，任务 5/8）"
```

---

## 任务 6：gRPC Server.Check + 错误码映射 + 装配

**文件：**
- 创建：`internal/sidecar/authz/server.go`
- 测试：`internal/sidecar/authz/server_test.go`

本任务落地 gRPC `Server`、`NewServer`/`NewGRPCServer`、`toStatus` 错误映射、`Check` RPC，并建立 bufconn 测试夹具。

- [ ] **步骤 1：编写失败的测试（含 bufconn 夹具）**

`internal/sidecar/authz/server_test.go`：

```go
package authz

import (
	"context"
	"net"
	"testing"
	"time"

	authv1 "github.com/nickZFZ/Sydom/gen/sydom/auth/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// startAuthServer 起 bufconn AuthService，返回拨号好的客户端。
func startAuthServer(t *testing.T, a *Authorizer) authv1.AuthServiceClient {
	t.Helper()
	g := grpc.NewServer()
	authv1.RegisterAuthServiceServer(g, NewServer(a))
	lis := bufconn.Listen(1024 * 1024)
	go func() { _ = g.Serve(lis) }()
	t.Cleanup(g.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return authv1.NewAuthServiceClient(conn)
}

func TestServer_Check_AllowViaRole(t *testing.T) {
	a := newAuthorizer(t, Config{}, fakeFresh{ready: true, last: time.Now()})
	cli := startAuthServer(t, a)
	resp, err := cli.Check(context.Background(), &authv1.CheckRequest{Subject: "alice", Object: "order", Action: "read"})
	require.NoError(t, err)
	require.True(t, resp.GetAllowed())
}

func TestServer_Check_NotReady_Unavailable(t *testing.T) {
	a := newAuthorizer(t, Config{}, fakeFresh{ready: false})
	cli := startAuthServer(t, a)
	_, err := cli.Check(context.Background(), &authv1.CheckRequest{Subject: "alice", Object: "order", Action: "read"})
	require.Equal(t, codes.Unavailable, status.Code(err), "未就绪应映射 Unavailable，而非 allowed=false")
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/sidecar/authz/ -run TestServer_Check -v`
预期：编译失败 `undefined: NewServer`。

- [ ] **步骤 3：编写实现**

`internal/sidecar/authz/server.go`：

```go
package authz

import (
	"context"
	"errors"

	authv1 "github.com/nickZFZ/Sydom/gen/sydom/auth/v1"
	"github.com/nickZFZ/Sydom/internal/sidecar/dataperm"
	"github.com/nickZFZ/Sydom/internal/sidecar/kernel"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)

// Server 把 Authorizer 适配为 gRPC AuthService。
type Server struct {
	authv1.UnimplementedAuthServiceServer
	a *Authorizer
}

// NewServer 包装 Authorizer 为 gRPC handler。
func NewServer(a *Authorizer) *Server { return &Server{a: a} }

// NewGRPCServer 装配带 AuthService 的 grpc.Server（供 cmd 监听本地端点）。
func NewGRPCServer(a *Authorizer) *grpc.Server {
	g := grpc.NewServer()
	authv1.RegisterAuthServiceServer(g, NewServer(a))
	return g
}

func (s *Server) Check(_ context.Context, req *authv1.CheckRequest) (*authv1.CheckResponse, error) {
	allowed, err := s.a.Check(req.GetSubject(), req.GetObject(), req.GetAction())
	if err != nil {
		return nil, toStatus(err)
	}
	return &authv1.CheckResponse{Allowed: allowed}, nil
}

// toStatus 把领域错误映射为 gRPC status：
// not-ready/too-stale→Unavailable（无法判定，调用方自定 fail-open/close）；
// ErrMissingVar→InvalidArgument（调用方入参）；ErrInvalidPolicy→FailedPrecondition（服务端数据损坏）；
// ErrForeignDomain/其它→Internal（配置错/未预期）。
func toStatus(err error) error {
	switch {
	case errors.Is(err, kernel.ErrNotReady), errors.Is(err, ErrTooStale):
		return status.Error(codes.Unavailable, err.Error())
	case errors.Is(err, dataperm.ErrMissingVar):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, dataperm.ErrInvalidPolicy):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, kernel.ErrForeignDomain):
		return status.Error(codes.Internal, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/sidecar/authz/ -run TestServer_Check -v`
预期：2 个测试 PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/sidecar/authz/server.go internal/sidecar/authz/server_test.go
git commit -m "feat(sidecar/authz): gRPC AuthService.Check + 错误码映射 + 装配（④-4，任务 6/8）"
```

---

## 任务 7：gRPC BatchCheck + FilterSQL（structpb 译码）

**文件：**
- 修改：`internal/sidecar/authz/server.go`
- 测试：`internal/sidecar/authz/server_test.go`

- [ ] **步骤 1：编写失败的测试**

在 `server_test.go` 追加（顶部 import 补 `"google.golang.org/protobuf/types/known/structpb"`）：

```go
func TestServer_BatchCheck(t *testing.T) {
	a := newAuthorizer(t, Config{}, fakeFresh{ready: true, last: time.Now()})
	cli := startAuthServer(t, a)
	resp, err := cli.BatchCheck(context.Background(), &authv1.BatchCheckRequest{
		Requests: []*authv1.CheckRequest{
			{Subject: "alice", Object: "order", Action: "read"},
			{Subject: "alice", Object: "order", Action: "delete"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, []bool{true, false}, resp.GetAllowed())
}

func TestServer_FilterSQL_DenyOverride_WKTRoundTrip(t *testing.T) {
	a := newAuthorizer(t, Config{}, fakeFresh{ready: true, last: time.Now()})
	cli := startAuthServer(t, a)
	attrs, err := structpb.NewStruct(map[string]any{"department": "HR"})
	require.NoError(t, err)
	resp, err := cli.FilterSQL(context.Background(), &authv1.FilterRequest{
		Subject: "alice", Resource: "order", Attrs: attrs,
	})
	require.NoError(t, err)
	require.Equal(t, "(dept = ? AND NOT (status IN (?, ?)))", resp.GetSql())
	gotArgs := make([]any, len(resp.GetArgs()))
	for i, v := range resp.GetArgs() {
		gotArgs[i] = v.AsInterface()
	}
	require.Equal(t, []any{"HR", "locked", "void"}, gotArgs)
}

func TestServer_FilterSQL_MissingVar_InvalidArgument(t *testing.T) {
	a := newAuthorizer(t, Config{}, fakeFresh{ready: true, last: time.Now()})
	cli := startAuthServer(t, a)
	empty, err := structpb.NewStruct(map[string]any{})
	require.NoError(t, err)
	_, err = cli.FilterSQL(context.Background(), &authv1.FilterRequest{
		Subject: "alice", Resource: "order", Attrs: empty, // 缺 department
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/sidecar/authz/ -run 'TestServer_BatchCheck|TestServer_FilterSQL' -v`
预期：FAIL —— `BatchCheck`/`FilterSQL` 走 `UnimplementedAuthServiceServer` 默认实现返回 `codes.Unimplemented`，断言不符。

- [ ] **步骤 3：编写实现**

在 `server.go` 的 `Check` 方法之后追加：

```go
func (s *Server) BatchCheck(_ context.Context, req *authv1.BatchCheckRequest) (*authv1.BatchCheckResponse, error) {
	reqs := make([]CheckReq, len(req.GetRequests()))
	for i, r := range req.GetRequests() {
		reqs[i] = CheckReq{Subject: r.GetSubject(), Object: r.GetObject(), Action: r.GetAction()}
	}
	allowed, err := s.a.BatchCheck(reqs)
	if err != nil {
		return nil, toStatus(err)
	}
	return &authv1.BatchCheckResponse{Allowed: allowed}, nil
}

func (s *Server) FilterSQL(_ context.Context, req *authv1.FilterRequest) (*authv1.FilterSQLResponse, error) {
	res, err := s.a.FilterSQL(req.GetSubject(), req.GetResource(), req.GetAttrs().AsMap())
	if err != nil {
		return nil, toStatus(err)
	}
	args := make([]*structpb.Value, len(res.Args))
	for i, v := range res.Args {
		val, verr := structpb.NewValue(v)
		if verr != nil {
			return nil, status.Errorf(codes.Internal, "encode arg %d: %v", i, verr)
		}
		args[i] = val
	}
	return &authv1.FilterSQLResponse{Sql: res.SQL, Args: args}, nil
}
```

> 注：`req.GetAttrs().AsMap()` 对 nil Struct 安全（返回空 map）；`structpb.NewValue` 处理 string/float64/bool/nil 标量。

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/sidecar/authz/ -run 'TestServer_BatchCheck|TestServer_FilterSQL' -v`
预期：3 个测试 PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/sidecar/authz/server.go internal/sidecar/authz/server_test.go
git commit -m "feat(sidecar/authz): gRPC BatchCheck + FilterSQL（structpb 译码，④-4，任务 7/8）"
```

---

## 任务 8：全量 -race 验证与收尾

**文件：** 无新增；全包验证。

端到端（快照含 g+p + allow/deny 数据策略 → Check 经角色继承、FilterSQL 反映 deny override）由 Task 2/4/7 共享的 `appliedEngine` 夹具及其 gRPC 往返断言覆盖，DRY 不另写冗余用例。

- [ ] **步骤 1：authz 包竞态测试**

运行：`go test -race ./internal/sidecar/authz/...`
预期：`ok`，无 DATA RACE（陈旧守卫读 fresh vs 并发同步写）。

- [ ] **步骤 2：proto 漂移 + vet + 全仓回归**

运行：`make proto-check && go vet ./internal/sidecar/authz/... && go build ./... && go test ./internal/sidecar/...`
预期：proto-check 无漂移（生成代码与 .proto 同步且已入库）；vet 无输出；build 成功；sidecar 各包（kernel/dataperm/syncclient/authz）测试 `ok`。

- [ ] **步骤 3：收尾**

调用 finishing-a-development-branch 技能决定集成方式（合并/PR/清理）。
更新进度记忆 `project_detailed_design_progress.md`：④-4 鉴权 API 已实现；Sidecar 内部结构（④）四子项目（内核/数据权限/同步客户端/鉴权 API）全部落地。下一步：⑤ SDK 接口规范 / cmd 进程装配。

---

## 自检结果

**1. 规格覆盖度**（对照 spec 各节）：
- §2 决策 1（门面+gRPC）→ Task 2-4（门面）+ Task 5-7（gRPC）。✅
- §2 决策 2（陈旧上限）→ Task 2 `checkFresh` + 边界/关闭测试（恰好阈值放行、超 1ns 拒、MaxStaleness=0 关闭）。✅
- §2 决策 3（Check/BatchCheck/FilterSQL/FilterRaw）→ Task 2/3/4。✅
- §2 决策 4（gRPC 无 FilterRaw）→ Task 5 proto 仅三 RPC。✅
- §2 决策 5（区分无法判定/判定为拒）→ Task 6 `toStatus`（Unavailable）+ Task 6 not-ready 测试断言 `codes.Unavailable`。✅
- §3 组件分解（errors/authorizer/server/proto + Engine.Domain()）→ Task 1（Domain）/2（errors+authorizer）/5（proto）/6（server）。✅
- §4 门面（Freshness/Config/CheckReq/New/checkFresh/四方法）→ Task 2-4。✅
- §5 gRPC 契约（CheckRequest…FilterSQLResponse + WKT）→ Task 5 proto + Task 7 structpb 往返测试。✅
- §6 错误映射（Unavailable/InvalidArgument/FailedPrecondition/Internal）→ Task 6 `toStatus` + Task 6/7 错误码断言（Unavailable、InvalidArgument）。✅
- §7 测试策略（门面单测/AuthService bufconn/端到端/陈旧边界/-race）→ Task 2-4/6-7/8。✅
- §8 移交 cmd → Task 8 步骤 3 记忆更新点出。✅

**2. 占位符扫描**：无 TODO/待定；每个代码步骤给出完整可编译代码 + 确切命令/预期。端到端"不另写"是经论证的 DRY 取舍（已被共享夹具覆盖），非占位。✅

**3. 类型一致性**：跨任务符号统一——`Engine.Domain()`（Task 1 定义，Task 2 `New` 消费）；`Authorizer`/`Config`/`Freshness`/`CheckReq`/`checkFresh`（Task 2 定义，Task 3/4 扩展方法，Task 6/7 server 消费 `CheckReq`/`Check`/`BatchCheck`/`FilterSQL`）；`ErrTooStale`（Task 2 定义，Task 6 `toStatus` 映射）；夹具 `fakeFresh`/`appliedEngine`/`newAuthorizer`（Task 2 定义，Task 3/4/6/7 复用）；生成符号 `authv1.NewAuthServiceClient`/`RegisterAuthServiceServer`/`UnimplementedAuthServiceServer`/各 Request/Response（Task 5 生成并核验，Task 6/7 消费）。错误映射用的 `dataperm.ErrMissingVar`/`ErrInvalidPolicy`、`kernel.ErrNotReady`/`ErrForeignDomain` 均已回源确认导出。✅
