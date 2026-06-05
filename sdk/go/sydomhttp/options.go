package sydomhttp

import "net/http"

type config struct {
	failOpen           bool
	denyHandler        http.Handler
	unavailableHandler http.Handler
	errorHandler       http.Handler
	errorLog           func(r *http.Request, err error)
}

// Option 配置中间件实例。
type Option func(*config)

// WithFailOpen 令本中间件实例在「无法判定」（ErrUnavailable）时放行（默认 fail-close）。
// 作用域为该实例——不同路由组挂不同实例即实现「每路由 opt-in」。硬错误不受此影响。
func WithFailOpen() Option { return func(c *config) { c.failOpen = true } }

// WithDenyHandler 自定义「判定为拒 或 resolver 非 skip 错误」的响应（默认 403）。
func WithDenyHandler(h http.Handler) Option { return func(c *config) { c.denyHandler = h } }

// WithUnavailableHandler 自定义「无法判定且 fail-close」的响应（默认 503）。
func WithUnavailableHandler(h http.Handler) Option {
	return func(c *config) { c.unavailableHandler = h }
}

// WithErrorHandler 自定义「Check 硬错误」的响应（默认 500）。
func WithErrorHandler(h http.Handler) Option { return func(c *config) { c.errorHandler = h } }

// WithErrorLog 注册错误日志钩子（resolver 错误 / ErrUnavailable / 硬错误时触发）。SDK 不绑定具体日志库。
func WithErrorLog(fn func(r *http.Request, err error)) Option {
	return func(c *config) { c.errorLog = fn }
}

func statusHandler(code int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, http.StatusText(code), code)
	})
}

func (c *config) applyDefaults() {
	if c.denyHandler == nil {
		c.denyHandler = statusHandler(http.StatusForbidden)
	}
	if c.unavailableHandler == nil {
		c.unavailableHandler = statusHandler(http.StatusServiceUnavailable)
	}
	if c.errorHandler == nil {
		c.errorHandler = statusHandler(http.StatusInternalServerError)
	}
	if c.errorLog == nil {
		c.errorLog = func(*http.Request, error) {}
	}
}
