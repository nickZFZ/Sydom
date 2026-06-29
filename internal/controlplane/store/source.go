package store

import (
	"context"
	"fmt"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
)

// ─── 结果结构体（供 5b 构建 iac.Current 快照）───

// PermissionWithSource 是含 source 标记的权限点快照行。
type PermissionWithSource struct {
	Code, Resource, Action, Type, Name, Description, Source string
}

// RoleWithSource 是含 source 标记的角色快照行。
type RoleWithSource struct {
	ID                              int64
	Code, Name, Description, Source string
}

// DataPolicyWithSource 是含 source 标记的数据策略快照行（无 Description，对齐 SELECT 列）。
type DataPolicyWithSource struct {
	ID                                                    int64
	SubjectType, SubjectID, Resource, Effect, Condition, Source string
}

// ─── 建改助手 ───

// UpsertPermissionWithSource 幂等写权限点并携带 source，ON CONFLICT(app_id,code) 全量覆盖，返回 id。
func UpsertPermissionWithSource(ctx context.Context, ex cp.DBTX,
	appID int64, code, resource, action, permType, name, description, source string) (int64, error) {
	var id int64
	err := ex.QueryRowContext(ctx, `
		INSERT INTO permission (app_id, code, resource, action, type, name, description, source)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (app_id, code) DO UPDATE SET
			resource=EXCLUDED.resource, action=EXCLUDED.action, type=EXCLUDED.type,
			name=EXCLUDED.name, description=EXCLUDED.description, source=EXCLUDED.source,
			updated_at=now()
		RETURNING id`,
		appID, code, resource, action, permType, name, description, source).Scan(&id)
	return id, err
}

// UpsertDataPolicyWithSource 新增或更新数据策略并携带 source，写入 version；返回 id、是否新增。
// p.ID==0 → INSERT 新行（created=true）；p.ID!=0 → UPDATE 该行（created=false）。
// UPDATE 命中 0 行（id 不存在或不属于本 app）报错——fail-close。
// effect 空串归一为 "allow"，对齐 DB DEFAULT。
func UpsertDataPolicyWithSource(ctx context.Context, ex cp.DBTX,
	appID int64, p cp.DataPolicy, source string, version int64) (id int64, created bool, err error) {
	effect := p.Effect
	if effect == "" {
		effect = cp.EffectAllow
	}
	if p.ID == 0 {
		err = ex.QueryRowContext(ctx, `
			INSERT INTO data_policy (app_id, subject_type, subject_id, resource, condition, effect, description, source, version)
			VALUES ($1,$2,$3,$4,$5::jsonb,$6,$7,$8,$9) RETURNING id`,
			appID, p.SubjectType, p.SubjectID, p.Resource, p.Condition,
			effect, p.Description, source, version).Scan(&id)
		return id, true, err
	}
	res, err := ex.ExecContext(ctx, `
		UPDATE data_policy SET subject_type=$1, subject_id=$2, resource=$3, condition=$4::jsonb,
		       effect=$5, description=$6, source=$7, version=$8, updated_at=now()
		WHERE app_id=$9 AND id=$10`,
		p.SubjectType, p.SubjectID, p.Resource, p.Condition,
		effect, p.Description, source, version, appID, p.ID)
	if err != nil {
		return p.ID, false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return p.ID, false, err
	}
	if n == 0 {
		return 0, false, fmt.Errorf("store: data_policy id=%d not found for app %d", p.ID, appID)
	}
	return p.ID, false, nil
}

// InsertRoleWithSource 建角色并携带 source，返回 id。
func InsertRoleWithSource(ctx context.Context, ex cp.DBTX,
	appID int64, code, name, source string) (int64, error) {
	var id int64
	err := ex.QueryRowContext(ctx,
		`INSERT INTO role (app_id, code, name, source) VALUES ($1,$2,$3,$4) RETURNING id`,
		appID, code, name, source).Scan(&id)
	return id, err
}

// UpdateRoleMeta 更新角色的 name 和 description（不改 source/code）。
func UpdateRoleMeta(ctx context.Context, ex cp.DBTX,
	appID, roleID int64, name, description string) error {
	_, err := ex.ExecContext(ctx,
		`UPDATE role SET name=$3, description=$4 WHERE app_id=$1 AND id=$2`,
		appID, roleID, name, description)
	return err
}

// ─── 采纳助手（manual → iac，PC-3 治理边界：只翻 manual，绝不动 auto/iac）───

// AdoptPermissionSource 把 manual 权限点的 source 翻为 iac。只影响 source='manual' 的行。
func AdoptPermissionSource(ctx context.Context, ex cp.DBTX, appID int64, code string) error {
	_, err := ex.ExecContext(ctx,
		`UPDATE permission SET source='iac', updated_at=now()
		 WHERE app_id=$1 AND code=$2 AND source='manual'`,
		appID, code)
	return err
}

// AdoptRoleSource 把 manual 角色的 source 翻为 iac。只影响 source='manual' 的行。
func AdoptRoleSource(ctx context.Context, ex cp.DBTX, appID, roleID int64) error {
	_, err := ex.ExecContext(ctx,
		`UPDATE role SET source='iac'
		 WHERE app_id=$1 AND id=$2 AND source='manual'`,
		appID, roleID)
	return err
}

// AdoptDataPolicySource 把 manual 数据策略的 source 翻为 iac。只影响 source='manual' 的行。
func AdoptDataPolicySource(ctx context.Context, ex cp.DBTX, appID, id int64) error {
	_, err := ex.ExecContext(ctx,
		`UPDATE data_policy SET source='iac'
		 WHERE app_id=$1 AND id=$2 AND source='manual'`,
		appID, id)
	return err
}

// ─── 快照读助手 ───

// ListAppPermissionsWithSource 读取某 app 全部权限点（含 source），按 code 排序。
func ListAppPermissionsWithSource(ctx context.Context, ex cp.DBTX, appID int64) ([]PermissionWithSource, error) {
	rows, err := ex.QueryContext(ctx,
		`SELECT code, resource, action, type, name, COALESCE(description,''), source
		 FROM permission WHERE app_id=$1 ORDER BY code`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PermissionWithSource
	for rows.Next() {
		var p PermissionWithSource
		if err := rows.Scan(&p.Code, &p.Resource, &p.Action, &p.Type, &p.Name, &p.Description, &p.Source); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListAppRolesWithSource 读取某 app 全部角色（含 source），按 id 排序。
func ListAppRolesWithSource(ctx context.Context, ex cp.DBTX, appID int64) ([]RoleWithSource, error) {
	rows, err := ex.QueryContext(ctx,
		`SELECT id, code, name, COALESCE(description,''), source
		 FROM role WHERE app_id=$1 ORDER BY id`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RoleWithSource
	for rows.Next() {
		var r RoleWithSource
		if err := rows.Scan(&r.ID, &r.Code, &r.Name, &r.Description, &r.Source); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListAppDataPoliciesWithSource 读取某 app 全部数据策略（含 source），按 id 排序。
// condition 取 ::text 得 JSON 字符串；DataPolicyWithSource 无 Description 字段，SELECT 列与结构体严格对应。
func ListAppDataPoliciesWithSource(ctx context.Context, ex cp.DBTX, appID int64) ([]DataPolicyWithSource, error) {
	rows, err := ex.QueryContext(ctx,
		`SELECT id, subject_type, subject_id, resource, effect, condition::text, source
		 FROM data_policy WHERE app_id=$1 ORDER BY id`, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DataPolicyWithSource
	for rows.Next() {
		var dp DataPolicyWithSource
		if err := rows.Scan(&dp.ID, &dp.SubjectType, &dp.SubjectID, &dp.Resource, &dp.Effect, &dp.Condition, &dp.Source); err != nil {
			return nil, err
		}
		out = append(out, dp)
	}
	return out, rows.Err()
}

// ─── 查询助手 ───

// RoleHasUserBindings 返回该角色在本 app 是否有用户绑定。
func RoleHasUserBindings(ctx context.Context, ex cp.DBTX, appID, roleID int64) (bool, error) {
	var exists bool
	err := ex.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM user_role_binding WHERE app_id=$1 AND role_id=$2)`,
		appID, roleID).Scan(&exists)
	return exists, err
}
