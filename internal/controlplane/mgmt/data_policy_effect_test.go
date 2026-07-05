package mgmt_test

import (
	"context"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestAdminService_UpsertDataPolicy_Effect(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	require.NoError(t, adminauthz.EnsureRootOperator(context.Background(), db, mk(), "root", []byte("root-secret")))
	cli := dialMgmt(t, db, "root", []byte("root-secret"))
	ctx := context.Background()

	// effect="deny" 落库为 deny
	_, err := cli.UpsertDataPolicy(ctx, &adminv1.UpsertDataPolicyRequest{
		AppId: uint64(appID), SubjectType: "role", SubjectId: "manager", Resource: "order", Condition: `{"field":"dept","op":"EQ","value":"$user.dept"}`, Effect: "deny",
	})
	require.NoError(t, err)

	// effect="" 归一为 allow
	_, err = cli.UpsertDataPolicy(ctx, &adminv1.UpsertDataPolicyRequest{
		AppId: uint64(appID), SubjectType: "user", SubjectId: "alice", Resource: "invoice", Condition: `{"field":"dept","op":"EQ","value":"$user.dept"}`, Effect: "",
	})
	require.NoError(t, err)

	got, err := store.ReadAppDataPolicies(ctx, db, appID)
	require.NoError(t, err)
	effs := map[string]string{} // resource→effect
	for _, p := range got {
		effs[p.Resource] = p.Effect
	}
	require.Equal(t, "deny", effs["order"])
	require.Equal(t, "allow", effs["invoice"])

	// effect="bogus" 返 InvalidArgument 且不落库
	_, err = cli.UpsertDataPolicy(ctx, &adminv1.UpsertDataPolicyRequest{
		AppId: uint64(appID), SubjectType: "role", SubjectId: "m", Resource: "audit", Condition: "{}", Effect: "bogus",
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	after, _ := store.ReadAppDataPolicies(ctx, db, appID)
	require.Len(t, after, 2) // 仍只有 order + invoice
}
