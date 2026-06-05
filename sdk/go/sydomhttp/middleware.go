package sydomhttp

import (
	"context"
	"errors"
	"net/http"

	"github.com/nickZFZ/Sydom/sdk/go/sydom"
)

// Checker 是中间件对核心客户端的窄依赖；*sydom.Client 自动满足。
type Checker interface {
	Check(ctx context.Context, subject, object, action string) (bool, error)
}

// New 返回标准 net/http 中间件：用 resolver 解析三元组、调 checker 判定，按终态分流。
// 默认 fail-close；fail-open 经 WithFailOpen 显式开启（仅作用于 ErrUnavailable，硬错误不豁免）。
func New(checker Checker, resolver Resolver, opts ...Option) func(http.Handler) http.Handler {
	var cfg config
	for _, o := range opts {
		o(&cfg)
	}
	cfg.applyDefaults()

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sub, obj, act, err := resolver(r)
			if err != nil {
				if errors.Is(err, ErrSkipAuth) {
					next.ServeHTTP(w, r)
					return
				}
				cfg.errorLog(r, err)
				cfg.denyHandler.ServeHTTP(w, r) // 无法识别请求/身份：与判定为拒同归一类（fail-close）
				return
			}

			allowed, err := checker.Check(r.Context(), sub, obj, act)
			switch {
			case err == nil && allowed:
				d := Decision{Subject: sub, Object: obj, Action: act}
				next.ServeHTTP(w, r.WithContext(withDecision(r.Context(), d)))
			case err == nil && !allowed:
				cfg.denyHandler.ServeHTTP(w, r)
			case errors.Is(err, sydom.ErrUnavailable):
				cfg.errorLog(r, err)
				if cfg.failOpen {
					next.ServeHTTP(w, r)
					return
				}
				cfg.unavailableHandler.ServeHTTP(w, r)
			default:
				cfg.errorLog(r, err)
				cfg.errorHandler.ServeHTTP(w, r) // 硬错误：fail-open 不豁免
			}
		})
	}
}
