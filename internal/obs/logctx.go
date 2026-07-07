package obs

import (
	"context"
	"log/slog"
)

type logCtxKey struct{}

// With 把请求级 logger 注入 context。
func With(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, logCtxKey{}, l)
}

// From 取请求级 logger；缺省返回 slog.Default()（绝不 nil）。
func From(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(logCtxKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}
