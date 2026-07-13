package mgmt

import (
	"context"
	"errors"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetTenantUsage 只读返租户套餐 + 应用用量/上限。授权由拦截器经 ruleTable(scopeTenant) 完成：
// 租户看自己、root 看全部、跨租户 PermissionDenied 早于本 handler。
func (s *AdminServer) GetTenantUsage(ctx context.Context, r *adminv1.GetTenantUsageRequest) (*adminv1.GetTenantUsageResponse, error) {
	u, err := store.TenantUsageOf(ctx, s.db, int64(r.TenantId))
	if errors.Is(err, store.ErrNotFound) {
		return nil, status.Error(codes.NotFound, "tenant not found")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	return &adminv1.GetTenantUsageResponse{
		PlanName:     u.PlanName,
		Applications: &adminv1.ResourceUsage{Used: uint32(u.UsedApplications), Limit: uint32(u.MaxApplications)},
	}, nil
}
