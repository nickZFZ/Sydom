package mgmt

import (
	"errors"
	"fmt"
	"testing"

	"github.com/lib/pq"
	"github.com/nickZFZ/Sydom/internal/controlplane/policy"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// mapWriteErr 把 PolicyManager 写错误按领域 sentinel 细化为 gRPC status 码。
func TestMapWriteErr_Codes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want codes.Code
	}{
		{"唯一冲突→AlreadyExists", fmt.Errorf("%w: dup", policy.ErrConflict), codes.AlreadyExists},
		{"前提不满足→FailedPrecondition", fmt.Errorf("%w: fk", policy.ErrPrecondition), codes.FailedPrecondition},
		{"不存在→NotFound", fmt.Errorf("%w: x", policy.ErrNotFound), codes.NotFound},
		{"输入非法→InvalidArgument", fmt.Errorf("%w: bad id", policy.ErrInvalidInput), codes.InvalidArgument},
		{"未分类→Internal", errors.New("dial tcp: connection refused"), codes.Internal},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := mapWriteErr("write", c.err)
			require.Equal(t, c.want, status.Code(got))
		})
	}
}

// 分类码（非 Internal）绕过 SanitizeErrorUnaryInterceptor 直接回客户端，
// 因此其 message 绝不能带 raw pq 详情（约束名/表名会泄露 schema）。
func TestMapWriteErr_NoRawLeakForClassified(t *testing.T) {
	raw := &pq.Error{
		Code:       "23505",
		Message:    "duplicate key value violates unique constraint",
		Constraint: "uq_role_app_code",
		Table:      "role",
	}
	got := mapWriteErr("写入", fmt.Errorf("%w: %w", policy.ErrConflict, raw))
	require.Equal(t, codes.AlreadyExists, status.Code(got))
	msg := status.Convert(got).Message()
	require.NotContains(t, msg, "uq_role_app_code", "约束名不得泄露")
	require.NotContains(t, msg, "duplicate key")
	require.NotContains(t, msg, "constraint")
	require.NotContains(t, msg, "pq:", "raw 驱动错误前缀不得泄露")
}
