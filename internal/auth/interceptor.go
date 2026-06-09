package auth

import (
	"context"
	"strconv"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// MaxClockSkew 是签名时间戳允许的前后偏移窗口（防重放）。
const MaxClockSkew = 5 * time.Minute

// authenticate 校验 metadata 中的 HMAC 凭据，成功返回带 app_id 的新 context。
func authenticate(ctx context.Context, resolver SecretResolver, method string, now time.Time) (context.Context, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "missing metadata")
	}
	appID, tsStr, sig := first(md, MDAppID), first(md, MDTimestamp), first(md, MDSignature)
	if appID == "" || tsStr == "" || sig == "" {
		return nil, status.Error(codes.Unauthenticated, "missing auth fields")
	}
	// app_id 来自不可信 metadata：拒绝含控制字符/换行者，防签名串分隔符歧义与下游注入。
	if !validAppID(appID) {
		return nil, status.Error(codes.Unauthenticated, "invalid app id")
	}
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "bad timestamp")
	}
	if d := now.Sub(time.Unix(ts, 0)); d > MaxClockSkew || d < -MaxClockSkew {
		return nil, status.Error(codes.Unauthenticated, "timestamp out of window")
	}
	secret, err := resolver.ResolveSecret(ctx, appID)
	// 统一用通用错误：避免"app 未注册"与"签名不符"消息可区分，被用作注册表枚举 Oracle。
	// 具体原因应记入服务端日志（带 app_id / trace），而非回传客户端。
	if err != nil || len(secret) == 0 {
		// len(secret)==0 兜底：DB 损坏/解密失败时空密钥的 HMAC 人人可算，必须拒绝（fail-close）。
		return nil, status.Error(codes.Unauthenticated, "authentication failed")
	}
	if !Verify(secret, appID, ts, method, sig) {
		return nil, status.Error(codes.Unauthenticated, "authentication failed")
	}
	// 校验通过：强制后续一切操作使用此 app_id（架构 I2）。
	return WithAppID(ctx, appID), nil
}

// validAppID 委托 ValidPrincipal（gRPC app_id 与 REST principal 同一字符集校验）。
func validAppID(s string) bool {
	return ValidPrincipal(s)
}

func first(md metadata.MD, key string) string {
	if v := md.Get(key); len(v) > 0 {
		return v[0]
	}
	return ""
}

// UnaryServerInterceptor 校验一元 RPC 的 HMAC 凭据并注入 app_id。
func UnaryServerInterceptor(resolver SecretResolver) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		newCtx, err := authenticate(ctx, resolver, info.FullMethod, time.Now())
		if err != nil {
			return nil, err
		}
		return handler(newCtx, req)
	}
}

// StreamServerInterceptor 校验流式 RPC 的 HMAC 凭据并注入 app_id。
func StreamServerInterceptor(resolver SecretResolver) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		newCtx, err := authenticate(ss.Context(), resolver, info.FullMethod, time.Now())
		if err != nil {
			return err
		}
		return handler(srv, &wrappedStream{ServerStream: ss, ctx: newCtx})
	}
}

// wrappedStream 用已认证 context 覆盖原 ServerStream.Context()。
type wrappedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedStream) Context() context.Context { return w.ctx }
