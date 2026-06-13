package auth_test

import (
	"context"
	"testing"

	"github.com/nickZFZ/Sydom/internal/auth"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type denyResolver struct{}

func (denyResolver) ResolveSecret(context.Context, string) ([]byte, error) {
	return nil, status.Error(codes.Unauthenticated, "no")
}

func TestUnaryServerInterceptorExempt(t *testing.T) {
	exempt := map[string]bool{"/svc/Public": true}
	ic := auth.UnaryServerInterceptorExempt(denyResolver{}, exempt)
	handler := func(ctx context.Context, req any) (any, error) { return "ok", nil }

	// 豁免方法：无凭据也放行。
	resp, err := ic(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/svc/Public"}, handler)
	require.NoError(t, err)
	require.Equal(t, "ok", resp)

	// 非豁免方法：无凭据 → Unauthenticated。
	_, err = ic(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/svc/Private"}, handler)
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}
