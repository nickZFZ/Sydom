package console

import (
	"net/http"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
)

// decisionExplainer：决策解释器页（读）。
// GET ?user_id=&resource=&action= → 三者齐备时调 ExplainDecision 渲染判定链；否则只渲染表单。
// 鉴权：ExplainDecision（scopeApp read）；拒绝走 renderGRPCError（降级无枚举、不泄露存在性）。
func (h *Handler) decisionExplainer(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	appID, err := pathUint64(r, "app_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"ExplainDecision", err)
		return
	}
	userID := r.FormValue("user_id")
	resource := r.FormValue("resource")
	action := r.FormValue("action")
	data := map[string]any{
		"Nav": "apps", "AppID": appID, "Tab": "decision",
		"UserID": userID, "Resource": resource, "Action": action, "CSRF": sess.CSRF,
	}
	if userID != "" && resource != "" && action != "" {
		msg := &adminv1.ExplainDecisionRequest{AppId: appID, UserId: userID, Resource: resource, Action: action}
		ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"ExplainDecision", principal, msg)
		if err != nil {
			h.renderGRPCError(w, r, svc+"ExplainDecision", err)
			return
		}
		resp, err := h.srv.ExplainDecision(ctx, msg)
		if err != nil {
			h.renderGRPCError(w, r, svc+"ExplainDecision", err)
			return
		}
		data["Queried"] = true
		data["Allowed"] = resp.Allowed
		data["Reason"] = resp.Reason
		data["DecidingRule"] = resp.DecidingRule
		data["EffRoles"] = resp.Roles
		data["DataScope"] = resp.DataScope
	}
	h.renderPage(w, r, "decision.html", http.StatusOK, data)
}
