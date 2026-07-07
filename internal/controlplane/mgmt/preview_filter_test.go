package mgmt_test

import (
	"context"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/controlplane/outbox"
	"github.com/nickZFZ/Sydom/internal/controlplane/policy"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestAdminServer_PreviewDataFilter(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	dom := dbtest.SeedDomain
	insertCasbinRuleM(t, db, appID, "g", "alice", "viewer", dom)
	mustDataPolicy(t, db, appID, "viewer", "order",
		`{"op":"EQ","field":"dept","value":"$user.dept"}`)
	srv := mgmt.NewAdminServer(db, policy.NewPolicyManager(db, outbox.NewSink()), mk())

	resp, err := srv.PreviewDataFilter(context.Background(), &adminv1.PreviewDataFilterRequest{
		AppId: uint64(appID), Subject: "alice", Resource: "order", Attrs: map[string]string{"dept": "shanghai"},
	})
	require.NoError(t, err)
	require.Equal(t, "dept = ?", resp.Sql)
	require.Equal(t, []string{"shanghai"}, resp.Args)
}

// 缺变量 → InvalidArgument（报错而非误导性 SQL）。
func TestAdminServer_PreviewDataFilter_MissingVar(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	dom := dbtest.SeedDomain
	insertCasbinRuleM(t, db, appID, "g", "alice", "viewer", dom)
	mustDataPolicy(t, db, appID, "viewer", "order",
		`{"op":"EQ","field":"dept","value":"$user.dept"}`)
	srv := mgmt.NewAdminServer(db, policy.NewPolicyManager(db, outbox.NewSink()), mk())

	_, err := srv.PreviewDataFilter(context.Background(), &adminv1.PreviewDataFilterRequest{
		AppId: uint64(appID), Subject: "alice", Resource: "order", Attrs: map[string]string{},
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// subject/resource 缺 → InvalidArgument。
func TestAdminServer_PreviewDataFilter_RequiredArgs(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	srv := mgmt.NewAdminServer(db, policy.NewPolicyManager(db, outbox.NewSink()), mk())
	_, err := srv.PreviewDataFilter(context.Background(), &adminv1.PreviewDataFilterRequest{AppId: uint64(appID), Subject: "", Resource: "order"})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}
