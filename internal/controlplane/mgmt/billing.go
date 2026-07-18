package mgmt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ChangeTenantPlan 变更租户套餐（仅平台超管，ruleTable scopeSystem）。
// 单事务锁租户行 → 改 tenant.plan_id（套餐真相源）+ 重置 subscription 周期 → 审计。
func (s *AdminServer) ChangeTenantPlan(ctx context.Context, r *adminv1.ChangeTenantPlanRequest) (*adminv1.ChangeTenantPlanResponse, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin: %v", err)
	}
	defer tx.Rollback()

	var before int64
	if err := tx.QueryRowContext(ctx,
		`SELECT plan_id FROM tenant WHERE id=$1 FOR UPDATE`, int64(r.TenantId)).Scan(&before); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "unknown tenant")
		}
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := store.ChangeTenantPlanTx(ctx, tx, int64(r.TenantId), int64(r.PlanId), time.Now()); err != nil {
		if isForeignKeyViolation(err) {
			return nil, status.Error(codes.FailedPrecondition, "unknown plan")
		}
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := adminauthz.InsertAdminAudit(ctx, tx,
		sql.NullInt64{Int64: int64(r.TenantId), Valid: true}, cp.OperatorFromContext(ctx),
		"change_plan", "tenant", fmt.Sprintf("%d", r.TenantId),
		auditJSON(map[string]any{"before": before, "after": r.PlanId}),
		sql.NullInt64{}); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "commit: %v", err)
	}

	sub, err := store.SubscriptionOf(ctx, s.db, int64(r.TenantId))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read subscription: %v", err)
	}
	resp := &adminv1.ChangeTenantPlanResponse{
		TenantId: r.TenantId, PlanId: r.PlanId, Status: sub.Status,
	}
	if sub.CurrentPeriodEnd.Valid {
		resp.CurrentPeriodEnd = sub.CurrentPeriodEnd.Time.Format(time.RFC3339)
	}
	return resp, nil
}
