package db

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMigration000023_PlanPricingAndSubscription 验证 M6-billing-1 迁移：
// plan 定价列（不 seed 价格）、subscription 生命周期表、回填、CHECK/唯一约束、down 对称。
func TestMigration000023_PlanPricingAndSubscription(t *testing.T) {
	dsn := startPostgres(t)
	require.NoError(t, RunMigrations(dsn, migrationsSource))
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	// plan 定价列存在，free 保持 0（AD-4 不 seed 价格），默认 month/CNY。
	var price int64
	var period, currency string
	require.NoError(t, db.QueryRow(
		`SELECT price_cents, billing_period, currency FROM plan WHERE name='free'`).
		Scan(&price, &period, &currency))
	require.Equal(t, int64(0), price)
	require.Equal(t, "month", period)
	require.Equal(t, "CNY", currency)

	// billing_period CHECK 拒非法值。
	_, err = db.Exec(`INSERT INTO plan (name, max_applications, max_members, billing_period)
		VALUES ('bad', 1, 1, 'weekly')`)
	require.Error(t, err, "billing_period CHECK 应拒 'weekly'")

	// subscription 表存在。
	require.True(t, tableExists(t, db, "subscription"))

	// uq_subscription_tenant：同租户第二条订阅→冲突。
	var tid int64
	require.NoError(t, db.QueryRow(
		`INSERT INTO tenant (name) VALUES ('mig-t') RETURNING id`).Scan(&tid))
	_, err = db.Exec(`INSERT INTO subscription (tenant_id) VALUES ($1)`, tid)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO subscription (tenant_id) VALUES ($1)`, tid)
	require.Error(t, err, "uq_subscription_tenant 应拒同租户第二条订阅")

	// ck_subscription_status 拒非法 status。
	_, err = db.Exec(`UPDATE subscription SET status='bogus' WHERE tenant_id=$1`, tid)
	require.Error(t, err, "ck_subscription_status 应拒非法 status")

	// down 干净回滚（subscription 表消失）。
	require.NoError(t, MigrateDown(dsn, migrationsSource))
	require.False(t, tableExists(t, db, "subscription"))
}
