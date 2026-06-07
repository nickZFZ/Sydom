package app

import (
	"net/http"
	"strconv"
	"strings"
)

func (s *server) handleLanding(w http.ResponseWriter, r *http.Request) {
	s.render(w, "landing", map[string]any{"User": currentUser(r)}, http.StatusOK)
}

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	user := r.URL.Query().Get("user")
	if user == "" {
		user = r.FormValue("user")
	}
	if user != "alice" && user != "bob" {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "demo_user", Value: user, Path: "/"})
	http.Redirect(w, r, "/orders", http.StatusSeeOther)
}

func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "demo_user", Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleOrders：GET 列表（数据权限过滤）。POST 创建本 demo 未启用，返回 405。
func (s *server) handleOrders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	user := currentUser(r)
	attrs := map[string]any{}
	if d := userDept(user); d != "" {
		attrs["department"] = d
	}
	fr, err := s.gw.FilterSQL(r.Context(), user, "order", attrs)
	if err != nil {
		s.handleUnavailable(w, r) // fail-close：拿不到数据权限片段不展示数据
		return
	}
	orders, err := ListOrders(r.Context(), s.db, fr)
	if err != nil {
		s.render(w, "error", map[string]any{"Msg": "查询订单失败"}, http.StatusInternalServerError)
		return
	}
	s.render(w, "orders", map[string]any{"User": user, "Orders": orders}, http.StatusOK)
}

// handleOrderItem：POST /orders/{id}/delete。中间件已做 Check(order,delete)，到这里即放行。
func (s *server) handleOrderItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/delete") {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	idStr := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/orders/"), "/delete")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if _, err := DeleteOrder(r.Context(), s.db, id); err != nil {
		s.render(w, "error", map[string]any{"Msg": "删除失败"}, http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/orders", http.StatusSeeOther)
}

func (s *server) handleForbidden(w http.ResponseWriter, r *http.Request) {
	s.render(w, "forbidden", map[string]any{"User": currentUser(r)}, http.StatusForbidden)
}

func (s *server) handleUnavailable(w http.ResponseWriter, r *http.Request) {
	s.render(w, "unavailable", nil, http.StatusServiceUnavailable)
}
