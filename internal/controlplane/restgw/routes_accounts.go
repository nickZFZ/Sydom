package restgw

import (
	"context"
	"net/http"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"google.golang.org/protobuf/proto"
)

// accountRoutes 是 M1.2 账户层 4 路由。RegisterTenant 免鉴权（serve 据 mgmt.UnauthenticatedMethods 跳认证/授权）。
func accountRoutes() []route {
	const pfx = "/sydom.admin.v1.AdminService/"
	return []route{
		{"POST", "/v1/tenants", pfx + "RegisterTenant",
			func(_ *http.Request, body []byte) (proto.Message, error) {
				m := &adminv1.RegisterTenantRequest{}
				if err := decodeBody(body, m); err != nil {
					return nil, err
				}
				return m, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.RegisterTenant(ctx, m.(*adminv1.RegisterTenantRequest))
			}},
		{"POST", "/v1/tenants/{tenant_id}/plan", pfx + "ChangeTenantPlan",
			func(r *http.Request, body []byte) (proto.Message, error) {
				m := &adminv1.ChangeTenantPlanRequest{}
				if err := decodeBody(body, m); err != nil {
					return nil, err
				}
				id, err := pathUint64(r, "tenant_id")
				if err != nil {
					return nil, err
				}
				m.TenantId = id
				return m, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.ChangeTenantPlan(ctx, m.(*adminv1.ChangeTenantPlanRequest))
			}},
		{"GET", "/v1/me/tenants", pfx + "ListMyTenants",
			func(r *http.Request, _ []byte) (proto.Message, error) {
				page, err := parseListPage(r)
				if err != nil {
					return nil, err
				}
				return &adminv1.ListMyTenantsRequest{Page: page}, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.ListMyTenants(ctx, m.(*adminv1.ListMyTenantsRequest))
			}},
		{"POST", "/v1/tenants/{tenant_id}/members", pfx + "InviteMember",
			func(r *http.Request, body []byte) (proto.Message, error) {
				m := &adminv1.InviteMemberRequest{}
				if err := decodeBody(body, m); err != nil {
					return nil, err
				}
				id, err := pathUint64(r, "tenant_id")
				if err != nil {
					return nil, err
				}
				m.TenantId = id // 路径权威
				return m, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.InviteMember(ctx, m.(*adminv1.InviteMemberRequest))
			}},
		{"GET", "/v1/tenants/{tenant_id}/members", pfx + "ListMembers",
			func(r *http.Request, _ []byte) (proto.Message, error) {
				id, err := pathUint64(r, "tenant_id")
				if err != nil {
					return nil, err
				}
				tierV, err := queryInt64(r, "tier")
				if err != nil {
					return nil, err
				}
				page, err := parseListPage(r)
				if err != nil {
					return nil, err
				}
				return &adminv1.ListMembersRequest{TenantId: id, Tier: int32(tierV), Page: page}, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.ListMembers(ctx, m.(*adminv1.ListMembersRequest))
			}},
	}
}
