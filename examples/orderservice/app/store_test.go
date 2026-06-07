package app_test

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/lib/pq"
	oapp "github.com/nickZFZ/Sydom/examples/orderservice/app"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/nickZFZ/Sydom/sdk/go/sydom"
	"github.com/stretchr/testify/require"
)

func setupOrders(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("postgres", dbtest.StartPostgres(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	require.NoError(t, oapp.EnsureSchema(ctx, db))
	require.NoError(t, oapp.SeedOrders(ctx, db))
	return db
}

func TestListOrders_MatchAll_ReturnsAll(t *testing.T) {
	db := setupOrders(t)
	got, err := oapp.ListOrders(context.Background(), db, sydom.FilterResult{SQL: "", Args: nil})
	require.NoError(t, err)
	require.Len(t, got, 6) // 无过滤=全部 6 行
}

func TestListOrders_DenyAll_ReturnsZero(t *testing.T) {
	db := setupOrders(t)
	got, err := oapp.ListOrders(context.Background(), db, sydom.FilterResult{SQL: "1=0", Args: nil})
	require.NoError(t, err)
	require.Len(t, got, 0) // deny-all 绝不退化为全表
}

func TestListOrders_Conditional_FiltersByDept(t *testing.T) {
	db := setupOrders(t)
	got, err := oapp.ListOrders(context.Background(), db,
		sydom.FilterResult{SQL: "dept = ?", Args: []any{"shanghai"}})
	require.NoError(t, err)
	require.Len(t, got, 3)
	for _, o := range got {
		require.Equal(t, "shanghai", o.Dept)
	}
}

func TestDeleteOrder(t *testing.T) {
	db := setupOrders(t)
	ok, err := oapp.DeleteOrder(context.Background(), db, 1)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = oapp.DeleteOrder(context.Background(), db, 999999)
	require.NoError(t, err)
	require.False(t, ok)
}
