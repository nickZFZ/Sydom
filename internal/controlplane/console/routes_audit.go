package console

import (
	"net/http"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
)

// registerAudit 注册审计页：app 域(appnav tab) + admin 域(系统区)。
func (h *Handler) registerAudit(mux *http.ServeMux) {
	mux.HandleFunc("GET /apps/{app_id}/audit", h.appAudit)
	mux.HandleFunc("GET /admin/audit", h.adminAudit)
}

// appAudit：app 审计 feed（读）。?entity_type=&entity_id=&action=&cursor= 过滤/翻页。
// 鉴权 QueryAuditLog(scopeApp)；拒绝走 renderGRPCError（降级无枚举）。
func (h *Handler) appAudit(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	appID, err := pathUint64(r, "app_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"QueryAuditLog", err)
		return
	}
	cursor, err := formUint64(r, "cursor")
	if err != nil {
		h.renderGRPCError(w, r, svc+"QueryAuditLog", err)
		return
	}
	q := r.URL.Query()
	msg := &adminv1.QueryAuditLogRequest{
		AppId: appID, EntityType: q.Get("entity_type"), EntityId: q.Get("entity_id"),
		Action: q.Get("action"), Cursor: cursor, Limit: 50,
	}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"QueryAuditLog", principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"QueryAuditLog", err)
		return
	}
	resp, err := h.srv.QueryAuditLog(ctx, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"QueryAuditLog", err)
		return
	}
	h.renderPage(w, r, "audit.html", http.StatusOK, map[string]any{
		"Nav": "apps", "AppID": appID, "Tab": "audit", "CSRF": sess.CSRF,
		"Entries": resp.Entries, "NextCursor": resp.NextCursor,
		"EntityType": q.Get("entity_type"), "EntityID": q.Get("entity_id"), "Action": q.Get("action"),
	})
}

// adminAudit：admin 审计 feed（读，系统区）。?tenant_id=（默认 0=超管全量）。
// 鉴权 QueryAdminAuditLog(scopeTenant)；拒绝即 403（renderGRPCError），绝不降级。
func (h *Handler) adminAudit(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	tenant, err := formUint64(r, "tenant_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"QueryAdminAuditLog", err)
		return
	}
	cursor, err := formUint64(r, "cursor")
	if err != nil {
		h.renderGRPCError(w, r, svc+"QueryAdminAuditLog", err)
		return
	}
	q := r.URL.Query()
	msg := &adminv1.QueryAdminAuditLogRequest{
		TenantId: tenant, EntityType: q.Get("entity_type"), EntityId: q.Get("entity_id"),
		Action: q.Get("action"), Cursor: cursor, Limit: 50,
	}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"QueryAdminAuditLog", principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"QueryAdminAuditLog", err)
		return
	}
	resp, err := h.srv.QueryAdminAuditLog(ctx, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"QueryAdminAuditLog", err)
		return
	}
	h.renderPage(w, r, "admin_audit.html", http.StatusOK, map[string]any{
		"Nav": "system", "CSRF": sess.CSRF, "TenantID": tenant,
		"Entries": resp.Entries, "NextCursor": resp.NextCursor,
	})
}
