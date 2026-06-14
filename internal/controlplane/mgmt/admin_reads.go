package mgmt

import (
	"context"
	"database/sql"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *AdminServer) ListRoles(ctx context.Context, r *adminv1.ListRolesRequest) (*adminv1.ListRolesResponse, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, code, name, COALESCE(description,'') FROM role WHERE app_id=$1 ORDER BY id`, int64(r.AppId))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list roles: %v", err)
	}
	defer rows.Close()
	out := &adminv1.ListRolesResponse{}
	for rows.Next() {
		var x adminv1.RoleSummary
		if err := rows.Scan(&x.RoleId, &x.Code, &x.Name, &x.Description); err != nil {
			return nil, status.Errorf(codes.Internal, "scan role: %v", err)
		}
		out.Roles = append(out.Roles, &x)
	}
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "rows role: %v", err)
	}
	return out, nil
}

func (s *AdminServer) ListPermissions(ctx context.Context, r *adminv1.ListPermissionsRequest) (*adminv1.ListPermissionsResponse, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, code, resource, action, type, name, source FROM permission WHERE app_id=$1 ORDER BY id`, int64(r.AppId))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list permissions: %v", err)
	}
	defer rows.Close()
	out := &adminv1.ListPermissionsResponse{}
	for rows.Next() {
		var x adminv1.PermissionSummary
		if err := rows.Scan(&x.PermissionId, &x.Code, &x.Resource, &x.Action, &x.Ptype, &x.Name, &x.Source); err != nil {
			return nil, status.Errorf(codes.Internal, "scan permission: %v", err)
		}
		out.Permissions = append(out.Permissions, &x)
	}
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "rows permission: %v", err)
	}
	return out, nil
}

func (s *AdminServer) ListGrants(ctx context.Context, r *adminv1.ListGrantsRequest) (*adminv1.ListGrantsResponse, error) {
	var rows *sql.Rows
	var err error
	if r.RoleId == 0 {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, role_id, permission_id, eft FROM role_permission WHERE app_id=$1 ORDER BY id`, int64(r.AppId))
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, role_id, permission_id, eft FROM role_permission WHERE app_id=$1 AND role_id=$2 ORDER BY id`, int64(r.AppId), r.RoleId)
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list grants: %v", err)
	}
	defer rows.Close()
	out := &adminv1.ListGrantsResponse{}
	for rows.Next() {
		var x adminv1.GrantSummary
		if err := rows.Scan(&x.GrantId, &x.RoleId, &x.PermissionId, &x.Eft); err != nil {
			return nil, status.Errorf(codes.Internal, "scan grant: %v", err)
		}
		out.Grants = append(out.Grants, &x)
	}
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "rows grant: %v", err)
	}
	return out, nil
}

func (s *AdminServer) ListRoleInheritances(ctx context.Context, r *adminv1.ListRoleInheritancesRequest) (*adminv1.ListRoleInheritancesResponse, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, parent_role_id, child_role_id FROM role_inheritance WHERE app_id=$1 ORDER BY id`, int64(r.AppId))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list inheritances: %v", err)
	}
	defer rows.Close()
	out := &adminv1.ListRoleInheritancesResponse{}
	for rows.Next() {
		var x adminv1.RoleInheritanceSummary
		if err := rows.Scan(&x.InheritanceId, &x.ParentRoleId, &x.ChildRoleId); err != nil {
			return nil, status.Errorf(codes.Internal, "scan inheritance: %v", err)
		}
		out.Inheritances = append(out.Inheritances, &x)
	}
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "rows inheritance: %v", err)
	}
	return out, nil
}

func (s *AdminServer) ListUserBindings(ctx context.Context, r *adminv1.ListUserBindingsRequest) (*adminv1.ListUserBindingsResponse, error) {
	var rows *sql.Rows
	var err error
	if r.UserId == "" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, user_id, role_id FROM user_role_binding WHERE app_id=$1 ORDER BY id`, int64(r.AppId))
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, user_id, role_id FROM user_role_binding WHERE app_id=$1 AND user_id=$2 ORDER BY id`, int64(r.AppId), r.UserId)
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list user bindings: %v", err)
	}
	defer rows.Close()
	out := &adminv1.ListUserBindingsResponse{}
	for rows.Next() {
		var x adminv1.UserBindingSummary
		if err := rows.Scan(&x.BindingId, &x.UserId, &x.RoleId); err != nil {
			return nil, status.Errorf(codes.Internal, "scan binding: %v", err)
		}
		out.Bindings = append(out.Bindings, &x)
	}
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "rows binding: %v", err)
	}
	return out, nil
}

func (s *AdminServer) ListDataPolicies(ctx context.Context, r *adminv1.ListDataPoliciesRequest) (*adminv1.ListDataPoliciesResponse, error) {
	var rows *sql.Rows
	var err error
	if r.Resource == "" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, subject_type, subject_id, resource, condition::text, effect, COALESCE(description,''), version FROM data_policy WHERE app_id=$1 ORDER BY id`, int64(r.AppId))
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, subject_type, subject_id, resource, condition::text, effect, COALESCE(description,''), version FROM data_policy WHERE app_id=$1 AND resource=$2 ORDER BY id`, int64(r.AppId), r.Resource)
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list data policies: %v", err)
	}
	defer rows.Close()
	out := &adminv1.ListDataPoliciesResponse{}
	for rows.Next() {
		var x adminv1.DataPolicySummary
		var ver int64
		if err := rows.Scan(&x.DataPolicyId, &x.SubjectType, &x.SubjectId, &x.Resource, &x.Condition, &x.Effect, &x.Description, &ver); err != nil {
			return nil, status.Errorf(codes.Internal, "scan data policy: %v", err)
		}
		x.Version = uint64(ver)
		out.DataPolicies = append(out.DataPolicies, &x)
	}
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "rows data policy: %v", err)
	}
	return out, nil
}

func (s *AdminServer) ListOperators(ctx context.Context, _ *adminv1.ListOperatorsRequest) (*adminv1.ListOperatorsResponse, error) {
	// 只 SELECT id/principal/status —— secret_enc 绝不出查询，物理保证不泄露。
	rows, err := s.db.QueryContext(ctx, `SELECT id, principal, status FROM admin_operator ORDER BY id`)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list operators: %v", err)
	}
	defer rows.Close()
	out := &adminv1.ListOperatorsResponse{}
	for rows.Next() {
		var x adminv1.OperatorSummary
		var st int16
		if err := rows.Scan(&x.OperatorId, &x.Principal, &st); err != nil {
			return nil, status.Errorf(codes.Internal, "scan operator: %v", err)
		}
		x.Status = uint32(st)
		out.Operators = append(out.Operators, &x)
	}
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "rows operator: %v", err)
	}
	return out, nil
}

func (s *AdminServer) ListAdminRoles(ctx context.Context, _ *adminv1.ListAdminRolesRequest) (*adminv1.ListAdminRolesResponse, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, code, name FROM admin_role ORDER BY id`)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list admin roles: %v", err)
	}
	defer rows.Close()
	out := &adminv1.ListAdminRolesResponse{}
	for rows.Next() {
		var x adminv1.AdminRoleSummary
		if err := rows.Scan(&x.RoleId, &x.Code, &x.Name); err != nil {
			return nil, status.Errorf(codes.Internal, "scan admin role: %v", err)
		}
		out.Roles = append(out.Roles, &x)
	}
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "rows admin role: %v", err)
	}
	return out, nil
}
