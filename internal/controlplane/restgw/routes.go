package restgw

import (
	"context"
	"net/http"
	"strconv"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// route 是一条静态路由登记。fullMethod 即 ruleTable 键，使授权核心与 gRPC 端逐字节复用同一 rpcRule。
type route struct {
	method     string // HTTP 动词
	pattern    string // ServeMux 方法感知模式的路径部分
	fullMethod string // gRPC FullMethod（ruleTable 键）
	decode     func(r *http.Request, body []byte) (proto.Message, error)
	invoke     func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error)
}

// —— decode helpers ——

// decodeBody 用 protojson 填 body 字段（DiscardUnknown：容忍多余字段）。空 body 跳过。
func decodeBody(body []byte, m proto.Message) error {
	if len(body) == 0 {
		return nil
	}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(body, m); err != nil {
		return status.Error(codes.InvalidArgument, "invalid json body")
	}
	return nil
}

func pathUint64(r *http.Request, key string) (uint64, error) {
	v, err := strconv.ParseUint(r.PathValue(key), 10, 64)
	if err != nil {
		return 0, status.Errorf(codes.InvalidArgument, "invalid path %s", key)
	}
	return v, nil
}

func pathInt64(r *http.Request, key string) (int64, error) {
	v, err := strconv.ParseInt(r.PathValue(key), 10, 64)
	if err != nil {
		return 0, status.Errorf(codes.InvalidArgument, "invalid path %s", key)
	}
	return v, nil
}

// queryInt64 取可选 int64 query（缺=0）。
func queryInt64(r *http.Request, key string) (int64, error) {
	s := r.URL.Query().Get(key)
	if s == "" {
		return 0, nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, status.Errorf(codes.InvalidArgument, "invalid query %s", key)
	}
	return v, nil
}

// appRoutes 是 app 域 20 路由（授权域=path app_id；path 值权威覆写 body）。
func appRoutes() []route {
	const pfx = "/sydom.admin.v1.AdminService/"
	return []route{
		{"GET", "/v1/apps/{app_id}/roles", pfx + "ListRoles",
			func(r *http.Request, _ []byte) (proto.Message, error) {
				id, err := pathUint64(r, "app_id")
				if err != nil {
					return nil, err
				}
				return &adminv1.ListRolesRequest{AppId: id}, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.ListRoles(ctx, m.(*adminv1.ListRolesRequest))
			}},
		{"POST", "/v1/apps/{app_id}/roles", pfx + "CreateRole",
			func(r *http.Request, body []byte) (proto.Message, error) {
				m := &adminv1.CreateRoleRequest{}
				if err := decodeBody(body, m); err != nil {
					return nil, err
				}
				id, err := pathUint64(r, "app_id")
				if err != nil {
					return nil, err
				}
				m.AppId = id
				return m, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.CreateRole(ctx, m.(*adminv1.CreateRoleRequest))
			}},
		{"POST", "/v1/apps/{app_id}/business-roles", pfx + "CreateBusinessRole",
			func(r *http.Request, body []byte) (proto.Message, error) {
				m := &adminv1.CreateBusinessRoleRequest{}
				if err := decodeBody(body, m); err != nil {
					return nil, err
				}
				id, err := pathUint64(r, "app_id")
				if err != nil {
					return nil, err
				}
				m.AppId = id // path 权威覆写
				return m, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.CreateBusinessRole(ctx, m.(*adminv1.CreateBusinessRoleRequest))
			}},
		{"DELETE", "/v1/apps/{app_id}/roles/{role_id}", pfx + "DeleteRole",
			func(r *http.Request, _ []byte) (proto.Message, error) {
				appID, err := pathUint64(r, "app_id")
				if err != nil {
					return nil, err
				}
				roleID, err := pathInt64(r, "role_id")
				if err != nil {
					return nil, err
				}
				return &adminv1.DeleteRoleRequest{AppId: appID, RoleId: roleID}, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.DeleteRole(ctx, m.(*adminv1.DeleteRoleRequest))
			}},
		{"GET", "/v1/apps/{app_id}/permissions", pfx + "ListPermissions",
			func(r *http.Request, _ []byte) (proto.Message, error) {
				id, err := pathUint64(r, "app_id")
				if err != nil {
					return nil, err
				}
				return &adminv1.ListPermissionsRequest{AppId: id}, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.ListPermissions(ctx, m.(*adminv1.ListPermissionsRequest))
			}},
		{"PUT", "/v1/apps/{app_id}/permissions/{code}", pfx + "UpsertPermission",
			func(r *http.Request, body []byte) (proto.Message, error) {
				m := &adminv1.UpsertPermissionRequest{}
				if err := decodeBody(body, m); err != nil {
					return nil, err
				}
				id, err := pathUint64(r, "app_id")
				if err != nil {
					return nil, err
				}
				m.AppId = id
				m.Code = r.PathValue("code") // 路径权威：code 由路径段决定（%2F 会被解码进段内，但 code 仅作权限码字符串、不参与鉴权域判定，无越权风险）
				return m, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.UpsertPermission(ctx, m.(*adminv1.UpsertPermissionRequest))
			}},
		{"GET", "/v1/apps/{app_id}/grants", pfx + "ListGrants",
			func(r *http.Request, _ []byte) (proto.Message, error) {
				id, err := pathUint64(r, "app_id")
				if err != nil {
					return nil, err
				}
				roleID, err := queryInt64(r, "role_id")
				if err != nil {
					return nil, err
				}
				return &adminv1.ListGrantsRequest{AppId: id, RoleId: roleID}, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.ListGrants(ctx, m.(*adminv1.ListGrantsRequest))
			}},
		{"POST", "/v1/apps/{app_id}/roles/{role_id}/grants", pfx + "GrantPermission",
			func(r *http.Request, body []byte) (proto.Message, error) {
				m := &adminv1.GrantPermissionRequest{}
				if err := decodeBody(body, m); err != nil {
					return nil, err
				}
				appID, err := pathUint64(r, "app_id")
				if err != nil {
					return nil, err
				}
				roleID, err := pathInt64(r, "role_id")
				if err != nil {
					return nil, err
				}
				m.AppId, m.RoleId = appID, roleID
				return m, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.GrantPermission(ctx, m.(*adminv1.GrantPermissionRequest))
			}},
		{"DELETE", "/v1/apps/{app_id}/roles/{role_id}/grants/{permission_id}", pfx + "RevokePermission",
			func(r *http.Request, _ []byte) (proto.Message, error) {
				appID, err := pathUint64(r, "app_id")
				if err != nil {
					return nil, err
				}
				roleID, err := pathInt64(r, "role_id")
				if err != nil {
					return nil, err
				}
				permID, err := pathInt64(r, "permission_id")
				if err != nil {
					return nil, err
				}
				return &adminv1.RevokePermissionRequest{AppId: appID, RoleId: roleID, PermissionId: permID}, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.RevokePermission(ctx, m.(*adminv1.RevokePermissionRequest))
			}},
		{"GET", "/v1/apps/{app_id}/role-inheritances", pfx + "ListRoleInheritances",
			func(r *http.Request, _ []byte) (proto.Message, error) {
				id, err := pathUint64(r, "app_id")
				if err != nil {
					return nil, err
				}
				return &adminv1.ListRoleInheritancesRequest{AppId: id}, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.ListRoleInheritances(ctx, m.(*adminv1.ListRoleInheritancesRequest))
			}},
		{"POST", "/v1/apps/{app_id}/roles/{child_role_id}/parents", pfx + "AddRoleInheritance",
			func(r *http.Request, body []byte) (proto.Message, error) {
				m := &adminv1.RoleInheritanceRequest{}
				if err := decodeBody(body, m); err != nil {
					return nil, err
				}
				appID, err := pathUint64(r, "app_id")
				if err != nil {
					return nil, err
				}
				childID, err := pathInt64(r, "child_role_id")
				if err != nil {
					return nil, err
				}
				m.AppId, m.ChildRoleId = appID, childID
				return m, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.AddRoleInheritance(ctx, m.(*adminv1.RoleInheritanceRequest))
			}},
		{"DELETE", "/v1/apps/{app_id}/roles/{child_role_id}/parents/{parent_role_id}", pfx + "RemoveRoleInheritance",
			func(r *http.Request, _ []byte) (proto.Message, error) {
				appID, err := pathUint64(r, "app_id")
				if err != nil {
					return nil, err
				}
				childID, err := pathInt64(r, "child_role_id")
				if err != nil {
					return nil, err
				}
				parentID, err := pathInt64(r, "parent_role_id")
				if err != nil {
					return nil, err
				}
				return &adminv1.RoleInheritanceRequest{AppId: appID, ChildRoleId: childID, ParentRoleId: parentID}, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.RemoveRoleInheritance(ctx, m.(*adminv1.RoleInheritanceRequest))
			}},
		{"GET", "/v1/apps/{app_id}/user-bindings", pfx + "ListUserBindings",
			func(r *http.Request, _ []byte) (proto.Message, error) {
				id, err := pathUint64(r, "app_id")
				if err != nil {
					return nil, err
				}
				return &adminv1.ListUserBindingsRequest{AppId: id, UserId: r.URL.Query().Get("user_id")}, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.ListUserBindings(ctx, m.(*adminv1.ListUserBindingsRequest))
			}},
		{"GET", "/v1/apps/{app_id}/effective-permissions", pfx + "GetEffectivePermissions",
			func(r *http.Request, _ []byte) (proto.Message, error) {
				id, err := pathUint64(r, "app_id")
				if err != nil {
					return nil, err
				}
				return &adminv1.GetEffectivePermissionsRequest{AppId: id, UserId: r.URL.Query().Get("user_id")}, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.GetEffectivePermissions(ctx, m.(*adminv1.GetEffectivePermissionsRequest))
			}},
		{"POST", "/v1/apps/{app_id}/users/{user_id}/roles", pfx + "BindUserRole",
			func(r *http.Request, body []byte) (proto.Message, error) {
				m := &adminv1.UserRoleRequest{}
				if err := decodeBody(body, m); err != nil {
					return nil, err
				}
				appID, err := pathUint64(r, "app_id")
				if err != nil {
					return nil, err
				}
				m.AppId = appID
				m.UserId = r.PathValue("user_id")
				return m, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.BindUserRole(ctx, m.(*adminv1.UserRoleRequest))
			}},
		{"DELETE", "/v1/apps/{app_id}/users/{user_id}/roles/{role_id}", pfx + "UnbindUserRole",
			func(r *http.Request, _ []byte) (proto.Message, error) {
				appID, err := pathUint64(r, "app_id")
				if err != nil {
					return nil, err
				}
				roleID, err := pathInt64(r, "role_id")
				if err != nil {
					return nil, err
				}
				return &adminv1.UserRoleRequest{AppId: appID, UserId: r.PathValue("user_id"), RoleId: roleID}, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.UnbindUserRole(ctx, m.(*adminv1.UserRoleRequest))
			}},
		{"GET", "/v1/apps/{app_id}/data-policies", pfx + "ListDataPolicies",
			func(r *http.Request, _ []byte) (proto.Message, error) {
				id, err := pathUint64(r, "app_id")
				if err != nil {
					return nil, err
				}
				return &adminv1.ListDataPoliciesRequest{AppId: id, Resource: r.URL.Query().Get("resource")}, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.ListDataPolicies(ctx, m.(*adminv1.ListDataPoliciesRequest))
			}},
		{"POST", "/v1/apps/{app_id}/data-policies", pfx + "UpsertDataPolicy",
			func(r *http.Request, body []byte) (proto.Message, error) {
				m := &adminv1.UpsertDataPolicyRequest{}
				if err := decodeBody(body, m); err != nil {
					return nil, err
				}
				id, err := pathUint64(r, "app_id")
				if err != nil {
					return nil, err
				}
				m.AppId, m.Id = id, 0 // POST 恒为新增（id=0），路径无 id
				return m, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.UpsertDataPolicy(ctx, m.(*adminv1.UpsertDataPolicyRequest))
			}},
		{"PUT", "/v1/apps/{app_id}/data-policies/{id}", pfx + "UpsertDataPolicy",
			func(r *http.Request, body []byte) (proto.Message, error) {
				m := &adminv1.UpsertDataPolicyRequest{}
				if err := decodeBody(body, m); err != nil {
					return nil, err
				}
				appID, err := pathUint64(r, "app_id")
				if err != nil {
					return nil, err
				}
				id, err := pathInt64(r, "id")
				if err != nil {
					return nil, err
				}
				m.AppId, m.Id = appID, id // 路径 id 权威
				return m, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.UpsertDataPolicy(ctx, m.(*adminv1.UpsertDataPolicyRequest))
			}},
		{"DELETE", "/v1/apps/{app_id}/data-policies/{id}", pfx + "DeleteDataPolicy",
			func(r *http.Request, _ []byte) (proto.Message, error) {
				appID, err := pathUint64(r, "app_id")
				if err != nil {
					return nil, err
				}
				id, err := pathInt64(r, "id")
				if err != nil {
					return nil, err
				}
				return &adminv1.DeleteDataPolicyRequest{AppId: appID, DataPolicyId: id}, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.DeleteDataPolicy(ctx, m.(*adminv1.DeleteDataPolicyRequest))
			}},
	}
}

// applicationRoutes 是 §3.2 应用管理 4 路由。
func applicationRoutes() []route {
	const pfx = "/sydom.admin.v1.AdminService/"
	return []route{
		{"GET", "/v1/applications", pfx + "ListApplications",
			func(r *http.Request, _ []byte) (proto.Message, error) {
				tid, err := queryInt64(r, "tenant_id")
				if err != nil {
					return nil, err
				}
				return &adminv1.ListApplicationsRequest{TenantId: uint64(tid)}, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.ListApplications(ctx, m.(*adminv1.ListApplicationsRequest))
			}},
		{"POST", "/v1/applications", pfx + "CreateApplication",
			func(_ *http.Request, body []byte) (proto.Message, error) {
				m := &adminv1.CreateApplicationRequest{}
				if err := decodeBody(body, m); err != nil {
					return nil, err
				}
				return m, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.CreateApplication(ctx, m.(*adminv1.CreateApplicationRequest))
			}},
		{"PUT", "/v1/applications/{app_id}/status", pfx + "SetApplicationStatus",
			func(r *http.Request, body []byte) (proto.Message, error) {
				m := &adminv1.SetApplicationStatusRequest{}
				if err := decodeBody(body, m); err != nil {
					return nil, err
				}
				id, err := pathUint64(r, "app_id")
				if err != nil {
					return nil, err
				}
				m.AppId = id // 路径权威；status 来自 body
				return m, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.SetApplicationStatus(ctx, m.(*adminv1.SetApplicationStatusRequest))
			}},
		{"POST", "/v1/applications/{app_id}/secret", pfx + "RotateApplicationSecret",
			func(r *http.Request, _ []byte) (proto.Message, error) {
				id, err := pathUint64(r, "app_id")
				if err != nil {
					return nil, err
				}
				return &adminv1.RotateApplicationSecretRequest{AppId: id}, nil // path 权威
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.RotateApplicationSecret(ctx, m.(*adminv1.RotateApplicationSecretRequest))
			}},
	}
}

// systemRoutes 是 §3.3 管理员/admin-role 域 10 路由（授权域 "*"）。
func systemRoutes() []route {
	const pfx = "/sydom.admin.v1.AdminService/"
	return []route{
		{"GET", "/v1/operators", pfx + "ListOperators",
			func(_ *http.Request, _ []byte) (proto.Message, error) {
				return &adminv1.ListOperatorsRequest{}, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.ListOperators(ctx, m.(*adminv1.ListOperatorsRequest))
			}},
		{"POST", "/v1/operators", pfx + "CreateOperator",
			func(_ *http.Request, body []byte) (proto.Message, error) {
				m := &adminv1.CreateOperatorRequest{}
				if err := decodeBody(body, m); err != nil {
					return nil, err
				}
				return m, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.CreateOperator(ctx, m.(*adminv1.CreateOperatorRequest))
			}},
		{"PUT", "/v1/operators/{operator_id}/status", pfx + "SetOperatorStatus",
			func(r *http.Request, body []byte) (proto.Message, error) {
				m := &adminv1.SetOperatorStatusRequest{}
				if err := decodeBody(body, m); err != nil {
					return nil, err
				}
				id, err := pathInt64(r, "operator_id")
				if err != nil {
					return nil, err
				}
				m.OperatorId = id
				return m, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.SetOperatorStatus(ctx, m.(*adminv1.SetOperatorStatusRequest))
			}},
		{"POST", "/v1/operators/{operator_id}/roles", pfx + "BindOperatorRole",
			func(r *http.Request, body []byte) (proto.Message, error) {
				m := &adminv1.BindOperatorRoleRequest{}
				if err := decodeBody(body, m); err != nil {
					return nil, err
				}
				id, err := pathInt64(r, "operator_id")
				if err != nil {
					return nil, err
				}
				m.OperatorId = id
				return m, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.BindOperatorRole(ctx, m.(*adminv1.BindOperatorRoleRequest))
			}},
		{"GET", "/v1/admin-roles", pfx + "ListAdminRoles",
			func(_ *http.Request, _ []byte) (proto.Message, error) {
				return &adminv1.ListAdminRolesRequest{}, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.ListAdminRoles(ctx, m.(*adminv1.ListAdminRolesRequest))
			}},
		{"POST", "/v1/admin-roles", pfx + "CreateAdminRole",
			func(_ *http.Request, body []byte) (proto.Message, error) {
				m := &adminv1.CreateAdminRoleRequest{}
				if err := decodeBody(body, m); err != nil {
					return nil, err
				}
				return m, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.CreateAdminRole(ctx, m.(*adminv1.CreateAdminRoleRequest))
			}},
		{"POST", "/v1/admin-roles/{role_id}/grants", pfx + "GrantAdminRole",
			func(r *http.Request, body []byte) (proto.Message, error) {
				m := &adminv1.GrantAdminRoleRequest{}
				if err := decodeBody(body, m); err != nil {
					return nil, err
				}
				id, err := pathInt64(r, "role_id")
				if err != nil {
					return nil, err
				}
				m.RoleId = id
				return m, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.GrantAdminRole(ctx, m.(*adminv1.GrantAdminRoleRequest))
			}},
		{"DELETE", "/v1/admin-roles/{role_id}/grants", pfx + "RevokeAdminGrant",
			func(r *http.Request, _ []byte) (proto.Message, error) {
				id, err := pathInt64(r, "role_id")
				if err != nil {
					return nil, err
				}
				return &adminv1.RevokeAdminGrantRequest{ // role_id path 权威；其余键走 query（含 "*"）
					RoleId:   id,
					Domain:   r.URL.Query().Get("domain"),
					Resource: r.URL.Query().Get("resource"),
					Action:   r.URL.Query().Get("action"),
				}, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.RevokeAdminGrant(ctx, m.(*adminv1.RevokeAdminGrantRequest))
			}},
		{"DELETE", "/v1/operators/{operator_id}/roles/{role_id}", pfx + "UnbindOperatorRole",
			func(r *http.Request, _ []byte) (proto.Message, error) {
				opID, err := pathInt64(r, "operator_id")
				if err != nil {
					return nil, err
				}
				roleID, err := pathInt64(r, "role_id")
				if err != nil {
					return nil, err
				}
				return &adminv1.UnbindOperatorRoleRequest{ // 两 id path 权威；domain 走 query（含 "*"）
					OperatorId: opID, RoleId: roleID, Domain: r.URL.Query().Get("domain"),
				}, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.UnbindOperatorRole(ctx, m.(*adminv1.UnbindOperatorRoleRequest))
			}},
		{"POST", "/v1/operators/{operator_id}/secret", pfx + "ResetOperatorSecret",
			func(r *http.Request, _ []byte) (proto.Message, error) {
				id, err := pathInt64(r, "operator_id")
				if err != nil {
					return nil, err
				}
				return &adminv1.ResetOperatorSecretRequest{OperatorId: id}, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.ResetOperatorSecret(ctx, m.(*adminv1.ResetOperatorSecretRequest))
			}},
	}
}

// allRoutes 汇总全部 38 路由（app 域 20 + 应用管理 4 + system 域 10 + 账户层 4）。
func allRoutes() []route {
	var rs []route
	rs = append(rs, appRoutes()...)
	rs = append(rs, applicationRoutes()...)
	rs = append(rs, systemRoutes()...)
	rs = append(rs, accountRoutes()...)
	return rs
}
