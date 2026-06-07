package app

import (
	"errors"
	"net/http"
	"strings"

	"github.com/nickZFZ/Sydom/sdk/go/sydomhttp"
)

// errNoUser：受保护路由但未选用户 → 中间件按 fail-close 拒绝（渲染友好 403）。
var errNoUser = errors.New("orderservice: 未选择用户")

// currentUser 从 cookie 取当前 demo 用户名（空=未登录）。
func currentUser(r *http.Request) string {
	c, err := r.Cookie("demo_user")
	if err != nil {
		return ""
	}
	return c.Value
}

// userDept 按 demo 约定把用户映射到部门（真实系统来自身份系统/请求属性，非硬编码）。
// manager（alice）返回空：其数据策略覆盖全部门，不需要 $user.department。
func userDept(user string) string {
	switch user {
	case "bob":
		return "shanghai"
	default:
		return ""
	}
}

// resolver 把请求映射为鉴权三元组：subject=cookie 用户；object/action 按路由。
// 公开路由（落地/登录/登出）返回 ErrSkipAuth；受保护路由未选用户 → errNoUser（fail-close）。
func resolver(r *http.Request) (subject, object, action string, err error) {
	switch r.URL.Path {
	case "/", "/login", "/logout":
		return "", "", "", sydomhttp.ErrSkipAuth
	}
	user := currentUser(r)
	if user == "" {
		return "", "", "", errNoUser
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/orders":
		return user, "order", "read", nil
	case r.Method == http.MethodPost && r.URL.Path == "/orders":
		return user, "order", "write", nil
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/orders/") && strings.HasSuffix(r.URL.Path, "/delete"):
		return user, "order", "delete", nil
	}
	return "", "", "", sydomhttp.ErrSkipAuth // 未知路由放行交 mux 404
}
