# M1.3 人→角色 + 「某人能做什么」有效权限视图 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 给 app 域 RBAC 补上「某人能做什么」反查——新增只读 RPC `GetEffectivePermissions`，算出某 user 的功能权限允许集 + 角色闭包 + 数据策略符号谓词，并经 gRPC/REST/Console 三面交付；「人→角色」复用既有绑定 RPC，组织成用户为中心的运营台旅程。

**架构：** 控制面**瞬态复用整条 Sidecar 求值栈**——`store.ReadAppRules`/`ReadAppDataPolicies`（与 Sidecar 快照同源）喂数据，建临时 `kernel.Engine`（功能权限 `BatchEnforce` 真实 deny 覆盖 + 角色闭包），`dataperm.Filter` 算数据策略符号谓词。零决策逻辑重写，算出即 Sidecar 会放行的。鉴权复用 M1.1 `scopeApp`，matcher 一字不改。

**技术栈：** Go、casbin v3.10.0、buf/protobuf、gRPC、net/http（REST + Console BFF）、html/template、PostgreSQL、testify、dbtest（容器化 PG）。

**规格：** `docs/superpowers/specs/2026-06-14-sydom-m1-3-effective-permissions-design.md`

---

## 文件结构

**新建：**
- `internal/sidecar/dataperm/symbolic.go` — `FilterSymbolic` + `renderSymbolic`（符号谓词渲染，变量不解析）。
- `internal/sidecar/dataperm/symbolic_test.go` — 符号渲染专项测试。
- `internal/controlplane/effperm/effperm.go` — 瞬态求值核心 `Compute`（cp→kernel 转换、功能集、角色闭包、数据预览编排）。
- `internal/controlplane/effperm/effperm_test.go` — 求值核心单测（dbtest 播种 casbin_rule/data_policy）。
- `internal/controlplane/mgmt/effective.go` — gRPC handler `GetEffectivePermissions`。
- `internal/controlplane/mgmt/effective_test.go` — handler + 鉴权矩阵测。
- `internal/controlplane/restgw/routes_effperm_test.go` — REST 路由测。
- `internal/controlplane/console/routes_effperm.go` — Console 用户为中心页面 handler。
- `internal/controlplane/console/routes_effperm_test.go` — Console 页面测。
- `internal/controlplane/console/templates/effective.html` — 页面模板。

**修改：**
- `api/proto/sydom/admin/v1/admin.proto` — 新增 RPC + 4 消息。
- `internal/sidecar/dataperm/filter.go` — 抽 `selectAndMerge`，`buildPlan` 改为薄封装（Sidecar 路径零漂移）。
- `internal/controlplane/mgmt/authz.go:45` — `ruleTable` 新增 1 条。
- `internal/controlplane/restgw/routes.go:68` — `appRoutes()` 追加 1 路由。
- `internal/controlplane/console/routes_rbac.go:15` — `registerRBAC` 注册 3 路由（effective GET + bind/unbind 回 effective）。
- `internal/controlplane/console/templates/_appnav.html` — 加「有效权限」导航。

---

## 任务 1：proto 新增 GetEffectivePermissions

**文件：**
- 修改：`api/proto/sydom/admin/v1/admin.proto`

- [ ] **步骤 1：在 service 读面分组加 RPC**

在 `admin.proto` 的 `rpc ListAdminRoles(...)` 行之后（只读 List 分组内）插入：

```proto
  // —— 反查 / 有效权限（M1.3）——
  rpc GetEffectivePermissions(GetEffectivePermissionsRequest) returns (GetEffectivePermissionsResponse);
```

- [ ] **步骤 2：在文件末尾追加消息**

```proto
// GetEffectivePermissions：反查「某 user 在某 app 能做什么」。app 域 / tenant-scoped 只读。
message GetEffectivePermissionsRequest {
  uint64 app_id = 1;
  string user_id = 2;
}
message GetEffectivePermissionsResponse {
  repeated string roles = 1;                       // 隐式角色闭包（含继承）
  repeated EffectivePermission permissions = 2;    // deny 覆盖后的功能允许集
  repeated DataPolicyPreview data_previews = 3;    // 每 resource 的符号行过滤
}
message EffectivePermission {
  string resource = 1;
  string action = 2;
}
message DataPolicyPreview {
  string resource = 1;
  string match = 2;     // all | none | conditional
  string predicate = 3; // 仅 match=conditional 非空
}
```

- [ ] **步骤 3：重新生成 + 校验编译**

运行：`make proto-gen && go build ./gen/...`
预期：`gen/sydom/admin/v1/admin.pb.go` 与 `admin_grpc.pb.go` 含新类型；编译通过。

- [ ] **步骤 4：proto 漂移检测**

运行：`make proto-check`
预期：`git diff --exit-code gen/` 无差异（生成代码已与 .proto 同步）。

- [ ] **步骤 5：Commit**

```bash
git add api/proto/sydom/admin/v1/admin.proto gen/
git commit -m "feat(proto): M1.3 GetEffectivePermissions RPC + 有效权限消息"
```

---

## 任务 2：dataperm 抽 selectAndMerge + FilterSymbolic（Sidecar 路径零漂移）

**文件：**
- 修改：`internal/sidecar/dataperm/filter.go`
- 创建：`internal/sidecar/dataperm/symbolic.go`
- 测试：`internal/sidecar/dataperm/symbolic_test.go`

- [ ] **步骤 1：重构 filter.go 抽 selectAndMerge（保持既有行为）**

把 `internal/sidecar/dataperm/filter.go` 现有 `buildPlan`（约 39-82 行）替换为下面两个函数（`selectAndMerge` 不解析变量，`buildPlan` 变薄封装）：

```go
// selectAndMerge 跑「主体匹配 + 中毒 fail-close + allow/deny 合并」，不解析变量。
// 产出含 $user.xxx 的原始合并树，供 SQL/raw 路径后置解析、符号路径直接渲染共用。
func (f *Filter) selectAndMerge(user, dom, resource string) (plan, error) {
	bucket, configured := f.table.Lookup(resource)
	if !configured {
		return plan{mode: modeNoFilter}, nil
	}
	roles, err := f.roles.GetImplicitRolesForUser(user, dom)
	if err != nil {
		return plan{}, err // fail-close 透传（含 ErrNotReady/ErrForeignDomain）
	}
	roleSet := make(map[string]struct{}, len(roles))
	for _, r := range roles {
		roleSet[r] = struct{}{}
	}
	var allow, deny []*Condition
	for _, s := range bucket {
		if !subjectMatches(s, user, roleSet) {
			continue
		}
		if s.parseErr != nil {
			return plan{}, s.parseErr // 命中中毒策略 → fail-close
		}
		if s.effect == effectDeny {
			deny = append(deny, s.cond)
		} else {
			allow = append(allow, s.cond)
		}
	}
	if len(allow) == 0 {
		return plan{mode: modeDenyAll}, nil
	}
	merged := orAll(allow)
	if len(deny) > 0 {
		merged = &Condition{Op: OpAnd, Children: []*Condition{
			merged,
			{Op: OpNot, Children: []*Condition{orAll(deny)}},
		}}
	}
	return plan{mode: modeTree, tree: merged}, nil
}

// buildPlan 在 selectAndMerge 之上后置解析变量（行为等价于原实现：合并为结构操作、
// 解析为逐叶纯函数，先合并后解析与先解析后合并结果一致；ErrMissingVar 仍 fail-close）。
func (f *Filter) buildPlan(user, dom, resource string, attrs map[string]any) (plan, error) {
	p, err := f.selectAndMerge(user, dom, resource)
	if err != nil || p.mode != modeTree {
		return p, err
	}
	resolved, err := resolveVars(p.tree, attrs)
	if err != nil {
		return plan{}, err // ErrMissingVar
	}
	return plan{mode: modeTree, tree: resolved}, nil
}
```

- [ ] **步骤 2：运行既有 dataperm 全测试验证零漂移**

运行：`go test ./internal/sidecar/dataperm/... -v`
预期：全 PASS（`FilterSQL`/`FilterRaw`/`ErrMissingVar`/中毒 fail-close 行为不变）。这是「Sidecar 路径零漂移」验收门。

- [ ] **步骤 3：写失败的符号渲染测试**

创建 `internal/sidecar/dataperm/symbolic_test.go`：

```go
package dataperm

import "testing"

// stubResolver 满足 RoleResolver，返回固定隐式角色集。
type stubResolver struct{ roles []string }

func (s stubResolver) GetImplicitRolesForUser(_, _ string) ([]string, error) {
	return s.roles, nil
}

func newFilterWith(roles []string, policies ...kernelDP) *Filter {
	t := NewTable()
	dps := make([]kernelDPSlice, 0)
	_ = dps
	t.ApplySnapshot(toKernelDPs(policies))
	return NewFilter(stubResolver{roles: roles}, t)
}

func TestFilterSymbolic_ConditionalUserVarPreserved(t *testing.T) {
	f := newFilterWith(
		[]string{"sales"},
		dp("user", "alice", "orders", "allow", `{"op":"EQ","field":"region","value":"$user.region"}`),
	)
	sr, err := f.FilterSymbolic("alice", "1", "orders")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if sr.Match != MatchConditional {
		t.Fatalf("match=%q want conditional", sr.Match)
	}
	if sr.Predicate != "region = $user.region" {
		t.Fatalf("predicate=%q", sr.Predicate)
	}
}

func TestFilterSymbolic_DenyOverrideShape(t *testing.T) {
	f := newFilterWith(
		[]string{"sales"},
		dp("role", "sales", "orders", "allow", `{"op":"EQ","field":"region","value":"east"}`),
		dp("user", "alice", "orders", "deny", `{"op":"EQ","field":"status","value":"archived"}`),
	)
	sr, err := f.FilterSymbolic("alice", "1", "orders")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if sr.Match != MatchConditional || sr.Predicate != "(region = 'east' AND NOT (status = 'archived'))" {
		t.Fatalf("got match=%q predicate=%q", sr.Match, sr.Predicate)
	}
}

func TestFilterSymbolic_AllWhenUnconfigured(t *testing.T) {
	f := newFilterWith(nil)
	sr, _ := f.FilterSymbolic("alice", "1", "orders")
	if sr.Match != MatchAll {
		t.Fatalf("match=%q want all", sr.Match)
	}
}

func TestFilterSymbolic_NoneWhenNoAllowHit(t *testing.T) {
	f := newFilterWith(
		nil, // alice 无角色
		dp("role", "sales", "orders", "allow", `{"op":"EQ","field":"region","value":"east"}`),
	)
	sr, _ := f.FilterSymbolic("alice", "1", "orders")
	if sr.Match != MatchNone {
		t.Fatalf("match=%q want none", sr.Match)
	}
}
```

辅助构造器放在测试文件顶部（`dp` 造 `kernel.DataPolicy`）：

```go
import "github.com/nickZFZ/Sydom/internal/sidecar/kernel"

type kernelDP = kernel.DataPolicy
type kernelDPSlice = []kernel.DataPolicy

var dpID uint64

func dp(subjType, subjID, resource, effect, cond string) kernel.DataPolicy {
	dpID++
	return kernel.DataPolicy{ID: dpID, SubjectType: subjType, SubjectID: subjID, Resource: resource, Condition: cond, Effect: effect}
}

func toKernelDPs(ps []kernel.DataPolicy) []kernel.DataPolicy { return ps }
```

> 注：上面 `newFilterWith` 里残留的 `dps`/`kernelDPSlice` 是占位噪声，实现时直接用：
> ```go
> func newFilterWith(roles []string, policies ...kernel.DataPolicy) *Filter {
> 	t := NewTable()
> 	t.ApplySnapshot(policies)
> 	return NewFilter(stubResolver{roles: roles}, t)
> }
> ```
> （删除 `kernelDP`/`kernelDPSlice`/`toKernelDPs` 别名，直接用 `kernel.DataPolicy`。）

- [ ] **步骤 4：运行测试验证失败**

运行：`go test ./internal/sidecar/dataperm/ -run TestFilterSymbolic -v`
预期：FAIL，`f.FilterSymbolic undefined`。

- [ ] **步骤 5：实现 symbolic.go**

创建 `internal/sidecar/dataperm/symbolic.go`：

```go
package dataperm

import (
	"fmt"
	"strings"
)

// SymbolicResult 是符号预览结果：Match 表整体语义，Predicate 为人类可读谓词（$user.xxx 保留符号）。
type SymbolicResult struct {
	Match     string // MatchAll | MatchNone | MatchConditional
	Predicate string // 仅 MatchConditional 非空
}

// FilterSymbolic 渲染合并后的行过滤谓词，变量保持符号形态（不解析 attrs）。
// 仅供展示：值内联进字符串，绝不进任何 SQL，无注入面。
func (f *Filter) FilterSymbolic(user, dom, resource string) (SymbolicResult, error) {
	p, err := f.selectAndMerge(user, dom, resource)
	if err != nil {
		return SymbolicResult{}, err
	}
	switch p.mode {
	case modeNoFilter:
		return SymbolicResult{Match: MatchAll}, nil
	case modeDenyAll:
		return SymbolicResult{Match: MatchNone}, nil
	default:
		var b strings.Builder
		renderSymbolic(p.tree, &b)
		return SymbolicResult{Match: MatchConditional, Predicate: b.String()}, nil
	}
}

func renderSymbolic(c *Condition, b *strings.Builder) {
	switch c.Op {
	case OpAnd, OpOr:
		sep := " AND "
		if c.Op == OpOr {
			sep = " OR "
		}
		b.WriteByte('(')
		for i, ch := range c.Children {
			if i > 0 {
				b.WriteString(sep)
			}
			renderSymbolic(ch, b)
		}
		b.WriteByte(')')
	case OpNot:
		b.WriteString("NOT (")
		renderSymbolic(c.Children[0], b)
		b.WriteByte(')')
	default:
		renderSymbolicLeaf(c, b)
	}
}

func renderSymbolicLeaf(c *Condition, b *strings.Builder) {
	switch c.Op {
	case OpIsNull:
		fmt.Fprintf(b, "%s IS NULL", c.Field)
	case OpIsNotNull:
		fmt.Fprintf(b, "%s IS NOT NULL", c.Field)
	case OpIN, OpNotIn:
		kw := "IN"
		if c.Op == OpNotIn {
			kw = "NOT IN"
		}
		fmt.Fprintf(b, "%s %s (%s)", c.Field, kw, symbolicList(c.Value))
	case OpBetween:
		if arr, ok := c.Value.([]any); ok && len(arr) == 2 {
			fmt.Fprintf(b, "%s BETWEEN %s AND %s", c.Field, symbolicValue(arr[0]), symbolicValue(arr[1]))
		}
	default: // 标量比较 / LIKE
		fmt.Fprintf(b, "%s %s %s", c.Field, sqlComparator(c.Op), symbolicValue(c.Value))
	}
}

// symbolicValue 格式化展示值：$user.xxx 原样；字符串字面量加单引号；数值/布尔 fmt。
func symbolicValue(v any) string {
	if s, ok := v.(string); ok {
		if strings.HasPrefix(s, "$user.") {
			return s
		}
		return "'" + s + "'"
	}
	return fmt.Sprintf("%v", v)
}

func symbolicList(v any) string {
	arr, ok := v.([]any)
	if !ok {
		return ""
	}
	parts := make([]string, len(arr))
	for i, e := range arr {
		parts[i] = symbolicValue(e)
	}
	return strings.Join(parts, ", ")
}
```

- [ ] **步骤 6：运行符号测试 + 全 dataperm 测试**

运行：`go test ./internal/sidecar/dataperm/... -v`
预期：新符号测试 + 既有测试全 PASS。

- [ ] **步骤 7：Commit**

```bash
git add internal/sidecar/dataperm/
git commit -m "feat(dataperm): 抽 selectAndMerge + FilterSymbolic 符号谓词渲染(Sidecar 路径零漂移)"
```

---

## 任务 3：effperm 瞬态求值核心

**文件：**
- 创建：`internal/controlplane/effperm/effperm.go`
- 测试：`internal/controlplane/effperm/effperm_test.go`

- [ ] **步骤 0：回源核实 casbin v3.10.0 语义（记忆铁律，先于实现）**

读 `casbin/enforcer.go` 的 `BatchEnforce`、`casbin/rbac_api.go` 的 `GetImplicitRolesForUser`，确认：
1. `BatchEnforce(reqs)` 对每条 req 等价单条 `Enforce`，**逐条套用 effect `some(allow)&&!some(deny)`**（deny 覆盖）；
2. `GetImplicitRolesForUser(user, dom)` 返回含继承的隐式角色、不含 user 自身。
将结论一句话写进 `effperm.go` 顶部注释（与 `kernel/engine.go:195` 既有核实注释同范式）。

- [ ] **步骤 1：写失败的求值测试**

创建 `internal/controlplane/effperm/effperm_test.go`。用 dbtest 起 PG、`SeedApp` 建 app，直插物化 `casbin_rule`/`data_policy`（effperm 与 Sidecar 同读这两张表）：

```go
package effperm_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/nickZFZ/Sydom/internal/controlplane/effperm"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// insertRule 直插一条 casbin_rule（dom 用 dbtest.SeedDomain）。
func insertRule(t *testing.T, db *sql.DB, appID int64, ptype string, v ...string) {
	t.Helper()
	cols := [6]string{}
	copy(cols[:], v)
	_, err := db.Exec(
		`INSERT INTO casbin_rule (app_id, ptype, v0, v1, v2, v3, v4, v5)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		appID, ptype, cols[0], cols[1], cols[2], cols[3], cols[4], cols[5])
	require.NoError(t, err)
}

func insertDataPolicy(t *testing.T, db *sql.DB, appID int64, subjType, subjID, resource, effect, condJSON string) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO data_policy (app_id, subject_type, subject_id, resource, condition, effect, version)
		 VALUES ($1,$2,$3,$4,$5::jsonb,$6,1)`,
		appID, subjType, subjID, resource, condJSON, effect)
	require.NoError(t, err)
}

func TestCompute_DirectBindingAndInheritance(t *testing.T) {
	db := dbtest.Setup(t)
	appID := dbtest.SeedApp(t, db)
	dom := dbtest.SeedDomain

	// 角色继承：sales 继承 viewer；viewer 可 read orders；sales 可 export orders。
	insertRule(t, db, appID, "p", "viewer", dom, "orders", "read", "allow")
	insertRule(t, db, appID, "p", "sales", dom, "orders", "export", "allow")
	insertRule(t, db, appID, "g", "sales", "viewer", dom) // child=sales 继承 parent=viewer
	insertRule(t, db, appID, "g", "alice", "sales", dom)  // alice→sales

	res, err := effperm.Compute(context.Background(), db, appID, "alice")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"sales", "viewer"}, res.Roles)
	require.ElementsMatch(t, []effperm.Perm{
		{Resource: "orders", Action: "read"},   // 经 viewer（继承）
		{Resource: "orders", Action: "export"}, // 经 sales（直绑）
	}, res.Permissions)
}

func TestCompute_DenyOverride(t *testing.T) {
	db := dbtest.Setup(t)
	appID := dbtest.SeedApp(t, db)
	dom := dbtest.SeedDomain

	insertRule(t, db, appID, "p", "sales", dom, "orders", "read", "allow")
	insertRule(t, db, appID, "p", "sales", dom, "orders", "read", "deny") // 同 (obj,act) deny 覆盖
	insertRule(t, db, appID, "g", "alice", "sales", dom)

	res, err := effperm.Compute(context.Background(), db, appID, "alice")
	require.NoError(t, err)
	require.Empty(t, res.Permissions) // deny 覆盖后不在允许集
}

func TestCompute_DataPolicySymbolicPreview(t *testing.T) {
	db := dbtest.Setup(t)
	appID := dbtest.SeedApp(t, db)
	dom := dbtest.SeedDomain

	insertRule(t, db, appID, "g", "alice", "sales", dom)
	insertDataPolicy(t, db, appID, "role", "sales", "orders", "allow",
		`{"op":"EQ","field":"region","value":"$user.region"}`)

	res, err := effperm.Compute(context.Background(), db, appID, "alice")
	require.NoError(t, err)
	require.Len(t, res.DataViews, 1)
	require.Equal(t, "orders", res.DataViews[0].Resource)
	require.Equal(t, "conditional", res.DataViews[0].Match)
	require.Equal(t, "region = $user.region", res.DataViews[0].Predicate)
}

func TestCompute_PoisonedDataPolicyFailClose(t *testing.T) {
	db := dbtest.Setup(t)
	appID := dbtest.SeedApp(t, db)
	dom := dbtest.SeedDomain

	insertRule(t, db, appID, "g", "alice", "sales", dom)
	insertDataPolicy(t, db, appID, "role", "sales", "orders", "allow", `{"op":"BOGUS"}`) // 中毒

	_, err := effperm.Compute(context.Background(), db, appID, "alice")
	require.Error(t, err) // 命中中毒 → fail-close，绝不空集冒充无权限
}

func TestCompute_EmptyAppNoError(t *testing.T) {
	db := dbtest.Setup(t)
	appID := dbtest.SeedApp(t, db)

	res, err := effperm.Compute(context.Background(), db, appID, "nobody")
	require.NoError(t, err)
	require.Empty(t, res.Roles)
	require.Empty(t, res.Permissions)
	require.Empty(t, res.DataViews)
}
```

> 若 `dbtest.Setup` 实际函数名不同（如 `NewDB`/`MustSetup`），按 `internal/dbtest/dbtest.go` 既有导出名校正——保持与 `internal/controlplane/store` 现有测试同一套法。

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/controlplane/effperm/ -v`
预期：FAIL，`effperm.Compute undefined` / 包不存在。

- [ ] **步骤 3：实现 effperm.go**

创建 `internal/controlplane/effperm/effperm.go`：

```go
// Package effperm 在控制面内瞬态复用 Sidecar 求值栈（kernel.Engine + dataperm），
// 从 DB 物化策略（store.ReadAppRules/ReadAppDataPolicies，与 Sidecar 快照同源）算「某 user 能做什么」。
//
// casbin v3.10.0 已回源核实：BatchEnforce 对每条 req 等价单条 Enforce，逐条套用
// effect some(allow)&&!some(deny)（deny 覆盖）；GetImplicitRolesForUser 返回含继承的隐式角色、不含 user 自身。
package effperm

import (
	"context"
	"fmt"
	"sort"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/sidecar/dataperm"
	"github.com/nickZFZ/Sydom/internal/sidecar/kernel"
)

// Perm 是一条功能权限（deny 覆盖后的允许动作）。
type Perm struct {
	Resource string
	Action   string
}

// DataView 是某 resource 的数据策略符号预览。
type DataView struct {
	Resource  string
	Match     string // all | none | conditional
	Predicate string // 仅 conditional 非空
}

// Result 是一次有效权限求值结果。
type Result struct {
	Roles       []string
	Permissions []Perm
	DataViews   []DataView
}

// Compute 在调用方提供的只读 tx 内，对 (appID, user) 做瞬态求值。
// 内部自读 application.domain 作为引擎单一域来源。
// 任一步失败一律返回 error（fail-close），绝不返回空 Result 冒充「无权限」。
func Compute(ctx context.Context, tx cp.DBTX, appID int64, user string) (Result, error) {
	var domain string
	if err := tx.QueryRowContext(ctx,
		`SELECT domain FROM application WHERE id=$1`, appID).Scan(&domain); err != nil {
		return Result{}, fmt.Errorf("effperm: read domain: %w", err)
	}

	rules, err := store.ReadAppRules(ctx, tx, appID)
	if err != nil {
		return Result{}, fmt.Errorf("effperm: read rules: %w", err)
	}
	dps, err := store.ReadAppDataPolicies(ctx, tx, appID)
	if err != nil {
		return Result{}, fmt.Errorf("effperm: read data policies: %w", err)
	}

	table := dataperm.NewTable()
	eng, err := kernel.New(domain, nil, table)
	if err != nil {
		return Result{}, fmt.Errorf("effperm: new engine: %w", err)
	}
	if err := eng.ApplySnapshot(toSnapshot(rules, dps)); err != nil {
		return Result{}, fmt.Errorf("effperm: apply snapshot: %w", err)
	}

	roles, err := eng.GetImplicitRolesForUser(user, domain)
	if err != nil {
		return Result{}, fmt.Errorf("effperm: implicit roles: %w", err)
	}
	sort.Strings(roles)

	perms, err := computePerms(eng, rules, domain, user)
	if err != nil {
		return Result{}, err
	}
	views, err := computeViews(eng, table, dps, domain, user)
	if err != nil {
		return Result{}, err
	}
	return Result{Roles: roles, Permissions: perms, DataViews: views}, nil
}

func toSnapshot(rules []cp.Rule, dps []cp.DataPolicy) kernel.Snapshot {
	ks := make([]kernel.Rule, len(rules))
	for i, r := range rules {
		ks[i] = kernel.Rule{Ptype: r.Ptype, V: r.V}
	}
	kd := make([]kernel.DataPolicy, len(dps))
	for i, d := range dps {
		kd[i] = kernel.DataPolicy{
			ID: uint64(d.ID), SubjectType: d.SubjectType, SubjectID: d.SubjectID,
			Resource: d.Resource, Condition: d.Condition, Effect: d.Effect,
		}
	}
	return kernel.Snapshot{Version: 1, Rules: ks, DataPolicies: kd}
}

// computePerms 枚举该域 p 行的 (obj,act) 候选去重，BatchEnforce 跑真实 deny 覆盖，收 allow 集（排序稳定）。
func computePerms(eng *kernel.Engine, rules []cp.Rule, domain, user string) ([]Perm, error) {
	type oa struct{ obj, act string }
	seen := map[oa]bool{}
	var cands []oa
	for _, r := range rules {
		if r.Ptype != "p" {
			continue
		}
		k := oa{r.V[2], r.V[3]} // p = sub,dom,obj,act,eft → V[2]=obj, V[3]=act
		if !seen[k] {
			seen[k] = true
			cands = append(cands, k)
		}
	}
	if len(cands) == 0 {
		return nil, nil
	}
	reqs := make([][]string, len(cands))
	for i, c := range cands {
		reqs[i] = []string{user, domain, c.obj, c.act}
	}
	results, err := eng.BatchEnforce(reqs)
	if err != nil {
		return nil, fmt.Errorf("effperm: batch enforce: %w", err)
	}
	var perms []Perm
	for i, ok := range results {
		if ok {
			perms = append(perms, Perm{Resource: cands[i].obj, Action: cands[i].act})
		}
	}
	sort.Slice(perms, func(i, j int) bool {
		if perms[i].Resource != perms[j].Resource {
			return perms[i].Resource < perms[j].Resource
		}
		return perms[i].Action < perms[j].Action
	})
	return perms, nil
}

// computeViews 对每个 distinct data_policy resource 做符号预览（resource 排序稳定）。
func computeViews(eng *kernel.Engine, table *dataperm.Table, dps []cp.DataPolicy, domain, user string) ([]DataView, error) {
	seen := map[string]bool{}
	var resources []string
	for _, d := range dps {
		if !seen[d.Resource] {
			seen[d.Resource] = true
			resources = append(resources, d.Resource)
		}
	}
	sort.Strings(resources)
	filter := dataperm.NewFilter(eng, table)
	var views []DataView
	for _, res := range resources {
		sr, err := filter.FilterSymbolic(user, domain, res)
		if err != nil {
			return nil, fmt.Errorf("effperm: symbolic filter %q: %w", res, err)
		}
		views = append(views, DataView{Resource: res, Match: sr.Match, Predicate: sr.Predicate})
	}
	return views, nil
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/controlplane/effperm/ -v`
预期：5 个测试全 PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/effperm/
git commit -m "feat(effperm): 控制面瞬态求值核心(复用 kernel+dataperm, store 同源, deny 覆盖, fail-close)"
```

---

## 任务 4：mgmt handler + ruleTable + 鉴权矩阵

**文件：**
- 创建：`internal/controlplane/mgmt/effective.go`
- 修改：`internal/controlplane/mgmt/authz.go:45`（ruleTable）
- 测试：`internal/controlplane/mgmt/effective_test.go`

- [ ] **步骤 1：ruleTable 新增条目**

在 `internal/controlplane/mgmt/authz.go` 的 `ruleTable` 内（`ListAdminRoles` 行之后）加：

```go
	"/sydom.admin.v1.AdminService/GetEffectivePermissions": {"effective_permission", "read", false, scopeApp},
```

- [ ] **步骤 2：写失败的 handler + 鉴权矩阵测试**

创建 `internal/controlplane/mgmt/effective_test.go`（沿用 `tenant_authz_test.go`/`account_isolation_test.go` 同套播种与 enforcer 装配；下例聚焦断言意图，装配细节对齐既有测试）：

```go
package mgmt_test

import (
	"context"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// 复用本包测试既有的 harness：建 server + enforcer + 两租户两 app + 播种角色绑定。
// （函数名/装配对齐 tenant_authz_test.go，下列为断言核心。）

func TestGetEffectivePermissions_UserIDRequired(t *testing.T) {
	h := newAuthzHarness(t) // 既有 harness 构造器
	_, err := h.srv.GetEffectivePermissions(h.adminCtx, &adminv1.GetEffectivePermissionsRequest{AppId: uint64(h.appA)})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestGetEffectivePermissions_TenantAdminSeesOwnApp(t *testing.T) {
	h := newAuthzHarness(t)
	// 经鉴权拦截器调用本租户 app → 放行
	ctx, err := h.authorize(h.tenantAdminA, "GetEffectivePermissions",
		&adminv1.GetEffectivePermissionsRequest{AppId: uint64(h.appA), UserId: "alice"})
	require.NoError(t, err)
	_, err = h.srv.GetEffectivePermissions(ctx, &adminv1.GetEffectivePermissionsRequest{AppId: uint64(h.appA), UserId: "alice"})
	require.NoError(t, err)
}

func TestGetEffectivePermissions_CrossTenant403(t *testing.T) {
	h := newAuthzHarness(t)
	// 租户 A 管理员查租户 B 的 app → PermissionDenied（不泄露存在性）
	_, err := h.authorize(h.tenantAdminA, "GetEffectivePermissions",
		&adminv1.GetEffectivePermissionsRequest{AppId: uint64(h.appB), UserId: "alice"})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

func TestGetEffectivePermissions_DisabledAppStillReadable(t *testing.T) {
	h := newAuthzHarness(t)
	h.setAppStatus(h.appA, 2) // 停用
	ctx, err := h.authorize(h.tenantAdminA, "GetEffectivePermissions",
		&adminv1.GetEffectivePermissionsRequest{AppId: uint64(h.appA), UserId: "alice"})
	require.NoError(t, err) // 读不受 status 写拦截
	_, err = h.srv.GetEffectivePermissions(ctx, &adminv1.GetEffectivePermissionsRequest{AppId: uint64(h.appA), UserId: "alice"})
	require.NoError(t, err)
}
```

> 若本包尚无统一 harness，按 `tenant_authz_test.go` 现有 setup 内联展开（建 db、`SeedAppInTenant` 两租户、`adminauthz` enforcer、`EnsureTenantAdmin`、`mgmt.AuthorizeRule` 调用）。harness 仅为消除重复，不改变断言。

- [ ] **步骤 3：运行测试验证失败**

运行：`go test ./internal/controlplane/mgmt/ -run TestGetEffectivePermissions -v`
预期：FAIL，`GetEffectivePermissions` 方法未定义。

- [ ] **步骤 4：实现 handler**

创建 `internal/controlplane/mgmt/effective.go`：

```go
package mgmt

import (
	"context"
	"database/sql"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/effperm"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetEffectivePermissions 反查「某 user 在某 app 能做什么」。app 域 / tenant-scoped 只读。
// 鉴权由 AuthorizeRule(scopeApp) 前置完成；本 handler 只在只读 tx 内瞬态求值。
func (s *AdminServer) GetEffectivePermissions(ctx context.Context, r *adminv1.GetEffectivePermissionsRequest) (*adminv1.GetEffectivePermissionsResponse, error) {
	if r.UserId == "" {
		return nil, status.Error(codes.InvalidArgument, "user_id required")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin read tx: %v", err)
	}
	defer tx.Rollback()

	res, err := effperm.Compute(ctx, tx, int64(r.AppId), r.UserId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "effective permissions: %v", err)
	}
	out := &adminv1.GetEffectivePermissionsResponse{Roles: res.Roles}
	for _, p := range res.Permissions {
		out.Permissions = append(out.Permissions, &adminv1.EffectivePermission{Resource: p.Resource, Action: p.Action})
	}
	for _, d := range res.DataViews {
		out.DataPreviews = append(out.DataPreviews, &adminv1.DataPolicyPreview{Resource: d.Resource, Match: d.Match, Predicate: d.Predicate})
	}
	return out, nil
}
```

- [ ] **步骤 5：运行测试验证通过 + 鉴权 scope 测试**

运行：`go test ./internal/controlplane/mgmt/ -run "TestGetEffectivePermissions|TestAuthz" -v`
预期：全 PASS（含 `authz_scope_test.go` 若覆盖全 ruleTable，需补 `GetEffectivePermissions` 进表）。

- [ ] **步骤 6：Commit**

```bash
git add internal/controlplane/mgmt/effective.go internal/controlplane/mgmt/authz.go internal/controlplane/mgmt/effective_test.go
git commit -m "feat(mgmt): GetEffectivePermissions handler + ruleTable scopeApp + 跨租户鉴权矩阵"
```

---

## 任务 5：REST 路由

**文件：**
- 修改：`internal/controlplane/restgw/routes.go:68`（`appRoutes()`）
- 测试：`internal/controlplane/restgw/routes_effperm_test.go`

- [ ] **步骤 1：写失败的路由测试**

创建 `internal/controlplane/restgw/routes_effperm_test.go`（对齐 `routes_accounts_test.go` 既有 HTTP 测试范式：起 handler、签 REST-HMAC、发请求、断言）：

```go
package restgw_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestEffectivePermissionsRoute_GET 验证 GET /v1/apps/{app_id}/effective-permissions?user_id=
// 命中 GetEffectivePermissions、app_id 取自 path、user_id 取自 query。
func TestEffectivePermissionsRoute_GET(t *testing.T) {
	h := newRESTHarness(t) // 既有 harness（对齐 routes_accounts_test.go）
	req := h.signedGET(t, "/v1/apps/1/effective-permissions?user_id=alice")
	resp := h.do(req)
	require.Equal(t, http.StatusOK, resp.StatusCode) // 鉴权放行下 200；body 为 JSON
}
```

> 装配/签名/harness 对齐 `routes_accounts_test.go`。重点断言：路由存在、绑定 `GetEffectivePermissions`、path/query 解析正确。

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/controlplane/restgw/ -run TestEffectivePermissionsRoute -v`
预期：FAIL（404：路由未注册）。

- [ ] **步骤 3：在 appRoutes() 追加路由**

在 `internal/controlplane/restgw/routes.go` 的 `appRoutes()` 返回切片内追加（紧邻 `ListUserBindings` 路由后）：

```go
		{"GET", "/v1/apps/{app_id}/effective-permissions", pfx + "GetEffectivePermissions",
			func(r *http.Request, _ []byte) (proto.Message, error) {
				id, err := pathUint64(r, "app_id")
				if err != nil {
					return nil, err
				}
				return &adminv1.GetEffectivePermissionsRequest{AppId: id, UserId: r.URL.Query().Get("user_id")}, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.GetEffectivePermissions(ctx, m.(*adminv1.GetEffectivePermissionsRequest))
			}},
```

- [ ] **步骤 4：运行测试验证通过 + 全 restgw 测试**

运行：`go test ./internal/controlplane/restgw/... -v`
预期：新路由测试 + 既有（含路由计数若有）全 PASS。若有「路由总数」断言，同步 +1。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/restgw/
git commit -m "feat(restgw): GET /v1/apps/{app_id}/effective-permissions 路由(复用 ruleTable scopeApp)"
```

---

## 任务 6：Console 用户为中心页面

**文件：**
- 创建：`internal/controlplane/console/routes_effperm.go`
- 创建：`internal/controlplane/console/templates/effective.html`
- 修改：`internal/controlplane/console/routes_rbac.go:15`（`registerRBAC` 注册 3 路由）
- 修改：`internal/controlplane/console/templates/_appnav.html`（加导航）
- 测试：`internal/controlplane/console/routes_effperm_test.go`

- [ ] **步骤 1：注册路由**

在 `internal/controlplane/console/routes_rbac.go` 的 `registerRBAC` 末尾加：

```go
	mux.HandleFunc("GET /apps/{app_id}/effective", h.effectivePermissions)
	mux.HandleFunc("POST /apps/{app_id}/effective/bind", h.bindUserOnEffective)
	mux.HandleFunc("POST /apps/{app_id}/effective/unbind", h.unbindUserOnEffective)
```

- [ ] **步骤 2：写失败的 Console 测试**

创建 `internal/controlplane/console/routes_effperm_test.go`（对齐 `handler_test.go` 既有页面测试范式）：

```go
package console_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEffectivePage_RendersForUser(t *testing.T) {
	h := newConsoleHarness(t) // 既有 harness（对齐 handler_test.go）
	resp, body := h.getAuthed(t, "/apps/1/effective?user_id=alice")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, body, "alice")       // 渲染了被查 user
	require.Contains(t, body, "有效权限")     // 页面标题/区块
}

func TestEffectivePage_DegradeNoEnumeration(t *testing.T) {
	h := newConsoleHarness(t)
	// 跨租户/越权访问 → 渲染降级页，不枚举资源、不泄露存在性
	resp, body := h.getAuthed(t, "/apps/999/effective?user_id=alice")
	require.NotEqual(t, http.StatusOK, resp.StatusCode)
	require.False(t, strings.Contains(body, "export")) // 不泄露任何资源细节
}
```

> harness、登录会话、CSRF 对齐 `handler_test.go`。

- [ ] **步骤 3：运行测试验证失败**

运行：`go test ./internal/controlplane/console/ -run TestEffectivePage -v`
预期：FAIL（handler/模板未定义，404 或编译错）。

- [ ] **步骤 4：实现 handler**

创建 `internal/controlplane/console/routes_effperm.go`：

```go
package console

import (
	"context"
	"fmt"
	"net/url"

	"net/http"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"google.golang.org/protobuf/proto"
)

// effectivePermissions：用户为中心页面（读）。GET ?user_id= → 角色 + 功能允许集 + 数据策略符号谓词。
// 同时取 ListRoles 供「分配角色」下拉、ListUserBindings 供「当前角色（可解绑）」。
func (h *Handler) effectivePermissions(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	appID, err := pathUint64(r, "app_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"GetEffectivePermissions", err)
		return
	}
	userID := r.FormValue("user_id")

	// 角色下拉（鉴权同 app 域；任一失败走降级页，不枚举）。
	rolesMsg := &adminv1.ListRolesRequest{AppId: appID}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"ListRoles", principal, rolesMsg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListRoles", err)
		return
	}
	rolesResp, err := h.srv.ListRoles(ctx, rolesMsg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListRoles", err)
		return
	}

	data := map[string]any{
		"Nav": "apps", "AppID": appID, "Tab": "effective",
		"UserID": userID, "Roles": rolesResp.Roles, "CSRF": sess.CSRF,
	}

	// 仅当指定 user_id 时算有效权限 + 当前绑定。
	if userID != "" {
		bindMsg := &adminv1.ListUserBindingsRequest{AppId: appID, UserId: userID}
		bctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"ListUserBindings", principal, bindMsg)
		if err != nil {
			h.renderGRPCError(w, r, svc+"ListUserBindings", err)
			return
		}
		bindResp, err := h.srv.ListUserBindings(bctx, bindMsg)
		if err != nil {
			h.renderGRPCError(w, r, svc+"ListUserBindings", err)
			return
		}
		effMsg := &adminv1.GetEffectivePermissionsRequest{AppId: appID, UserId: userID}
		ectx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"GetEffectivePermissions", principal, effMsg)
		if err != nil {
			h.renderGRPCError(w, r, svc+"GetEffectivePermissions", err)
			return
		}
		effResp, err := h.srv.GetEffectivePermissions(ectx, effMsg)
		if err != nil {
			h.renderGRPCError(w, r, svc+"GetEffectivePermissions", err)
			return
		}
		data["Bindings"] = bindResp.Bindings
		data["EffRoles"] = effResp.Roles
		data["Permissions"] = effResp.Permissions
		data["DataPreviews"] = effResp.DataPreviews
		data["Queried"] = true
	}
	h.renderPage(w, r, "effective.html", http.StatusOK, data)
}

// effectiveRedirect：bind/unbind 后回到本页面（保留 user_id），形成「分配→看能做什么」闭环。
func effectiveRedirect(r *http.Request) string {
	return fmt.Sprintf("/apps/%s/effective?user_id=%s",
		r.PathValue("app_id"), url.QueryEscape(r.FormValue("user_id")))
}

// bindUserOnEffective：复用 doWrite + BindUserRole，仅重定向回 effective 页。
func (h *Handler) bindUserOnEffective(w http.ResponseWriter, r *http.Request) {
	h.doWrite(w, r, svc+"BindUserRole",
		func(r *http.Request) (proto.Message, error) {
			appID, err := pathUint64(r, "app_id")
			if err != nil {
				return nil, err
			}
			roleID, err := formInt64(r, "role_id")
			if err != nil {
				return nil, err
			}
			return &adminv1.UserRoleRequest{AppId: appID, UserId: r.FormValue("user_id"), RoleId: roleID}, nil
		},
		func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
			return s.BindUserRole(ctx, m.(*adminv1.UserRoleRequest))
		},
		effectiveRedirect)
}

// unbindUserOnEffective：复用 doWrite + UnbindUserRole，重定向回 effective 页。
func (h *Handler) unbindUserOnEffective(w http.ResponseWriter, r *http.Request) {
	h.doWrite(w, r, svc+"UnbindUserRole",
		func(r *http.Request) (proto.Message, error) {
			appID, err := pathUint64(r, "app_id")
			if err != nil {
				return nil, err
			}
			roleID, err := formInt64(r, "role_id")
			if err != nil {
				return nil, err
			}
			return &adminv1.UserRoleRequest{AppId: appID, UserId: r.FormValue("user_id"), RoleId: roleID}, nil
		},
		func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
			return s.UnbindUserRole(ctx, m.(*adminv1.UserRoleRequest))
		},
		effectiveRedirect)
}
```

- [ ] **步骤 5：实现模板 effective.html**

创建 `internal/controlplane/console/templates/effective.html`（对齐 `bindings.html` 的 layout 嵌入范式；字段名与 handler `data` map 一致）：

```html
{{define "content"}}
{{template "_appnav" .}}
<h2>有效权限 — 某人能做什么</h2>

<form method="GET" action="/apps/{{.AppID}}/effective">
  <label>用户 ID <input type="text" name="user_id" value="{{.UserID}}" required></label>
  <button type="submit">查询</button>
</form>

{{if .Queried}}
<h3>{{.UserID}} 的角色</h3>
<ul>
  {{range .Bindings}}
  <li>role_id={{.RoleId}}
    <form method="POST" action="/apps/{{$.AppID}}/effective/unbind" style="display:inline">
      <input type="hidden" name="csrf" value="{{$.CSRF}}">
      <input type="hidden" name="user_id" value="{{$.UserID}}">
      <input type="hidden" name="role_id" value="{{.RoleId}}">
      <button type="submit">解绑</button>
    </form>
  </li>
  {{else}}<li>（无直绑角色）</li>{{end}}
</ul>

<form method="POST" action="/apps/{{.AppID}}/effective/bind">
  <input type="hidden" name="csrf" value="{{.CSRF}}">
  <input type="hidden" name="user_id" value="{{.UserID}}">
  <label>分配角色
    <select name="role_id">
      {{range .Roles}}<option value="{{.RoleId}}">{{.Name}} ({{.Code}})</option>{{end}}
    </select>
  </label>
  <button type="submit">分配</button>
</form>

<h3>隐式角色闭包（含继承）</h3>
<p>{{range .EffRoles}}<code>{{.}}</code> {{else}}（无）{{end}}</p>

<h3>功能权限（最终允许集）</h3>
<table>
  <tr><th>资源</th><th>动作</th></tr>
  {{range .Permissions}}<tr><td>{{.Resource}}</td><td>{{.Action}}</td></tr>
  {{else}}<tr><td colspan="2">（无功能权限）</td></tr>{{end}}
</table>

<h3>数据策略（行过滤符号谓词）</h3>
<table>
  <tr><th>资源</th><th>范围</th><th>谓词</th></tr>
  {{range .DataPreviews}}<tr><td>{{.Resource}}</td><td>{{.Match}}</td><td><code>{{.Predicate}}</code></td></tr>
  {{else}}<tr><td colspan="3">（无数据策略）</td></tr>{{end}}
</table>
{{end}}
{{end}}
```

- [ ] **步骤 6：加导航入口**

在 `internal/controlplane/console/templates/_appnav.html` 的 app 子导航中加一项（对齐既有 tab 写法）：

```html
<a href="/apps/{{.AppID}}/effective"{{if eq .Tab "effective"}} class="active"{{end}}>有效权限</a>
```

- [ ] **步骤 7：运行测试验证通过 + 全 console 测试**

运行：`go test ./internal/controlplane/console/... -v`
预期：新页面测试 + 既有（含 Playwright 之外的单测）全 PASS。

- [ ] **步骤 8：Commit**

```bash
git add internal/controlplane/console/
git commit -m "feat(console): 用户为中心有效权限页(分配角色+看能做什么闭环, 降级无枚举)"
```

---

## 任务 7：整体验证 + 安全评审

**文件：** 无新增代码（验证 + 评审）。

- [ ] **步骤 1：全仓测试**

运行：`go test ./...`
预期：0 FAIL（含 effperm/dataperm/mgmt/restgw/console/e2e）。

- [ ] **步骤 2：全仓 vet（跨包改签名兜底）**

运行：`go vet ./...`
预期：无输出（干净）。

- [ ] **步骤 3：EP 不变量逐条核验（file:line 证据）**

对照规格 §11，逐条给证据：
- EP-1 一致性：`effperm.Compute` 读 `store.ReadAppRules`/`ReadAppDataPolicies` + `kernel.Engine` + `dataperm`，无第二套决策逻辑。
- EP-2 deny 覆盖：功能集经 `eng.BatchEnforce`（真实 effect），非 SQL 重算。
- EP-3 fail-close：`Compute` 任一步失败 return error；`TestCompute_PoisonedDataPolicyFailClose` 守护。
- EP-4 租户隔离零旁路：`ruleTable` `scopeApp` + `AuthorizeRule`；`TestGetEffectivePermissions_CrossTenant403` 守护；M1.1 matcher 未改（`git diff` 确认 `adminauthz/enforcer.go` 无改动）。
- EP-5 Sidecar 路径零漂移：`go test ./internal/sidecar/dataperm/...` 既有测试全绿。
- EP-6 符号口径：`FilterSymbolic` 不解析 attrs、值仅进展示串、不接客户数据。
- EP-7 secret 不泄露：新 RPC/页面不触 `secret_enc`（`grep -rn secret_enc internal/controlplane/effperm internal/controlplane/mgmt/effective.go` 应无命中）。

- [ ] **步骤 4：（推荐）opus 整体安全评审**

调度安全评审子代理：复核 EP-1..EP-7 + 跨租户矩阵 + 无新枚举面 + 错误回传脱敏沿用既有非阻塞 TODO。

- [ ] **步骤 5：收尾 Commit（如评审有修补）**

```bash
git add -A
git commit -m "test(m1.3): 整体验证全绿 + EP-1..EP-7 安全评审通过"
```

---

## 自检结果

**1. 规格覆盖度：** 规格 §5 求值核心→任务 3；§6 dataperm 符号→任务 2；§7 RPC→任务 1+4；§8 鉴权→任务 4；§9 Console→任务 6；§10 三面 parity→任务 4(gRPC)+5(REST)+6(Console)；§11 不变量→任务 7；§12 测试→各任务 TDD + 任务 7；§13 任务分解→本计划 7 任务一一对应。无遗漏。

**2. 占位符扫描：** 测试 harness 处标注「对齐既有 X_test.go」非占位——指明确切复用来源（`tenant_authz_test.go`/`routes_accounts_test.go`/`handler_test.go`），实现时按既有导出名落地；步骤 2 测试代码顶部的 `kernelDP` 别名已显式标注删除并给出最终版。无 TODO/待定。

**3. 类型一致性：** `effperm.Perm{Resource,Action}` / `DataView{Resource,Match,Predicate}` / `Result{Roles,Permissions,DataViews}` 跨任务 3→4 一致；proto `EffectivePermission{resource,action}` / `DataPolicyPreview{resource,match,predicate}` 跨任务 1→4 一致；`FilterSymbolic`→`SymbolicResult{Match,Predicate}` 跨任务 2→3 一致；`Compute(ctx, cp.DBTX, int64, string)` 签名跨任务 3→4 一致。`*sql.Tx` 满足 `cp.DBTX`。无漂移。
