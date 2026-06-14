package store_test

import (
	"context"
	"testing"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestUpsertDataPolicy_DescriptionRoundTrip(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	ctx := context.Background()

	id, created, err := store.UpsertDataPolicy(ctx, db, appID, cp.DataPolicy{
		SubjectType: "role", SubjectID: "sales", Resource: "orders",
		Condition: `{"op":"EQ","field":"region","value":"east"}`, Effect: "allow",
		Description: "仅限本人区域的订单",
	}, 1)
	require.NoError(t, err)
	require.True(t, created)

	dps, err := store.ReadAppDataPolicies(ctx, db, appID)
	require.NoError(t, err)
	require.Len(t, dps, 1)
	require.Equal(t, int64(id), dps[0].ID)
	require.Equal(t, "仅限本人区域的订单", dps[0].Description)
}
