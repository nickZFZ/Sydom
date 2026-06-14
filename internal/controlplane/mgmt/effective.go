package mgmt

import (
	"context"
	"database/sql"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/effperm"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetEffectivePermissions 反查「某 user 在某 app 能做什么」。app 域 / tenant-scoped 只读。
// 鉴权由 AuthorizeRule(scopeApp) 前置完成；本 handler 只在只读 tx 内瞬态求值。
func (s *AdminServer) GetEffectivePermissions(ctx context.Context, r *adminv1.GetEffectivePermissionsRequest) (*adminv1.GetEffectivePermissionsResponse, error) {
	if r.UserId == "" {
		return nil, status.Error(codes.InvalidArgument, "user_id required")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin read tx: %v", err)
	}
	defer tx.Rollback() //nolint:errcheck

	res, err := effperm.Compute(ctx, tx, int64(r.AppId), r.UserId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "effective permissions: %v", err)
	}
	out := &adminv1.GetEffectivePermissionsResponse{Roles: res.Roles}
	for _, p := range res.Permissions {
		out.Permissions = append(out.Permissions, &adminv1.EffectivePermission{Resource: p.Resource, Action: p.Action})
	}
	for _, d := range res.DataViews {
		out.DataPreviews = append(out.DataPreviews, &adminv1.DataPolicyPreview{Resource: d.Resource, Match: d.Match, Predicate: d.Predicate})
	}
	return out, nil
}
