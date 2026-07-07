package obs

import (
	"context"
	"log/slog"
	"strings"
	"time"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// UnaryServerInterceptor 记 gRPC RED 指标 + 每请求一条结构化访问日志（request_id·principal·app·code·耗时）。
// 应挂在拦截器链**最外层**（计入被 auth/authz 拒绝的请求）。m==nil 时退化为纯访问日志（指标 no-op）。
func (m *Metrics) UnaryServerInterceptor(logger *slog.Logger) grpc.UnaryServerInterceptor {
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		rid := requestIDFromMD(ctx)
		ctx = With(ctx, logger.With("request_id", rid))
		resp, err := handler(ctx, req)
		dur := time.Since(start)
		code := status.Code(err)
		service, method := splitFullMethod(info.FullMethod)
		m.ObserveGRPC(service, method, code.String(), dur)
		logger.Info("grpc_request",
			"request_id", rid, "service", service, "method", method,
			"code", code.String(), "duration_ms", dur.Milliseconds(),
			"principal", cp.OperatorFromContext(ctx), "app", appIDOf(req))
		return resp, err
	}
}

// splitFullMethod "/pkg.Svc/Method" → ("Svc","Method")；异常输入退化为 ("unknown", full)。
func splitFullMethod(full string) (service, method string) {
	full = strings.TrimPrefix(full, "/")
	i := strings.LastIndex(full, "/")
	if i < 0 {
		return "unknown", full
	}
	svc := full[:i]
	if j := strings.LastIndex(svc, "."); j >= 0 {
		svc = svc[j+1:] // pkg.AdminService → AdminService
	}
	return svc, full[i+1:]
}

// appIDOf 复用既有 appIDGetter 式鸭子类型：请求含 GetAppId() 则取，否则空（低基数，仅进日志）。
func appIDOf(req any) uint64 {
	if g, ok := req.(interface{ GetAppId() uint64 }); ok {
		return g.GetAppId()
	}
	return 0
}

// requestIDFromMD 入站 metadata x-request-id 有则透传，无则生成。
func requestIDFromMD(ctx context.Context) string {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if v := md.Get("x-request-id"); len(v) > 0 && v[0] != "" {
			return v[0]
		}
	}
	return newRequestID()
}
