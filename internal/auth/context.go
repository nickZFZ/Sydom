package auth

import "context"

type ctxKey struct{}

// WithAppID 把已认证的 app_id 注入 context。
func WithAppID(ctx context.Context, appID string) context.Context {
	return context.WithValue(ctx, ctxKey{}, appID)
}

// AppIDFromContext 取出已认证 app_id；未认证返回 ("", false)。
func AppIDFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(ctxKey{}).(string)
	return v, ok
}
