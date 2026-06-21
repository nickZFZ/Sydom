package store_test

import (
	"context"
	"testing"

	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestTenantTemplateCRUD(t *testing.T) {
	db := dbtest.SetupSchema(t)
	tID, appID := dbtest.SeedAppInTenant(t, db, "t-a", "dom-a", "AK_a")
	ctx := context.Background()
	bundle := []byte(`{"permissions":[],"roles":[]}`)

	id, err := store.InsertTenantTemplate(ctx, db, tID, "标准后台", "通用", bundle, appID)
	require.NoError(t, err)
	require.NotZero(t, id)

	// 同租户重名→ErrConflict。
	_, err = store.InsertTenantTemplate(ctx, db, tID, "标准后台", "x", bundle, appID)
	require.ErrorIs(t, err, store.ErrConflict)

	// Get（tenant-scoped）。
	got, err := store.GetTenantTemplate(ctx, db, tID, id)
	require.NoError(t, err)
	require.Equal(t, "标准后台", got.Name)
	require.JSONEq(t, `{"permissions":[],"roles":[]}`, string(got.Bundle))

	// 跨租户 Get→ErrNotFound。
	tB, _ := dbtest.SeedAppInTenant(t, db, "t-b", "dom-b", "AK_b")
	_, err = store.GetTenantTemplate(ctx, db, tB, id)
	require.ErrorIs(t, err, store.ErrNotFound)

	// List（tenant-scoped）。
	rows, total, err := store.ListTenantTemplates(ctx, db, tID, 50, 0, "id", "ASC", "")
	require.NoError(t, err)
	require.Equal(t, uint32(1), total)
	require.Len(t, rows, 1)

	// Delete（tenant-scoped；跨租户→ErrNotFound）。
	require.ErrorIs(t, store.DeleteTenantTemplate(ctx, db, tB, id), store.ErrNotFound)
	require.NoError(t, store.DeleteTenantTemplate(ctx, db, tID, id))
	_, err = store.GetTenantTemplate(ctx, db, tID, id)
	require.ErrorIs(t, err, store.ErrNotFound)
}
