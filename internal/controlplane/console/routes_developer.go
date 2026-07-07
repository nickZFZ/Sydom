package console

import (
	"net/http"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
)

// registerDeveloper 注册开发者文档区（建模台只读 tab）。
func (h *Handler) registerDeveloper(mux *http.ServeMux) {
	mux.HandleFunc("GET /apps/{app_id}/developer", h.developer)
}

// developer 渲染开发者文档区（会话只读：接入凭据总览 + quickstart+概念+SDK 参考 + 管理面 API 参考自派生）。
// 幂等只读——不写、不 bump、不写审计、无 CSRF 写。凭据经 GetApplication（scopeApp read，fail-close）
// 读回 app_key/domain，绝不渲染任何 app secret（ApplicationSummary 类型层无 secret 字段）。
func (h *Handler) developer(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	appID, err := pathUint64(r, "app_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"GetApplication", err)
		return
	}
	msg := &adminv1.GetApplicationRequest{AppId: appID}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"GetApplication", principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"GetApplication", err)
		return
	}
	appResp, err := h.srv.GetApplication(ctx, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"GetApplication", err)
		return
	}
	h.renderPage(w, r, "developer.html", http.StatusOK, map[string]any{
		"Nav": "apps", "AppID": appID, "Tab": "developer",
		"CSRF": sess.CSRF, "APIRef": buildAPIReference(),
		"App": appResp.Application,
	})
}
