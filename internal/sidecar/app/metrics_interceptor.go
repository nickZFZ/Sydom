package app

import (
	"context"
	"time"

	authv1 "github.com/nickZFZ/Sydom/gen/sydom/auth/v1"
	"github.com/nickZFZ/Sydom/internal/obs"
	"google.golang.org/grpc"
)

// decisionInterceptor 从 sidecar 授权响应读判定结果记指标（只读响应，绝不改判定）。
// Check → 一个 allow/deny；BatchCheck → 逐条 allow/deny；每次 RPC 记一次 Check 耗时。
func decisionInterceptor(m *obs.Metrics) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		dur := time.Since(start)
		switch r := resp.(type) {
		case *authv1.CheckResponse:
			m.ObserveCheck(dur)
			m.AuthzDecision(r.GetAllowed())
		case *authv1.BatchCheckResponse:
			m.ObserveCheck(dur)
			for _, a := range r.GetAllowed() {
				m.AuthzDecision(a)
			}
		}
		return resp, err
	}
}
