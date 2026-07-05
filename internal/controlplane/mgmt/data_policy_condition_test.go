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

// TestAdminService_UpsertDataPolicy_InvalidCondition_Rejected：M4.3 任务3——写入路径必须
// 校验 condition（复用 dataperm.ValidateCondition，与数据面 eval 同一文法定义），非法条件
// 经 gRPC handler 一律 InvalidArgument 且绝不落库（fail-close 前移到写时，而非只在评估时中毒）。
// 表驱动覆盖多类非法：未知算子 / 非法字段名（防注入） / IN 需非空数组 / 空串。
func TestAdminService_UpsertDataPolicy_InvalidCondition_Rejected(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	require.NoError(t, adminauthz.EnsureRootOperator(context.Background(), db, mk(), "root", []byte("root-secret")))
	cli := dialMgmt(t, db, "root", []byte("root-secret"))
	ctx := context.Background()

	bad := []string{
		`{"op":"ALL"}`, // 未知算子
		`{"field":"a;DROP","op":"EQ","value":"x"}`, // 非法字段名
		`{"field":"a","op":"IN","value":"notarr"}`, // IN 非数组
		``, // 空串
	}
	for _, cond := range bad {
		_, err := cli.UpsertDataPolicy(ctx, &adminv1.UpsertDataPolicyRequest{
			AppId: uint64(appID), SubjectType: "role", SubjectId: "manager", Resource: "order",
			Condition: cond, Effect: "allow",
		})
		require.Equal(t, codes.InvalidArgument, status.Code(err), "cond=%q 应被拒", cond)
	}
	// 一条都不落库。
	after, err := store.ReadAppDataPolicies(ctx, db, appID)
	require.NoError(t, err)
	require.Len(t, after, 0)
}

// TestAdminService_UpsertDataPolicy_ValidCondition_OK：合法条件仍正常写入成功（正向对照，
// 避免上面的拒绝测试掩盖「校验把合法条件也一并误拒」的回归）。
func TestAdminService_UpsertDataPolicy_ValidCondition_OK(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	require.NoError(t, adminauthz.EnsureRootOperator(context.Background(), db, mk(), "root", []byte("root-secret")))
	cli := dialMgmt(t, db, "root", []byte("root-secret"))
	ctx := context.Background()

	resp, err := cli.UpsertDataPolicy(ctx, &adminv1.UpsertDataPolicyRequest{
		AppId: uint64(appID), SubjectType: "role", SubjectId: "manager", Resource: "order",
		Condition: `{"field":"dept","op":"EQ","value":"$user.dept"}`, Effect: "allow",
	})
	require.NoError(t, err)
	require.True(t, resp.Changed)
}
