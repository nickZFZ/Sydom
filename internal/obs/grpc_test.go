package obs

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestUnaryInterceptor_CountsAndClassifiesCode(t *testing.T) {
	m := New()
	interceptor := m.UnaryServerInterceptor(nil) // logger=nil → 用 slog.Default
	info := &grpc.UnaryServerInfo{FullMethod: "/sydom.admin.v1.AdminService/ListRoles"}

	// OK 一次
	_, err := interceptor(context.Background(), nil, info,
		func(ctx context.Context, req any) (any, error) { return "ok", nil })
	require.NoError(t, err)
	// PermissionDenied 一次（模拟被 authz 拒——obs 在最外层能计入）
	_, err = interceptor(context.Background(), nil, info,
		func(ctx context.Context, req any) (any, error) {
			return nil, status.Error(codes.PermissionDenied, "no")
		})
	require.Error(t, err)

	require.Equal(t, 1.0, testutil.ToFloat64(m.grpcReqs.WithLabelValues("AdminService", "ListRoles", "OK")))
	require.Equal(t, 1.0, testutil.ToFloat64(m.grpcReqs.WithLabelValues("AdminService", "ListRoles", "PermissionDenied")))
}
