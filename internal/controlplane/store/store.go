// Package store 封装控制面对数据库的访问：casbin_rule DAO、版本行锁、audit、业务表 CUD。
// 所有函数接受 cp.DBTX，可在事务内调用。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
)

// ReadAppRules 读取某 app 当前全部 casbin_rule 行（diff 基准 / 快照来源）。
func ReadAppRules(ctx context.Context, q cp.DBTX, appID int64) ([]cp.Rule, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT ptype, v0, v1, v2, v3, v4, v5 FROM casbin_rule WHERE app_id = $1`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []cp.Rule
	for rows.Next() {
		var r cp.Rule
		if err := rows.Scan(&r.Ptype, &r.V[0], &r.V[1], &r.V[2], &r.V[3], &r.V[4], &r.V[5]); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ApplyDiff 按 diff 删除 removes、插入 adds（version 标为 version）。
func ApplyDiff(ctx context.Context, ex cp.DBTX, appID int64, adds, removes []cp.Rule, version int64) error {
	for _, r := range removes {
		if _, err := ex.ExecContext(ctx, `
			DELETE FROM casbin_rule
			WHERE app_id=$1 AND ptype=$2 AND v0=$3 AND v1=$4 AND v2=$5 AND v3=$6 AND v4=$7 AND v5=$8`,
			appID, r.Ptype, r.V[0], r.V[1], r.V[2], r.V[3], r.V[4], r.V[5]); err != nil {
			return err
		}
	}
	for _, r := range adds {
		if _, err := ex.ExecContext(ctx, `
			INSERT INTO casbin_rule (app_id, ptype, v0, v1, v2, v3, v4, v5, version)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			appID, r.Ptype, r.V[0], r.V[1], r.V[2], r.V[3], r.V[4], r.V[5], version); err != nil {
			return err
		}
	}
	return nil
}

// LockAppVersion 以 FOR UPDATE 行锁读取并返回 app 当前版本号（串行化本 app 变更）。
func LockAppVersion(ctx context.Context, ex cp.DBTX, appID int64) (int64, error) {
	var v int64
	err := ex.QueryRowContext(ctx,
		`SELECT current_version FROM application WHERE id=$1 FOR UPDATE`, appID).Scan(&v)
	return v, err
}

// BumpAppVersion 把 app 版本号写为 vNew。
func BumpAppVersion(ctx context.Context, ex cp.DBTX, appID, vNew int64) error {
	_, err := ex.ExecContext(ctx,
		`UPDATE application SET current_version=$1, updated_at=now() WHERE id=$2`, vNew, appID)
	return err
}

// InsertAudit 写一条审计记录。
func InsertAudit(ctx context.Context, ex cp.DBTX, appID int64, operator, action, entityType, entityID string, version int64) error {
	_, err := ex.ExecContext(ctx, `
		INSERT INTO policy_audit_log (app_id, operator, action, entity_type, entity_id, version)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		appID, operator, action, entityType, entityID, version)
	return err
}

// ── 业务表 CUD（全部幂等友好） ──

// InsertRole 建角色，返回 id。
func InsertRole(ctx context.Context, ex cp.DBTX, appID int64, code, name string) (int64, error) {
	var id int64
	err := ex.QueryRowContext(ctx,
		`INSERT INTO role (app_id, code, name) VALUES ($1,$2,$3) RETURNING id`,
		appID, code, name).Scan(&id)
	return id, err
}

// DeleteRole 删角色，并先删其全部引用（role_permission / role_inheritance / user_role_binding），避免 FK 冲突。
func DeleteRole(ctx context.Context, ex cp.DBTX, appID, roleID int64) error {
	stmts := []string{
		`DELETE FROM role_permission WHERE app_id=$1 AND role_id=$2`,
		`DELETE FROM role_inheritance WHERE app_id=$1 AND (parent_role_id=$2 OR child_role_id=$2)`,
		`DELETE FROM user_role_binding WHERE app_id=$1 AND role_id=$2`,
		`DELETE FROM role WHERE app_id=$1 AND id=$2`,
	}
	for _, s := range stmts {
		if _, err := ex.ExecContext(ctx, s, appID, roleID); err != nil {
			return err
		}
	}
	return nil
}

// UpsertPermission 幂等注册权限点（按 app_id+code 去重），返回 id。
// 注意：数据库列名为 type（非保留关键字，PostgreSQL DML 中可直接使用）。
// 冲突时全量同步可变字段（resource/action/type/name）：resource、action 参与 p 行投影，
// 若仅更新 name 会让"同 code 重登记但改了资源/动作"被静默丢弃，导致 DB 真相源与调用方意图分叉、
// 重投影拿不到新值——违反权限一致性。故 DO UPDATE 覆盖全部业务字段。
func UpsertPermission(ctx context.Context, ex cp.DBTX, appID int64, code, resource, action, permType, name string) (int64, error) {
	var id int64
	err := ex.QueryRowContext(ctx, `
		INSERT INTO permission (app_id, code, resource, action, type, name)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (app_id, code) DO UPDATE SET
			resource=EXCLUDED.resource, action=EXCLUDED.action, type=EXCLUDED.type,
			name=EXCLUDED.name, updated_at=now()
		RETURNING id`, appID, code, resource, action, permType, name).Scan(&id)
	return id, err
}

// InsertRolePermission 幂等授权（已存在则不动）。
func InsertRolePermission(ctx context.Context, ex cp.DBTX, appID, roleID, permID int64, eft string) error {
	_, err := ex.ExecContext(ctx, `
		INSERT INTO role_permission (app_id, role_id, permission_id, eft)
		VALUES ($1,$2,$3,$4) ON CONFLICT (app_id, role_id, permission_id) DO NOTHING`,
		appID, roleID, permID, eft)
	return err
}

// DeleteRolePermission 撤权。
func DeleteRolePermission(ctx context.Context, ex cp.DBTX, appID, roleID, permID int64) error {
	_, err := ex.ExecContext(ctx,
		`DELETE FROM role_permission WHERE app_id=$1 AND role_id=$2 AND permission_id=$3`,
		appID, roleID, permID)
	return err
}

// InsertRoleInheritance 幂等加继承边。
func InsertRoleInheritance(ctx context.Context, ex cp.DBTX, appID, childID, parentID int64) error {
	_, err := ex.ExecContext(ctx, `
		INSERT INTO role_inheritance (app_id, parent_role_id, child_role_id)
		VALUES ($1,$2,$3) ON CONFLICT (app_id, parent_role_id, child_role_id) DO NOTHING`,
		appID, parentID, childID)
	return err
}

// DeleteRoleInheritance 删继承边。
func DeleteRoleInheritance(ctx context.Context, ex cp.DBTX, appID, childID, parentID int64) error {
	_, err := ex.ExecContext(ctx,
		`DELETE FROM role_inheritance WHERE app_id=$1 AND parent_role_id=$2 AND child_role_id=$3`,
		appID, parentID, childID)
	return err
}

// InsertUserRoleBinding 幂等绑定用户角色。
func InsertUserRoleBinding(ctx context.Context, ex cp.DBTX, appID int64, userID string, roleID int64) error {
	_, err := ex.ExecContext(ctx, `
		INSERT INTO user_role_binding (app_id, user_id, role_id)
		VALUES ($1,$2,$3) ON CONFLICT (app_id, user_id, role_id) DO NOTHING`,
		appID, userID, roleID)
	return err
}

// DeleteUserRoleBinding 解绑。
func DeleteUserRoleBinding(ctx context.Context, ex cp.DBTX, appID int64, userID string, roleID int64) error {
	_, err := ex.ExecContext(ctx,
		`DELETE FROM user_role_binding WHERE app_id=$1 AND user_id=$2 AND role_id=$3`,
		appID, userID, roleID)
	return err
}

// UpsertDataPolicy 新增或更新一条数据策略，写入 version；返回行 id 与是否为新增。
// data_policy 无唯一约束，INSERT/UPDATE 依据 p.ID==0 区分。
// UPDATE 命中 0 行（id 不存在或不属于本 app）即报错——fail-close：宁可拒绝也不让
// 版本号在 DB 无实际变更时跳变、不让下游收到描述不存在策略的 Delta（权限一致性铁律）。
// effect 空串归一为 "allow"，对齐 DB DEFAULT。
func UpsertDataPolicy(ctx context.Context, ex cp.DBTX, appID int64, p cp.DataPolicy, version int64) (id int64, created bool, err error) {
	effect := p.Effect
	if effect == "" {
		effect = cp.EffectAllow
	}
	if p.ID == 0 {
		err = ex.QueryRowContext(ctx, `
			INSERT INTO data_policy (app_id, subject_type, subject_id, resource, condition, effect, description, version)
			VALUES ($1,$2,$3,$4,$5::jsonb,$6,$7,$8) RETURNING id`,
			appID, p.SubjectType, p.SubjectID, p.Resource, p.Condition, effect, p.Description, version).Scan(&id)
		return id, true, err
	}
	res, err := ex.ExecContext(ctx, `
		UPDATE data_policy SET subject_type=$1, subject_id=$2, resource=$3, condition=$4::jsonb,
		       effect=$5, description=$6, version=$7, updated_at=now()
		WHERE app_id=$8 AND id=$9`,
		p.SubjectType, p.SubjectID, p.Resource, p.Condition, effect, p.Description, version, appID, p.ID)
	if err != nil {
		return p.ID, false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return p.ID, false, err
	}
	if n == 0 {
		return 0, false, fmt.Errorf("data_policy id=%d not found for app %d", p.ID, appID)
	}
	return p.ID, false, nil
}

// DeleteDataPolicy 删一条数据策略。命中 0 行即报错（fail-close）：避免删不存在的策略
// 却仍 bump 版本、向下游广播一条无中生有的 ChangeRemove。
func DeleteDataPolicy(ctx context.Context, ex cp.DBTX, appID, id int64) error {
	res, err := ex.ExecContext(ctx, `DELETE FROM data_policy WHERE app_id=$1 AND id=$2`, appID, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("data_policy id=%d not found for app %d", id, appID)
	}
	return nil
}

// UpsertAutoPermission 上报式幂等写权限点：新增标 source='auto'；冲突时仅当现有行
// source='auto' 才覆盖（DO UPDATE ... WHERE），命中 manual 行原样保留（§8 不覆盖人工配置）。
// 返回 applied：true=新增或刷新了 auto 行；false=命中 manual 行被跳过（非错误）。
func UpsertAutoPermission(ctx context.Context, ex cp.DBTX, appID int64, code, resource, action, permType, name, description string) (bool, error) {
	var id int64
	err := ex.QueryRowContext(ctx, `
		INSERT INTO permission (app_id, code, resource, action, type, name, description, source)
		VALUES ($1,$2,$3,$4,$5,$6,$7,'auto')
		ON CONFLICT (app_id, code) DO UPDATE SET
			resource=EXCLUDED.resource, action=EXCLUDED.action, type=EXCLUDED.type,
			name=EXCLUDED.name, description=EXCLUDED.description, updated_at=now()
		WHERE permission.source='auto'
		RETURNING id`, appID, code, resource, action, permType, name, description).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil // 命中 manual 行，DO UPDATE 的 WHERE 为假，零行返回 → 跳过
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
