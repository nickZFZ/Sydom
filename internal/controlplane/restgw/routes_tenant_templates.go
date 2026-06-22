package restgw

import (
	"context"
	"net/http"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"google.golang.org/protobuf/proto"
)

// tenantTemplateRoutes 是 M3.2c-2 租户自有模板 5 路由（path 权威填鉴权域字段：
// scopeApp 读 app_id、scopeTenant 读 tenant_id；ruleTable 已注册，授权由 AuthorizeRule 统一执行）。
// 路径段刻意用 template-captures / tenant-templates，避与既有 app 域 templates（presets）路由冲突。
func tenantTemplateRoutes() []route {
	const pfx = "/sydom.admin.v1.AdminService/"
	return []route{
		{"POST", "/v1/apps/{app_id}/template-captures", pfx + "SaveAppAsTemplate",
			func(r *http.Request, body []byte) (proto.Message, error) {
				m := &adminv1.SaveAppAsTemplateRequest{}
				if err := decodeBody(body, m); err != nil {
					return nil, err
				}
				id, err := pathUint64(r, "app_id")
				if err != nil {
					return nil, err
				}
				m.AppId = id // 路径权威覆写（鉴权域=path app_id）
				return m, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.SaveAppAsTemplate(ctx, m.(*adminv1.SaveAppAsTemplateRequest))
			}},
		{"GET", "/v1/tenants/{tenant_id}/templates", pfx + "ListTenantTemplates",
			func(r *http.Request, _ []byte) (proto.Message, error) {
				id, err := pathUint64(r, "tenant_id")
				if err != nil {
					return nil, err
				}
				page, err := parseListPage(r)
				if err != nil {
					return nil, err
				}
				return &adminv1.ListTenantTemplatesRequest{TenantId: id, Page: page}, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.ListTenantTemplates(ctx, m.(*adminv1.ListTenantTemplatesRequest))
			}},
		{"GET", "/v1/tenants/{tenant_id}/templates/{template_id}", pfx + "GetTenantTemplate",
			func(r *http.Request, _ []byte) (proto.Message, error) {
				tid, err := pathUint64(r, "tenant_id")
				if err != nil {
					return nil, err
				}
				tplID, err := pathUint64(r, "template_id")
				if err != nil {
					return nil, err
				}
				return &adminv1.GetTenantTemplateRequest{TenantId: tid, TemplateId: tplID}, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.GetTenantTemplate(ctx, m.(*adminv1.GetTenantTemplateRequest))
			}},
		{"POST", "/v1/apps/{app_id}/tenant-templates/{template_id}/apply", pfx + "ApplyTenantTemplate",
			func(r *http.Request, _ []byte) (proto.Message, error) {
				// app_id 与 template_id 均取自 path（权威；无 body 字段可伪造）。
				appID, err := pathUint64(r, "app_id")
				if err != nil {
					return nil, err
				}
				tplID, err := pathUint64(r, "template_id")
				if err != nil {
					return nil, err
				}
				return &adminv1.ApplyTenantTemplateRequest{AppId: appID, TemplateId: tplID}, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.ApplyTenantTemplate(ctx, m.(*adminv1.ApplyTenantTemplateRequest))
			}},
		{"DELETE", "/v1/tenants/{tenant_id}/templates/{template_id}", pfx + "DeleteTenantTemplate",
			func(r *http.Request, _ []byte) (proto.Message, error) {
				tid, err := pathUint64(r, "tenant_id")
				if err != nil {
					return nil, err
				}
				tplID, err := pathUint64(r, "template_id")
				if err != nil {
					return nil, err
				}
				return &adminv1.DeleteTenantTemplateRequest{TenantId: tid, TemplateId: tplID}, nil
			},
			func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
				return s.DeleteTenantTemplate(ctx, m.(*adminv1.DeleteTenantTemplateRequest))
			}},
	}
}
