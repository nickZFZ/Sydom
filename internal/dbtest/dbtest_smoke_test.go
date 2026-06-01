package dbtest_test

import (
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestDBTest_SetupAndSeed(t *testing.T) {
	conn := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, conn)
	require.Positive(t, appID)

	// casbin_rule 表存在且初始为空
	var n int
	require.NoError(t, conn.QueryRow(
		`SELECT count(*) FROM casbin_rule WHERE app_id = $1`, appID).Scan(&n))
	require.Equal(t, 0, n)

	// 种子应用的 domain 可读且等于约定值
	var domain string
	require.NoError(t, conn.QueryRow(
		`SELECT domain FROM application WHERE id = $1`, appID).Scan(&domain))
	require.Equal(t, dbtest.SeedDomain, domain)
}
