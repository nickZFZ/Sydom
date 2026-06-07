package app

import (
	"context"
	"database/sql"
	"html/template"
	"log"
	"net/http"

	"github.com/nickZFZ/Sydom/sdk/go/sydom"
	"github.com/nickZFZ/Sydom/sdk/go/sydomhttp"
)

// AuthGateway 是订单服务对司域 SDK 的窄依赖；*sydom.Client 自动满足。
type AuthGateway interface {
	Check(ctx context.Context, subject, object, action string) (bool, error)
	FilterSQL(ctx context.Context, subject, resource string, attrs map[string]any) (sydom.FilterResult, error)
}

type server struct {
	db   *sql.DB
	gw   AuthGateway
	tmpl *template.Template
}

// New 装配订单服务 HTTP handler：建表/播种 + sydomhttp 中间件 + 路由。
func New(ctx context.Context, db *sql.DB, gw AuthGateway) (http.Handler, error) {
	if err := EnsureSchema(ctx, db); err != nil {
		return nil, err
	}
	if err := SeedOrders(ctx, db); err != nil {
		return nil, err
	}
	s := &server{db: db, gw: gw, tmpl: template.Must(template.New("root").Parse(tmplText))}

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleLanding)
	mux.HandleFunc("/login", s.handleLogin)
	mux.HandleFunc("/logout", s.handleLogout)
	mux.HandleFunc("/orders", s.handleOrders)
	mux.HandleFunc("/orders/", s.handleOrderItem)

	mw := sydomhttp.New(s.gw, resolver,
		sydomhttp.WithDenyHandler(http.HandlerFunc(s.handleForbidden)),
		sydomhttp.WithUnavailableHandler(http.HandlerFunc(s.handleUnavailable)),
		sydomhttp.WithErrorLog(func(r *http.Request, err error) {
			log.Printf("authz %s %s: %v", r.Method, r.URL.Path, err)
		}),
	)
	return mw(mux), nil
}

// render 执行命名模板；出错记日志并 500。
func (s *server) render(w http.ResponseWriter, name string, data any, status int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("render %s: %v", name, err)
	}
}
