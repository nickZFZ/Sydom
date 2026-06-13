package adminauthz

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
)

// 成员档位（账户层 tenant_membership.tier）。本期签发 owner/admin；member 预留不签发。
const (
	TierOwner  int16 = 1
	TierAdmin  int16 = 2
	TierMember int16 = 3 // 预留，本期不签发
)

// Membership 是运营者在某租户的归属。
type Membership struct {
	TenantID   int64
	TenantName string
	Tier       int16
}

// InsertMembership 写一行 membership，返回是否真正插入（ON CONFLICT DO NOTHING → false）。
func InsertMembership(ctx context.Context, q cp.DBTX, tenantID, operatorID int64, tier int16) (bool, error) {
	res, err := q.ExecContext(ctx,
		`INSERT INTO tenant_membership (tenant_id, operator_id, tier) VALUES ($1,$2,$3)
		 ON CONFLICT (tenant_id, operator_id) DO NOTHING`, tenantID, operatorID, tier)
	if err != nil {
		return false, fmt.Errorf("adminauthz: insert membership: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("adminauthz: membership rows affected: %w", err)
	}
	return n > 0, nil
}

// TenantsOfOperator 返回 principal 的全部租户归属（按 tenant id 排序）。
func TenantsOfOperator(ctx context.Context, q cp.DBTX, principal string) ([]Membership, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT t.id, t.name, m.tier
		 FROM tenant_membership m
		 JOIN tenant t          ON t.id = m.tenant_id
		 JOIN admin_operator o  ON o.id = m.operator_id
		 WHERE o.principal = $1
		 ORDER BY t.id`, principal)
	if err != nil {
		return nil, fmt.Errorf("adminauthz: tenants of operator: %w", err)
	}
	defer rows.Close()
	var out []Membership
	for rows.Next() {
		var m Membership
		if err := rows.Scan(&m.TenantID, &m.TenantName, &m.Tier); err != nil {
			return nil, fmt.Errorf("adminauthz: scan membership: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("adminauthz: tenants of operator: %w", err)
	}
	return out, nil
}

// IsOperatingPlane 判定 principal 是否运营平面（超管）：在 "*" 域有任一角色绑定。
// 仅作 UI 提示（非授权决策；真正 enforce 仍在各 RPC）；与 DB 真相源一致。
func IsOperatingPlane(ctx context.Context, q cp.DBTX, principal string) (bool, error) {
	var exists bool
	err := q.QueryRowContext(ctx,
		`SELECT EXISTS (
		   SELECT 1 FROM admin_subject_role sr
		   JOIN admin_operator o ON o.id = sr.operator_id
		   WHERE o.principal = $1 AND sr.domain = '*')`, principal).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("adminauthz: is operating plane: %w", err)
	}
	return exists, nil
}

// EnsureOperator 幂等取/建 operator，返回 (id, created)。created=false 表示已存在（不覆盖凭据）。
// secretEnc 仅在新建时使用。供 RPC handler（自带 masterKey 加密后传入）复用。
func EnsureOperator(ctx context.Context, q cp.DBTX, principal string, secretEnc []byte) (int64, bool, error) {
	var id int64
	err := q.QueryRowContext(ctx, `SELECT id FROM admin_operator WHERE principal=$1`, principal).Scan(&id)
	if err == nil {
		return id, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false, fmt.Errorf("adminauthz: find operator: %w", err)
	}
	id, err = InsertOperator(ctx, q, principal, secretEnc)
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

// BindTenantAdminTx 在 t:<tenantID> 域把 operator 绑定为租户管理员：
// 取/建角色 tenant-admin-<id> + 授单条通配 (t:<id>,*,*) + 绑定 operator→角色@t:<id>。
// 不建 operator、不写 membership、不 bump（由调用方在同一事务统筹）。
func BindTenantAdminTx(ctx context.Context, q cp.DBTX, tenantID, operatorID int64) error {
	code := fmt.Sprintf("tenant-admin-%d", tenantID)
	var roleID int64
	err := q.QueryRowContext(ctx, `SELECT id FROM admin_role WHERE code=$1`, code).Scan(&roleID)
	if errors.Is(err, sql.ErrNoRows) {
		roleID, err = InsertRole(ctx, q, code, fmt.Sprintf("租户%d管理员", tenantID))
		if err != nil {
			return err
		}
	} else if err != nil {
		return fmt.Errorf("adminauthz: find tenant-admin role: %w", err)
	}
	dom := TenantDomain(tenantID)
	if err := InsertRoleGrant(ctx, q, roleID, dom, "*", "*"); err != nil {
		return err
	}
	return InsertSubjectRole(ctx, q, operatorID, roleID, dom)
}
