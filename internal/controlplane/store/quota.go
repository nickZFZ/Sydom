package store

import (
	"context"
	"database/sql"
	"errors"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
)

// PlanLimits 是租户套餐的资源上限（applications + members 维）。
type PlanLimits struct {
	MaxApplications int
	MaxMembers      int
}

// TenantPlanLimits 锁租户行（FOR UPDATE，序列化本租户资源创建）并返回其套餐限额。
// 须在调用方事务内调用（锁随 tx 生命周期）；租户不存在 → ErrNotFound。
// 一次锁 + 一次查满足应用门与成员门（DRY；CreateApplication 用 MaxApplications、InviteMember 用 MaxMembers）。
func TenantPlanLimits(ctx context.Context, ex cp.DBTX, tenantID int64) (PlanLimits, error) {
	var pl PlanLimits
	err := ex.QueryRowContext(ctx,
		`SELECT p.max_applications, p.max_members
		   FROM tenant t JOIN plan p ON p.id = t.plan_id
		  WHERE t.id = $1 FOR UPDATE OF t`, tenantID).Scan(&pl.MaxApplications, &pl.MaxMembers)
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

// CountMembers 返回租户当前成员数（同 tx 内，配合 TenantPlanLimits 的锁）。
func CountMembers(ctx context.Context, ex cp.DBTX, tenantID int64) (int, error) {
	var n int
	err := ex.QueryRowContext(ctx, `SELECT count(*) FROM tenant_membership WHERE tenant_id=$1`, tenantID).Scan(&n)
	return n, err
}

// TenantUsage 是租户的套餐名 + 各资源用量/上限（applications + members）。
type TenantUsage struct {
	PlanName         string
	MaxApplications  int
	UsedApplications int
	MaxMembers       int
	UsedMembers      int
}

// TenantUsageOf 只读返租户套餐名 + 应用/成员上限与当前用量（无锁，读路径）。租户不存在→ErrNotFound。
func TenantUsageOf(ctx context.Context, ex cp.DBTX, tenantID int64) (TenantUsage, error) {
	var u TenantUsage
	err := ex.QueryRowContext(ctx,
		`SELECT p.name, p.max_applications,
		        (SELECT count(*) FROM application a WHERE a.tenant_id = t.id),
		        p.max_members,
		        (SELECT count(*) FROM tenant_membership tm WHERE tm.tenant_id = t.id)
		   FROM tenant t JOIN plan p ON p.id = t.plan_id WHERE t.id = $1`,
		tenantID).Scan(&u.PlanName, &u.MaxApplications, &u.UsedApplications, &u.MaxMembers, &u.UsedMembers)
	if errors.Is(err, sql.ErrNoRows) {
		return TenantUsage{}, ErrNotFound
	}
	return u, err
}
