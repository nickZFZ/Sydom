package store

import (
	"context"

	"github.com/lib/pq"
	cp "github.com/nickZFZ/Sydom/internal/controlplane"
)

// UserRolePair 供批量解绑；(user_id, role_id) 复合身份。
type UserRolePair struct {
	UserID string
	RoleID int64
}

// GrantPair 供批量撤权；(role_id, permission_id)。
type GrantPair struct {
	RoleID       int64
	PermissionID int64
}

// InheritancePair 供批量移除继承；(child_role_id, parent_role_id)。
type InheritancePair struct {
	ChildRoleID  int64
	ParentRoleID int64
}

// DeleteRolesBatch 级联批量删角色（对齐单数 DeleteRole 的级联语句，均改 ANY($2)），
// source-blind：按所给 id 精确删除，不按 source 过滤。
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

// DeleteDataPoliciesBatch 批量删数据策略；RETURNING id 回传实际删除的 id（供 data 面 ChangeRemove 逐条构造）。
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
