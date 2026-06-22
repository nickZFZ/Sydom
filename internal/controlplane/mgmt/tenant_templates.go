package mgmt

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/policy"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/controlplane/tenanttemplate"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SaveAppAsTemplate 捕获源 app 全模型存为本租户模板。
// scopeApp 鉴权已校验调用方在该 app 域有权限；SaveAppAsTemplate isWrite=false，停用 app 亦可快照。
func (s *AdminServer) SaveAppAsTemplate(ctx context.Context, r *adminv1.SaveAppAsTemplateRequest) (*adminv1.TenantTemplateRef, error) {
	if r.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name required")
	}
	// 解析 tenant_id：scopeApp 鉴权已保证 app 存在且调用方有权，此处查库仅取归属租户。
	var tenantID int64
	if err := s.db.QueryRowContext(ctx, `SELECT tenant_id FROM application WHERE id=$1`, int64(r.AppId)).Scan(&tenantID); err != nil {
		return nil, status.Errorf(codes.Internal, "resolve tenant: %v", err)
	}
	b, err := tenanttemplate.Capture(ctx, s.db, int64(r.AppId))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "capture: %v", err)
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "marshal bundle: %v", err)
	}
	id, err := store.InsertTenantTemplate(ctx, s.db, tenantID, r.Name, r.Description, raw, int64(r.AppId))
	if errors.Is(err, store.ErrConflict) {
		return nil, status.Errorf(codes.AlreadyExists, "template name %q already exists", r.Name)
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "save template: %v", err)
	}
	return &adminv1.TenantTemplateRef{Id: uint64(id), Name: r.Name}, nil
}

// DeleteTenantTemplate 删本租户模板（tenant-scoped）。
func (s *AdminServer) DeleteTenantTemplate(ctx context.Context, r *adminv1.DeleteTenantTemplateRequest) (*adminv1.WriteResponse, error) {
	err := store.DeleteTenantTemplate(ctx, s.db, int64(r.TenantId), int64(r.TemplateId))
	if errors.Is(err, store.ErrNotFound) {
		return nil, status.Error(codes.NotFound, "template not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "delete template: %v", err)
	}
	return &adminv1.WriteResponse{}, nil
}

// ListTenantTemplates 列出本租户模板（分页/搜索/排序）。
func (s *AdminServer) ListTenantTemplates(ctx context.Context, r *adminv1.ListTenantTemplatesRequest) (*adminv1.ListTenantTemplatesResponse, error) {
	order := resolveOrder(r.Page.GetSort(), r.Page.GetOrder(),
		map[string]string{"id": "id", "name": "name"}, "id")
	limit, offset := pageOf(r.Page)
	rows, total, err := store.ListTenantTemplates(ctx, s.db, int64(r.TenantId), limit, offset, order, r.Page.GetQ())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list templates: %v", err)
	}
	out := &adminv1.ListTenantTemplatesResponse{Total: total}
	for _, t := range rows {
		out.Templates = append(out.Templates, &adminv1.TenantTemplateSummary{
			Id: uint64(t.ID), Name: t.Name, Description: t.Description, SourceAppId: uint64(t.SourceAppID),
		})
	}
	return out, nil
}

// GetTenantTemplate 取模板并把 bundle 渲染为预览（含 data_scopes，符号谓词在表现层渲染）。
func (s *AdminServer) GetTenantTemplate(ctx context.Context, r *adminv1.GetTenantTemplateRequest) (*adminv1.TenantTemplate, error) {
	t, err := store.GetTenantTemplate(ctx, s.db, int64(r.TenantId), int64(r.TemplateId))
	if errors.Is(err, store.ErrNotFound) {
		return nil, status.Error(codes.NotFound, "template not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get template: %v", err)
	}
	var b tenanttemplate.Bundle
	if err := json.Unmarshal(t.Bundle, &b); err != nil {
		return nil, status.Error(codes.Internal, "bundle parse") // TT-8 fail-close，不回显原文
	}
	out := &adminv1.TenantTemplate{Id: uint64(t.ID), Name: t.Name, Description: t.Description, SourceAppId: uint64(t.SourceAppID)}
	for _, p := range b.Permissions {
		out.Permissions = append(out.Permissions, &adminv1.TemplatePermission{
			Code: p.Code, Resource: p.Resource, Action: p.Action, Type: p.Type, Name: p.Name, Description: p.Description,
		})
	}
	for _, role := range b.Roles {
		tr := &adminv1.TemplateRole{Key: role.Key, Name: role.Name, Description: role.Description, PermissionCodes: role.PermissionCodes}
		for _, ds := range role.DataScopes {
			tr.DataScopes = append(tr.DataScopes, &adminv1.TemplateDataScope{
				Resource: ds.Resource, Effect: ds.Effect, Condition: string(ds.Condition),
			})
		}
		out.Roles = append(out.Roles, tr)
	}
	return out, nil
}

// ApplyTenantTemplate 把本租户模板 apply 到本租户目标 app（复用同一 ApplyTemplate 引擎）。
func (s *AdminServer) ApplyTenantTemplate(ctx context.Context, r *adminv1.ApplyTenantTemplateRequest) (*adminv1.ApplyTemplateResponse, error) {
	var tenantID int64
	if err := s.db.QueryRowContext(ctx, `SELECT tenant_id FROM application WHERE id=$1`, int64(r.AppId)).Scan(&tenantID); err != nil {
		return nil, status.Errorf(codes.Internal, "resolve tenant: %v", err)
	}
	t, err := store.GetTenantTemplate(ctx, s.db, tenantID, int64(r.TemplateId))
	if errors.Is(err, store.ErrNotFound) {
		return nil, status.Error(codes.NotFound, "template not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get template: %v", err)
	}
	var b tenanttemplate.Bundle
	if err := json.Unmarshal(t.Bundle, &b); err != nil {
		return nil, status.Error(codes.Internal, "bundle parse")
	}
	perms, roles := bundleToApplyInputs(b)
	res, _, err := s.mgr.ApplyTemplate(ctx, int64(r.AppId), "tt-"+strconv.FormatInt(t.ID, 10), perms, roles)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "apply tenant template: %v", err)
	}
	return &adminv1.ApplyTemplateResponse{
		PermissionsUpserted: uint32(res.PermsUpserted), PermissionsSkipped: uint32(res.PermsSkipped),
		RolesCreated: uint32(res.RolesCreated), RolesSkipped: uint32(res.RolesSkipped),
		DataScopesCreated: uint32(res.DataScopesCreated),
	}, nil
}

// bundleToApplyInputs 把捕获 bundle 转为 ApplyTemplate 引擎输入（与 presets 路径解耦）。
func bundleToApplyInputs(b tenanttemplate.Bundle) ([]cp.PermissionPoint, []policy.TemplateRole) {
	perms := make([]cp.PermissionPoint, 0, len(b.Permissions))
	for _, p := range b.Permissions {
		perms = append(perms, cp.PermissionPoint{
			Code: p.Code, Resource: p.Resource, Action: p.Action, Type: p.Type, Name: p.Name, Description: p.Description,
		})
	}
	roles := make([]policy.TemplateRole, 0, len(b.Roles))
	for _, r := range b.Roles {
		tr := policy.TemplateRole{Key: r.Key, Name: r.Name, PermissionCodes: r.PermissionCodes}
		for _, ds := range r.DataScopes {
			tr.DataScopes = append(tr.DataScopes, policy.TemplateDataScope{
				Resource: ds.Resource, Effect: ds.Effect, Condition: string(ds.Condition),
			})
		}
		roles = append(roles, tr)
	}
	return perms, roles
}
