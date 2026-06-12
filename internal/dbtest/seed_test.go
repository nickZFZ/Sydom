package dbtest_test

import (
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestSeedAppInTenant_MultiTenant(t *testing.T) {
	db := dbtest.SetupSchema(t)
	tA, appA := dbtest.SeedAppInTenant(t, db, "tenant-a", "app-a", "AK_a")
	tB, appB := dbtest.SeedAppInTenant(t, db, "tenant-b", "app-b", "AK_b")
	require.NotZero(t, tA)
	require.NotZero(t, appA)
	require.NotEqual(t, tA, tB, "两个租户必须不同")
	require.NotEqual(t, appA, appB, "两个应用必须不同")
}
