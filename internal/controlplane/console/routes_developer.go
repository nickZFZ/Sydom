package console

import "net/http"

// registerDeveloper 注册开发者文档区（建模台只读 tab）。
func (h *Handler) registerDeveloper(mux *http.ServeMux) {
	mux.HandleFunc("GET /apps/{app_id}/developer", h.developer)
}

// developer 渲染开发者文档区（会话只读：quickstart+概念+SDK 参考 手写 + 管理面 API 参考自 ruleTable/route 派生）。
// 幂等只读——不写、不 bump、不写审计、无 CSRF；不渲染任何 app secret。
func (h *Handler) developer(w http.ResponseWriter, r *http.Request) {
	_, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	appID, err := pathUint64(r, "app_id")
	if err != nil {
		h.renderGRPCError(w, r, "console/developer", err) // 纯渲染页，无 RPC：标签忠实反映 handler
		return
	}
	h.renderPage(w, r, "developer.html", http.StatusOK, map[string]any{
		"Nav": "apps", "AppID": appID, "Tab": "developer",
		"CSRF": sess.CSRF, "APIRef": buildAPIReference(),
	})
}
