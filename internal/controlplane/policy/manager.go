// Package policy 编排控制面的版本号写事务，对外暴露策略写方法，返回领域 Delta。
package policy

import (
	"context"
	"database/sql"
	"fmt"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/projection"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
)

// PolicyManager 是控制面真相源写入引擎。
type PolicyManager struct {
	db *sql.DB
}

// NewPolicyManager 构造 PolicyManager。
func NewPolicyManager(db *sql.DB) *PolicyManager {
	return &PolicyManager{db: db}
}

// writeOp 描述一次版本化写：审计元信息 + 业务表变更闭包 + 可选的 data 变更产出。
type writeOp struct {
	action     string
	entityType string
	entityID   string
	// mutate 在事务内执行业务表 CUD；返回本次的 data_policy 变更（功能权限类返回 nil）。
	mutate func(ctx context.Context, tx *sql.Tx) ([]cp.DataPolicyChange, error)
	// preCheck 在加锁后、mutate 前执行（如环检测）；可为 nil。
	preCheck func(ctx context.Context, tx *sql.Tx) error
}

// runVersionedWrite 是 spec §6 统一写事务模板。
func (m *PolicyManager) runVersionedWrite(ctx context.Context, appID int64, op writeOp) (*cp.Delta, error) {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("policy: begin tx: %w", err)
	}
	defer tx.Rollback() // COMMIT 成功后再次 Rollback 是 no-op；失败路径确保回滚

	// 1. 行锁串行化本 app
	cur, err := store.LockAppVersion(ctx, tx, appID)
	if err != nil {
		return nil, fmt.Errorf("policy: lock app %d version: %w", appID, err)
	}
	// 2. 前置校验（环检测等）
	if op.preCheck != nil {
		if err := op.preCheck(ctx, tx); err != nil {
			return nil, fmt.Errorf("policy: precheck %s: %w", op.action, err)
		}
	}
	// 3. 业务表 CUD
	dataChanges, err := op.mutate(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("policy: mutate %s: %w", op.action, err)
	}
	// 4. 重投影 + diff
	desired, err := projection.ProjectApp(ctx, tx, appID)
	if err != nil {
		return nil, fmt.Errorf("policy: reproject app %d: %w", appID, err)
	}
	current, err := store.ReadAppRules(ctx, tx, appID)
	if err != nil {
		return nil, fmt.Errorf("policy: read current rules app %d: %w", appID, err)
	}
	adds, removes := projection.Diff(current, desired)

	// 5. 无策略影响 → COMMIT 业务态，不 bump、不 audit、返回 nil
	if len(adds) == 0 && len(removes) == 0 && len(dataChanges) == 0 {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("policy: commit no-op %s: %w", op.action, err)
		}
		return nil, nil
	}

	// 6. bump 版本、写 casbin_rule、写 audit
	vNew := cur + 1
	if err := store.ApplyDiff(ctx, tx, appID, adds, removes, vNew); err != nil {
		return nil, fmt.Errorf("policy: apply diff app %d v%d: %w", appID, vNew, err)
	}
	if err := store.BumpAppVersion(ctx, tx, appID, vNew); err != nil {
		return nil, fmt.Errorf("policy: bump app %d to v%d: %w", appID, vNew, err)
	}
	if err := store.InsertAudit(ctx, tx, appID,
		cp.OperatorFromContext(ctx), op.action, op.entityType, op.entityID, vNew); err != nil {
		return nil, fmt.Errorf("policy: audit %s v%d: %w", op.action, vNew, err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("policy: commit %s v%d: %w", op.action, vNew, err)
	}
	return &cp.Delta{
		AppID: appID, Version: vNew,
		RuleAdds: adds, RuleRemoves: removes, DataChanges: dataChanges,
	}, nil
}

// GrantPermission 给角色授予权限点（幂等）。
// 契约：幂等命中或无策略影响时返回 (nil, nil)——业务态已持久化但无需下发；
// 出错时返回 (nil, err)。调用方据 (err==nil && delta==nil) 判定"无变更、不下发"。
func (m *PolicyManager) GrantPermission(ctx context.Context, appID, roleID, permID int64, eft string) (*cp.Delta, error) {
	return m.runVersionedWrite(ctx, appID, writeOp{
		action: "grant", entityType: "role_permission", entityID: fmt.Sprintf("%d:%d", roleID, permID),
		mutate: func(ctx context.Context, tx *sql.Tx) ([]cp.DataPolicyChange, error) {
			return nil, store.InsertRolePermission(ctx, tx, appID, roleID, permID, eft)
		},
	})
}

// RevokePermission 撤销角色的权限点。
// 契约同 GrantPermission：无策略影响（如该授权本不存在）返回 (nil, nil)。
func (m *PolicyManager) RevokePermission(ctx context.Context, appID, roleID, permID int64) (*cp.Delta, error) {
	return m.runVersionedWrite(ctx, appID, writeOp{
		action: "revoke", entityType: "role_permission", entityID: fmt.Sprintf("%d:%d", roleID, permID),
		mutate: func(ctx context.Context, tx *sql.Tx) ([]cp.DataPolicyChange, error) {
			return nil, store.DeleteRolePermission(ctx, tx, appID, roleID, permID)
		},
	})
}

// CreateRole 建角色。建角色本身不产生 casbin_rule（无绑定/授权），故通常返回 nil Delta。
func (m *PolicyManager) CreateRole(ctx context.Context, appID int64, code, name string) (roleID int64, d *cp.Delta, err error) {
	d, err = m.runVersionedWrite(ctx, appID, writeOp{
		action: "create_role", entityType: "role", entityID: code,
		mutate: func(ctx context.Context, tx *sql.Tx) ([]cp.DataPolicyChange, error) {
			id, e := store.InsertRole(ctx, tx, appID, code, name)
			roleID = id
			return nil, e
		},
	})
	return roleID, d, err
}

// DeleteRole 删角色（级联删其全部引用），重投影会清掉相关 casbin_rule 行。
func (m *PolicyManager) DeleteRole(ctx context.Context, appID, roleID int64) (*cp.Delta, error) {
	return m.runVersionedWrite(ctx, appID, writeOp{
		action: "delete_role", entityType: "role", entityID: fmt.Sprintf("%d", roleID),
		mutate: func(ctx context.Context, tx *sql.Tx) ([]cp.DataPolicyChange, error) {
			return nil, store.DeleteRole(ctx, tx, appID, roleID)
		},
	})
}

// UpsertPermission 幂等注册权限点。仅注册不授权时不产生 casbin_rule。
func (m *PolicyManager) UpsertPermission(ctx context.Context, appID int64, code, resource, action, ptype, name string) (permID int64, d *cp.Delta, err error) {
	d, err = m.runVersionedWrite(ctx, appID, writeOp{
		action: "upsert_permission", entityType: "permission", entityID: code,
		mutate: func(ctx context.Context, tx *sql.Tx) ([]cp.DataPolicyChange, error) {
			id, e := store.UpsertPermission(ctx, tx, appID, code, resource, action, ptype, name)
			permID = id
			return nil, e
		},
	})
	return permID, d, err
}

// AddRoleInheritance 加角色继承边（child 继承 parent），加锁后先做环检测。
func (m *PolicyManager) AddRoleInheritance(ctx context.Context, appID, childID, parentID int64) (*cp.Delta, error) {
	return m.runVersionedWrite(ctx, appID, writeOp{
		action: "add_inheritance", entityType: "role_inheritance", entityID: fmt.Sprintf("%d->%d", childID, parentID),
		preCheck: func(ctx context.Context, tx *sql.Tx) error {
			return projection.CheckNoCycle(ctx, tx, appID, childID, parentID)
		},
		mutate: func(ctx context.Context, tx *sql.Tx) ([]cp.DataPolicyChange, error) {
			return nil, store.InsertRoleInheritance(ctx, tx, appID, childID, parentID)
		},
	})
}

// RemoveRoleInheritance 删角色继承边。
func (m *PolicyManager) RemoveRoleInheritance(ctx context.Context, appID, childID, parentID int64) (*cp.Delta, error) {
	return m.runVersionedWrite(ctx, appID, writeOp{
		action: "remove_inheritance", entityType: "role_inheritance", entityID: fmt.Sprintf("%d->%d", childID, parentID),
		mutate: func(ctx context.Context, tx *sql.Tx) ([]cp.DataPolicyChange, error) {
			return nil, store.DeleteRoleInheritance(ctx, tx, appID, childID, parentID)
		},
	})
}

// BindUserRole 绑定用户到角色（幂等）。
func (m *PolicyManager) BindUserRole(ctx context.Context, appID int64, userID string, roleID int64) (*cp.Delta, error) {
	return m.runVersionedWrite(ctx, appID, writeOp{
		action: "bind_user", entityType: "user_role_binding", entityID: fmt.Sprintf("%s:%d", userID, roleID),
		mutate: func(ctx context.Context, tx *sql.Tx) ([]cp.DataPolicyChange, error) {
			return nil, store.InsertUserRoleBinding(ctx, tx, appID, userID, roleID)
		},
	})
}

// UnbindUserRole 解绑用户角色。
func (m *PolicyManager) UnbindUserRole(ctx context.Context, appID int64, userID string, roleID int64) (*cp.Delta, error) {
	return m.runVersionedWrite(ctx, appID, writeOp{
		action: "unbind_user", entityType: "user_role_binding", entityID: fmt.Sprintf("%s:%d", userID, roleID),
		mutate: func(ctx context.Context, tx *sql.Tx) ([]cp.DataPolicyChange, error) {
			return nil, store.DeleteUserRoleBinding(ctx, tx, appID, userID, roleID)
		},
	})
}
