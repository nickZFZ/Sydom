package store_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestQueryAppAudit_KeysetAndFilter(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	for i := 1; i <= 5; i++ {
		et := "role"
		if i%2 == 0 {
			et = "data_policy"
		}
		require.NoError(t, store.InsertAudit(context.Background(), db, appID,
			"alice", "create", et, fmt.Sprintf("%d", i), []byte(`{"adds":[]}`), int64(i)))
	}
	// 首页 limit=2 → 最新两条(id 降序)，nextCursor 非 0
	e1, c1, err := store.QueryAppAudit(context.Background(), db, appID, store.AppAuditFilter{Limit: 2})
	require.NoError(t, err)
	require.Len(t, e1, 2)
	require.NotZero(t, c1)
	// 次页接 cursor，不重叠
	e2, _, err := store.QueryAppAudit(context.Background(), db, appID, store.AppAuditFilter{Limit: 2, Cursor: c1})
	require.NoError(t, err)
	require.Len(t, e2, 2)
	require.Less(t, e2[0].ID, e1[1].ID)
	// 过滤 entity_type=role
	ef, _, err := store.QueryAppAudit(context.Background(), db, appID,
		store.AppAuditFilter{Limit: 50, EntityType: "role"})
	require.NoError(t, err)
	for _, e := range ef {
		require.Equal(t, "role", e.EntityType)
	}
}
