package restgw

import (
	"context"
	"net/http"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"google.golang.org/protobuf/proto"
)

// roleGraphRoutes 是 M3.3 角色全景 + 决策模拟 2 路由（app 域；app_id/role_id 取自 path 权威）。
// ruleTable 已由任务 3/4 注册（mgmt/authz.go），授权由 AuthorizeRule 统一执行，此处无第二套授权。
func roleGraphRoutes() []route {
	const pfx = "/sydom.admin.v1.AdminService/"
	return []route{
		{"GET", "/v1/apps/{app_id}/roles/{role_id}/graph", pfx + "GetRoleGraph",
			func(r *http.Request, _ []byte) (proto.Message, error) {
				appID, err := pathUint64(r, "app_id")
				if err != nil {
					return nil, err
				}
				roleID, err := pathInt64(r, "role_id")
				if err != nil {
					return nil, err
				}
				return &adminv1.GetRoleGraphRequest{AppId: appID, RoleId: roleID}, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.GetRoleGraph(ctx, m.(*adminv1.GetRoleGraphRequest))
			}},
		{"GET", "/v1/apps/{app_id}/roles/{role_id}/simulation", pfx + "SimulateRoleChange",
			func(r *http.Request, _ []byte) (proto.Message, error) {
				appID, err := pathUint64(r, "app_id")
				if err != nil {
					return nil, err
				}
				roleID, err := pathInt64(r, "role_id")
				if err != nil {
					return nil, err
				}
				q := r.URL.Query()
				return &adminv1.SimulateRoleChangeRequest{
					AppId:      appID,
					RoleId:     roleID,
					ChangeType: parseRoleChangeType(q.Get("change_type")),
					UserId:     q.Get("user_id"),
					Resource:   q.Get("resource"),
					Action:     q.Get("action"),
				}, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.SimulateRoleChange(ctx, m.(*adminv1.SimulateRoleChangeRequest))
			}},
	}
}

// parseRoleChangeType 把 query 串映射为枚举（未知→UNSPECIFIED，由 handler 校验拒绝）。
func parseRoleChangeType(s string) adminv1.RoleChangeType {
	switch s {
	case "bind_user":
		return adminv1.RoleChangeType_BIND_USER
	case "add_capability":
		return adminv1.RoleChangeType_ADD_CAPABILITY
	default:
		return adminv1.RoleChangeType_ROLE_CHANGE_UNSPECIFIED
	}
}
