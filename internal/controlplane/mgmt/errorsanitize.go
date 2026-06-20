package mgmt

import (
	"context"
	"log/slog"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SanitizeErrorUnaryInterceptor 是错误脱敏边界（装配为最外层拦截器）：对 Internal/Unknown
// （含裸 error——status.Code 归为 Unknown）一律回通用文案 "internal error"，原始细节只进服务端
// 日志——防 PolicyManager/DB 内部细节（约束名/SQL/secret 上下文）经直连 gRPC 外泄。
// 与 REST writeError / Console renderGRPCError 的 500 脱敏铁律对齐，补齐直连 gRPC 这一路。
// 其余 code（NotFound/InvalidArgument/PermissionDenied/...）文案是 API 契约，原样透出。
func SanitizeErrorUnaryInterceptor(logger *slog.Logger) grpc.UnaryServerInterceptor {
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		resp, err := handler(ctx, req)
		if err == nil {
			return resp, nil
		}
		st := status.Convert(err)
		if c := st.Code(); c == codes.Internal || c == codes.Unknown {
			logger.Error("control-plane internal error",
				"method", info.FullMethod, "code", c.String(), "detail", st.Message())
			return resp, status.Error(c, "internal error")
		}
		return resp, err
	}
}
