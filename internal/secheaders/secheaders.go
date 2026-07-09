// Package secheaders 提供按内容类型裁剪的安全响应头中间件：Console（HTML BFF）与 API（JSON）。
// 纯设响应头后透传 next——不改 body、不改 status、不吞 next 返回；观测性/授权无关。
// HSTS 仅在 secure=true（部署已声明 HTTPS）下发，防明文部署被浏览器强制 HTTPS 锁死。
package secheaders

import "net/http"

const hstsValue = "max-age=63072000; includeSubDomains" // 2 年 + 子域

// cspConsole 是 Console（HTML BFF）的严格内容安全策略：
// 无 unsafe-inline / unsafe-eval——脚本与样式一律来自同源静态资源，内联被拒。
const cspConsole = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; " +
	"object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'"

// cspAPI 是 API（JSON）的锁死策略：JSON 响应不加载任何子资源，default-src 'none' 即可。
const cspAPI = "default-src 'none'; frame-ancestors 'none'"

// writeCommon 设两面共享的响应头；HSTS 仅在 secure 下发。
func writeCommon(h http.Header, secure bool) {
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	if secure {
		h.Set("Strict-Transport-Security", hstsValue)
	}
}

// Console 返回 Console（HTML BFF）中间件：共享头 + 严格 CSP + Permissions-Policy。
func Console(secure bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			writeCommon(h, secure)
			h.Set("Content-Security-Policy", cspConsole)
			h.Set("Permissions-Policy", "geolocation=(), camera=(), microphone=()")
			next.ServeHTTP(w, r)
		})
	}
}

// API 返回 API（JSON）中间件：共享头 + 锁死 CSP（无 Permissions-Policy，JSON 面无浏览器特性可禁）。
func API(secure bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			writeCommon(h, secure)
			h.Set("Content-Security-Policy", cspAPI)
			next.ServeHTTP(w, r)
		})
	}
}
