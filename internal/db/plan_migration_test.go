package db

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration_PlanQuota(t *testing.T) {
	dsn := startPostgres(t)
	require.NoError(t, RunMigrations(dsn, migrationsSource))
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	// plan 表存在 + seed free/pro
	require.True(t, tableExists(t, db, "plan"))
	var freeMax, proMax int
	require.NoError(t, db.QueryRow(`SELECT max_applications FROM plan WHERE name='free'`).Scan(&freeMax))
	require.NoError(t, db.QueryRow(`SELECT max_applications FROM plan WHERE name='pro'`).Scan(&proMax))
	require.Equal(t, 3, freeMax)
	require.Equal(t, 50, proMax)

	// M6.1d：max_members 列 + 种子 free=3/pro=25
	var freeMem, proMem int
	require.NoError(t, db.QueryRow(`SELECT max_members FROM plan WHERE name='free'`).Scan(&freeMem))
	require.NoError(t, db.QueryRow(`SELECT max_members FROM plan WHERE name='pro'`).Scan(&proMem))
	require.Equal(t, 3, freeMem)
	require.Equal(t, 25, proMem)

	// tenant.plan_id 存在且默认 free(id=1)
	var tid, planID int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant (name) VALUES ('t') RETURNING id, plan_id`).Scan(&tid, &planID))
	require.Equal(t, int64(1), planID, "新租户应默认 free(id=1)")

	// down 干净回滚
	require.NoError(t, MigrateDown(dsn, migrationsSource))
	require.False(t, tableExists(t, db, "plan"))
}
