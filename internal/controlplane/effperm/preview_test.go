package effperm_test

import (
	"context"
	"testing"

	"github.com/nickZFZ/Sydom/internal/controlplane/effperm"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/nickZFZ/Sydom/internal/sidecar/dataperm"
	"github.com/stretchr/testify/require"
)

func TestPreviewFilter_RendersParameterizedSQL(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	dom := dbtest.SeedDomain
	insertRule(t, db, appID, "g", "alice", "viewer", dom)
	insertDataPolicy(t, db, appID, "role", "viewer", "order", "allow",
		`{"op":"EQ","field":"dept","value":"$user.dept"}`)

	res, err := effperm.PreviewFilter(context.Background(), db, appID, "alice", "order", map[string]any{"dept": "shanghai"})
	require.NoError(t, err)
	require.Equal(t, "dept = ?", res.SQL)
	require.Equal(t, []any{"shanghai"}, res.Args)
}

func TestPreviewFilter_MissingVar(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	dom := dbtest.SeedDomain
	insertRule(t, db, appID, "g", "alice", "viewer", dom)
	insertDataPolicy(t, db, appID, "role", "viewer", "order", "allow",
		`{"op":"EQ","field":"dept","value":"$user.dept"}`)

	_, err := effperm.PreviewFilter(context.Background(), db, appID, "alice", "order", map[string]any{}) // 缺 dept
	require.ErrorIs(t, err, dataperm.ErrMissingVar)
}
