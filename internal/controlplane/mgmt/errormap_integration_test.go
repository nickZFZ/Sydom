package mgmt_test

import (
	"context"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// 端到端验证写错误语义细化：真实 pq 违约经 classify→mapWriteErr 映射为可操作的 gRPC 码，
// 而非先前一律 codes.Internal。直调 handler（不过 gRPC 拦截链）足以断言 status 码——
// 分类码本就绕过脱敏拦截器。

// 唯一约束冲突（重复角色 code，uq_role_app_code / SQLSTATE 23505）→ AlreadyExists。
func TestWriteErr_DuplicateRoleCode_AlreadyExists(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	srv := accountsSrv(db)
	ctx := context.Background()

	_, err := srv.CreateRole(ctx, &adminv1.CreateRoleRequest{AppId: uint64(appID), Code: "dup", Name: "n1"})
	require.NoError(t, err)

	_, err = srv.CreateRole(ctx, &adminv1.CreateRoleRequest{AppId: uint64(appID), Code: "dup", Name: "n2"})
	require.Equal(t, codes.AlreadyExists, status.Code(err), "重复 code 须回 AlreadyExists 而非 Internal")
	require.NotContains(t, status.Convert(err).Message(), "uq_role_app_code", "约束名不得泄露给客户端")
}

// 外键引用缺失（授权给不存在的 role/permission，SQLSTATE 23503）→ FailedPrecondition。
func TestWriteErr_GrantMissingRefs_FailedPrecondition(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	srv := accountsSrv(db)
	ctx := context.Background()

	_, err := srv.GrantPermission(ctx, &adminv1.GrantPermissionRequest{
		AppId: uint64(appID), RoleId: 999999, PermissionId: 888888, Eft: "allow",
	})
	require.Equal(t, codes.FailedPrecondition, status.Code(err), "引用缺失须回 FailedPrecondition 而非 Internal")
}

// 更新不存在的数据策略（id 无对应行，UPDATE 命中 0 行）→ NotFound。
// store 先前返回裸 fmt.Errorf 未带 store.ErrNotFound，classify 认不出 → 落 Internal。
func TestWriteErr_UpsertDataPolicyMissingID_NotFound(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	srv := accountsSrv(db)
	ctx := context.Background()

	_, err := srv.UpsertDataPolicy(ctx, &adminv1.UpsertDataPolicyRequest{
		AppId: uint64(appID), Id: 999999, SubjectType: "role", SubjectId: "m",
		Resource: "order", Condition: `{"field":"dept","op":"EQ","value":"$user.dept"}`, Effect: "allow",
	})
	require.Equal(t, codes.NotFound, status.Code(err), "更新不存在的数据策略须回 NotFound 而非 Internal")
	require.NotContains(t, status.Convert(err).Message(), "999999", "内部行 id/表细节不得泄露给客户端")
}

// 删除不存在的数据策略（DELETE 命中 0 行，fail-close 报错）→ NotFound。
func TestWriteErr_DeleteDataPolicyMissingID_NotFound(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	srv := accountsSrv(db)
	ctx := context.Background()

	_, err := srv.DeleteDataPolicy(ctx, &adminv1.DeleteDataPolicyRequest{
		AppId: uint64(appID), DataPolicyId: 999999,
	})
	require.Equal(t, codes.NotFound, status.Code(err), "删除不存在的数据策略须回 NotFound 而非 Internal")
}
