package policy

import (
	"context"
	"database/sql"
	"fmt"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
)

// batchEntityID 给审计 entity_id 一个稳定的批量标记（不逐项，批是一个原子写单元）。
func batchEntityID(n int) string { return fmt.Sprintf("batch:%d", n) }

// BatchUnbindUserRole 原子批量解绑：一个 versioned write 内一条 set-based DELETE。
// 全 no-op（所给 pair 均不存在）→ runVersionedWrite 因 diff 为空返回 nil Delta，版本不变。
// source-blind：按所给 pair 精确匹配删除，不按 source 过滤。
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

// BatchRevokePermission 原子批量撤权：一个 versioned write 内一条 set-based DELETE。
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

// BatchRemoveRoleInheritance 原子批量移除继承边：一个 versioned write 内一条 set-based DELETE。
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

// BatchDeleteRole 原子批量删角色（级联）：一个 versioned write 内一条 set-based DELETE。
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

// BatchDeleteDataPolicy 批量删数据策略；走 data 写变体，与单数 DeleteDataPolicy 一致地
// 非空输入即始终 bump（data 变更即策略变更，与"无投影影响不 bump"的 casbin 写不同）——
// runVersionedWriteData 本身无 diff 判定，无论 ids 内几个真实存在都会 bump。
// 空输入（ids 为空切片）显式短路为 no-op：不进事务、不 bump、返回 (nil,0,nil)，
// 对齐其余 4 个批量方法"空批不产生任何效果"的直觉（它们靠 diff 为空自然达成，
// data 写变体无该天然兜底，故此处显式加一道）。
// applied 为实际删除数（RETURNING id 的行数，不存在的 id 静默跳过、非错误）。
func (m *PolicyManager) BatchDeleteDataPolicy(ctx context.Context, appID int64, ids []int64) (*cp.Delta, int, error) {
	if len(ids) == 0 {
		return nil, 0, nil
	}
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
