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
// 纯结构读（不求值，不用 effperm）。跨租户/未知 role → NotFound 不泄露（RG-6/RG-8）。
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

	// 父角色（直接父，非递归）。
	prows, err := s.db.QueryContext(ctx,
		`SELECT r.id, r.code, r.name FROM role_inheritance ri JOIN role r ON r.id=ri.parent_role_id
		 WHERE ri.child_role_id=$1 AND ri.app_id=$2 ORDER BY r.code`, r.RoleId, appID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read parents: %v", err)
	}
	defer prows.Close()
	for prows.Next() {
		var p adminv1.RoleGraphParent
		if err := prows.Scan(&p.Id, &p.Code, &p.Name); err != nil {
			return nil, status.Errorf(codes.Internal, "scan parent: %v", err)
		}
		out.Parents = append(out.Parents, &p)
	}
	if err := prows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "parents: %v", err)
	}

	// 能力：直接(source="direct") + 祖先(nearest-first, source=祖先 name)。
	// seen 去重：直接角色优先；祖先按 nearest-first 顺序，已有的跳过。
	seen := map[[2]string]bool{}
	addCaps := func(roleID int64, source string) error {
		grows, err := s.db.QueryContext(ctx,
			`SELECT p.resource, p.action, p.name
			 FROM role_permission rp JOIN permission p ON p.id=rp.permission_id
			 WHERE rp.role_id=$1 AND rp.app_id=$2 AND rp.eft='allow'
			 ORDER BY p.resource, p.action`, roleID, appID)
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

	// 直接数据范围（原始 condition JSON，Console 经 conditionPredicate 渲符号谓词）。
	drows, err := s.db.QueryContext(ctx,
		`SELECT resource, effect, condition::text FROM data_policy
		 WHERE subject_type='role' AND subject_id=$1 AND app_id=$2 ORDER BY resource`, code, appID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read data scopes: %v", err)
	}
	defer drows.Close()
	for drows.Next() {
		var d adminv1.RoleGraphDataScope
		if err := drows.Scan(&d.Resource, &d.Effect, &d.Condition); err != nil {
			return nil, status.Errorf(codes.Internal, "scan data scope: %v", err)
		}
		out.DataScopes = append(out.DataScopes, &d)
	}
	if err := drows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "data scopes: %v", err)
	}
	return out, nil
}

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

// roleRef 是递归 CTE 查询祖先时的内部结构。
type roleRef struct {
	id   int64
	name string
}

// roleAncestors 返回角色的所有祖先（最近优先），使用递归 CTE 闭包。
func (s *AdminServer) roleAncestors(ctx context.Context, appID, roleID int64) ([]roleRef, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH RECURSIVE anc(rid, depth) AS (
			SELECT parent_role_id, 1
			FROM role_inheritance
			WHERE child_role_id=$1 AND app_id=$2
			UNION
			SELECT ri.parent_role_id, anc.depth+1
			FROM role_inheritance ri JOIN anc ON ri.child_role_id=anc.rid
			WHERE ri.app_id=$2
		)
		SELECT r.id, r.name, min(anc.depth) AS d
		FROM anc JOIN role r ON r.id=anc.rid
		GROUP BY r.id, r.name
		ORDER BY d, r.name`, roleID, appID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []roleRef
	for rows.Next() {
		var rr roleRef
		var depth int
		if err := rows.Scan(&rr.id, &rr.name, &depth); err != nil {
			return nil, err
		}
		out = append(out, rr)
	}
	return out, rows.Err()
}

// scanStrings 执行单列字符串查询，返回结果切片。
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
