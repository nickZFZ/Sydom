package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
)

// Subscription 是租户的订阅生命周期状态（不含 plan_id：套餐真相源在 tenant.plan_id）。
type Subscription struct {
	TenantID           int64
	Status             string
	CurrentPeriodStart time.Time
	CurrentPeriodEnd   sql.NullTime
}

// SubscriptionOf 读租户订阅（无锁读路径）；无行→ErrNotFound。
func SubscriptionOf(ctx context.Context, ex cp.DBTX, tenantID int64) (Subscription, error) {
	var s Subscription
	err := ex.QueryRowContext(ctx,
		`SELECT tenant_id, status, current_period_start, current_period_end
		   FROM subscription WHERE tenant_id=$1`, tenantID).
		Scan(&s.TenantID, &s.Status, &s.CurrentPeriodStart, &s.CurrentPeriodEnd)
	if errors.Is(err, sql.ErrNoRows) {
		return Subscription{}, ErrNotFound
	}
	return s, err
}

// ChangeTenantPlanTx 在调用方事务内改租户套餐指针 + 重置订阅周期。
// tenant 不存在→ErrNotFound；plan 不存在→FK 违约(pq 23503)由调用方映射为 FailedPrecondition。
// period_end：目标 plan price_cents=0→NULL（无到期）；否则 month→+1 月、year→+1 年。
func ChangeTenantPlanTx(ctx context.Context, tx cp.DBTX, tenantID, planID int64, now time.Time) error {
	res, err := tx.ExecContext(ctx,
		`UPDATE tenant SET plan_id=$1, updated_at=now() WHERE id=$2`, planID, tenantID)
	if err != nil {
		return err // plan 不存在→FK 23503
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	var period string
	var price int64
	if err := tx.QueryRowContext(ctx,
		`SELECT billing_period, price_cents FROM plan WHERE id=$1`, planID).Scan(&period, &price); err != nil {
		return err
	}
	var end sql.NullTime
	if price > 0 {
		if period == "year" {
			end = sql.NullTime{Time: now.AddDate(1, 0, 0), Valid: true}
		} else {
			end = sql.NullTime{Time: now.AddDate(0, 1, 0), Valid: true}
		}
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE subscription SET current_period_start=$1, current_period_end=$2, updated_at=now()
		  WHERE tenant_id=$3`, now, end, tenantID)
	return err
}
