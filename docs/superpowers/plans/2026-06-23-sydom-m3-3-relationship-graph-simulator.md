# M3.3 角色全景 + 决策模拟器 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 让运营者一屏看清一个角色的全貌（绑定用户/能力/继承/数据范围），并模拟「假如把某用户绑到本角色 / 假如给本角色加某能力」对有效权限的影响（RBAC + 数据范围符号 diff），不落库。

**架构：** 2 个新只读 RPC——`GetRoleGraph`（结构聚合，直读关系表）与 `SimulateRoleChange`（反事实求值，复用 `effperm.buildEngine` 同求值栈，往内存 rules 快照注入一条合成规则后重建瞬态引擎、双向 diff）。三面 parity（gRPC + REST + Console），`scopeApp`、只读、复用唯一 `AuthorizeRule`+ruleTable（+2）。M1.1 matcher / enforcer.go / adminauthz / sidecar 一字不碰。

**技术栈：** Go、PostgreSQL、protobuf(buf)、testcontainers PG、html/template、casbin v3.10.0（经 effperm 间接）。

**spec：** `docs/superpowers/specs/2026-06-23-sydom-m3-3-relationship-graph-simulator-design.md`（commit `4b18dec`）。**BASE sha**=本计划 commit。

---

## 文件结构

| 文件 | 职责 | 任务 |
|---|---|---|
| `api/proto/sydom/admin/v1/admin.proto` + `gen/**` | 2 RPC + 6 message | 1 |
| `internal/controlplane/effperm/effperm.go` | 抽 `readAppPolicy`/`buildEngineFrom`/`computeResult`；新增 `Change`/`SubjectDiff`/`Simulate` | 2 |
| `internal/controlplane/effperm/simulate_test.go` | Simulate 单测（diff/deny 反转/零副作用） | 2 |
| `internal/controlplane/mgmt/role_graph.go` | `GetRoleGraph` + `SimulateRoleChange` handler + `roleAncestors` 助手 | 3,4 |
| `internal/controlplane/mgmt/role_graph_test.go` | 两 handler 单测 | 3,4 |
| `internal/controlplane/mgmt/authz.go` | ruleTable +2 | 3,4 |
| `internal/controlplane/restgw/routes_role_graph.go` + `_test.go` | REST 2 路由 | 5 |
| `internal/controlplane/restgw/routes.go` | allRoutes 注册 + 计数注释 | 5 |
| `internal/controlplane/console/routes_role_graph.go` + `_test.go` | 角色全景页 + 模拟 diff 页 | 6 |
| `internal/controlplane/console/templates/ops_role_graph.html` / `ops_role_simulate.html` | 两模板（//go:embed 自动发现） | 6 |
| `internal/controlplane/console/templates/ops_roles.html` | 每行加「全景」入口 | 6 |
| `internal/controlplane/console/handler.go` | `registerRoleGraph` 注册一行 | 6 |

---

## 任务 1：proto 2 RPC + message + regen

**文件：**
- 修改：`api/proto/sydom/admin/v1/admin.proto`
- 生成：`gen/sydom/admin/v1/admin.pb.go`、`admin_grpc.pb.go`

- [ ] **步骤 1：在 `service AdminService { … }` 块内加 2 RPC（紧随既有 ExplainDecision rpc 行后）**

```proto
  rpc GetRoleGraph(GetRoleGraphRequest) returns (GetRoleGraphResponse);
  rpc SimulateRoleChange(SimulateRoleChangeRequest) returns (SimulateRoleChangeResponse);
```

- [ ] **步骤 2：在文件 message 区末尾（最后一个 message 之后）追加 6 个 message**

```proto
// —— M3.3 角色全景 + 决策模拟器 ——
message GetRoleGraphRequest { uint64 app_id = 1; int64 role_id = 2; }

message RoleGraphCapability {
  string resource = 1;
  string action   = 2;
  string name     = 3;   // 权限点原始 name（Console 经 bizterm 渲染业务名）
  string source   = 4;   // "direct" 或贡献该能力的父角色显示名
}
message RoleGraphParent { int64 id = 1; string code = 2; string name = 3; }
message RoleGraphDataScope {
  string resource  = 1;
  string effect    = 2;   // allow | deny
  string condition = 3;   // 原始条件树 JSON（Console 经 conditionPredicate 渲符号谓词）
}
message GetRoleGraphResponse {
  int64  role_id   = 1;
  string role_code = 2;
  string role_name = 3;
  repeated string bound_users               = 4;
  repeated RoleGraphCapability capabilities = 5;
  repeated RoleGraphParent parents          = 6;
  repeated RoleGraphDataScope data_scopes   = 7;
}

enum RoleChangeType { ROLE_CHANGE_UNSPECIFIED = 0; BIND_USER = 1; ADD_CAPABILITY = 2; }

message SimulateRoleChangeRequest {
  uint64 app_id  = 1;
  int64  role_id = 2;
  RoleChangeType change_type = 3;
  string user_id  = 4;   // BIND_USER
  string resource = 5;   // ADD_CAPABILITY
  string action   = 6;   // ADD_CAPABILITY
}
message SubjectDiff {
  string user_id = 1;
  repeated EffectivePermission added_permissions   = 2;   // 复用既有 message
  repeated EffectivePermission removed_permissions = 3;
  repeated DataPolicyPreview   added_data_previews   = 4; // 复用既有 message（符号 predicate）
  repeated DataPolicyPreview   removed_data_previews = 5;
}
message SimulateRoleChangeResponse { repeated SubjectDiff subjects = 1; }
```

> `EffectivePermission{resource,action}` 与 `DataPolicyPreview{resource,match,predicate}` 是既有 message（M1.3），直接复用。

- [ ] **步骤 3：regen + 校验**

运行：`make proto-gen && make proto-check`
预期：buf lint 通过、`git diff --exit-code gen/` 在 regen 后无意外漂移（仅本次新增）。若 buf lint 抱怨 enum 值未加前缀，沿用既有放行（既有 enum 已有先例则照搬；如需 `buf.yaml` except 则比照既有条目添加）。

- [ ] **步骤 4：构建**

运行：`go build ./...`
预期：通过（gen 新类型可用）。

- [ ] **步骤 5：Commit**

```bash
git add api/proto/sydom/admin/v1/admin.proto gen/ buf.yaml 2>/dev/null
git commit -m "feat(proto): M3.3 GetRoleGraph + SimulateRoleChange 2 RPC + 6 message(复用 EffectivePermission/DataPolicyPreview)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 2：effperm.Simulate（反事实求值核心）

**文件：**
- 修改：`internal/controlplane/effperm/effperm.go`（抽 3 个内部助手 + 新增 Change/SubjectDiff/Simulate）
- 测试：`internal/controlplane/effperm/simulate_test.go`

**重构铁律：** `buildEngine`、`Compute` 的对外行为/签名**不变**；只抽出可复用内部件，使 Simulate 与它们共用同一求值栈（杜绝第二套决策逻辑，RG-3）。

- [ ] **步骤 1：先写失败测试 `simulate_test.go`**

参照既有 `effperm_test.go` 的播种方式（`dbtest.SetupSchema` + `store.UpsertPermission`/`InsertRole`/`InsertRolePermission`/`BindUserRole` 或直接 casbin_rule 播种）。核对既有 `effperm_test.go` 的 helper 后照搬。

```go
package effperm_test

import (
	"context"
	"testing"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/effperm"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// 播种：app(domain=dom) + 角色 viewer 授 order:read(allow) + 角色 viewer 的 order 数据范围($user.tenant_id)。
// 返回 appID, viewer 角色 code。
func seedRoleForSim(t *testing.T, db *... , domain string) (int64, string) {
	// 见 effperm_test.go 既有播种范式；data_policy subject_id = role.code。
	// 用 dbtest.SeedAppInTenant 拿 appID + 把 application.domain 设为 domain（或用 dbtest.SeedApp 取其默认 domain）。
	// permID := store.UpsertPermission(ctx, db, appID, "order:read","order","read","app","查看订单")
	// roleID := store.InsertRole(ctx, db, appID, "viewer","查看员"); InsertRolePermission(... allow)
	// store.UpsertDataPolicy(ctx, db, appID, cp.DataPolicy{SubjectType:"role",SubjectID:"viewer",Resource:"order",Condition:`{"field":"tenant_id","op":"EQ","value":"$user.tenant_id"}`,Effect:"allow"}, 1)
	return 0, "viewer"
}

func TestSimulate_BindUser_GainsRolePerms(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	appID, _ := seedRoleForSim(t, db, dbtest.SeedDomain)

	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback() //nolint:errcheck

	diffs, err := effperm.Simulate(ctx, tx, appID, "viewer",
		effperm.Change{Type: "bind_user", UserID: "u-1"})
	require.NoError(t, err)
	require.Len(t, diffs, 1)
	require.Equal(t, "u-1", diffs[0].UserID)
	// 绑定前 u-1 无权限，绑定后获得 order:read。
	require.Contains(t, permPairs(diffs[0].AddedPermissions), "order/read")
	require.Empty(t, diffs[0].RemovedPermissions)
	// 数据范围：u-1 获得 order 的符号谓词（含 $user.）。
	require.NotEmpty(t, diffs[0].AddedDataViews)
	require.Contains(t, diffs[0].AddedDataViews[0].Predicate, "$user.")
}

func TestSimulate_NoSideEffects(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	appID, _ := seedRoleForSim(t, db, dbtest.SeedDomain)

	var rowsBefore int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM casbin_rule WHERE app_id=$1`, appID).Scan(&rowsBefore))
	var verBefore int64
	require.NoError(t, db.QueryRow(`SELECT current_version FROM application WHERE id=$1`, appID).Scan(&verBefore))

	tx, _ := db.BeginTx(ctx, nil)
	_, err := effperm.Simulate(ctx, tx, appID, "viewer", effperm.Change{Type: "bind_user", UserID: "u-1"})
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

	var rowsAfter int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM casbin_rule WHERE app_id=$1`, appID).Scan(&rowsAfter))
	var verAfter int64
	require.NoError(t, db.QueryRow(`SELECT current_version FROM application WHERE id=$1`, appID).Scan(&verAfter))
	require.Equal(t, rowsBefore, rowsAfter, "模拟绝不写 casbin_rule")
	require.Equal(t, verBefore, verAfter, "模拟绝不 bump 版本")
}

// permPairs 把 []Perm 转 "resource/action" 串列表（测试辅助）。
func permPairs(ps []effperm.Perm) []string {
	var out []string
	for _, p := range ps { out = append(out, p.Resource+"/"+p.Action) }
	return out
}
```

加一个 **deny 反转有齿**用例：播种角色 R1 授 order:read(allow)、绑 u-2 到 R1；再播种角色 R2 对 order:read **deny**。模拟「把 u-2 绑到 R2」→ 断言 `RemovedPermissions` 含 `order/read`（deny 覆盖致失去）。

```go
func TestSimulate_BindUser_DenyOverrideRemoves(t *testing.T) {
	// 播种：u-2 经 R1 有 order:read(allow)；R2 对 order:read 持 deny 且 u-2 未绑 R2。
	// 模拟 bind_user(u-2 → R2) → 断言 RemovedPermissions 含 order/read（忠实 deny 反转，RG-4）。
	// 若实现把 diff 写成单调（只算 added）则此用例 FAIL —— 这是齿。
}
```

- [ ] **步骤 2：运行验证失败**

运行：`go test ./internal/controlplane/effperm/ -run TestSimulate -count=1`
预期：FAIL（`effperm.Simulate` / `effperm.Change` / `effperm.SubjectDiff` 未定义）。

- [ ] **步骤 3：重构既有 `effperm.go`——抽 3 个内部助手**

把 `buildEngine` 拆为「读 DB」+「建引擎」两段，并抽出 `Compute` 求值体：

```go
// readAppPolicy 在只读 tx 内读 app 的 domain + casbin rules + data policies（与 Sidecar 快照同源）。
func readAppPolicy(ctx context.Context, tx cp.DBTX, appID int64) (string, []cp.Rule, []cp.DataPolicy, error) {
	var domain string
	if err := tx.QueryRowContext(ctx, `SELECT domain FROM application WHERE id=$1`, appID).Scan(&domain); err != nil {
		return "", nil, nil, fmt.Errorf("effperm: read domain: %w", err)
	}
	rules, err := store.ReadAppRules(ctx, tx, appID)
	if err != nil {
		return "", nil, nil, fmt.Errorf("effperm: read rules: %w", err)
	}
	dps, err := store.ReadAppDataPolicies(ctx, tx, appID)
	if err != nil {
		return "", nil, nil, fmt.Errorf("effperm: read data policies: %w", err)
	}
	return domain, rules, dps, nil
}

// buildEngineFrom 从给定 domain+rules+dps 建瞬态引擎（不读 DB；baseline 与 hypothetical 共用）。
func buildEngineFrom(domain string, rules []cp.Rule, dps []cp.DataPolicy) (*kernel.Engine, *dataperm.Table, error) {
	table := dataperm.NewTable()
	eng, err := kernel.New(domain, nil, table)
	if err != nil {
		return nil, nil, fmt.Errorf("effperm: new engine: %w", err)
	}
	if err := eng.ApplySnapshot(toSnapshot(rules, dps)); err != nil {
		return nil, nil, fmt.Errorf("effperm: apply snapshot: %w", err)
	}
	return eng, table, nil
}

// computeResult 在已建引擎上算 (user) 的 roles+perms+views（Compute 与 Simulate 共用）。
func computeResult(eng *kernel.Engine, table *dataperm.Table, rules []cp.Rule, dps []cp.DataPolicy, domain, user string) (Result, error) {
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

把 `buildEngine` 改为复用上面两段（保持原签名与返回值不变）：

```go
func buildEngine(ctx context.Context, tx cp.DBTX, appID int64) (*kernel.Engine, *dataperm.Table, []cp.Rule, []cp.DataPolicy, string, error) {
	domain, rules, dps, err := readAppPolicy(ctx, tx, appID)
	if err != nil {
		return nil, nil, nil, nil, "", err
	}
	eng, table, err := buildEngineFrom(domain, rules, dps)
	if err != nil {
		return nil, nil, nil, nil, "", err
	}
	return eng, table, rules, dps, domain, nil
}
```

把 `Compute` 末段改为调 `computeResult`（替换原地 roles/perms/views 三段）：

```go
func Compute(ctx context.Context, tx cp.DBTX, appID int64, user string) (Result, error) {
	eng, table, rules, dps, domain, err := buildEngine(ctx, tx, appID)
	if err != nil {
		return Result{}, err
	}
	return computeResult(eng, table, rules, dps, domain, user)
}
```

- [ ] **步骤 4：新增 Change/SubjectDiff/Simulate + 4 个 diff 助手**

```go
// Change 是一次反事实假设变更。
type Change struct {
	Type     string // "bind_user" | "add_capability"
	UserID   string // bind_user
	Resource string // add_capability
	Action   string // add_capability
}

// SubjectDiff 是某 user 在假设变更前后有效权限的双向 diff。
type SubjectDiff struct {
	UserID             string
	AddedPermissions   []Perm
	RemovedPermissions []Perm
	AddedDataViews     []DataView
	RemovedDataViews   []DataView
}

func (d SubjectDiff) nonEmpty() bool {
	return len(d.AddedPermissions)+len(d.RemovedPermissions)+len(d.AddedDataViews)+len(d.RemovedDataViews) > 0
}

// Simulate 反事实预览：对 (appID, roleCode) 施加假设 change，返回受影响 user 的有效权限双向 diff。
// 复用 buildEngineFrom + computeResult 同求值栈（RG-3）；绝不写库/bump/广播（RG-2，纯瞬态）。
// 任一步失败 fail-close 返 error（RG-8）。
func Simulate(ctx context.Context, tx cp.DBTX, appID int64, roleCode string, change Change) ([]SubjectDiff, error) {
	domain, rules, dps, err := readAppPolicy(ctx, tx, appID)
	if err != nil {
		return nil, err
	}
	baseEng, baseTable, err := buildEngineFrom(domain, rules, dps)
	if err != nil {
		return nil, err
	}

	var synthetic cp.Rule
	var subjects []string
	switch change.Type {
	case "bind_user":
		if change.UserID == "" {
			return nil, fmt.Errorf("effperm: simulate: user_id required")
		}
		synthetic = cp.Rule{Ptype: "g", V: [6]string{change.UserID, roleCode, domain, "", "", ""}}
		subjects = []string{change.UserID}
	case "add_capability":
		if change.Resource == "" || change.Action == "" {
			return nil, fmt.Errorf("effperm: simulate: resource/action required")
		}
		synthetic = cp.Rule{Ptype: "p", V: [6]string{roleCode, domain, change.Resource, change.Action, "allow", ""}}
		subjects, err = usersWithRole(ctx, tx, baseEng, appID, domain, roleCode)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("effperm: simulate: unknown change type %q", change.Type)
	}

	hypoRules := make([]cp.Rule, len(rules), len(rules)+1)
	copy(hypoRules, rules)
	hypoRules = append(hypoRules, synthetic)
	hypoEng, hypoTable, err := buildEngineFrom(domain, hypoRules, dps)
	if err != nil {
		return nil, err
	}

	var out []SubjectDiff
	for _, u := range subjects {
		base, err := computeResult(baseEng, baseTable, rules, dps, domain, u)
		if err != nil {
			return nil, err
		}
		hypo, err := computeResult(hypoEng, hypoTable, hypoRules, dps, domain, u)
		if err != nil {
			return nil, err
		}
		d := SubjectDiff{
			UserID:             u,
			AddedPermissions:   permDiff(hypo.Permissions, base.Permissions),
			RemovedPermissions: permDiff(base.Permissions, hypo.Permissions),
			AddedDataViews:     viewDiff(hypo.DataViews, base.DataViews),
			RemovedDataViews:   viewDiff(base.DataViews, hypo.DataViews),
		}
		if d.nonEmpty() {
			out = append(out, d)
		}
	}
	return out, nil
}

// usersWithRole 返回本 app 中隐式角色闭包含 roleCode 的真实绑定用户（从 user_role_binding，排除 role→role 行）。
func usersWithRole(ctx context.Context, tx cp.DBTX, eng *kernel.Engine, appID int64, domain, roleCode string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT user_id FROM user_role_binding WHERE app_id=$1 ORDER BY user_id`, appID)
	if err != nil {
		return nil, fmt.Errorf("effperm: read binding users: %w", err)
	}
	defer rows.Close()
	var users []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []string
	for _, u := range users {
		roles, err := eng.GetImplicitRolesForUser(u, domain)
		if err != nil {
			return nil, fmt.Errorf("effperm: implicit roles %q: %w", u, err)
		}
		for _, rc := range roles {
			if rc == roleCode {
				out = append(out, u)
				break
			}
		}
	}
	return out, nil
}

// permDiff 返回 a − b（Perm 全字段可比，用作 map key）。
func permDiff(a, b []Perm) []Perm {
	set := make(map[Perm]bool, len(b))
	for _, p := range b {
		set[p] = true
	}
	var out []Perm
	for _, p := range a {
		if !set[p] {
			out = append(out, p)
		}
	}
	return out
}

// viewDiff 返回 a − b（DataView 全字段可比；predicate 变化体现为一删一增）。
func viewDiff(a, b []DataView) []DataView {
	set := make(map[DataView]bool, len(b))
	for _, v := range b {
		set[v] = true
	}
	var out []DataView
	for _, v := range a {
		if !set[v] {
			out = append(out, v)
		}
	}
	return out
}
```

- [ ] **步骤 5：运行测试验证通过**

运行：`go test ./internal/controlplane/effperm/ -count=1`
预期：PASS（含既有 Compute/Explain 测试不回归 + 新 Simulate 测试）。补跑齿验证：临时把 `RemovedPermissions` 改为 `nil` → `TestSimulate_BindUser_DenyOverrideRemoves` 应 FAIL，改回。

- [ ] **步骤 6：gofmt/vet/build**

运行：`gofmt -l internal/controlplane/effperm/ && go vet ./internal/controlplane/effperm/ && go build ./...`
预期：空 / 净 / 通过。

- [ ] **步骤 7：Commit**

```bash
git add internal/controlplane/effperm/
git commit -m "feat(effperm): Simulate 反事实求值(注入合成 g/p 规则重建瞬态引擎+双向 diff,复用 buildEngineFrom/computeResult 同栈,零副作用)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 3：mgmt GetRoleGraph handler + ruleTable

**文件：**
- 创建：`internal/controlplane/mgmt/role_graph.go`
- 创建：`internal/controlplane/mgmt/role_graph_test.go`
- 修改：`internal/controlplane/mgmt/authz.go`（ruleTable +1）

- [ ] **步骤 1：ruleTable 加 1 条（与既有 ListTemplates 等同组）**

在 `authz.go` 的 ruleTable map 内加：

```go
	"/sydom.admin.v1.AdminService/GetRoleGraph":           {"role", "read", false, scopeApp},
```

- [ ] **步骤 2：先写失败测试 `role_graph_test.go`**

```go
package mgmt_test

// 复用既有 mgmt_test helper：accountsSrv(db)、dbtest.SeedAppInTenant。
// 播种：app 内 role viewer 授 order:read；role admin 继承 viewer 且授 order:write；绑 alice→admin；
//       viewer 持 order 数据范围($user.tenant_id)。
func TestGetRoleGraph_AggregatesAndInheritance(t *testing.T) {
	db := dbtest.SetupSchema(t)
	tID, appID := dbtest.SeedAppInTenant(t, db, "t-a", "dom-a", "AK_a")
	_ = tID
	srv := accountsSrv(db)
	ctx := context.Background()

	viewerID := mustRole(t, db, appID, "viewer", "查看员")
	adminID := mustRole(t, db, appID, "admin", "管理员")
	permR := mustPerm(t, db, appID, "order:read", "order", "read", "查看订单")
	permW := mustPerm(t, db, appID, "order:write", "order", "write", "修改订单")
	mustGrant(t, db, appID, viewerID, permR)
	mustGrant(t, db, appID, adminID, permW)
	mustInherit(t, db, appID, adminID /*child*/, viewerID /*parent*/)
	mustBind(t, db, appID, "alice", adminID)
	mustDataPolicy(t, db, appID, "viewer", "order", `{"field":"tenant_id","op":"EQ","value":"$user.tenant_id"}`)

	resp, err := srv.GetRoleGraph(ctx, &adminv1.GetRoleGraphRequest{AppId: uint64(appID), RoleId: adminID})
	require.NoError(t, err)
	require.Equal(t, "admin", resp.RoleCode)
	require.Contains(t, userIDs(resp.BoundUsers), "alice")
	// 能力：直接 order/write(source=direct) + 继承 order/read(source=查看员)。
	require.Equal(t, "direct", capSource(resp.Capabilities, "order", "write"))
	require.Equal(t, "查看员", capSource(resp.Capabilities, "order", "read"))
	// 父角色 viewer。
	require.Len(t, resp.Parents, 1)
	require.Equal(t, "viewer", resp.Parents[0].Code)

	// 跨租户/未知 app role → NotFound（不泄露）。
	_, err = srv.GetRoleGraph(ctx, &adminv1.GetRoleGraphRequest{AppId: uint64(appID), RoleId: 999999})
	require.Equal(t, codes.NotFound, status.Code(err))
}
```

> `mustRole/mustPerm/mustGrant/mustInherit/mustBind/mustDataPolicy/userIDs/capSource` 为本测试文件内的小 helper，用 `store.InsertRole/UpsertPermission/InsertRolePermission/InsertRoleInheritance` + 直 INSERT user_role_binding + `store.UpsertDataPolicy` 实现（核对各 store 函数签名后写）。

- [ ] **步骤 3：运行验证失败** → FAIL（`GetRoleGraph` 未实现）。

- [ ] **步骤 4：实现 `role_graph.go` 的 GetRoleGraph + roleAncestors**

```go
package mgmt

import (
	"context"
	"database/sql"
	"errors"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/effperm"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetRoleGraph 聚合一个角色的全貌：绑定用户 + 能力(直接+继承,标来源) + 父角色 + 直接数据范围。
// 纯结构读（不求值）。跨租户/未知 → NotFound 不泄露（RG-6/RG-8）。
func (s *AdminServer) GetRoleGraph(ctx context.Context, r *adminv1.GetRoleGraphRequest) (*adminv1.GetRoleGraphResponse, error) {
	appID := int64(r.AppId)
	var code, name string
	err := s.db.QueryRowContext(ctx,
		`SELECT code, name FROM role WHERE id=$1 AND app_id=$2`, r.RoleId, appID).Scan(&code, &name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "role not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read role: %v", err)
	}
	out := &adminv1.GetRoleGraphResponse{RoleId: r.RoleId, RoleCode: code, RoleName: name}

	// 绑定用户。
	if out.BoundUsers, err = s.scanStrings(ctx,
		`SELECT user_id FROM user_role_binding WHERE role_id=$1 AND app_id=$2 ORDER BY user_id`, r.RoleId, appID); err != nil {
		return nil, status.Errorf(codes.Internal, "read bindings: %v", err)
	}

	// 父角色。
	prows, err := s.db.QueryContext(ctx,
		`SELECT r.id, r.code, r.name FROM role_inheritance ri JOIN role r ON r.id=ri.parent_role_id
		 WHERE ri.child_role_id=$1 AND ri.app_id=$2 ORDER BY r.code`, r.RoleId, appID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read parents: %v", err)
	}
	for prows.Next() {
		var p adminv1.RoleGraphParent
		if err := prows.Scan(&p.Id, &p.Code, &p.Name); err != nil {
			prows.Close()
			return nil, status.Errorf(codes.Internal, "scan parent: %v", err)
		}
		out.Parents = append(out.Parents, &p)
	}
	prows.Close()
	if err := prows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "parents: %v", err)
	}

	// 能力：直接(source=direct) + 祖先(nearest-first, source=祖先 name)。
	seen := map[[2]string]bool{}
	addCaps := func(roleID int64, source string) error {
		grows, err := s.db.QueryContext(ctx,
			`SELECT p.resource, p.action, p.name FROM role_permission rp JOIN permission p ON p.id=rp.permission_id
			 WHERE rp.role_id=$1 AND rp.app_id=$2 AND rp.eft='allow' ORDER BY p.resource, p.action`, roleID, appID)
		if err != nil {
			return err
		}
		defer grows.Close()
		for grows.Next() {
			var c adminv1.RoleGraphCapability
			if err := grows.Scan(&c.Resource, &c.Action, &c.Name); err != nil {
				return err
			}
			k := [2]string{c.Resource, c.Action}
			if seen[k] {
				continue
			}
			seen[k] = true
			c.Source = source
			out.Capabilities = append(out.Capabilities, &c)
		}
		return grows.Err()
	}
	if err := addCaps(r.RoleId, "direct"); err != nil {
		return nil, status.Errorf(codes.Internal, "read direct caps: %v", err)
	}
	ancestors, err := s.roleAncestors(ctx, appID, r.RoleId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read ancestors: %v", err)
	}
	for _, a := range ancestors {
		if err := addCaps(a.id, a.name); err != nil {
			return nil, status.Errorf(codes.Internal, "read inherited caps: %v", err)
		}
	}

	// 直接数据范围（原始 condition，Console 渲符号谓词）。
	drows, err := s.db.QueryContext(ctx,
		`SELECT resource, effect, condition::text FROM data_policy
		 WHERE subject_type='role' AND subject_id=$1 AND app_id=$2 ORDER BY resource`, code, appID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read data scopes: %v", err)
	}
	for drows.Next() {
		var d adminv1.RoleGraphDataScope
		if err := drows.Scan(&d.Resource, &d.Effect, &d.Condition); err != nil {
			drows.Close()
			return nil, status.Errorf(codes.Internal, "scan data scope: %v", err)
		}
		out.DataScopes = append(out.DataScopes, &d)
	}
	drows.Close()
	if err := drows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "data scopes: %v", err)
	}
	return out, nil
}

type roleRef struct {
	id         int64
	code, name string
}

// roleAncestors 返回角色的祖先（最近优先），递归 CTE 闭包。
func (s *AdminServer) roleAncestors(ctx context.Context, appID, roleID int64) ([]roleRef, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH RECURSIVE anc(rid, depth) AS (
			SELECT parent_role_id, 1 FROM role_inheritance WHERE child_role_id=$1 AND app_id=$2
			UNION
			SELECT ri.parent_role_id, anc.depth+1 FROM role_inheritance ri JOIN anc ON ri.child_role_id=anc.rid WHERE ri.app_id=$2
		)
		SELECT r.id, r.code, r.name, min(anc.depth) AS d
		FROM anc JOIN role r ON r.id=anc.rid
		GROUP BY r.id, r.code, r.name ORDER BY d, r.code`, roleID, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []roleRef
	for rows.Next() {
		var rr roleRef
		var depth int
		if err := rows.Scan(&rr.id, &rr.code, &rr.name, &depth); err != nil {
			return nil, err
		}
		out = append(out, rr)
	}
	return out, rows.Err()
}

// scanStrings 跑单列字符串查询。
func (s *AdminServer) scanStrings(ctx context.Context, q string, args ...any) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
```

> 注：`effperm` import 在本文件由任务 4 的 SimulateRoleChange 使用；任务 3 单独提交时若 `errors/sql` 之外 import 未用，按实际增删。

- [ ] **步骤 5：运行测试验证通过**

运行：`go test ./internal/controlplane/mgmt/ -run TestGetRoleGraph -count=1`
预期：PASS。

- [ ] **步骤 6：Commit**

```bash
git add internal/controlplane/mgmt/role_graph.go internal/controlplane/mgmt/role_graph_test.go internal/controlplane/mgmt/authz.go
git commit -m "feat(mgmt): GetRoleGraph 角色全景聚合(绑定用户+能力直接/继承标来源+父角色+直接数据范围)+ruleTable role/read scopeApp

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 4：mgmt SimulateRoleChange handler + ruleTable

**文件：**
- 修改：`internal/controlplane/mgmt/role_graph.go`（追加 handler）
- 修改：`internal/controlplane/mgmt/role_graph_test.go`（追加测试）
- 修改：`internal/controlplane/mgmt/authz.go`（ruleTable +1）

- [ ] **步骤 1：ruleTable 加 1 条（求值类，同 GetEffectivePermissions 组）**

```go
	"/sydom.admin.v1.AdminService/SimulateRoleChange":     {"effective_permission", "read", false, scopeApp},
```

- [ ] **步骤 2：先写失败测试（追加到 role_graph_test.go）**

```go
func TestSimulateRoleChange_BindUserDiff(t *testing.T) {
	db := dbtest.SetupSchema(t)
	_, appID := dbtest.SeedAppInTenant(t, db, "t-a", "dom-a", "AK_a")
	srv := accountsSrv(db)
	ctx := context.Background()

	viewerID := mustRole(t, db, appID, "viewer", "查看员")
	permR := mustPerm(t, db, appID, "order:read", "order", "read", "查看订单")
	mustGrant(t, db, appID, viewerID, permR)
	mustDataPolicy(t, db, appID, "viewer", "order", `{"field":"tenant_id","op":"EQ","value":"$user.tenant_id"}`)

	resp, err := srv.SimulateRoleChange(ctx, &adminv1.SimulateRoleChangeRequest{
		AppId: uint64(appID), RoleId: viewerID,
		ChangeType: adminv1.RoleChangeType_BIND_USER, UserId: "bob"})
	require.NoError(t, err)
	require.Len(t, resp.Subjects, 1)
	require.Equal(t, "bob", resp.Subjects[0].UserId)
	require.NotEmpty(t, resp.Subjects[0].AddedPermissions)
	require.NotEmpty(t, resp.Subjects[0].AddedDataPreviews)
	require.Contains(t, resp.Subjects[0].AddedDataPreviews[0].Predicate, "$user.")

	// 未知 role → NotFound。
	_, err = srv.SimulateRoleChange(ctx, &adminv1.SimulateRoleChangeRequest{
		AppId: uint64(appID), RoleId: 999, ChangeType: adminv1.RoleChangeType_BIND_USER, UserId: "x"})
	require.Equal(t, codes.NotFound, status.Code(err))
}
```

- [ ] **步骤 3：运行验证失败** → FAIL。

- [ ] **步骤 4：实现 SimulateRoleChange（追加到 role_graph.go）**

```go
// SimulateRoleChange 反事实预览：把假设变更施于角色，返回受影响用户的有效权限 diff（不落库）。
func (s *AdminServer) SimulateRoleChange(ctx context.Context, r *adminv1.SimulateRoleChangeRequest) (*adminv1.SimulateRoleChangeResponse, error) {
	appID := int64(r.AppId)
	var code string
	err := s.db.QueryRowContext(ctx,
		`SELECT code FROM role WHERE id=$1 AND app_id=$2`, r.RoleId, appID).Scan(&code)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "role not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read role: %v", err)
	}

	var ch effperm.Change
	switch r.ChangeType {
	case adminv1.RoleChangeType_BIND_USER:
		if r.UserId == "" {
			return nil, status.Error(codes.InvalidArgument, "user_id required")
		}
		ch = effperm.Change{Type: "bind_user", UserID: r.UserId}
	case adminv1.RoleChangeType_ADD_CAPABILITY:
		if r.Resource == "" || r.Action == "" {
			return nil, status.Error(codes.InvalidArgument, "resource/action required")
		}
		ch = effperm.Change{Type: "add_capability", Resource: r.Resource, Action: r.Action}
	default:
		return nil, status.Error(codes.InvalidArgument, "unknown change_type")
	}

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin read tx: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck

	diffs, err := effperm.Simulate(ctx, tx, appID, code, ch)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "simulate: %v", err)
	}

	out := &adminv1.SimulateRoleChangeResponse{}
	for _, d := range diffs {
		sd := &adminv1.SubjectDiff{UserId: d.UserID}
		for _, p := range d.AddedPermissions {
			sd.AddedPermissions = append(sd.AddedPermissions, &adminv1.EffectivePermission{Resource: p.Resource, Action: p.Action})
		}
		for _, p := range d.RemovedPermissions {
			sd.RemovedPermissions = append(sd.RemovedPermissions, &adminv1.EffectivePermission{Resource: p.Resource, Action: p.Action})
		}
		for _, v := range d.AddedDataViews {
			sd.AddedDataPreviews = append(sd.AddedDataPreviews, &adminv1.DataPolicyPreview{Resource: v.Resource, Match: v.Match, Predicate: v.Predicate})
		}
		for _, v := range d.RemovedDataViews {
			sd.RemovedDataPreviews = append(sd.RemovedDataPreviews, &adminv1.DataPolicyPreview{Resource: v.Resource, Match: v.Match, Predicate: v.Predicate})
		}
		out.Subjects = append(out.Subjects, sd)
	}
	return out, nil
}
```

- [ ] **步骤 5：测试 + 全包回归**

运行：`go test ./internal/controlplane/mgmt/ -run 'TestGetRoleGraph|TestSimulateRoleChange' -count=1`，再 `go test ./internal/controlplane/mgmt/ -count=1`
预期：PASS。`gofmt -l` 空、`go vet ./internal/controlplane/mgmt/` 净。

- [ ] **步骤 6：Commit**

```bash
git add internal/controlplane/mgmt/role_graph.go internal/controlplane/mgmt/role_graph_test.go internal/controlplane/mgmt/authz.go
git commit -m "feat(mgmt): SimulateRoleChange 反事实 diff handler(复用 effperm.Simulate,两 change_type,未知 role NotFound)+ruleTable effective_permission/read

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 5：REST 2 路由

**文件：**
- 创建：`internal/controlplane/restgw/routes_role_graph.go`
- 创建：`internal/controlplane/restgw/routes_role_graph_test.go`
- 修改：`internal/controlplane/restgw/routes.go`（allRoutes append + 计数注释）

- [ ] **步骤 1：先写失败测试 `routes_role_graph_test.go`**

```go
package restgw_test

func TestREST_RoleGraph_And_Simulate(t *testing.T) {
	ts, db := newTestGW(t)
	c := rootClient(t, ts.URL)
	appID := uint64(dbtest.SeedApp(t, db))

	// 经 REST 建角色 + 权限 + 授权 + 绑定。
	resp, body := c.do("POST", "/v1/apps/"+u(appID)+"/roles", map[string]any{"code": "viewer", "name": "查看员"})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var cr adminv1.CreateRoleResponse
	require.NoError(t, protoUnmarshal(body, &cr))
	resp, body = c.do("PUT", "/v1/apps/"+u(appID)+"/permissions/order:read", map[string]any{
		"resource": "order", "action": "read", "ptype": "api", "name": "查看订单"})
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var up adminv1.UpsertPermissionResponse
	require.NoError(t, protoUnmarshal(body, &up))
	resp, _ = c.do("POST", "/v1/apps/"+u(appID)+"/roles/"+i(cr.RoleId)+"/grants", map[string]any{
		"permissionId": strconv.FormatInt(up.PermissionId, 10), "eft": "allow"})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// GetRoleGraph。
	resp, body = c.do("GET", "/v1/apps/"+u(appID)+"/roles/"+i(cr.RoleId)+"/graph", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var rg adminv1.GetRoleGraphResponse
	require.NoError(t, protoUnmarshal(body, &rg))
	require.Equal(t, "viewer", rg.RoleCode)
	require.NotEmpty(t, rg.Capabilities)

	// SimulateRoleChange（bind_user）。
	resp, body = c.do("GET", "/v1/apps/"+u(appID)+"/roles/"+i(cr.RoleId)+"/simulation?change_type=bind_user&user_id=bob", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var sim adminv1.SimulateRoleChangeResponse
	require.NoError(t, protoUnmarshal(body, &sim))
	require.Len(t, sim.Subjects, 1)
	require.NotEmpty(t, sim.Subjects[0].AddedPermissions)
}
```

- [ ] **步骤 2：运行验证失败** → FAIL（404 路由未注册）。

- [ ] **步骤 3：实现 `routes_role_graph.go`**

```go
package restgw

import (
	"context"
	"net/http"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"google.golang.org/protobuf/proto"
)

// roleGraphRoutes 是 M3.3 角色全景 + 决策模拟 2 路由（app 域；app_id/role_id 取自 path 权威）。
func roleGraphRoutes() []route {
	const pfx = "/sydom.admin.v1.AdminService/"
	return []route{
		{"GET", "/v1/apps/{app_id}/roles/{role_id}/graph", pfx + "GetRoleGraph",
			func(r *http.Request, _ []byte) (proto.Message, error) {
				appID, err := pathUint64(r, "app_id")
				if err != nil {
					return nil, err
				}
				roleID, err := pathInt64(r, "role_id")
				if err != nil {
					return nil, err
				}
				return &adminv1.GetRoleGraphRequest{AppId: appID, RoleId: roleID}, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.GetRoleGraph(ctx, m.(*adminv1.GetRoleGraphRequest))
			}},
		{"GET", "/v1/apps/{app_id}/roles/{role_id}/simulation", pfx + "SimulateRoleChange",
			func(r *http.Request, _ []byte) (proto.Message, error) {
				appID, err := pathUint64(r, "app_id")
				if err != nil {
					return nil, err
				}
				roleID, err := pathInt64(r, "role_id")
				if err != nil {
					return nil, err
				}
				q := r.URL.Query()
				return &adminv1.SimulateRoleChangeRequest{
					AppId: appID, RoleId: roleID,
					ChangeType: parseRoleChangeType(q.Get("change_type")),
					UserId:     q.Get("user_id"),
					Resource:   q.Get("resource"),
					Action:     q.Get("action"),
				}, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.SimulateRoleChange(ctx, m.(*adminv1.SimulateRoleChangeRequest))
			}},
	}
}

// parseRoleChangeType 把 query 串映射为枚举（未知→UNSPECIFIED，由 handler 校验拒绝）。
func parseRoleChangeType(s string) adminv1.RoleChangeType {
	switch s {
	case "bind_user":
		return adminv1.RoleChangeType_BIND_USER
	case "add_capability":
		return adminv1.RoleChangeType_ADD_CAPABILITY
	default:
		return adminv1.RoleChangeType_ROLE_CHANGE_UNSPECIFIED
	}
}
```

- [ ] **步骤 4：在 `routes.go` 的 `allRoutes()` append + 更新计数注释**

```go
	rs = append(rs, roleGraphRoutes()...)
```
把 `allRoutes` 上方「全部 48 路由」注释改为 50，并补「+ 角色全景 2」。

- [ ] **步骤 5：测试 + 全包**

运行：`go test ./internal/controlplane/restgw/ -run TestREST_RoleGraph -count=1` → PASS；`go test ./internal/controlplane/restgw/ -count=1` → 全绿（若有路由计数断言因 +2 失败则更新该常量）。`gofmt -l`/`go vet` 净。

- [ ] **步骤 6：Commit**

```bash
git add internal/controlplane/restgw/
git commit -m "feat(rest): 角色全景 2 路由(GET graph/simulation,path 权威 app_id+role_id,复用 AuthorizeRule;48→50)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 6：Console 角色全景页 + 模拟 diff 页

**文件：**
- 创建：`internal/controlplane/console/routes_role_graph.go` + `_test.go`
- 创建：`internal/controlplane/console/templates/ops_role_graph.html`、`ops_role_simulate.html`
- 修改：`internal/controlplane/console/templates/ops_roles.html`（每行加「全景」链接）
- 修改：`internal/controlplane/console/handler.go`（NewHandler 加 `h.registerRoleGraph(mux)`）

**形态：分区面板，复用 M3.1 设计系统、bizterm `capabilityName`、`conditionPredicate`、无新 JS。**

- [ ] **步骤 1：先写失败测试 `routes_role_graph_test.go`**

```go
package console

// 复用 handler_test.go：newConsole / loginAndCSRF("root@sydom","rootsecret") / readBody。
// 播种 app + 角色 viewer 授 order:read + viewer 数据范围($user.tenant_id)（store 直接播种，参照 mgmt seedConfiguredApp 范式）。
func TestConsole_RoleGraph_And_Simulate(t *testing.T) {
	ts, store, db := newConsole(t)
	appID, roleID := seedRoleGraphApp(t, db) // 本文件内 helper
	c, _ := loginAndCSRF(t, ts, store, "root@sydom", "rootsecret")

	// 角色全景页。
	resp, err := c.Get(ts.URL + "/ops/apps/" + u(appID) + "/roles/" + i(roleID) + "/graph")
	require.NoError(t, err)
	body := readBody(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, body, "查看订单")        // 能力业务名(bizterm)
	require.Contains(t, body, "$user.")          // 数据范围符号谓词(conditionPredicate)
	require.NotContains(t, body, "app_secret")

	// 模拟 diff 页（bind_user）。
	resp, err = c.Get(ts.URL + "/ops/apps/" + u(appID) + "/roles/" + i(roleID) + "/simulate?change_type=bind_user&user_id=bob")
	require.NoError(t, err)
	body = readBody(t, resp)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, body, "bob")
	require.Contains(t, body, "查看订单")        // 新增能力业务名
	require.Contains(t, body, "$user.")          // 新增数据范围符号谓词
}
```

- [ ] **步骤 2：运行验证失败** → FAIL（404）。

- [ ] **步骤 3：实现 `routes_role_graph.go`**

```go
package console

import (
	"net/http"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
)

func (h *Handler) registerRoleGraph(mux *http.ServeMux) {
	mux.HandleFunc("GET /ops/apps/{app_id}/roles/{role_id}/graph", h.roleGraph)
	mux.HandleFunc("GET /ops/apps/{app_id}/roles/{role_id}/simulate", h.roleSimulate)
}

// roleGraph：角色全景（分区面板）。能力经 capabilityName 渲业务名，数据范围经 conditionPredicate 渲符号谓词。
func (h *Handler) roleGraph(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	appID, err := pathUint64(r, "app_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"GetRoleGraph", err)
		return
	}
	roleID, err := pathInt64(r, "role_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"GetRoleGraph", err)
		return
	}
	msg := &adminv1.GetRoleGraphRequest{AppId: appID, RoleId: roleID}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"GetRoleGraph", principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"GetRoleGraph", err)
		return
	}
	g, err := h.srv.GetRoleGraph(ctx, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"GetRoleGraph", err)
		return
	}

	type capRow struct{ Name, Source string }
	type scopeRow struct{ Resource, Predicate string }
	caps := make([]capRow, 0, len(g.Capabilities))
	for _, c := range g.Capabilities {
		caps = append(caps, capRow{Name: capabilityName(c.Name, c.Resource, c.Action), Source: c.Source})
	}
	scopes := make([]scopeRow, 0, len(g.DataScopes))
	for _, d := range g.DataScopes {
		scopes = append(scopes, scopeRow{Resource: d.Resource, Predicate: conditionPredicate(d.Condition)})
	}
	h.renderPage(w, r, "ops_role_graph.html", http.StatusOK, map[string]any{
		"AppID": appID, "RoleID": roleID, "RoleName": g.RoleName,
		"BoundUsers": g.BoundUsers, "Caps": caps, "Parents": g.Parents, "Scopes": scopes,
		"CSRF": sess.CSRF, "OpsNav": "roles",
	})
}

// roleSimulate：反事实 diff 页（读语义，GET，无 CSRF/status 闸——与 decision.html 一致）。
func (h *Handler) roleSimulate(w http.ResponseWriter, r *http.Request) {
	principal, _, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	appID, err := pathUint64(r, "app_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"SimulateRoleChange", err)
		return
	}
	roleID, err := pathInt64(r, "role_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"SimulateRoleChange", err)
		return
	}
	q := r.URL.Query()
	msg := &adminv1.SimulateRoleChangeRequest{
		AppId: appID, RoleId: roleID,
		ChangeType: parseConsoleChangeType(q.Get("change_type")),
		UserId:     q.Get("user_id"), Resource: q.Get("resource"), Action: q.Get("action"),
	}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"SimulateRoleChange", principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"SimulateRoleChange", err)
		return
	}
	res, err := h.srv.SimulateRoleChange(ctx, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"SimulateRoleChange", err)
		return
	}

	type permRow struct{ Name string }
	type viewRow struct{ Resource, Predicate string }
	type subjRow struct {
		UserID                 string
		Added, Removed         []permRow
		AddedScopes, RemScopes []viewRow
	}
	var subjects []subjRow
	for _, s := range res.Subjects {
		sr := subjRow{UserID: s.UserId}
		for _, p := range s.AddedPermissions {
			sr.Added = append(sr.Added, permRow{Name: capabilityName(p.Resource+":"+p.Action, p.Resource, p.Action)})
		}
		for _, p := range s.RemovedPermissions {
			sr.Removed = append(sr.Removed, permRow{Name: capabilityName(p.Resource+":"+p.Action, p.Resource, p.Action)})
		}
		for _, v := range s.AddedDataPreviews {
			sr.AddedScopes = append(sr.AddedScopes, viewRow{Resource: v.Resource, Predicate: v.Predicate})
		}
		for _, v := range s.RemovedDataPreviews {
			sr.RemScopes = append(sr.RemScopes, viewRow{Resource: v.Resource, Predicate: v.Predicate})
		}
		subjects = append(subjects, sr)
	}
	h.renderPage(w, r, "ops_role_simulate.html", http.StatusOK, map[string]any{
		"AppID": appID, "RoleID": roleID, "Subjects": subjects, "OpsNav": "roles",
	})
}

func parseConsoleChangeType(s string) adminv1.RoleChangeType {
	if s == "add_capability" {
		return adminv1.RoleChangeType_ADD_CAPABILITY
	}
	return adminv1.RoleChangeType_BIND_USER
}
```

> 模拟 diff 页的 AddedPermissions 无原始 name（EffectivePermission 仅 resource/action），故用 `capabilityName(resource:action, resource, action)` 兜底合成业务名（与既有 effective.html 渲染口径一致；bizterm 缺名合成「resource · 动词」绝不裸 resource:action）。模拟数据范围 predicate 由 effperm/dataperm 已渲符号，直接显示。

- [ ] **步骤 4：模板**

`ops_role_graph.html`（分区面板 + 两个「假如…」表单，复用 M3.1 类 `workspace/appnav/card/card-header/list-plain/hint/badge/empty-state/inline-form/btn btn-primary`）：

```html
{{define "title"}}角色全景 · {{.RoleName}}{{end}}
{{define "content"}}<div class="workspace">
<aside class="appnav" aria-label="运营台导航"><div class="appname">运营台 · App #{{.AppID}}</div>
<a href="/ops/apps/{{.AppID}}/roles" {{if eq .OpsNav "roles"}}class="active"{{end}}>业务角色</a>
</aside>
<section>
<nav class="breadcrumb" aria-label="面包屑">运营台 · 业务角色 · 全景</nav>
<h1>{{.RoleName}} <span class="badge badge-muted">角色全景</span></h1>

<div class="card"><h2 class="card-header">绑定用户 · {{len .BoundUsers}}</h2>
{{if .BoundUsers}}<ul class="list-plain">{{range .BoundUsers}}<li>{{.}}</li>{{end}}</ul>{{else}}<div class="empty-state">暂无绑定用户。</div>{{end}}</div>

<div class="card"><h2 class="card-header">能力 · {{len .Caps}}</h2>
{{if .Caps}}<ul class="list-plain">{{range .Caps}}<li>{{.Name}} <span class="hint">（{{.Source}}）</span></li>{{end}}</ul>{{else}}<div class="empty-state">暂无能力。</div>{{end}}</div>

<div class="card"><h2 class="card-header">继承自</h2>
{{if .Parents}}<ul class="list-plain">{{range .Parents}}<li><a href="/ops/apps/{{$.AppID}}/roles/{{.Id}}/graph">{{.Name}}</a></li>{{end}}</ul>{{else}}<div class="empty-state">无父角色。</div>{{end}}</div>

<div class="card"><h2 class="card-header">数据范围</h2>
{{if .Scopes}}<ul class="list-plain">{{range .Scopes}}<li>{{.Resource}}：仅 {{.Predicate}}</li>{{end}}</ul>{{else}}<div class="empty-state">无数据范围。</div>{{end}}</div>

<div class="card"><h2 class="card-header">假如……（模拟，不落库）</h2>
<form method="get" action="/ops/apps/{{.AppID}}/roles/{{.RoleID}}/simulate" class="inline-form">
  <input type="hidden" name="change_type" value="bind_user">
  <label>把用户绑到本角色：<input name="user_id" placeholder="user_id" required></label>
  <button class="btn btn-primary">模拟</button>
</form>
<form method="get" action="/ops/apps/{{.AppID}}/roles/{{.RoleID}}/simulate" class="inline-form">
  <input type="hidden" name="change_type" value="add_capability">
  <label>给本角色加能力：资源 <input name="resource" placeholder="resource" required> 动作 <input name="action" placeholder="action" required></label>
  <button class="btn btn-primary">模拟</button>
</form>
</div>
</section></div>{{end}}
```

`ops_role_simulate.html`（diff 摘要）：

```html
{{define "title"}}模拟结果 · 运营台{{end}}
{{define "content"}}<div class="workspace">
<aside class="appnav" aria-label="运营台导航"><div class="appname">运营台 · App #{{.AppID}}</div>
<a href="/ops/apps/{{.AppID}}/roles">业务角色</a></aside>
<section>
<nav class="breadcrumb" aria-label="面包屑"><a href="/ops/apps/{{.AppID}}/roles/{{.RoleID}}/graph">← 返回角色全景</a></nav>
<h1>模拟结果 <span class="badge badge-muted">未落库</span></h1>
{{if .Subjects}}
{{range .Subjects}}
<div class="card" style="margin-bottom:var(--space-4)">
<h2 class="card-header">{{.UserID}}</h2>
{{if .Added}}<h3>新增能力</h3><ul class="list-plain">{{range .Added}}<li>＋ {{.Name}}</li>{{end}}</ul>{{end}}
{{if .Removed}}<h3>失去能力</h3><ul class="list-plain">{{range .Removed}}<li>－ {{.Name}}</li>{{end}}</ul>{{end}}
{{if .AddedScopes}}<h3>新增数据范围</h3><ul class="list-plain">{{range .AddedScopes}}<li>＋ {{.Resource}}：仅 {{.Predicate}}</li>{{end}}</ul>{{end}}
{{if .RemScopes}}<h3>失去数据范围</h3><ul class="list-plain">{{range .RemScopes}}<li>－ {{.Resource}}：仅 {{.Predicate}}</li>{{end}}</ul>{{end}}
</div>
{{end}}
{{else}}<div class="empty-state">此假设变更对任何用户的有效权限无影响。</div>{{end}}
</section></div>{{end}}
```

`ops_roles.html` 每个角色行加「全景」链接（找到角色 range 内的操作区，加 `<a href="/ops/apps/{{$.AppID}}/roles/{{.ID}}/graph">全景</a>`；核对该模板里角色字段名后填）。

`handler.go` 的 NewHandler 在既有 `h.registerTenantTemplates(mux)` 行后加：
```go
	h.registerRoleGraph(mux) // M3.3 角色全景 + 决策模拟
```

- [ ] **步骤 5：测试 + 全包**

运行：`go test ./internal/controlplane/console/ -run TestConsole_RoleGraph -count=1` → PASS；`go test ./internal/controlplane/console/ -count=1` → 全绿。`gofmt -l`/`go vet` 净。确认无新增 `.js`（`git status` 仅 .go/.html）。

- [ ] **步骤 6：Commit**

```bash
git add internal/controlplane/console/
git commit -m "feat(console): 运营台角色全景页(分区面板)+反事实模拟 diff 页(bizterm 业务名/符号谓词/无新 JS,模拟读语义 GET)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

## 任务 7：整体验证 RG-1..8 + opus 评审 + FF 合并

- [ ] **步骤 1：RG 不变量逐条核验**

```bash
BASE=<本计划 commit sha>
git diff $BASE..HEAD -- internal/controlplane/adminauthz/ casbin/enforcer.go | wc -l   # RG-1 期望 0
git diff $BASE..HEAD -- internal/controlplane/mgmt/authz.go | grep -E '^\+' | grep -v '^+++'  # 仅 +2 ruleTable
git diff $BASE..HEAD -- internal/sidecar/ | wc -l                                      # RG-7 期望 0
ls internal/controlplane/console/static/*.js                                           # 仅 datapolicy.js
grep -rn "casbin_rule\|BumpAppVersion\|ApplyDiff\|broadcast\|Outbox" internal/controlplane/effperm/effperm.go || echo "RG-2 OK: Simulate 不写库/不 bump/不广播"
```
RG-2 另由 `TestSimulate_NoSideEffects` 覆盖；RG-3/4/5/8 由 effperm + mgmt 单测覆盖（复跑确认）。

- [ ] **步骤 2：格式/静态/proto/全量测试**

```bash
gofmt -l internal/ api/        # 空
go vet ./...                   # 净
make proto-check               # 无漂移
go test ./... 2>&1 | grep -cE '^FAIL'   # 0
```

- [ ] **步骤 3：opus 整体安全评审**（子代理 model=opus）：逐条核 RG-1..8 + 深挖（模拟零副作用真实性、deny 反转忠实、符号口径不枚举、跨租户 fail-close、ADD_CAPABILITY 影响用户集正确、未知 role/user 不泄露）。READY 方可合并。

- [ ] **步骤 4：更新记忆**：`project_detailed_design_progress.md` 加 M3.3 节；`MEMORY.md` 索引钩子追加 M3.3 完成 + 下一步 M3.4。

- [ ] **步骤 5：FF 合并本地 main（不 push origin）**：worktree 全绿 + opus READY 后 `git merge --ff-only`，清 worktree（参照 M3.2c-2 收尾）。

---

## 自检记录

**规格覆盖度（对照 spec）：** §4.1 GetRoleGraph→任务 1(proto)+3(handler) ✓；§4.2 SimulateRoleChange→任务 1+2(effperm)+4(handler) ✓；§5.1 Console→任务 6 ✓；§5.2 REST→任务 5 ✓；§5.3 ruleTable+2→任务 3+4 ✓；§6 RG-1..8→任务 2/3/4/6/7 ✓；§7 测试策略→各任务 TDD + 任务 7 全量 ✓。

**规格偏差（计划期明确）：** ① spec §4.1 `data_scopes` 松写为 `DataPolicyPreview`，计划改用新 message `RoleGraphDataScope{resource,effect,condition}`（**原始 condition**，Console 经 `conditionPredicate` 渲染）——与 M3.2c `TemplateDataScope` 一致、符号渲染收口 Console、避免在 mgmt 跑用户级 FilterSymbolic（GetRoleGraph 是结构读非求值）。② capability `name` 由 mgmt 返回**原始 permission.name**、Console 经 `capabilityName` 渲业务名（bizterm 在 console 包，mgmt 不可依赖）——与既有所有页一致。SimulateRoleChange 的数据 diff 仍用 `DataPolicyPreview`（predicate 由 effperm/dataperm 求值期已渲符号，与 GetEffectivePermissions 一致）。

**占位符扫描：** 各任务给出实际 Go/SQL/proto/HTML；测试 helper（mustRole/mustPerm/seedRoleForSim/seedRoleGraphApp）注明「用既有 store 函数实现、核对签名后写」——是适配既有夹具的明确指令，非占位。

**类型一致性：** `effperm.Change{Type,UserID,Resource,Action}`/`effperm.SubjectDiff{UserID,Added/RemovedPermissions []Perm,Added/RemovedDataViews []DataView}`（任务 2）↔ mgmt 映射到 `adminv1.SubjectDiff{UserId,Added/RemovedPermissions []*EffectivePermission,Added/RemovedDataPreviews []*DataPolicyPreview}`（任务 4）一致；`adminv1.RoleChangeType_{BIND_USER,ADD_CAPABILITY}`（任务 1）↔ REST `parseRoleChangeType`（任务 5）↔ mgmt switch（任务 4）一致；`cp.Rule{Ptype,V [6]string}` 合成规则 g=[user,role,domain]、p=[role,domain,resource,action,allow]（任务 2，对齐 manager_rbac_test.go:25 / manager_test.go:111）；`RoleGraphCapability{resource,action,name,source}`（任务 1）↔ handler 填充（任务 3）↔ Console capRow（任务 6）一致。
