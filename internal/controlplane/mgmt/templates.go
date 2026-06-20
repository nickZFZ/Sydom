package mgmt

import (
	"context"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/policy"
	"github.com/nickZFZ/Sydom/internal/controlplane/presets"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ListTemplates 返回司域官方预设包（全局产品资料；鉴权以 app 为上下文 scopeApp read）。
func (s *AdminServer) ListTemplates(ctx context.Context, r *adminv1.ListTemplatesRequest) (*adminv1.ListTemplatesResponse, error) {
	resp := &adminv1.ListTemplatesResponse{}
	for _, t := range presets.All() {
		resp.Templates = append(resp.Templates, toProtoTemplate(t))
	}
	return resp, nil
}

// ApplyTemplate 原子幂等应用预设包到 app。
func (s *AdminServer) ApplyTemplate(ctx context.Context, r *adminv1.ApplyTemplateRequest) (*adminv1.ApplyTemplateResponse, error) {
	tpl, ok := presets.Get(r.TemplateId)
	if !ok {
		return nil, status.Errorf(codes.InvalidArgument, "unknown template %q", r.TemplateId)
	}
	perms := make([]cp.PermissionPoint, 0, len(tpl.Permissions))
	for _, p := range tpl.Permissions {
		perms = append(perms, cp.PermissionPoint{
			Code: p.Code, Resource: p.Resource, Action: p.Action,
			Type: p.Type, Name: p.Name, Description: p.Description,
		})
	}
	roles := make([]policy.TemplateRole, 0, len(tpl.Roles))
	for _, rr := range tpl.Roles {
		roles = append(roles, policy.TemplateRole{Key: rr.Key, Name: rr.Name, PermissionCodes: rr.PermissionCodes})
	}
	res, _, err := s.mgr.ApplyTemplate(ctx, int64(r.AppId), tpl.ID, perms, roles)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "apply template: %v", err)
	}
	return &adminv1.ApplyTemplateResponse{
		PermissionsUpserted: uint32(res.PermsUpserted),
		PermissionsSkipped:  uint32(res.PermsSkipped),
		RolesCreated:        uint32(res.RolesCreated),
		RolesSkipped:        uint32(res.RolesSkipped),
	}, nil
}

func toProtoTemplate(t presets.Template) *adminv1.Template {
	pt := &adminv1.Template{Id: t.ID, Name: t.Name, Description: t.Description, Version: t.Version}
	for _, p := range t.Permissions {
		pt.Permissions = append(pt.Permissions, &adminv1.TemplatePermission{
			Code: p.Code, Resource: p.Resource, Action: p.Action, Type: p.Type, Name: p.Name, Description: p.Description,
		})
	}
	for _, r := range t.Roles {
		pt.Roles = append(pt.Roles, &adminv1.TemplateRole{
			Key: r.Key, Name: r.Name, Description: r.Description, PermissionCodes: r.PermissionCodes,
		})
	}
	return pt
}
