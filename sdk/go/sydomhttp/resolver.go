// Package sydomhttp 是司域 Go SDK 的 net/http 鉴权中间件：拦截请求调 Sidecar Check，
// 默认 fail-close。不含任何鉴权逻辑，仅做框架胶水。
package sydomhttp

import (
	"errors"
	"net/http"
)

// Resolver 从 HTTP 请求解析鉴权三元组 (subject, object, action)。
// 返回 ErrSkipAuth 表示该请求为公开路由、应直接放行；返回其它 error 按 fail-close 拒绝。
type Resolver func(r *http.Request) (subject, object, action string, err error)

// ErrSkipAuth 由 Resolver 返回以放行公开路由。
var ErrSkipAuth = errors.New("sydomhttp: skip authorization")

// PathMethodResolver 是便利约定：object=请求 path、action=HTTP method；
// 业务只提供 subject 提取函数。subjectFn 返回 error 时该请求按 fail-close 拒绝。
func PathMethodResolver(subjectFn func(r *http.Request) (string, error)) Resolver {
	return func(r *http.Request) (string, string, string, error) {
		sub, err := subjectFn(r)
		if err != nil {
			return "", "", "", err
		}
		return sub, r.URL.Path, r.Method, nil
	}
}
