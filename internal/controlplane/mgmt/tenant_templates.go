package mgmt

import (
	"context"
	"encoding/json"
	"errors"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
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
