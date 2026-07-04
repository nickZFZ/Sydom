# M4.2 批量操作（Bulk Operations）实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 为 5 个 app 域「移除族」操作加原子批量变体（勾选即操作、source-agnostic、全原子+幂等即 no-op），三面 parity（gRPC+REST+Console）。

**架构：** 方案 A——每操作一个 batch RPC，复用其单数兄弟的 `ruleTable` 规则（授权同构）。每 batch = 一个 `PolicyManager.runVersionedWrite`（数据策略走 `runVersionedWriteData`）：mutate 内一条 set-based `DELETE … WHERE app_id=$1 AND … = ANY(...)` → 一次 reproject+Diff → 一次 version bump + 一次 outbox 广播。授权决策核心（enforcer/adminauthz/sidecar/kernel）一字不碰；`authz.go` 仅 +5 ruleTable 行。

**技术栈：** Go、PostgreSQL（`lib/pq` 数组 `pq.Array`）、protobuf/buf、testcontainers（PG+Redis）、testify、html/template、`net/http`。

**规格：** `docs/superpowers/specs/2026-07-04-sydom-m4-2-bulk-operations-design.md`（BASE=main `f84d452`）。

**范式纪律：** 子代理驱动 + 两阶段审查（规格→质量）；TDD；每任务独立 commit；**禁用 `git commit --amend`**（环境硬禁）；实现者不读本 plan（由控制者派发任务文本）。

---

## 文件结构（先锁定分解）

| 文件 | 职责 | 任务 |
|---|---|---|
| `api/proto/sydom/admin/v1/admin.proto` | +5 RPC、+3 Ref message、+5 Request、+`BatchWriteResponse` | 1 |
| `gen/sydom/admin/v1/*.pb.go`（生成物） | `make proto` 产出 | 1 |
| `internal/controlplane/store/source.go` 或新 `store/batch.go` | 5 个 set-based 批量删除 store 助手（返回 applied 计数） | 2 |
| `internal/controlplane/store/batch_test.go` | store 批量助手 testcontainers 测试 | 2 |
| `internal/controlplane/policy/bulk.go` | 5 个 `PolicyManager.BatchXxx` 方法（各一 versioned write，返回 `(*cp.Delta, int, error)`） | 2 |
| `internal/controlplane/policy/bulk_test.go` | 批量 manager testcontainers 测试（原子/no-op/source-blind/级联/边界） | 2 |
| `internal/controlplane/mgmt/server.go` | +5 薄 handler | 3 |
| `internal/controlplane/mgmt/authz.go` | `ruleTable` +5 | 3 |
| `internal/controlplane/mgmt/bulk_test.go` | mgmt 批量 handler + 跨租户矩阵测试 | 3 |
| `internal/controlplane/restgw/routes.go` | +5 路由 + 计数注释更新 | 4 |
| `internal/controlplane/restgw/routes_bulk_test.go` | REST 批量路由测试（HMAC、path 权威、403 fail-close） | 4 |
| `internal/controlplane/console/routes_rbac.go` | +4 批量 handler（roles/grants/inheritances/bindings）+ 注册路由 | 5 |
| `internal/controlplane/console/routes_datapolicy.go` | +1 批量 handler（data-policies）+ 注册路由 | 5 |
| `internal/controlplane/console/templates/{roles,grants,inheritances,bindings,datapolicies}.html` | 行首复选框 + 批量移除表单 | 5 |
| `internal/controlplane/console/bulk_test.go` | Console 多选 + 确认门 + PRG 测试 | 5 |

**关键分解决策：**
- 批量 store 助手用 **set-based SQL**（一条 `DELETE ... = ANY($2)` / pair 用 `unnest`），非 N 次单删——高效且单 SQL home（DRY）。
- 批量 manager 方法返回 `(*cp.Delta, int /*applied*/, error)`——`applied` 由 store 助手回传（RowsAffected 或 `RETURNING id` 计数），供 `BatchWriteResponse`。
- `BatchDeleteDataPolicy` **独走 `runVersionedWriteData`**（data 写变体，始终 bump），其余 4 走 `runVersionedWrite`（含 no-op 分支）。

---

## 任务 1：proto 契约

**文件：**
- 修改：`api/proto/sydom/admin/v1/admin.proto`（RPC 加在 `ImportAppPolicy` 后即 line 88 与 `}` line 89 之间；message 加在文件 message 区末尾）
- 生成：`gen/sydom/admin/v1/admin.pb.go`、`gen/sydom/admin/v1/admin_grpc.pb.go`（`make proto`）

- [ ] **步骤 1：加 5 RPC 到 service 块**

在 `service AdminService {` 内 `rpc ImportAppPolicy(...) returns (ImportAppPolicyResponse);`（line 88）之后加一组：

```proto
  // ---- M4.2 批量操作（app 域移除族，全原子+幂等即 no-op，勾选即操作 source-agnostic）----
  rpc BatchUnbindUserRole       (BatchUnbindUserRoleRequest)        returns (BatchWriteResponse);
  rpc BatchRevokePermission     (BatchRevokePermissionRequest)      returns (BatchWriteResponse);
  rpc BatchRemoveRoleInheritance(BatchRemoveRoleInheritanceRequest) returns (BatchWriteResponse);
  rpc BatchDeleteRole           (BatchDeleteRoleRequest)            returns (BatchWriteResponse);
  rpc BatchDeleteDataPolicy     (BatchDeleteDataPolicyRequest)      returns (BatchWriteResponse);
```

- [ ] **步骤 2：加 message（文件 message 区末尾，`ImportAppPolicyResponse` 之后）**

```proto
message UserRoleRef    { string user_id = 1; int64 role_id = 2; }
message GrantRef       { int64 role_id = 1; int64 permission_id = 2; }
message InheritanceRef { int64 child_role_id = 1; int64 parent_role_id = 2; }

message BatchUnbindUserRoleRequest        { uint64 app_id = 1; repeated UserRoleRef    items = 2; }
message BatchRevokePermissionRequest      { uint64 app_id = 1; repeated GrantRef       items = 2; }
message BatchRemoveRoleInheritanceRequest { uint64 app_id = 1; repeated InheritanceRef items = 2; }
message BatchDeleteRoleRequest            { uint64 app_id = 1; repeated int64 role_ids = 2; }
message BatchDeleteDataPolicyRequest      { uint64 app_id = 1; repeated int64 data_policy_ids = 2; }

message BatchWriteResponse {
  uint64 version   = 1; // bump 后新版本（全 no-op 时为当前值）
  uint32 requested = 2; // 请求项数
  uint32 applied   = 3; // 实际改变项数（requested−applied = no-op 跳过数）
  bool   changed   = 4; // applied>0 → 已 bump+广播（数据策略批量非空即 changed）
}
```

- [ ] **步骤 3：生成并校验零漂移**

运行：`make proto && make proto-check`
预期：生成成功；`proto-check` 报告无未提交漂移（新增内容已入生成物）。若 buf lint 因 `Batch*` 前缀报 message 命名，检查是否需 `buf.yaml` except（预期不需要——命名符合 PascalCase）。

- [ ] **步骤 4：编译验证**

运行：`go build ./...`
预期：PASS（新类型 `adminv1.BatchWriteResponse` 等可引用；`AdminService` 接口新增 5 方法——此时 `AdminServer` 未实现会**断编译**，属预期，由任务 3 补齐；故本步仅验证 `gen/` 包本身可编译，用 `go build ./gen/...`）。

运行：`go build ./gen/...`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add api/proto/sydom/admin/v1/admin.proto gen/
git commit -m "feat(proto): M4.2 批量操作 5 RPC + BatchWriteResponse(勾选即操作移除族)"
```

---

## 任务 2：store 批量删除助手 + policy Manager 批量方法（核心）

**文件：**
- 创建：`internal/controlplane/store/batch.go`
- 创建：`internal/controlplane/store/batch_test.go`
- 创建：`internal/controlplane/policy/bulk.go`
- 创建：`internal/controlplane/policy/bulk_test.go`

参考既有：`store/store.go:98`（`DeleteRole` 级联 4 语句）、`policy/manager.go:65`（`runVersionedWrite`）、`policy/manager.go:478`（`runVersionedWriteData`）、`policy/manager.go:182/404/424/458`（4 个单数移除 + DeleteDataPolicy）。

### 2a. store 批量助手（set-based，返回 applied 计数）

- [ ] **步骤 1：写失败测试 `store/batch_test.go`**

复用既有 store 测试的 testcontainers 夹具（看 `store/source_test.go` 如何起库、seed app/role/permission）。测试建两个角色、两个权限、若干绑定，然后批量删，断言 applied 计数与残留。

```go
//go:build integration || testcontainers
// (与既有 store 测试相同的 build 约束/夹具；照抄 source_test.go 顶部)

func TestBatchDeleteRolesBatch_CascadesAndCounts(t *testing.T) {
	ctx, db := newStoreTestDB(t)        // 复用 source_test.go 的夹具函数名
	appID := seedApp(t, db)
	r1 := mustInsertRole(t, db, appID, "iac:a", "A")
	r2 := mustInsertRole(t, db, appID, "iac:b", "B")
	// 给 r1 一个绑定，验证级联
	mustBindUser(t, db, appID, "u1", r1)

	applied, err := store.DeleteRolesBatch(ctx, db, appID, []int64{r1, r2, 999999})
	if err != nil { t.Fatal(err) }
	if applied != 2 { t.Fatalf("applied=%d want 2 (r1,r2 存在;999999 no-op)", applied) }
	// 级联：r1 的绑定应被删
	if n := countBindings(t, db, appID, r1); n != 0 { t.Fatalf("binding 残留 %d", n) }
}

func TestBatchDeleteUserBindingsBatch_PairMatch(t *testing.T) {
	ctx, db := newStoreTestDB(t)
	appID := seedApp(t, db)
	r1 := mustInsertRole(t, db, appID, "iac:a", "A")
	mustBindUser(t, db, appID, "u1", r1)
	mustBindUser(t, db, appID, "u2", r1)
	applied, err := store.DeleteUserRoleBindingsBatch(ctx, db, appID,
		[]store.UserRolePair{{UserID: "u1", RoleID: r1}, {UserID: "nobody", RoleID: r1}})
	if err != nil { t.Fatal(err) }
	if applied != 1 { t.Fatalf("applied=%d want 1", applied) }
}
```

> 若 `source_test.go` 的夹具/辅助函数名不同（如 `newStoreTestDB`/`seedApp`/`mustInsertRole`），实现者照该文件实际函数名对齐；这里给出意图与断言，函数名以现有测试为准。

- [ ] **步骤 2：运行确认失败**

运行：`go test ./internal/controlplane/store/ -run TestBatchDelete -tags testcontainers -v`
预期：FAIL（`store.DeleteRolesBatch` 未定义）。

- [ ] **步骤 3：实现 `store/batch.go`**

```go
package store

import (
	"context"

	"github.com/lib/pq"
	cp "<module>/internal/controlplane" // 与 store.go 同一 cp 导入路径
)

// UserRolePair 供批量解绑；(user_id, role_id) 复合身份。
type UserRolePair struct {
	UserID string
	RoleID int64
}

// GrantPair 供批量撤权；(role_id, permission_id)。
type GrantPair struct{ RoleID, PermissionID int64 }

// InheritancePair 供批量移除继承；(child_role_id, parent_role_id)。
type InheritancePair struct{ ChildRoleID, ParentRoleID int64 }

// DeleteRolesBatch 级联批量删角色（对齐单数 DeleteRole 的 4 语句，均改 ANY($2)）。
// 返回实际删除的 role 行数（applied）；不存在的 id 为 no-op 不计。
func DeleteRolesBatch(ctx context.Context, ex cp.DBTX, appID int64, roleIDs []int64) (int64, error) {
	if len(roleIDs) == 0 {
		return 0, nil
	}
	ids := pq.Array(roleIDs)
	for _, s := range []string{
		`DELETE FROM role_permission   WHERE app_id=$1 AND role_id = ANY($2)`,
		`DELETE FROM role_inheritance  WHERE app_id=$1 AND (parent_role_id = ANY($2) OR child_role_id = ANY($2))`,
		`DELETE FROM user_role_binding WHERE app_id=$1 AND role_id = ANY($2)`,
	} {
		if _, err := ex.ExecContext(ctx, s, appID, ids); err != nil {
			return 0, err
		}
	}
	res, err := ex.ExecContext(ctx, `DELETE FROM role WHERE app_id=$1 AND id = ANY($2)`, appID, ids)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteRolePermissionsBatch 批量撤权。pair (role_id, permission_id) 用双数组 unnest 精确匹配。
func DeleteRolePermissionsBatch(ctx context.Context, ex cp.DBTX, appID int64, pairs []GrantPair) (int64, error) {
	if len(pairs) == 0 {
		return 0, nil
	}
	roleIDs := make([]int64, len(pairs))
	permIDs := make([]int64, len(pairs))
	for i, p := range pairs {
		roleIDs[i], permIDs[i] = p.RoleID, p.PermissionID
	}
	res, err := ex.ExecContext(ctx, `
		DELETE FROM role_permission
		WHERE app_id=$1
		  AND (role_id, permission_id) IN (
		    SELECT unnest($2::bigint[]), unnest($3::bigint[])
		  )`, appID, pq.Array(roleIDs), pq.Array(permIDs))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteRoleInheritancesBatch 批量移除继承边。pair (child, parent)。
func DeleteRoleInheritancesBatch(ctx context.Context, ex cp.DBTX, appID int64, pairs []InheritancePair) (int64, error) {
	if len(pairs) == 0 {
		return 0, nil
	}
	childIDs := make([]int64, len(pairs))
	parentIDs := make([]int64, len(pairs))
	for i, p := range pairs {
		childIDs[i], parentIDs[i] = p.ChildRoleID, p.ParentRoleID
	}
	res, err := ex.ExecContext(ctx, `
		DELETE FROM role_inheritance
		WHERE app_id=$1
		  AND (child_role_id, parent_role_id) IN (
		    SELECT unnest($2::bigint[]), unnest($3::bigint[])
		  )`, appID, pq.Array(childIDs), pq.Array(parentIDs))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteUserRoleBindingsBatch 批量解绑。pair (user_id text, role_id bigint)。
func DeleteUserRoleBindingsBatch(ctx context.Context, ex cp.DBTX, appID int64, pairs []UserRolePair) (int64, error) {
	if len(pairs) == 0 {
		return 0, nil
	}
	userIDs := make([]string, len(pairs))
	roleIDs := make([]int64, len(pairs))
	for i, p := range pairs {
		userIDs[i], roleIDs[i] = p.UserID, p.RoleID
	}
	res, err := ex.ExecContext(ctx, `
		DELETE FROM user_role_binding
		WHERE app_id=$1
		  AND (user_id, role_id) IN (
		    SELECT unnest($2::text[]), unnest($3::bigint[])
		  )`, appID, pq.Array(userIDs), pq.Array(roleIDs))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteDataPoliciesBatch 批量删数据策略；RETURNING id 回传实际删除的 id（供 data 面 ChangeRemove）。
func DeleteDataPoliciesBatch(ctx context.Context, ex cp.DBTX, appID int64, ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := ex.QueryContext(ctx,
		`DELETE FROM data_policy WHERE app_id=$1 AND id = ANY($2) RETURNING id`, appID, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var removed []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		removed = append(removed, id)
	}
	return removed, rows.Err()
}
```

> `<module>` 取 `go.mod` 的 module path（与 `store.go` 顶部 `cp` 导入一致）。`cp.DBTX` 是既有接口（`store.go` 已用），`*sql.Tx` 满足它。确认 `github.com/lib/pq` 已在 `go.mod`（既有 PG 驱动应已引入 `pq`；若 store 里没 import pq，检查现有数组用法或改用 `pq.Array`——`lib/pq` 是标准 PG 驱动，`pq.Array` 可用）。

- [ ] **步骤 4：运行 store 测试确认通过**

运行：`go test ./internal/controlplane/store/ -run TestBatchDelete -tags testcontainers -v`
预期：PASS。

- [ ] **步骤 5：Commit**

```bash
git add internal/controlplane/store/batch.go internal/controlplane/store/batch_test.go
git commit -m "feat(store): M4.2 set-based 批量删除助手(级联/pair 匹配/RETURNING,返回 applied)"
```

### 2b. policy Manager 批量方法

- [ ] **步骤 6：写失败测试 `policy/bulk_test.go`**

复用 `policy` 包既有 testcontainers 夹具（见 `policy/policy_as_code_test.go` 如何构造 `PolicyManager`、seed）。核心断言：原子回滚、no-op 不 bump、source-blind、applied 计数、级联。

```go
func TestBatchDeleteRole_AtomicAndCounts(t *testing.T) {
	ctx, mgr, appID := newPolicyMgrTest(t)   // 复用 policy_as_code_test.go 的夹具名
	r1 := seedRole(t, mgr, appID, "iac:a")
	r2 := seedRole(t, mgr, appID, "iac:b")
	d, applied, err := mgr.BatchDeleteRole(ctx, appID, []int64{r1, r2, 424242})
	if err != nil { t.Fatal(err) }
	if applied != 2 { t.Fatalf("applied=%d want 2", applied) }
	if d == nil { t.Fatal("期望非 nil Delta(有实际删除应 bump)") }
}

func TestBatchDeleteRole_AllNoOp_NoBump(t *testing.T) {
	ctx, mgr, appID := newPolicyMgrTest(t)
	v0 := currentVersion(t, mgr, appID)
	d, applied, err := mgr.BatchDeleteRole(ctx, appID, []int64{111, 222}) // 均不存在
	if err != nil { t.Fatal(err) }
	if applied != 0 { t.Fatalf("applied=%d want 0", applied) }
	if d != nil { t.Fatal("全 no-op 不应 bump(期望 nil Delta)") }
	if currentVersion(t, mgr, appID) != v0 { t.Fatal("版本不应变") }
}

func TestBatchUnbindUserRole_SourceBlind(t *testing.T) {
	ctx, mgr, appID := newPolicyMgrTest(t)
	r := seedRole(t, mgr, appID, "iac:a")
	// 绑定两用户（绑定无 source 维度，此测点在于批量作用于所选确切行）
	bindUser(t, mgr, appID, "u1", r)
	bindUser(t, mgr, appID, "u2", r)
	_, applied, err := mgr.BatchUnbindUserRole(ctx, appID, []store.UserRolePair{{"u1", r}, {"u2", r}})
	if err != nil { t.Fatal(err) }
	if applied != 2 { t.Fatalf("applied=%d want 2", applied) }
}

func TestBatchDeleteDataPolicy_AlwaysBumps(t *testing.T) {
	ctx, mgr, appID := newPolicyMgrTest(t)
	p := seedDataPolicy(t, mgr, appID)
	d, applied, err := mgr.BatchDeleteDataPolicy(ctx, appID, []int64{p, 999})
	if err != nil { t.Fatal(err) }
	if applied != 1 { t.Fatalf("applied=%d want 1", applied) }
	if d == nil { t.Fatal("data 写变体应 bump") }
}
```

> 夹具函数名（`newPolicyMgrTest`/`seedRole`/`bindUser`/`seedDataPolicy`/`currentVersion`）以 `policy` 包现有测试为准；照 `policy_as_code_test.go` 对齐。

- [ ] **步骤 7：运行确认失败**

运行：`go test ./internal/controlplane/policy/ -run TestBatch -tags testcontainers -v`
预期：FAIL（方法未定义）。

- [ ] **步骤 8：实现 `policy/bulk.go`**

```go
package policy

import (
	"context"
	"database/sql"

	cp "<module>/internal/controlplane"
	"<module>/internal/controlplane/store"
)

// BatchUnbindUserRole 原子批量解绑。返回 (Delta, applied, err)；全 no-op → (nil, 0, nil)。
func (m *PolicyManager) BatchUnbindUserRole(ctx context.Context, appID int64, pairs []store.UserRolePair) (*cp.Delta, int, error) {
	var applied int64
	d, err := m.runVersionedWrite(ctx, appID, writeOp{
		action: "batch_unbind_user_role", entityType: "user_role_binding", entityID: batchEntityID(len(pairs)),
		mutate: func(ctx context.Context, tx *sql.Tx) ([]cp.DataPolicyChange, error) {
			n, e := store.DeleteUserRoleBindingsBatch(ctx, tx, appID, pairs)
			applied = n
			return nil, e
		},
	})
	return d, int(applied), err
}

func (m *PolicyManager) BatchRevokePermission(ctx context.Context, appID int64, pairs []store.GrantPair) (*cp.Delta, int, error) {
	var applied int64
	d, err := m.runVersionedWrite(ctx, appID, writeOp{
		action: "batch_revoke", entityType: "role_permission", entityID: batchEntityID(len(pairs)),
		mutate: func(ctx context.Context, tx *sql.Tx) ([]cp.DataPolicyChange, error) {
			n, e := store.DeleteRolePermissionsBatch(ctx, tx, appID, pairs)
			applied = n
			return nil, e
		},
	})
	return d, int(applied), err
}

func (m *PolicyManager) BatchRemoveRoleInheritance(ctx context.Context, appID int64, pairs []store.InheritancePair) (*cp.Delta, int, error) {
	var applied int64
	d, err := m.runVersionedWrite(ctx, appID, writeOp{
		action: "batch_remove_inheritance", entityType: "role_inheritance", entityID: batchEntityID(len(pairs)),
		mutate: func(ctx context.Context, tx *sql.Tx) ([]cp.DataPolicyChange, error) {
			n, e := store.DeleteRoleInheritancesBatch(ctx, tx, appID, pairs)
			applied = n
			return nil, e
		},
	})
	return d, int(applied), err
}

func (m *PolicyManager) BatchDeleteRole(ctx context.Context, appID int64, roleIDs []int64) (*cp.Delta, int, error) {
	var applied int64
	d, err := m.runVersionedWrite(ctx, appID, writeOp{
		action: "batch_delete_role", entityType: "role", entityID: batchEntityID(len(roleIDs)),
		mutate: func(ctx context.Context, tx *sql.Tx) ([]cp.DataPolicyChange, error) {
			n, e := store.DeleteRolesBatch(ctx, tx, appID, roleIDs)
			applied = n
			return nil, e
		},
	})
	return d, int(applied), err
}

// BatchDeleteDataPolicy 走 data 写变体(始终 bump)；applied=实际删除数(RETURNING id)。
func (m *PolicyManager) BatchDeleteDataPolicy(ctx context.Context, appID int64, ids []int64) (*cp.Delta, int, error) {
	var applied int
	d, err := m.runVersionedWriteData(ctx, appID, writeOpData{
		action: "batch_delete_data_policy", entityType: "data_policy", entityID: batchEntityID(len(ids)),
		apply: func(ctx context.Context, tx *sql.Tx, vNew int64) ([]cp.DataPolicyChange, error) {
			removed, e := store.DeleteDataPoliciesBatch(ctx, tx, appID, ids)
			if e != nil {
				return nil, e
			}
			applied = len(removed)
			changes := make([]cp.DataPolicyChange, 0, len(removed))
			for _, id := range removed {
				changes = append(changes, cp.DataPolicyChange{Op: cp.ChangeRemove, Policy: cp.DataPolicy{ID: id}})
			}
			return changes, nil
		},
	})
	return d, applied, err
}
```

- [ ] **步骤 9：实现 `batchEntityID`（审计 entity_id 紧凑表示）在 `policy/bulk.go`**

```go
import "fmt"

// batchEntityID 给审计 entity_id 一个稳定的批量标记（不逐项，批是一个原子写单元）。
func batchEntityID(n int) string { return fmt.Sprintf("batch:%d", n) }
```

> 说明：`writeOp`/`writeOpData` 的 `entityID` 仅供审计标签（见 `manager.go` audit 钩子）。批量用 `batch:<count>`；如需 id 明细可后续增强，本轮 YAGNI。

- [ ] **步骤 10：运行 policy 测试确认通过**

运行：`go test ./internal/controlplane/policy/ -run TestBatch -tags testcontainers -v`
预期：PASS。

- [ ] **步骤 11：Commit**

```bash
git add internal/controlplane/policy/bulk.go internal/controlplane/policy/bulk_test.go
git commit -m "feat(policy): M4.2 PolicyManager 5 批量方法(各一 versioned write,返回 applied;data 走 data 变体)"
```

---

## 任务 3：mgmt handler + ruleTable +5 + 跨租户矩阵

**文件：**
- 修改：`internal/controlplane/mgmt/server.go`（+5 handler）
- 修改：`internal/controlplane/mgmt/authz.go`（`ruleTable` +5）
- 创建：`internal/controlplane/mgmt/bulk_test.go`

参考：`server.go:63-120`（单数移除 handler 范式）、`authz.go:45`（ruleTable）、`mgmt/policy_as_code_test.go`（跨租户矩阵 + testcontainers 夹具范式）。

- [ ] **步骤 1：ruleTable +5（authz.go，复用单数兄弟规则）**

在 `ruleTable` map 内 `"/sydom.admin.v1.AdminService/ImportAppPolicy": ...` 后加：

```go
	"/sydom.admin.v1.AdminService/BatchUnbindUserRole":        {"binding", "delete", true, scopeApp},
	"/sydom.admin.v1.AdminService/BatchRevokePermission":      {"grant", "delete", true, scopeApp},
	"/sydom.admin.v1.AdminService/BatchRemoveRoleInheritance": {"inheritance", "delete", true, scopeApp},
	"/sydom.admin.v1.AdminService/BatchDeleteRole":            {"role", "delete", true, scopeApp},
	"/sydom.admin.v1.AdminService/BatchDeleteDataPolicy":      {"data_policy", "delete", true, scopeApp},
```

- [ ] **步骤 2：写失败测试 `mgmt/bulk_test.go`**

复用 `policy_as_code_test.go` 的夹具（起 AdminServer + testcontainers + root/租户 seed）。覆盖：正常批量、空 items→InvalidArgument、超 1000→InvalidArgument、跨租户→PermissionDenied、path 权威（gRPC 无 path 覆写，故此处测 request app_id 与鉴权域一致；跨租户覆盖在任务 4 REST 层 path 权威）。

```go
func TestBatchDeleteRole_OK(t *testing.T) {
	env := newAdminTestEnv(t)                 // 复用 policy_as_code_test.go 夹具名
	appID := env.seedAppWithRoles(t, "iac:a", "iac:b")
	resp, err := env.srv.BatchDeleteRole(env.rootCtx, &adminv1.BatchDeleteRoleRequest{
		AppId: appID, RoleIds: env.roleIDs, // 两个存在的 role
	})
	if err != nil { t.Fatal(err) }
	if resp.Applied != 2 || resp.Requested != 2 || !resp.Changed { t.Fatalf("resp=%+v", resp) }
}

func TestBatchDeleteRole_Empty_InvalidArgument(t *testing.T) {
	env := newAdminTestEnv(t)
	appID := env.seedApp(t)
	_, err := env.srv.BatchDeleteRole(env.rootCtx, &adminv1.BatchDeleteRoleRequest{AppId: appID})
	if status.Code(err) != codes.InvalidArgument { t.Fatalf("code=%v want InvalidArgument", status.Code(err)) }
}

func TestBatchDeleteRole_CrossTenant_PermissionDenied(t *testing.T) {
	env := newAdminTestEnv(t)
	otherApp := env.seedAppInOtherTenant(t)   // 复用 policy_as_code_test.go 跨租户 seed
	_, err := env.srv.BatchDeleteRole(env.tenantACtx, &adminv1.BatchDeleteRoleRequest{
		AppId: otherApp, RoleIds: []int64{1},
	})
	if status.Code(err) != codes.PermissionDenied { t.Fatalf("code=%v want PermissionDenied", status.Code(err)) }
}
```

> 夹具名以 `policy_as_code_test.go` 实际为准。跨租户断言镜像该文件既有的 `TestPolicyAsCode_*CrossTenant*`。

- [ ] **步骤 3：运行确认失败**

运行：`go test ./internal/controlplane/mgmt/ -run TestBatch -tags testcontainers -v`
预期：FAIL（handler 未定义 / `AdminServer` 未实现接口 → 编译错）。

- [ ] **步骤 4：实现 5 handler（server.go，加在 `ImportAppPolicy` handler 附近）**

```go
const maxBatchItems = 1000

func batchResp(d *cp.Delta, applied, requested int, err error) (*adminv1.BatchWriteResponse, error) {
	if err != nil {
		return nil, status.Errorf(codes.Internal, "batch write: %v", err)
	}
	resp := &adminv1.BatchWriteResponse{
		Requested: uint32(requested),
		Applied:   uint32(applied),
	}
	if d != nil {
		resp.Version, resp.Changed = uint64(d.Version), true
	}
	return resp, nil
}

func (s *AdminServer) BatchUnbindUserRole(ctx context.Context, r *adminv1.BatchUnbindUserRoleRequest) (*adminv1.BatchWriteResponse, error) {
	if len(r.Items) == 0 || len(r.Items) > maxBatchItems {
		return nil, status.Errorf(codes.InvalidArgument, "items 数须在 1..%d", maxBatchItems)
	}
	pairs := make([]store.UserRolePair, len(r.Items))
	for i, it := range r.Items {
		pairs[i] = store.UserRolePair{UserID: it.UserId, RoleID: it.RoleId}
	}
	d, applied, err := s.mgr.BatchUnbindUserRole(ctx, int64(r.AppId), pairs)
	return batchResp(d, applied, len(r.Items), err)
}

func (s *AdminServer) BatchRevokePermission(ctx context.Context, r *adminv1.BatchRevokePermissionRequest) (*adminv1.BatchWriteResponse, error) {
	if len(r.Items) == 0 || len(r.Items) > maxBatchItems {
		return nil, status.Errorf(codes.InvalidArgument, "items 数须在 1..%d", maxBatchItems)
	}
	pairs := make([]store.GrantPair, len(r.Items))
	for i, it := range r.Items {
		pairs[i] = store.GrantPair{RoleID: it.RoleId, PermissionID: it.PermissionId}
	}
	d, applied, err := s.mgr.BatchRevokePermission(ctx, int64(r.AppId), pairs)
	return batchResp(d, applied, len(r.Items), err)
}

func (s *AdminServer) BatchRemoveRoleInheritance(ctx context.Context, r *adminv1.BatchRemoveRoleInheritanceRequest) (*adminv1.BatchWriteResponse, error) {
	if len(r.Items) == 0 || len(r.Items) > maxBatchItems {
		return nil, status.Errorf(codes.InvalidArgument, "items 数须在 1..%d", maxBatchItems)
	}
	pairs := make([]store.InheritancePair, len(r.Items))
	for i, it := range r.Items {
		pairs[i] = store.InheritancePair{ChildRoleID: it.ChildRoleId, ParentRoleID: it.ParentRoleId}
	}
	d, applied, err := s.mgr.BatchRemoveRoleInheritance(ctx, int64(r.AppId), pairs)
	return batchResp(d, applied, len(r.Items), err)
}

func (s *AdminServer) BatchDeleteRole(ctx context.Context, r *adminv1.BatchDeleteRoleRequest) (*adminv1.BatchWriteResponse, error) {
	if len(r.RoleIds) == 0 || len(r.RoleIds) > maxBatchItems {
		return nil, status.Errorf(codes.InvalidArgument, "role_ids 数须在 1..%d", maxBatchItems)
	}
	d, applied, err := s.mgr.BatchDeleteRole(ctx, int64(r.AppId), r.RoleIds)
	return batchResp(d, applied, len(r.RoleIds), err)
}

func (s *AdminServer) BatchDeleteDataPolicy(ctx context.Context, r *adminv1.BatchDeleteDataPolicyRequest) (*adminv1.BatchWriteResponse, error) {
	if len(r.DataPolicyIds) == 0 || len(r.DataPolicyIds) > maxBatchItems {
		return nil, status.Errorf(codes.InvalidArgument, "data_policy_ids 数须在 1..%d", maxBatchItems)
	}
	d, applied, err := s.mgr.BatchDeleteDataPolicy(ctx, int64(r.AppId), r.DataPolicyIds)
	return batchResp(d, applied, len(r.DataPolicyIds), err)
}
```

> 确认 `server.go` 已 import `store`（若未，加 `"<module>/internal/controlplane/store"`）。`cp.Delta` 已在包内可见（单数 handler 已用）。鉴权（AuthorizeRule scopeApp + CheckStatusWrite）由 gRPC 拦截器统一施加，handler 无需重复——与所有单数写 handler 一致。

- [ ] **步骤 5：运行确认通过 + 全包 vet**

运行：`go test ./internal/controlplane/mgmt/ -run TestBatch -tags testcontainers -v`
预期：PASS。
运行：`go vet ./...`
预期：干净（接口现已被 `AdminServer` 完整实现）。

- [ ] **步骤 6：Commit**

```bash
git add internal/controlplane/mgmt/server.go internal/controlplane/mgmt/authz.go internal/controlplane/mgmt/bulk_test.go
git commit -m "feat(mgmt): M4.2 5 批量 handler + ruleTable +5(复用单数规则)+ 空/超限 InvalidArgument + 跨租户矩阵"
```

---

## 任务 4：REST 5 路由（path 权威）

**文件：**
- 修改：`internal/controlplane/restgw/routes.go`（+5 路由 + 计数注释）
- 创建：`internal/controlplane/restgw/routes_bulk_test.go`

参考：`routes.go` 既有路由结构（`{"METHOD","/path", pfx+"RPC", decodeFn, callFn}`）、`routes.go:170/257/311/398/465`（单数 DELETE）、`routes_policy_as_code_test.go`（HMAC 测试夹具 + 未知 app→403 fail-close 范式）、M4.1 `queryBool`/path 权威覆写手法。

- [ ] **步骤 1：写失败测试 `routes_bulk_test.go`**

镜像 `routes_policy_as_code_test.go`：起 REST server + HMAC 客户端，POST batch，断言 200 + 计数；未知 app → 403 fail-close；无鉴权 → 401；path app_id 权威（body 带别的 app_id 被忽略）。

```go
func TestREST_BatchDeleteRole_OK(t *testing.T) {
	env := newRESTTestEnv(t)                    // 复用 routes_policy_as_code_test.go 夹具
	appID, roleIDs := env.seedAppWithRoles(t)
	body := map[string]any{"role_ids": roleIDs}
	resp := env.doHMAC(t, "POST", fmt.Sprintf("/v1/apps/%d/roles/batch-delete", appID), body)
	if resp.StatusCode != 200 { t.Fatalf("status=%d", resp.StatusCode) }
	// 断言响应含 applied==len(roleIDs)
}

func TestREST_BatchDeleteRole_UnknownApp_403(t *testing.T) {
	env := newRESTTestEnv(t)
	resp := env.doHMAC(t, "POST", "/v1/apps/424242/roles/batch-delete", map[string]any{"role_ids": []int64{1}})
	if resp.StatusCode != 403 { t.Fatalf("status=%d want 403(fail-close)", resp.StatusCode) }
}
```

- [ ] **步骤 2：运行确认失败**

运行：`go test ./internal/controlplane/restgw/ -run TestREST_Batch -tags testcontainers -v`
预期：FAIL（路由 404 / 未注册）。

- [ ] **步骤 3：实现 5 路由（routes.go，app 域路由组内，各资源 DELETE 单数附近）**

每条形如（以 BatchDeleteRole 为例，`app_id` **path 权威**覆写 body）：

```go
	{"POST", "/v1/apps/{app_id}/roles/batch-delete", pfx + "BatchDeleteRole",
		func(r *http.Request) (proto.Message, error) {
			id, err := pathUint64(r, "app_id")
			if err != nil {
				return nil, err
			}
			var body struct {
				RoleIds []int64 `json:"role_ids"`
			}
			if err := decodeBody(r, &body); err != nil {
				return nil, err
			}
			return &adminv1.BatchDeleteRoleRequest{AppId: id, RoleIds: body.RoleIds}, nil // app_id 取 path
		},
		func(ctx context.Context, m proto.Message) (proto.Message, error) {
			return s.BatchDeleteRole(ctx, m.(*adminv1.BatchDeleteRoleRequest))
		}},
```

五条路由与 RPC/资源段：

| 路由 | RPC | body |
|---|---|---|
| `POST /v1/apps/{app_id}/user-bindings/batch-delete` | `BatchUnbindUserRole` | `{"items":[{"user_id","role_id"}]}` |
| `POST /v1/apps/{app_id}/grants/batch-delete` | `BatchRevokePermission` | `{"items":[{"role_id","permission_id"}]}` |
| `POST /v1/apps/{app_id}/role-inheritances/batch-delete` | `BatchRemoveRoleInheritance` | `{"items":[{"child_role_id","parent_role_id"}]}` |
| `POST /v1/apps/{app_id}/roles/batch-delete` | `BatchDeleteRole` | `{"role_ids":[…]}` |
| `POST /v1/apps/{app_id}/data-policies/batch-delete` | `BatchDeleteDataPolicy` | `{"data_policy_ids":[…]}` |

> decode 的 body struct 各自定义（items 用中间 struct 转 `[]*adminv1.UserRoleRef` 等）。`pathUint64`/`decodeBody` 用 routes.go 既有 helper（若名不同照实际）。**app_id 恒取 path**，body 内即便含 app_id 也不读——path 权威。

- [ ] **步骤 4：更新路由计数注释**

routes.go 顶部/分组处若有「app 域 N 路由 / 全部 M 路由」计数注释，app 域 +5、全部 +5，改数字（照 M4.1 更新计数注释的做法）。

- [ ] **步骤 5：运行确认通过**

运行：`go test ./internal/controlplane/restgw/ -run TestREST_Batch -tags testcontainers -v`
预期：PASS。

- [ ] **步骤 6：Commit**

```bash
git add internal/controlplane/restgw/routes.go internal/controlplane/restgw/routes_bulk_test.go
git commit -m "feat(restgw): M4.2 5 批量删除路由(POST .../batch-delete,app_id path 权威,复用 AuthorizeRule)"
```

---

## 任务 5：Console 多选 UX + 批量 handler（无新 JS）

**文件：**
- 修改：`internal/controlplane/console/routes_rbac.go`（注册 4 路由 + 4 handler：roles/grants/inheritances/bindings）
- 修改：`internal/controlplane/console/routes_datapolicy.go`（注册 1 路由 + 1 handler：data-policies）
- 修改：`internal/controlplane/console/templates/{roles,grants,inheritances,bindings,datapolicies}.html`（行首复选框 + 批量表单）
- 创建：`internal/controlplane/console/bulk_test.go`

参考：`routes_rbac.go:280`（`removeInheritance` 单数删除 handler 范式）、`routes_datapolicy.go:79`（`deleteDataPolicy` + M3.4a `requireConfirm` 二次确认）、`console/flash.go`（flash 文案）、`console` 既有 `doWrite`/`requireConfirm`/`decode*` helper、M3.4a `ops_confirm.html`。

- [ ] **步骤 1：写失败测试 `console/bulk_test.go`**

镜像既有 Console 写测试（会话+CSRF+PRG，见 `routes_datapolicy` 测试或 `flash_test.go`）。覆盖：多选提交→二次确认页（缺 confirmed 不落库）、confirmed=1→BatchXxx→303 PRG+flash、勾选 0 项处理。

```go
func TestConsole_BatchDeleteRole_ConfirmGate(t *testing.T) {
	c := newConsoleTest(t)                       // 复用既有 Console 测试夹具
	appID, roleIDs := c.seedAppWithRoles(t)
	// 无 confirmed → 渲确认页，不落库
	resp := c.postForm(t, fmt.Sprintf("/apps/%d/roles/batch-delete", appID),
		url.Values{"ids": toStrs(roleIDs)})
	if resp.StatusCode != 200 { t.Fatalf("确认门应渲页 200,got %d", resp.StatusCode) }
	if c.rolesCount(t, appID) != len(roleIDs) { t.Fatal("未确认不应删") }
	// confirmed=1 → 删 + 303
	v := url.Values{"ids": toStrs(roleIDs)}; v.Set("confirmed", "1")
	resp2 := c.postForm(t, fmt.Sprintf("/apps/%d/roles/batch-delete", appID), v)
	if resp2.StatusCode != 303 { t.Fatalf("PRG 期望 303,got %d", resp2.StatusCode) }
	if c.rolesCount(t, appID) != 0 { t.Fatal("确认后应删空") }
}
```

- [ ] **步骤 2：运行确认失败**

运行：`go test ./internal/controlplane/console/ -run TestConsole_Batch -v`
预期：FAIL（路由未注册）。

- [ ] **步骤 3：实现批量 handler（routes_rbac.go，镜像单数 `removeInheritance` + requireConfirm）**

以 `batchDeleteRole` 为例（其余 4 个同构，仅 decode 的 ids 类型与 mgr 调用不同）：

```go
func (h *Handler) batchDeleteRole(w http.ResponseWriter, r *http.Request) {
	if !h.requireConfirm(w, r, "确认批量移除选中的角色？将一并移除其授权与绑定。") {
		return // requireConfirm 已渲服务端确认页(回显 ids 隐藏件)
	}
	h.doWrite(w, r, func(ctx context.Context, appID uint64) (uint64, error) {
		ids := parseInt64s(r.PostForm["ids"]) // 复用/新增小 helper
		if len(ids) == 0 {
			return 0, errNoSelection        // 映射为友好提示,不调 RPC
		}
		resp, err := h.admin.BatchDeleteRole(ctx, &adminv1.BatchDeleteRoleRequest{AppId: appID, RoleIds: ids})
		if err != nil {
			return 0, err
		}
		return resp.Version, nil
	}, flashBatchRemoved) // flash「已移除选中项」
}
```

> 对齐既有 `doWrite` 签名（看 `routes_datapolicy.go:79` `deleteDataPolicy` 实际用法：session→CSRF→AuthorizeRule→CheckStatusWrite→RPC→PRG）。`requireConfirm` 用法照 M3.4a 既有调用（`deleteDataPolicy`/`deleteRole` 已用）。`parseInt64s`/`flashBatchRemoved`/`errNoSelection` 为本任务新增小 helper（parseInt64s 忽略非法项；空选 → 渲提示不 500）。bindings 批量 decode `ids` 为 `user_id:role_id` 复合串（表单 checkbox value 编码），解析回 `[]*adminv1.UserRoleRef`；grants/inheritances 同理复合串。

- [ ] **步骤 4：注册 5 路由**

`routes_rbac.go` 的 `registerRBAC`（或对应 register 函数）内，各 list 页附近加：

```go
	mux.HandleFunc("POST /apps/{app_id}/roles/batch-delete", h.batchDeleteRole)
	mux.HandleFunc("POST /apps/{app_id}/grants/batch-delete", h.batchRevokePermission)
	mux.HandleFunc("POST /apps/{app_id}/inheritances/batch-delete", h.batchRemoveInheritance)
	mux.HandleFunc("POST /apps/{app_id}/bindings/batch-delete", h.batchUnbindUser)
```

`routes_datapolicy.go` 的 `registerDataPolicy` 内加：

```go
	mux.HandleFunc("POST /apps/{app_id}/data-policies/batch-delete", h.batchDeleteDataPolicy)
```

- [ ] **步骤 5：模板加行首复选框 + 批量表单（5 个 .html）**

每个 list 模板：把行表格包进一个 `<form method="post" action="{{...}}/batch-delete">`，行首列加复选框，表尾加提交按钮 + `data-confirm`。以 `roles.html` 为例：

```html
<form method="post" action="/apps/{{.AppID}}/roles/batch-delete">
  <input type="hidden" name="csrf_token" value="{{.CSRF}}">
  <table class="table">
    <thead><tr><th class="visually-hidden">选择</th><th>角色</th>…</tr></thead>
    <tbody>
    {{range .Roles}}
      <tr>
        <td><input type="checkbox" name="ids" value="{{.ID}}" aria-label="选择角色 {{.Name}}"></td>
        <td>{{.Name}}</td>…
      </tr>
    {{end}}
    </tbody>
  </table>
  <button type="submit" class="btn danger" data-confirm="确认批量移除选中的角色？">批量移除选中</button>
</form>
```

> bindings/grants/inheritances 的 checkbox value 用复合串（如 `{{.UserID}}:{{.RoleID}}`），handler 解析。datapolicies 同 roles（裸 id）。复用 M3.1 设计系统类（`.table`/`.btn`/`.danger`/`.visually-hidden`）；`aria-label` 关联；**无 `<script>`**。确认沿用 M3.4a：无 JS 时 `requireConfirm` 渲服务端确认页，有 JS 时 `data-confirm` 弹窗（interactions.js 既有，不新增）。

- [ ] **步骤 6：flash 文案（flash.go）**

`flash.go` 加批量移除的静态业务文案（照既有 flash map 风格）：

```go
	// 批量移除成功
	"batch-removed": "已移除选中项",
```

> 具体键名/机制对齐 `flash.go` 既有实现（`flashFor`/`SetFlash`）。

- [ ] **步骤 7：运行确认通过 + a11y 结构测试**

运行：`go test ./internal/controlplane/console/ -run TestConsole_Batch -v`
预期：PASS。

- [ ] **步骤 8：Commit**

```bash
git add internal/controlplane/console/
git commit -m "feat(console): M4.2 5 列表页多选 + 批量移除 handler(requireConfirm 二次确认+PRG,无新 JS)"
```

---

## 任务 6：整体核验 BC-1..8 + 走查 + opus 评审 + FF

**文件：** 无代码改动（除走查涌现修复）；产出走查记录 `docs/superpowers/2026-07-04-m4-2-bulk-operations-walkthrough.md`。

- [ ] **步骤 1：BC 零触碰核验**

运行：
```bash
BASE=$(git merge-base main HEAD)
git diff $BASE..HEAD -- casbin/enforcer.go internal/controlplane/adminauthz/ internal/sidecar/ internal/kernel/ | wc -l   # 期望 0
git diff $BASE..HEAD -- internal/controlplane/mgmt/authz.go | grep -c '^+.*ruleTable\|^+\s*"/sydom'                        # 期望仅 +5 Batch 行
```
预期：授权核心 diff=0；authz.go 仅 +5 ruleTable 行；M1.1 matcher（enforcer.go）一字未改。

- [ ] **步骤 2：全量验证**

运行：
```bash
gofmt -l internal/ api/                       # 期望空
go vet ./...                                  # 期望干净
make proto-check                              # 期望零漂移
go test ./... -tags testcontainers            # 期望 0 FAIL(含 e2e)
```
预期：全绿。任一 gofmt 未对齐 → `gofmt -w` 独立提交。

- [ ] **步骤 3：真实浏览器 axe 走查**

一次性 build-tag `walkthrough` 脚手架（testcontainers PG+Redis、root 超管、`SeedApp` + seed 多角色/绑定/授权/继承/数据策略使 5 个列表页非空、会话 TTL `time.Hour`、URL `os.WriteFile` 传文件）+ 系统 Chrome via Playwright MCP + axe-core 4.10.2 页内注入。逐页：5 个列表页各 axe 0 违规 + 单 h1 + breadcrumb + 复选框 `aria-label`；勾选 2 行 → 提交 → 服务端确认页（显「将移除 N 项」）axe 0 违规 → confirmed 后 PRG + flash。**走查纪律**：停后台进程按确切 PID（非 `pkill -f` 防自杀）；脚手架/axe 静态服务走查后删除未提交。记录到 walkthrough.md 并 commit。

- [ ] **步骤 4：opus 整体安全评审**

派 opus 子代理（或控制者 inline）对全 diff 做整体评审，逐条核验 BC-1..8。重点：原子回滚（一坏项整批无落库）、no-op 语义（含 data-policy 始终 bump 例外的正确性）、跨租户 fail-close、source-blind、授权同构无第二套判定、path 权威、无 secret 泄露。产出 READY 或阻断项清单。

- [ ] **步骤 5：更新记忆**

`project_detailed_design_progress.md` 加 M4.2 节；`MEMORY.md` 索引 M4 条目下 M4.2 标 ✅（保持一行、明细入 detailed 文件）。

- [ ] **步骤 6：FF 并入本地 main + 问用户 push**

```bash
git -C /home/tongyu/codes/Sydom merge --ff-only worktree-feat+m4-2-bulk-operations
# 核实 main==feature tip；push origin 与否问用户(本轮已建立 push 习惯)
```
清理 worktree（在主 checkout：`git worktree remove`）。

---

## 自检（写完计划后，全新视角对照）

**1. 规格覆盖度：**
- 规格 §3 API 契约 → 任务 1 ✅
- §4 BC-1..8（原子/no-op/无冲突/租户隔离/status/source-blind/授权核心零触碰/dedup）→ 任务 2（原子/no-op/source-blind/级联）+ 任务 3（ruleTable/租户/空超限）+ 任务 6（BC-7 核验）✅
- §4 数据策略 no-op 例外（始终 bump）→ 任务 2 步骤 8 `BatchDeleteDataPolicy` + 步骤 6 测试 `TestBatchDeleteDataPolicy_AlwaysBumps` ✅
- §5 三面 parity → 任务 3（gRPC）+ 任务 4（REST）+ 任务 5（Console）✅
- §6 无新 JS + requireConfirm → 任务 5 步骤 3/5 ✅
- §7 测试策略 → 各任务 TDD 步骤 ✅
- §8 任务分解 → 6 任务 ✅

**2. 占位符扫描：** 无「待定/TODO」；每步含实际代码/命令/预期。夹具函数名标注「以现有测试为准」是刻意的（实现者对齐真实夹具），非占位。

**3. 类型一致性：**
- `store.UserRolePair`/`GrantPair`/`InheritancePair`（任务 2a 定义）→ 任务 2b manager + 任务 3 handler 一致引用 ✅
- 批量 manager 返回 `(*cp.Delta, int, error)`（任务 2b）→ 任务 3 handler `d, applied, err :=` 一致 ✅
- `adminv1.BatchWriteResponse{Version,Requested,Applied,Changed}`（任务 1）→ 任务 3 `batchResp` 一致 ✅
- `adminv1.UserRoleRef{UserId,RoleId}` 等（任务 1 proto 生成的 Go 字段名 PascalCase）→ 任务 3 `it.UserId/it.RoleId` 一致 ✅

对照无缺口。
