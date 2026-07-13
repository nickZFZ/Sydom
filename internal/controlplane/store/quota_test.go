package store_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestTenantPlanLimits_ReadAndNotFound(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db) // 建 acme 租户 + 一应用
	var tenantID int64
	require.NoError(t, db.QueryRow(`SELECT tenant_id FROM application WHERE id=$1`, appID).Scan(&tenantID))

	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	defer tx.Rollback()
	pl, err := store.TenantPlanLimits(context.Background(), tx, tenantID)
	require.NoError(t, err)
	require.Equal(t, 3, pl.MaxApplications, "默认 free 限 3")
	n, err := store.CountApplications(context.Background(), tx, tenantID)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	_, err = store.TenantPlanLimits(context.Background(), tx, 999999)
	require.ErrorIs(t, err, store.ErrNotFound)
}

func TestTenantUsageOf(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	var tenantID int64
	require.NoError(t, db.QueryRow(`SELECT tenant_id FROM application WHERE id=$1`, appID).Scan(&tenantID))

	u, err := store.TenantUsageOf(context.Background(), db, tenantID)
	require.NoError(t, err)
	require.Equal(t, "free", u.PlanName)
	require.Equal(t, 3, u.MaxApplications)
	require.Equal(t, 1, u.UsedApplications, "seed 1 应用")

	_, err = store.TenantUsageOf(context.Background(), db, 999999)
	require.ErrorIs(t, err, store.ErrNotFound)
}

// 并发 8 个 tx 各 TenantPlanLimits(FOR UPDATE)→+1 应用→commit：行锁串行，无交错超计。
func TestTenantPlanLimits_LockSerializes(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	var tenantID int64
	require.NoError(t, db.QueryRow(`SELECT tenant_id FROM application WHERE id=$1`, appID).Scan(&tenantID))
	// 抬高限额只测锁串行性（不测拒绝）
	_, err := db.Exec(`UPDATE plan SET max_applications=1000 WHERE id=(SELECT plan_id FROM tenant WHERE id=$1)`, tenantID)
	require.NoError(t, err)

	const N = 8
	var wg sync.WaitGroup
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tx, e := db.BeginTx(context.Background(), nil)
			if e != nil {
				errs <- e
				return
			}
			if _, e = store.TenantPlanLimits(context.Background(), tx, tenantID); e != nil {
				tx.Rollback()
				errs <- e
				return
			}
			if _, e = store.CountApplications(context.Background(), tx, tenantID); e != nil {
				tx.Rollback()
				errs <- e
				return
			}
			if _, e = tx.Exec(
				`INSERT INTO application (tenant_id, domain, name, app_key, app_secret_enc)
				 VALUES ($1,$2,$3,$4,'\xab'::bytea)`,
				tenantID, fmt.Sprintf("d%d", i), "n", fmt.Sprintf("ak_%d", i)); e != nil {
				tx.Rollback()
				errs <- e
				return
			}
			errs <- tx.Commit()
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		require.NoError(t, e)
	}
	var total int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM application WHERE tenant_id=$1`, tenantID).Scan(&total))
	require.Equal(t, 1+N, total, "1 seed + 8 并发插入，行锁串行无丢")
}
