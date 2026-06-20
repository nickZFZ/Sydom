package mgmt_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestSanitizeErrorInterceptor 验证错误脱敏边界：Internal/Unknown（含裸 error）回通用文案，
// 安全码（NotFound/InvalidArgument/PermissionDenied）文案原样透出，nil 错误 resp 透传。
func TestSanitizeErrorInterceptor(t *testing.T) {
	icept := mgmt.SanitizeErrorUnaryInterceptor(slog.New(slog.NewTextHandler(io.Discard, nil)))
	info := &grpc.UnaryServerInfo{FullMethod: "/sydom.admin.v1.AdminService/ListRoles"}
	call := func(retErr error) (any, error) {
		return icept(context.Background(), nil, info,
			func(ctx context.Context, req any) (any, error) { return "ok", retErr })
	}

	// Internal（含 SQL 内部细节）→ 脱敏为通用文案，code 保留
	_, err := call(status.Error(codes.Internal, `count roles: pq: relation "role" does not exist`))
	st := status.Convert(err)
	require.Equal(t, codes.Internal, st.Code())
	require.Equal(t, "internal error", st.Message())

	// 裸 error（status.Code → Unknown）→ 脱敏，原始 detail 不外泄
	_, err = call(errors.New("raw boom leaking secret context"))
	st = status.Convert(err)
	require.Equal(t, codes.Unknown, st.Code())
	require.Equal(t, "internal error", st.Message())

	// 安全码：文案是 API 契约，原样透出
	for _, c := range []codes.Code{codes.NotFound, codes.InvalidArgument, codes.PermissionDenied} {
		_, err = call(status.Error(c, "tenant_name required"))
		st = status.Convert(err)
		require.Equal(t, c, st.Code())
		require.Equal(t, "tenant_name required", st.Message())
	}

	// nil 错误 → resp 透传
	resp, err := call(nil)
	require.NoError(t, err)
	require.Equal(t, "ok", resp)
}
