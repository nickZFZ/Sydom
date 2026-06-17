# 司域 M2.2 实现计划 · 决策可解释性（为什么 allow / deny）

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 新增 `ExplainDecision` RPC（三面 parity），解释单条数据面授权决策「为什么 allow / deny」——判定规则、判定角色、用户有效角色链（含继承）、该 resource 数据范围符号谓词。

**架构：** 复用 M1.3 控制面瞬态求值机器（`effperm` + `kernel.Engine`，从 DB 物化策略、与 Sidecar 快照同源），新增 `kernel.Engine.EnforceEx`（取判定规则）与 `effperm.Explain`（组装解释），杜绝第二套决策逻辑。三面共用同一 `AuthorizeRule`/`ruleTable`。

**技术栈：** Go、casbin v3.10.0（`EnforceEx`）、protobuf/buf、`internal/sidecar/kernel`+`dataperm`、`internal/controlplane/effperm`、net/http（REST + Console）、html/template、`dbtest` 集成测试（真实 Postgres + bufconn gRPC + 真实 HMAC）。

**对应规格：** `docs/superpowers/specs/2026-06-17-sydom-m2-2-decision-explainability-design.md`（决策 a/b/c、DX-1..DX-6）。

---

## 文件结构

| 文件 | 职责 | 任务 |
|---|---|---|
| `api/proto/sydom/admin/v1/admin.proto` | `ExplainDecision` RPC + Req/Resp + `DecidingRule`/`DecisionDataScope` | 1 |
| `gen/sydom/admin/v1/*.pb.go`（生成） | `make proto-gen` 产出，入库 | 1 |
| `internal/sidecar/kernel/engine.go` | 新增 `Engine.EnforceEx`（fail-close 守卫镜像 Enforce） | 2 |
| `internal/sidecar/kernel/engine_test.go` | EnforceEx 单测（判定规则、与 Enforce parity、fail-close） | 2 |
| `internal/controlplane/effperm/effperm.go` | 抽 `buildEngine` 共享步 + 新增 `Explain` + `Explanation`/`DecidingRule` 类型 + reason 常量 | 3 |
| `internal/controlplane/effperm/effperm_test.go` | Explain 单测（三 reason、继承、数据范围符号） | 3 |
| `internal/controlplane/mgmt/decision.go`（新建） | `ExplainDecision` handler（只读 tx → effperm.Explain → proto） | 4 |
| `internal/controlplane/mgmt/authz.go` | ruleTable +1 条 | 4 |
| `internal/controlplane/mgmt/decision_test.go`（新建） | gRPC 全栈测试（三 reason + 跨租户 403） | 4 |
| `internal/controlplane/restgw/routes.go` | appRoutes +1 GET 路由 + 计数注释 | 5 |
| `internal/controlplane/restgw/routes_decision_test.go`（新建） | REST parity 测试 | 5 |
| `internal/controlplane/console/routes_decision.go`（新建） | 决策解释器页 handler | 6 |
| `internal/controlplane/console/routes_rbac.go` | 注册 `GET /apps/{app_id}/decision` | 6 |
| `internal/controlplane/console/templates/decision.html`（新建） | 解释器页模板 | 6 |
| `internal/controlplane/console/templates/_appnav.html` | 加「决策解释」tab 链接 | 6 |
| `internal/controlplane/console/routes_decision_test.go`（新建） | Console 测试（无会话 302 / 渲染表单 / 显示 ALLOW） | 6 |

**关键决策（已在源码核实）：**
- **复用 effperm/kernel 单一真相源**：与 M1.3 `Compute` 同一求值栈，无第二套决策逻辑（DX-1）。`Explain` 与 `Compute` 共用新抽出的 `buildEngine`（建引擎+灌快照），避免两份漂移。
- **`EnforceEx` 回源核实（v3.10.0，已核）**：`CachedEnforcer`/`SyncedCachedEnforcer` 只覆写 `Enforce`、**未覆写 `EnforceEx`** → `ce.EnforceEx` 落基类 `Enforcer.EnforceEx`，真实求值、返回判定规则 `[sub,dom,obj,act,eft]`、绕决策缓存。基类 EnforceEx 不走 synced 锁——故 `Engine.EnforceEx` 注释标注「仅供 effperm 瞬态（每请求新建、非共享）引擎；production 共享引擎不调用」。
- **reason 分类**：按 `(allowed, len(rule))` 分类——`len(rule)==0 → DENY_NO_MATCH`；`allowed → ALLOW_GRANTED`；否则 `DENY_OVERRIDDEN`。（不依赖解析 eft；与 `allowed` 直接一致。）`DecidingRule.Effect` 从 `rule[4]` 取作展示。
- **鉴权同 M1.3**：`{"effective_permission","read",false,scopeApp}`——同读能力、同 scope、同受众；matcher/ruleTable 既有条目零触碰。
- **Console 独立互补页**：新 `/apps/{app_id}/decision`（appnav 加 tab），与 M1.3 `/apps/{app_id}/effective` 互补；不并入既有页。

---

## 任务 1：Proto 契约（ExplainDecision）

**文件：**
- 修改：`api/proto/sydom/admin/v1/admin.proto`（service 块；message 区）
- 生成：`gen/sydom/admin/v1/*.pb.go`

- [ ] **步骤 1：在 service AdminService 块加 RPC**

在 `rpc GetEffectivePermissions(GetEffectivePermissionsRequest) returns (GetEffectivePermissionsResponse);` 之后插入：

```proto
  // —— M2.2 决策可解释性 ——
  rpc ExplainDecision(ExplainDecisionRequest) returns (ExplainDecisionResponse);
```

- [ ] **步骤 2：在 message 区加 message（紧接 `GetEffectivePermissionsResponse`/`DataPolicyPreview` 之后）**

```proto
// —— M2.2 决策可解释性 ——
message ExplainDecisionRequest {
  uint64 app_id   = 1;
  string user_id  = 2;
  string resource = 3;
  string action   = 4;
}
message DecidingRule {                 // 由 EnforceEx 的 [sub,dom,obj,act,eft] 解构；默认拒绝时为空
  string subject  = 1;                 // 携带判定的角色码（或 user 直授）
  string resource = 2;
  string action   = 3;
  string effect   = 4;                 // allow | deny
}
message DecisionDataScope {
  string match     = 1;                // all | none | conditional
  string predicate = 2;                // 仅 conditional 非空（符号，$user.xxx 保留）
}
message ExplainDecisionResponse {
  bool   allowed                = 1;
  string reason                 = 2;   // ALLOW_GRANTED | DENY_OVERRIDDEN | DENY_NO_MATCH
  DecidingRule deciding_rule    = 3;
  string deciding_role          = 4;
  repeated string roles         = 5;   // 用户有效角色(含继承, casbin 角色码)
  DecisionDataScope data_scope  = 6;
}
```

- [ ] **步骤 3：生成并验证无漂移**

运行：`make proto-gen && make proto-check`
预期：`proto-lint` PASS（buf.yaml 已 except 相关规则）；`git add gen/` 后 `proto-check` 的 `git diff --exit-code gen/` 退出 0。

- [ ] **步骤 4：确认全仓编译（Unimplemented 兜底）**

运行：`go build ./...`
预期：PASS（`AdminServer` 内嵌 `UnimplementedAdminServiceServer`，新 RPC 由默认实现兜底）。

- [ ] **步骤 5：Commit**

```bash
git add api/proto/sydom/admin/v1/admin.proto gen/
git commit -m "feat(proto): M2.2 ExplainDecision RPC 契约(决策可解释性)"
```

---

## 任务 2：kernel.Engine.EnforceEx（TDD）

**文件：**
- 修改：`internal/sidecar/kernel/engine.go`（新增 `EnforceEx`）
- 测试：`internal/sidecar/kernel/engine_test.go`（`package kernel`，追加；既有 `mgrSnapshot`/`New`/`ErrNotReady`/`ErrForeignDomain` 可直接用）

- [ ] **步骤 1：编写失败的测试（追加到 engine_test.go）**

```go
func TestEngine_EnforceEx_ReturnsDecidingRule(t *testing.T) {
	e, _ := New("dom1", nil, nil)
	require.NoError(t, e.ApplySnapshot(mgrSnapshot(3)))

	// allow：命中 manager 的 (order,read,allow) 规则；bool 与 Enforce 同输入一致。
	allow, rule, err := e.EnforceEx("alice", "dom1", "order", "read")
	require.NoError(t, err)
	require.True(t, allow)
	require.Equal(t, []string{"manager", "dom1", "order", "read", "allow"}, rule)

	plain, err := e.Enforce("alice", "dom1", "order", "read")
	require.NoError(t, err)
	require.Equal(t, plain, allow) // DX-2 引擎层 parity：EnforceEx.bool ≡ Enforce

	// 默认拒绝：无规则命中 → explain 空。
	allow2, rule2, err := e.EnforceEx("alice", "dom1", "order", "delete")
	require.NoError(t, err)
	require.False(t, allow2)
	require.Empty(t, rule2)
}

func TestEngine_EnforceEx_FailClose(t *testing.T) {
	e, _ := New("dom1", nil, nil)
	_, _, err := e.EnforceEx("alice", "dom1", "order", "read") // 未就绪
	require.ErrorIs(t, err, ErrNotReady)

	require.NoError(t, e.ApplySnapshot(mgrSnapshot(1)))
	_, _, err = e.EnforceEx("alice", "other", "order", "read") // 越域
	require.ErrorIs(t, err, ErrForeignDomain)
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/sidecar/kernel/ -run 'TestEngine_EnforceEx' -v`
预期：FAIL —— `e.EnforceEx undefined`。

- [ ] **步骤 3：实现 EnforceEx（engine.go，紧接 `Enforce` 之后）**

```go
// EnforceEx 判定 (sub,dom,obj,act) 并返回判定规则（explain，[]string=[sub,dom,obj,act,eft]；
// 默认拒绝时为空）。未就绪/越域/出错一律 fail-close。
//
// 仅供 effperm 瞬态（每请求新建、非共享）引擎调用：EnforceEx 落 casbin 基类、不走
// SyncedCachedEnforcer 的锁与决策缓存；production 共享 Sidecar 引擎不调用本方法。
func (e *Engine) EnforceEx(sub, dom, obj, act string) (bool, []string, error) {
	if !e.ready.Load() {
		return false, nil, ErrNotReady
	}
	if dom != e.domain {
		return false, nil, ErrForeignDomain
	}
	return e.ce.EnforceEx(sub, dom, obj, act)
}
```

- [ ] **步骤 4：运行测试验证通过**

运行：`go test ./internal/sidecar/kernel/ -run 'TestEngine_EnforceEx' -v`
预期：PASS（两个测试）。

- [ ] **步骤 5：Commit**

```bash
git add internal/sidecar/kernel/engine.go internal/sidecar/kernel/engine_test.go
git commit -m "feat(kernel): Engine.EnforceEx(取判定规则, 仅供瞬态引擎, fail-close 镜像 Enforce)"
```

---

## 任务 3：effperm.Explain + buildEngine 重构（TDD）

**文件：**
- 修改：`internal/controlplane/effperm/effperm.go`（抽 `buildEngine`、重构 `Compute` 调它、新增 `Explain` + 类型 + 常量）
- 测试：`internal/controlplane/effperm/effperm_test.go`（`package effperm_test`，追加；既有 `insertRule`/`insertDataPolicy`/`dbtest.SeedApp`/`dbtest.SeedDomain` 直接用）

> `effperm.go` 已 import `context`/`fmt`/`sort`/`cp`/`store`/`dataperm`/`kernel`，无需新增。

- [ ] **步骤 1：编写失败的测试（追加到 effperm_test.go）**

```go
func TestExplain_AllowGrantedViaInheritance(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	dom := dbtest.SeedDomain
	insertRule(t, db, appID, "p", "viewer", dom, "orders", "read", "allow")
	insertRule(t, db, appID, "g", "sales", "viewer", dom) // sales 继承 viewer
	insertRule(t, db, appID, "g", "alice", "sales", dom)  // alice→sales

	exp, err := effperm.Explain(context.Background(), db, appID, "alice", "orders", "read")
	require.NoError(t, err)
	require.True(t, exp.Allowed)
	require.Equal(t, effperm.ReasonAllowGranted, exp.Reason)
	require.NotNil(t, exp.DecidingRule)
	require.Equal(t, "viewer", exp.DecidingRole) // 携权角色 viewer（经 sales 继承）
	require.Equal(t, "allow", exp.DecidingRule.Effect)
	require.ElementsMatch(t, []string{"sales", "viewer"}, exp.Roles)
}

func TestExplain_DenyOverridden(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	dom := dbtest.SeedDomain
	insertRule(t, db, appID, "p", "sales", dom, "orders", "read", "allow")
	insertRule(t, db, appID, "p", "sales", dom, "orders", "read", "deny")
	insertRule(t, db, appID, "g", "alice", "sales", dom)

	exp, err := effperm.Explain(context.Background(), db, appID, "alice", "orders", "read")
	require.NoError(t, err)
	require.False(t, exp.Allowed)
	require.Equal(t, effperm.ReasonDenyOverridden, exp.Reason)
	require.NotNil(t, exp.DecidingRule)
	require.Equal(t, "deny", exp.DecidingRule.Effect)
}

func TestExplain_DenyNoMatch(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	dom := dbtest.SeedDomain
	insertRule(t, db, appID, "g", "alice", "sales", dom) // 有角色但无任何 grant

	exp, err := effperm.Explain(context.Background(), db, appID, "alice", "orders", "read")
	require.NoError(t, err)
	require.False(t, exp.Allowed)
	require.Equal(t, effperm.ReasonDenyNoMatch, exp.Reason)
	require.Nil(t, exp.DecidingRule)
	require.Equal(t, "", exp.DecidingRole)
	require.Contains(t, exp.Roles, "sales") // 仍列出用户角色（帮助排障）
}

func TestExplain_DataScopeSymbolic(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	dom := dbtest.SeedDomain
	insertRule(t, db, appID, "p", "sales", dom, "orders", "read", "allow")
	insertRule(t, db, appID, "g", "alice", "sales", dom)
	insertDataPolicy(t, db, appID, "role", "sales", "orders", "allow",
		`{"op":"EQ","field":"region","value":"$user.region"}`)

	exp, err := effperm.Explain(context.Background(), db, appID, "alice", "orders", "read")
	require.NoError(t, err)
	require.True(t, exp.Allowed)
	require.Equal(t, "conditional", exp.DataScope.Match)
	require.Contains(t, exp.DataScope.Predicate, "$user.region") // 符号谓词保留
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/controlplane/effperm/ -run 'TestExplain' -v`
预期：FAIL —— `effperm.Explain` / `effperm.Explanation` / `effperm.ReasonAllowGranted` 未定义。

- [ ] **步骤 3：抽 `buildEngine` + 重构 `Compute`**

把 `Compute` 函数体开头「读 domain → ReadAppRules → ReadAppDataPolicies → NewTable → kernel.New → ApplySnapshot」一段抽成共享函数。在 `toSnapshot` 之前加：

```go
// buildEngine 在只读 tx 内从 DB 物化策略、建瞬态引擎（Compute/Explain 共用，杜绝两份漂移）。
// 返回引擎、数据策略表、原始 rules/dps（供 Compute 枚举）、域。
func buildEngine(ctx context.Context, tx cp.DBTX, appID int64) (*kernel.Engine, *dataperm.Table, []cp.Rule, []cp.DataPolicy, string, error) {
	var domain string
	if err := tx.QueryRowContext(ctx,
		`SELECT domain FROM application WHERE id=$1`, appID).Scan(&domain); err != nil {
		return nil, nil, nil, nil, "", fmt.Errorf("effperm: read domain: %w", err)
	}
	rules, err := store.ReadAppRules(ctx, tx, appID)
	if err != nil {
		return nil, nil, nil, nil, "", fmt.Errorf("effperm: read rules: %w", err)
	}
	dps, err := store.ReadAppDataPolicies(ctx, tx, appID)
	if err != nil {
		return nil, nil, nil, nil, "", fmt.Errorf("effperm: read data policies: %w", err)
	}
	table := dataperm.NewTable()
	eng, err := kernel.New(domain, nil, table)
	if err != nil {
		return nil, nil, nil, nil, "", fmt.Errorf("effperm: new engine: %w", err)
	}
	if err := eng.ApplySnapshot(toSnapshot(rules, dps)); err != nil {
		return nil, nil, nil, nil, "", fmt.Errorf("effperm: apply snapshot: %w", err)
	}
	return eng, table, rules, dps, domain, nil
}
```

把 `Compute` 函数体的对应开头替换为调用（其余 roles/computePerms/computeViews 逻辑不变）：

```go
func Compute(ctx context.Context, tx cp.DBTX, appID int64, user string) (Result, error) {
	eng, table, rules, dps, domain, err := buildEngine(ctx, tx, appID)
	if err != nil {
		return Result{}, err
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
```

- [ ] **步骤 4：新增类型、常量、`Explain`（effperm.go 末尾）**

```go
// reason 分类（决策可解释性）。
const (
	ReasonAllowGranted   = "ALLOW_GRANTED"   // 命中 allow 授权
	ReasonDenyOverridden = "DENY_OVERRIDDEN" // 命中 deny 规则覆盖
	ReasonDenyNoMatch    = "DENY_NO_MATCH"   // 无任何规则命中（默认拒绝）
)

// DecidingRule 是判定的 casbin p 规则（解构自 EnforceEx 的 [sub,dom,obj,act,eft]）。
type DecidingRule struct {
	Subject  string
	Resource string
	Action   string
	Effect   string // allow | deny
}

// Explanation 是一次单决策 explain 结果。
type Explanation struct {
	Allowed      bool
	Reason       string
	DecidingRule *DecidingRule // 默认拒绝时为 nil
	DecidingRole string        // 默认拒绝时为 ""
	Roles        []string      // 用户有效角色(含继承)，排序稳定
	DataScope    DataView      // 该 resource 数据策略符号预览(复用 DataView)
}

// Explain 在只读 tx 内对单条 (appID, user, resource, action) 做瞬态求值并解释。
// 复用与 Compute 同一引擎栈（buildEngine），杜绝第二套决策逻辑。任一步失败一律返 error（fail-close），
// 绝不返回空 Explanation 冒充「拒绝」。
func Explain(ctx context.Context, tx cp.DBTX, appID int64, user, resource, action string) (Explanation, error) {
	eng, table, _, _, domain, err := buildEngine(ctx, tx, appID)
	if err != nil {
		return Explanation{}, err
	}

	roles, err := eng.GetImplicitRolesForUser(user, domain)
	if err != nil {
		return Explanation{}, fmt.Errorf("effperm: implicit roles: %w", err)
	}
	sort.Strings(roles)

	allowed, rule, err := eng.EnforceEx(user, domain, resource, action)
	if err != nil {
		return Explanation{}, fmt.Errorf("effperm: enforce ex: %w", err)
	}

	exp := Explanation{Allowed: allowed, Roles: roles}
	if len(rule) == 0 {
		exp.Reason = ReasonDenyNoMatch // 无规则命中
	} else {
		// rule = [sub, dom, obj, act, eft]（Sydom p 行 5 段）。
		exp.DecidingRole = rule[0]
		dr := &DecidingRule{Subject: rule[0]}
		if len(rule) >= 5 {
			dr.Resource, dr.Action, dr.Effect = rule[2], rule[3], rule[4]
		}
		exp.DecidingRule = dr
		if allowed {
			exp.Reason = ReasonAllowGranted
		} else {
			exp.Reason = ReasonDenyOverridden
		}
	}

	sr, err := dataperm.NewFilter(eng, table).FilterSymbolic(user, domain, resource)
	if err != nil {
		return Explanation{}, fmt.Errorf("effperm: symbolic filter %q: %w", resource, err)
	}
	exp.DataScope = DataView{Resource: resource, Match: sr.Match, Predicate: sr.Predicate}
	return exp, nil
}
```

- [ ] **步骤 5：运行测试验证通过 + Compute 无回归**

运行：`go test ./internal/controlplane/effperm/ -v`
预期：新增 4 个 `TestExplain_*` PASS；既有 `TestCompute_*` 全绿（buildEngine 重构零行为变更）。

- [ ] **步骤 6：Commit**

```bash
git add internal/controlplane/effperm/effperm.go internal/controlplane/effperm/effperm_test.go
git commit -m "feat(effperm): Explain 单决策解释(复用 buildEngine+EnforceEx, 三 reason, 符号数据范围)"
```

---

## 任务 4：mgmt handler + ruleTable（TDD）

**文件：**
- 新建：`internal/controlplane/mgmt/decision.go`
- 修改：`internal/controlplane/mgmt/authz.go`（ruleTable +1）
- 测试：`internal/controlplane/mgmt/decision_test.go`（`package mgmt_test`）

- [ ] **步骤 1：编写失败的测试（新建 decision_test.go）**

```go
package mgmt_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// 直插一条 casbin_rule（p: v0=sub v1=dom v2=obj v3=act v4=eft；g: v0=child v1=parent v2=dom）。
func insertCasbinRuleM(t *testing.T, db *sql.DB, appID int64, ptype string, v ...string) {
	t.Helper()
	var c [6]string
	copy(c[:], v)
	_, err := db.Exec(
		`INSERT INTO casbin_rule (app_id, ptype, v0, v1, v2, v3, v4, v5, version)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,1)`,
		appID, ptype, c[0], c[1], c[2], c[3], c[4], c[5])
	require.NoError(t, err)
}

func TestExplainDecision_ThreeReasons(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	dom := dbtest.SeedDomain
	insertCasbinRuleM(t, db, appID, "p", "manager", dom, "orders", "read", "allow")
	insertCasbinRuleM(t, db, appID, "g", "alice", "manager", dom)
	require.NoError(t, adminauthz.EnsureRootOperator(context.Background(), db, mk(), "root", []byte("root-secret")))
	root := dialMgmt(t, db, "root", []byte("root-secret"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// allow
	r1, err := root.ExplainDecision(ctx, &adminv1.ExplainDecisionRequest{
		AppId: uint64(appID), UserId: "alice", Resource: "orders", Action: "read"})
	require.NoError(t, err)
	require.True(t, r1.Allowed)
	require.Equal(t, "ALLOW_GRANTED", r1.Reason)
	require.NotNil(t, r1.DecidingRule)
	require.Equal(t, "manager", r1.DecidingRole)

	// default-deny（无 grant 命中 delete）
	r2, err := root.ExplainDecision(ctx, &adminv1.ExplainDecisionRequest{
		AppId: uint64(appID), UserId: "alice", Resource: "orders", Action: "delete"})
	require.NoError(t, err)
	require.False(t, r2.Allowed)
	require.Equal(t, "DENY_NO_MATCH", r2.Reason)
	require.Nil(t, r2.DecidingRule)
	require.Contains(t, r2.Roles, "manager") // 仍列角色

	// user_id 空 → InvalidArgument
	_, err = root.ExplainDecision(ctx, &adminv1.ExplainDecisionRequest{
		AppId: uint64(appID), Resource: "orders", Action: "read"})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// 跨租户：非超管操作员对他人 app 的 app 域无授权 → 403。
func TestExplainDecision_CrossTenantDenied(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	require.NoError(t, adminauthz.EnsureRootOperator(context.Background(), db, mk(), "root", []byte("root-secret")))
	root := dialMgmt(t, db, "root", []byte("root-secret"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	op, err := root.CreateOperator(ctx, &adminv1.CreateOperatorRequest{Principal: "mallory"})
	require.NoError(t, err)
	mallory := dialMgmt(t, db, "mallory", []byte(op.Secret))
	_, err = mallory.ExplainDecision(ctx, &adminv1.ExplainDecisionRequest{
		AppId: uint64(appID), UserId: "alice", Resource: "orders", Action: "read"})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/controlplane/mgmt/ -run 'TestExplainDecision' -v`
预期：FAIL —— `root.ExplainDecision` 未定义 / 路由未在 ruleTable（unknown method）。

- [ ] **步骤 3：ruleTable 加 1 条（authz.go）**

在 `ruleTable` 中 `GetEffectivePermissions` 那条之后插入：

```go
	"/sydom.admin.v1.AdminService/ExplainDecision":         {"effective_permission", "read", false, scopeApp},
```

- [ ] **步骤 4：实现 handler（新建 decision.go）**

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

// ExplainDecision 解释单条数据面授权决策「为什么 allow/deny」。app 域只读。
// 鉴权由 AuthorizeRule(scopeApp) 前置完成；本 handler 只在只读 tx 内瞬态求值（复用 effperm，与 Sidecar 同源）。
func (s *AdminServer) ExplainDecision(ctx context.Context, r *adminv1.ExplainDecisionRequest) (*adminv1.ExplainDecisionResponse, error) {
	if r.UserId == "" {
		return nil, status.Error(codes.InvalidArgument, "user_id required")
	}
	if r.Resource == "" || r.Action == "" {
		return nil, status.Error(codes.InvalidArgument, "resource and action required")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin read tx: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck

	exp, err := effperm.Explain(ctx, tx, int64(r.AppId), r.UserId, r.Resource, r.Action)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "explain decision: %v", err)
	}
	out := &adminv1.ExplainDecisionResponse{
		Allowed:      exp.Allowed,
		Reason:       exp.Reason,
		DecidingRole: exp.DecidingRole,
		Roles:        exp.Roles,
		DataScope:    &adminv1.DecisionDataScope{Match: exp.DataScope.Match, Predicate: exp.DataScope.Predicate},
	}
	if exp.DecidingRule != nil {
		out.DecidingRule = &adminv1.DecidingRule{
			Subject:  exp.DecidingRule.Subject,
			Resource: exp.DecidingRule.Resource,
			Action:   exp.DecidingRule.Action,
			Effect:   exp.DecidingRule.Effect,
		}
	}
	return out, nil
}
```

- [ ] **步骤 5：运行测试验证通过**

运行：`go test ./internal/controlplane/mgmt/ -run 'TestExplainDecision' -v`
预期：PASS（两个测试）。

- [ ] **步骤 6：Commit**

```bash
git add internal/controlplane/mgmt/decision.go internal/controlplane/mgmt/authz.go internal/controlplane/mgmt/decision_test.go
git commit -m "feat(mgmt): ExplainDecision handler + ruleTable(scopeApp read, 复用 effperm.Explain)"
```

---

## 任务 5：REST 路由（TDD）

**文件：**
- 修改：`internal/controlplane/restgw/routes.go`（appRoutes +1 + 计数注释）
- 测试：`internal/controlplane/restgw/routes_decision_test.go`（`package restgw_test`）

- [ ] **步骤 1：编写失败的测试（新建 routes_decision_test.go）**

```go
package restgw_test

import (
	"net/http"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestREST_ExplainDecision_OK(t *testing.T) {
	ts, db := newTestGW(t)
	c := rootClient(t, ts.URL)
	appID := uint64(dbtest.SeedApp(t, db))
	// 直插 grant：manager 可 read orders；alice→manager。
	dom := dbtest.SeedDomain
	_, err := db.Exec(`INSERT INTO casbin_rule (app_id,ptype,v0,v1,v2,v3,v4,v5,version) VALUES ($1,'p','manager',$2,'orders','read','allow','',1)`, int64(appID), dom)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO casbin_rule (app_id,ptype,v0,v1,v2,v3,v4,v5,version) VALUES ($1,'g','alice','manager',$2,'','','',1)`, int64(appID), dom)
	require.NoError(t, err)

	resp, body := c.do("GET", "/v1/apps/"+u(appID)+"/decision?user_id=alice&resource=orders&action=read", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var out adminv1.ExplainDecisionResponse
	require.NoError(t, protoUnmarshal(body, &out))
	require.True(t, out.Allowed)
	require.Equal(t, "ALLOW_GRANTED", out.Reason)
	require.Equal(t, "manager", out.DecidingRole)
}

// app_id 取自 path（路径权威）；user_id 空 → 400 InvalidArgument。
func TestREST_ExplainDecision_MissingUser_400(t *testing.T) {
	ts, db := newTestGW(t)
	c := rootClient(t, ts.URL)
	appID := uint64(dbtest.SeedApp(t, db))
	resp, body := c.do("GET", "/v1/apps/"+u(appID)+"/decision?resource=orders&action=read", nil)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode, string(body))
}

// 受 HMAC 保护：无凭据 → 401。
func TestREST_ExplainDecision_NoAuth(t *testing.T) {
	ts, db := newTestGW(t)
	appID := uint64(dbtest.SeedApp(t, db))
	resp, err := http.Get(ts.URL + "/v1/apps/" + u(appID) + "/decision?user_id=alice&resource=orders&action=read")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/controlplane/restgw/ -run 'TestREST_ExplainDecision' -v`
预期：FAIL（路由未注册 → 404/405）。

- [ ] **步骤 3：在 appRoutes() 末尾加 1 路由**

在 `appRoutes()` 返回切片最后一个路由（`DeleteDataPolicy`）之后插入：

```go
		{"GET", "/v1/apps/{app_id}/decision", pfx + "ExplainDecision",
			func(r *http.Request, _ []byte) (proto.Message, error) {
				id, err := pathUint64(r, "app_id")
				if err != nil {
					return nil, err
				}
				return &adminv1.ExplainDecisionRequest{ // app_id path 权威；其余取 query
					AppId:    id,
					UserId:   r.URL.Query().Get("user_id"),
					Resource: r.URL.Query().Get("resource"),
					Action:   r.URL.Query().Get("action"),
				}, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.ExplainDecision(ctx, m.(*adminv1.ExplainDecisionRequest))
			}},
```

- [ ] **步骤 4：更新路由计数注释**

`appRoutes` 函数注释 `app 域 20 路由` → `21 路由`；`allRoutes` 注释 `全部 38 路由（app 域 20 + ...）` → `全部 39 路由（app 域 21 + 应用管理 4 + system 域 10 + 账户层 4）`。

- [ ] **步骤 5：运行测试验证通过**

运行：`go test ./internal/controlplane/restgw/ -run 'TestREST_ExplainDecision' -v`
预期：PASS（三个测试）。

- [ ] **步骤 6：Commit**

```bash
git add internal/controlplane/restgw/routes.go internal/controlplane/restgw/routes_decision_test.go
git commit -m "feat(restgw): M2.2 REST GET /v1/apps/{app_id}/decision(ExplainDecision parity, path 权威)"
```

---

## 任务 6：Console 决策解释器页（TDD）

**文件：**
- 新建：`internal/controlplane/console/routes_decision.go`
- 修改：`internal/controlplane/console/routes_rbac.go`（注册路由）
- 新建：`internal/controlplane/console/templates/decision.html`
- 修改：`internal/controlplane/console/templates/_appnav.html`（加 tab）
- 测试：`internal/controlplane/console/routes_decision_test.go`（`package console`）

- [ ] **步骤 1：编写失败的测试（新建 routes_decision_test.go）**

```go
package console

import (
	"database/sql"
	"net/http"
	"strconv"
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func insertCasbinRuleC(t *testing.T, db *sql.DB, appID int64, ptype string, v ...string) {
	t.Helper()
	var c [6]string
	copy(c[:], v)
	_, err := db.Exec(
		`INSERT INTO casbin_rule (app_id, ptype, v0, v1, v2, v3, v4, v5, version)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,1)`,
		appID, ptype, c[0], c[1], c[2], c[3], c[4], c[5])
	require.NoError(t, err)
}

// 无会话 → 302/303 去登录。
func TestConsole_Decision_NoSession_Redirects(t *testing.T) {
	ts, _, _ := newConsole(t)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(ts.URL + "/apps/1/decision")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
}

// 有会话、无查询参数 → 200 渲染表单。
func TestConsole_Decision_RendersForm(t *testing.T) {
	ts, store, _ := newConsole(t)
	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	resp, err := c.Get(ts.URL + "/apps/1/decision")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, readBody(t, resp), "决策解释器")
}

// 有会话 + 有效查询 → 200 显示 ALLOW + reason。
func TestConsole_Decision_ShowsAllow(t *testing.T) {
	ts, store, db := newConsole(t)
	appID := dbtest.SeedApp(t, db)
	dom := dbtest.SeedDomain
	insertCasbinRuleC(t, db, appID, "p", "manager", dom, "orders", "read", "allow")
	insertCasbinRuleC(t, db, appID, "g", "alice", "manager", dom)
	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")
	resp, err := c.Get(ts.URL + "/apps/" + strconv.FormatInt(appID, 10) + "/decision?user_id=alice&resource=orders&action=read")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body := readBody(t, resp)
	require.Contains(t, body, "ALLOW")
	require.Contains(t, body, "ALLOW_GRANTED")
}
```

- [ ] **步骤 2：运行测试验证失败**

运行：`go test ./internal/controlplane/console/ -run 'TestConsole_Decision' -v`
预期：FAIL（路由未注册 → 404）。

- [ ] **步骤 3：注册路由（routes_rbac.go）**

在 `registerRBAC` 中 `mux.HandleFunc("GET /apps/{app_id}/effective", h.effectivePermissions)` 之后加：

```go
	mux.HandleFunc("GET /apps/{app_id}/decision", h.decisionExplainer)
```

- [ ] **步骤 4：实现 handler（新建 routes_decision.go）**

```go
package console

import (
	"net/http"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
)

// decisionExplainer：决策解释器页（读）。
// GET ?user_id=&resource=&action= → 三者齐备时调 ExplainDecision 渲染判定链；否则只渲染表单。
// 鉴权：ExplainDecision（scopeApp read）；拒绝走 renderGRPCError（降级无枚举、不泄露存在性）。
func (h *Handler) decisionExplainer(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	appID, err := pathUint64(r, "app_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"ExplainDecision", err)
		return
	}
	userID := r.FormValue("user_id")
	resource := r.FormValue("resource")
	action := r.FormValue("action")
	data := map[string]any{
		"Nav": "apps", "AppID": appID, "Tab": "decision",
		"UserID": userID, "Resource": resource, "Action": action, "CSRF": sess.CSRF,
	}
	if userID != "" && resource != "" && action != "" {
		msg := &adminv1.ExplainDecisionRequest{AppId: appID, UserId: userID, Resource: resource, Action: action}
		ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"ExplainDecision", principal, msg)
		if err != nil {
			h.renderGRPCError(w, r, svc+"ExplainDecision", err)
			return
		}
		resp, err := h.srv.ExplainDecision(ctx, msg)
		if err != nil {
			h.renderGRPCError(w, r, svc+"ExplainDecision", err)
			return
		}
		data["Queried"] = true
		data["Allowed"] = resp.Allowed
		data["Reason"] = resp.Reason
		data["DecidingRule"] = resp.DecidingRule
		data["EffRoles"] = resp.Roles
		data["DataScope"] = resp.DataScope
	}
	h.renderPage(w, r, "decision.html", http.StatusOK, data)
}
```

- [ ] **步骤 5：新建模板 decision.html**

```html
{{define "title"}}决策解释 · App {{.AppID}}{{end}}
{{define "content"}}<div class="workspace">{{template "appnav" .}}
<section><h2>决策解释器</h2>
<p class="hint">输入 user / resource / action，解释这条数据面授权决策为何 allow / deny。</p>
<form method="get" action="/apps/{{.AppID}}/decision" class="inline-form">
<input name="user_id" placeholder="user_id" value="{{.UserID}}">
<input name="resource" placeholder="resource" value="{{.Resource}}">
<input name="action" placeholder="action" value="{{.Action}}">
<button>解释</button></form>

{{if .Queried}}
<h3>判定结果</h3>
<p>{{if .Allowed}}<strong class="allow">ALLOW</strong>{{else}}<strong class="deny">DENY</strong>{{end}}
 · 原因：<code>{{.Reason}}</code></p>

{{if .DecidingRule}}
<h3>判定规则</h3>
<table><thead><tr><th>Subject(角色)</th><th>Resource</th><th>Action</th><th>Effect</th></tr></thead>
<tbody><tr><td>{{.DecidingRule.Subject}}</td><td>{{.DecidingRule.Resource}}</td>
<td>{{.DecidingRule.Action}}</td><td>{{.DecidingRule.Effect}}</td></tr></tbody></table>
{{else}}<p>无任何授权规则命中（默认拒绝）。</p>{{end}}

<h3>用户有效角色（含继承）</h3>
{{if .EffRoles}}<ul>{{range .EffRoles}}<li>{{.}}</li>{{end}}</ul>{{else}}<p>无角色。</p>{{end}}

<h3>数据范围（符号谓词）</h3>
<p>Match：<code>{{.DataScope.Match}}</code>{{if .DataScope.Predicate}} · 谓词：<code>{{.DataScope.Predicate}}</code>{{end}}</p>
{{end}}
</section></div>{{end}}
```

- [ ] **步骤 6：appnav 加 tab（_appnav.html）**

在「有效权限」`<a>` 那行之后插入：

```html
<a href="/apps/{{.AppID}}/decision" {{if eq .Tab "decision"}}class="active"{{end}}>决策解释</a>
```

- [ ] **步骤 7：运行测试验证通过**

运行：`go test ./internal/controlplane/console/ -run 'TestConsole_Decision' -v`
预期：PASS（三个测试）。

- [ ] **步骤 8：Commit**

```bash
git add internal/controlplane/console/
git commit -m "feat(console): M2.2 决策解释器页(/apps/{id}/decision + appnav tab, 复用 ExplainDecision)"
```

---

## 任务 7：全量验证 + 整体安全评审（DX-1..DX-6）

**文件：** 无新增（仅验证）

- [ ] **步骤 1：格式 + 静态检查 + proto 漂移**

```bash
gofmt -l internal/ api/ && echo "gofmt clean"
go vet ./...
make proto-check
```
预期：`gofmt -l` 无输出；`go vet` 干净；`proto-check` 的 `git diff --exit-code gen/` 为空。

- [ ] **步骤 2：全仓测试**

运行：`go test ./...`
预期：0 FAIL（含 kernel / effperm / mgmt / restgw / console / e2e）。

- [ ] **步骤 3：DX-6 matcher/租户隔离零触碰核验**

运行：`git diff <M2.2 起点 SHA>..HEAD -- internal/controlplane/adminauthz/ | wc -l`
（起点 = 任务 1 commit 的父提交。）
预期：`0`（adminauthz/matcher 一字未改）。再人工确认 `authz.go` 仅新增 1 条 ruleTable、`kernel/engine.go` 仅新增 `EnforceEx`（不碰 `Enforce`/`ApplySnapshot`）。

- [ ] **步骤 4：逐条核验 DX-1..DX-6**

对照 spec §5：DX-1 单一真相源(effperm/kernel,无第二套逻辑)；DX-2 EnforceEx.bool≡Enforce(任务2测试)+Explain.allowed 忠实；DX-3 Sidecar 同源(DB=快照源,kernel 零漂移);DX-4 符号口径(数据范围符号、功能决策真实);DX-5 fail-close(任一步 error 不空响应);DX-6 adminauthz diff=0 + scopeApp read + secret 不沾(explain 不碰凭据)。

- [ ] **步骤 5：整体安全评审（opus）**

调用一次 opus 整体安全评审（项目里程碑范式：末尾单次综合评审），聚焦：与真实 Enforce 一致(不分叉)、符号口径不冒充对真实数据行的判定、fail-close、scopeApp 租户隔离不旁路、explain 不泄露 secret/不泄露跨租户存在性、EnforceEx 仅瞬态引擎用（不引入 production 并发风险）。修 Blocker/Major。

- [ ] **步骤 6：Commit（若有 fmt/评审修整）**

```bash
git add -A
git commit -m "chore(m2.2): 全量验证 + 整体安全评审收尾(DX-1..DX-6 PASS)"
```

---

## 自检（writing-plans）

**1. 规格覆盖度**（对照 spec 各节）：
- §3 RPC 契约 → 任务 1 ✓
- §4.1 effperm.Explain + buildEngine → 任务 3 ✓
- §4.2 kernel.EnforceEx → 任务 2 ✓
- §4.3 ruleTable + §4.4 handler → 任务 4 ✓
- §4.5 REST → 任务 5；Console → 任务 6 ✓
- §5 DX-1..DX-6 → 任务 7 步骤 3/4/5 逐条映射 ✓
- §7 测试策略（三 reason / 继承 / 数据范围符号 / parity / 跨租户 403 / 三面）→ 分布于任务 2-6 ✓

**2. 占位符扫描**：所有生产代码（proto、EnforceEx、Explain、handler、route、template）均完整可粘贴；测试均为完整可运行代码。无 TODO/占位。

**3. 类型一致性**：`effperm.Explanation{Allowed,Reason,DecidingRule,DecidingRole,Roles,DataScope}` / `effperm.DecidingRule{Subject,Resource,Action,Effect}` / `ReasonAllowGranted|DenyOverridden|DenyNoMatch` 在任务 3 定义、任务 4 handler 消费一致；proto `adminv1.DecidingRule`/`DecisionDataScope`/`ExplainDecisionResponse` 字段（任务 1）与 handler 映射（任务 4）一致；`Engine.EnforceEx`（任务 2）签名与 `effperm.Explain` 调用（任务 3）一致；`buildEngine` 6 返回值在 Compute/Explain 用法一致。`DataView` 复用既有 effperm 类型（带 Resource/Match/Predicate）。

---

## 执行交接

**计划已完成并保存到 `docs/superpowers/plans/2026-06-17-sydom-m2-2-decision-explainability.md`。两种执行方式：**

**1. 子代理驱动（推荐）** — 每任务一个新子代理，控制者逐任务独立验证（git show + 重跑测试 + DX-6 diff），末尾单次 opus 整体安全评审（延续 M1.x/M2.1 范式）。

**2. 内联执行** — 当前会话用 executing-plans 批量执行，设检查点。

**选哪种方式？**
