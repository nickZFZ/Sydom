package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestChangeTenantPlanTx_UpdatesPlanAndPeriod(t *testing.T) {
	db := dbtest.SetupSchema(t)
	dbtest.SeedApp(t, db) // 建 tenant(含订阅)+app
	var tid int64
	require.NoError(t, db.QueryRow(`SELECT id FROM tenant WHERE name='acme'`).Scan(&tid))
	// pro 设价格 + 年周期，验证 period_end = now + 1 年。
	_, err := db.Exec(`UPDATE plan SET price_cents=9900, billing_period='year' WHERE name='pro'`)
	require.NoError(t, err)
	var proID int64
	require.NoError(t, db.QueryRow(`SELECT id FROM plan WHERE name='pro'`).Scan(&proID))

	now := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	require.NoError(t, store.ChangeTenantPlanTx(context.Background(), tx, tid, proID, now))
	require.NoError(t, tx.Commit())

	var planID int64
	require.NoError(t, db.QueryRow(`SELECT plan_id FROM tenant WHERE id=$1`, tid).Scan(&planID))
	require.Equal(t, proID, planID, "tenant.plan_id 应变为 pro")

	sub, err := store.SubscriptionOf(context.Background(), db, tid)
	require.NoError(t, err)
	require.Equal(t, "active", sub.Status)
	require.True(t, sub.CurrentPeriodEnd.Valid)
	require.Equal(t, now.AddDate(1, 0, 0), sub.CurrentPeriodEnd.Time.UTC())
}

func TestTenantUsageOf_EchoesPricing(t *testing.T) {
	db := dbtest.SetupSchema(t)
	dbtest.SeedApp(t, db)
	var tid int64
	require.NoError(t, db.QueryRow(`SELECT id FROM tenant WHERE name='acme'`).Scan(&tid))

	u, err := store.TenantUsageOf(context.Background(), db, tid)
	require.NoError(t, err)
	require.Equal(t, "free", u.PlanName)
	require.Equal(t, int64(0), u.PriceCents)
	require.Equal(t, "CNY", u.Currency)
	require.Equal(t, "month", u.BillingPeriod)
	require.Equal(t, "active", u.SubStatus, "SeedApp 已建订阅")
}
