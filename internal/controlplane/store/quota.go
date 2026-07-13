package store

import (
	"context"
	"database/sql"
	"errors"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
)

// PlanLimits 是租户套餐的资源上限（本增量仅 applications 维）。
type PlanLimits struct {
	MaxApplications int
}

// TenantPlanLimits 锁租户行（FOR UPDATE，序列化本租户资源创建）并返回其套餐限额。
// 须在调用方事务内调用（锁随 tx 生命周期）；租户不存在 → ErrNotFound。
func TenantPlanLimits(ctx context.Context, ex cp.DBTX, tenantID int64) (PlanLimits, error) {
	var pl PlanLimits
	err := ex.QueryRowContext(ctx,
		`SELECT p.max_applications
		   FROM tenant t JOIN plan p ON p.id = t.plan_id
		  WHERE t.id = $1 FOR UPDATE OF t`, tenantID).Scan(&pl.MaxApplications)
	if errors.Is(err, sql.ErrNoRows) {
		return PlanLimits{}, ErrNotFound
	}
	return pl, err
}

// CountApplications 返回租户当前应用数（同 tx 内，配合 TenantPlanLimits 的锁）。
func CountApplications(ctx context.Context, ex cp.DBTX, tenantID int64) (int, error) {
	var n int
	err := ex.QueryRowContext(ctx, `SELECT count(*) FROM application WHERE tenant_id=$1`, tenantID).Scan(&n)
	return n, err
}
