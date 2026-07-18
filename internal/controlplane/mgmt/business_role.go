package mgmt

import (
	"context"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// CreateBusinessRole 业务语言建角色：原子建角色 + 批量授权（下沉 PolicyManager 单事务）。
func (s *AdminServer) CreateBusinessRole(ctx context.Context, r *adminv1.CreateBusinessRoleRequest) (*adminv1.CreateBusinessRoleResponse, error) {
	if r.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name required")
	}
	roleID, d, err := s.mgr.CreateBusinessRole(ctx, int64(r.AppId), r.Name, r.PermissionIds)
	if err != nil {
		return nil, mapWriteErr("create business role", err)
	}
	resp := &adminv1.CreateBusinessRoleResponse{RoleId: roleID}
	if d != nil {
		resp.Version, resp.Changed = uint64(d.Version), true
	}
	return resp, nil
}
