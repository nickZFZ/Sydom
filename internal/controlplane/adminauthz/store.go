// Package adminauthz 实现控制面管理鉴权：独立 casbin RBAC-with-domain enforcer、
// 操作者凭据解析与 bootstrap。与 ③-1 业务策略投影完全分离。
package adminauthz

import (
	"context"
	"errors"
	"fmt"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
)

// ErrNotFound 表示 Delete* 未命中任何行。fail-close：撤不存在的授权/绑定时，
// 上层据此映射 NotFound、回滚事务、绝不 bump 版本（防幽灵 delta / 版本跳变）。
var ErrNotFound = errors.New("adminauthz: not found")

// InsertOperator 建管理操作者，返回 id。secretEnc 为已加密的凭据字节。
func InsertOperator(ctx context.Context, q cp.DBTX, principal string, secretEnc []byte) (int64, error) {
	var id int64
	err := q.QueryRowContext(ctx,
		`INSERT INTO admin_operator (principal, secret_enc) VALUES ($1,$2) RETURNING id`,
		principal, secretEnc).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("adminauthz: insert operator: %w", err)
	}
	return id, nil
}

// InsertRole 建管理角色，返回 id。
func InsertRole(ctx context.Context, q cp.DBTX, code, name string) (int64, error) {
	var id int64
	err := q.QueryRowContext(ctx,
		`INSERT INTO admin_role (code, name) VALUES ($1,$2) RETURNING id`, code, name).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("adminauthz: insert role: %w", err)
	}
	return id, nil
}

// InsertRoleGrant 给角色加一条管理权（casbin p 行）。
func InsertRoleGrant(ctx context.Context, q cp.DBTX, roleID int64, domain, resource, action string) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO admin_role_grant (role_id, domain, resource, action) VALUES ($1,$2,$3,$4)
		 ON CONFLICT (role_id, domain, resource, action) DO NOTHING`,
		roleID, domain, resource, action)
	if err != nil {
		return fmt.Errorf("adminauthz: insert role grant: %w", err)
	}
	return nil
}

// InsertSubjectRole 绑定操作者到角色（casbin g 行）。
func InsertSubjectRole(ctx context.Context, q cp.DBTX, operatorID, roleID int64, domain string) error {
	_, err := q.ExecContext(ctx,
		`INSERT INTO admin_subject_role (operator_id, role_id, domain) VALUES ($1,$2,$3)
		 ON CONFLICT (operator_id, role_id, domain) DO NOTHING`,
		operatorID, roleID, domain)
	if err != nil {
		return fmt.Errorf("adminauthz: insert subject role: %w", err)
	}
	return nil
}

// DeleteRoleGrant 撤角色一条管理权（casbin p 行），镜像 InsertRoleGrant。
// 命中 0 行 → ErrNotFound（不静默）。
func DeleteRoleGrant(ctx context.Context, q cp.DBTX, roleID int64, domain, resource, action string) error {
	res, err := q.ExecContext(ctx,
		`DELETE FROM admin_role_grant WHERE role_id=$1 AND domain=$2 AND resource=$3 AND action=$4`,
		roleID, domain, resource, action)
	if err != nil {
		return fmt.Errorf("adminauthz: delete role grant: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("adminauthz: delete role grant rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteSubjectRole 解绑操作者与角色（casbin g 行），镜像 InsertSubjectRole。
// 命中 0 行 → ErrNotFound。
func DeleteSubjectRole(ctx context.Context, q cp.DBTX, operatorID, roleID int64, domain string) error {
	res, err := q.ExecContext(ctx,
		`DELETE FROM admin_subject_role WHERE operator_id=$1 AND role_id=$2 AND domain=$3`,
		operatorID, roleID, domain)
	if err != nil {
		return fmt.Errorf("adminauthz: delete subject role: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("adminauthz: delete subject role rows affected: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// LoadPolicyRows 读全部 p 行：[role_code, domain, resource, action]。
func LoadPolicyRows(ctx context.Context, q cp.DBTX) ([][]string, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT r.code, g.domain, g.resource, g.action
		 FROM admin_role_grant g JOIN admin_role r ON r.id = g.role_id`)
	if err != nil {
		return nil, fmt.Errorf("adminauthz: load policy rows: %w", err)
	}
	defer rows.Close()
	var out [][]string
	for rows.Next() {
		var code, domain, resource, action string
		if err := rows.Scan(&code, &domain, &resource, &action); err != nil {
			return nil, fmt.Errorf("adminauthz: scan policy row: %w", err)
		}
		out = append(out, []string{code, domain, resource, action})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("adminauthz: load policy rows: %w", err)
	}
	return out, nil
}

// LoadGroupingRows 读全部 g 行：[operator_principal, role_code, domain]。
func LoadGroupingRows(ctx context.Context, q cp.DBTX) ([][]string, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT o.principal, r.code, sr.domain
		 FROM admin_subject_role sr
		 JOIN admin_operator o ON o.id = sr.operator_id
		 JOIN admin_role r     ON r.id = sr.role_id`)
	if err != nil {
		return nil, fmt.Errorf("adminauthz: load grouping rows: %w", err)
	}
	defer rows.Close()
	var out [][]string
	for rows.Next() {
		var principal, code, domain string
		if err := rows.Scan(&principal, &code, &domain); err != nil {
			return nil, fmt.Errorf("adminauthz: scan grouping row: %w", err)
		}
		out = append(out, []string{principal, code, domain})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("adminauthz: load grouping rows: %w", err)
	}
	return out, nil
}

// ReadPolicyVersion 读 admin 策略版本（单调递增）。
func ReadPolicyVersion(ctx context.Context, q cp.DBTX) (int64, error) {
	var v int64
	if err := q.QueryRowContext(ctx,
		`SELECT version FROM admin_policy_version WHERE id=1`).Scan(&v); err != nil {
		return 0, fmt.Errorf("adminauthz: read policy version: %w", err)
	}
	return v, nil
}

// BumpPolicyVersion 自增 admin 策略版本（任何 admin 写后调用，触发 enforcer 重载）。
func BumpPolicyVersion(ctx context.Context, q cp.DBTX) error {
	res, err := q.ExecContext(ctx,
		`UPDATE admin_policy_version SET version = version + 1 WHERE id=1`)
	if err != nil {
		return fmt.Errorf("adminauthz: bump policy version: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("adminauthz: bump policy version rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("adminauthz: bump policy version: no version row")
	}
	return nil
}
