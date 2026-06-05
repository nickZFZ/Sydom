package sydomhttp

import "context"

// Decision 是放行请求的鉴权三元组，注入到请求 context 供下游 handler 取用（审计等）。
type Decision struct {
	Subject string
	Object  string
	Action  string
}

type ctxKey struct{}

func withDecision(ctx context.Context, d Decision) context.Context {
	return context.WithValue(ctx, ctxKey{}, d)
}

// FromContext 取出中间件放行时注入的 Decision；未注入返回 (zero, false)。
func FromContext(ctx context.Context) (Decision, bool) {
	d, ok := ctx.Value(ctxKey{}).(Decision)
	return d, ok
}
