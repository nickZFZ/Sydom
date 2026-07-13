package console

import (
	"net/http"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
)

// registerUsage 注册租户用量页（M6.1c 计量可见）。
func (h *Handler) registerUsage(mux *http.ServeMux) {
	mux.HandleFunc("GET /tenants/{tenant_id}/usage", h.usage)
}

// usage 渲染租户套餐 + 应用配额用量页（纯读，消费 GetTenantUsage 第四面）。
// 授权经 ruleTable(scopeTenant)：租户看自己、root 看全、跨租户 PermissionDenied(403)；
// 未知租户 NotFound(404)。幂等只读——零 bump、零写、零审计、无 CSRF。
func (h *Handler) usage(w http.ResponseWriter, r *http.Request) {
	principal, _, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	tid, err := pathUint64(r, "tenant_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"GetTenantUsage", err)
		return
	}
	msg := &adminv1.GetTenantUsageRequest{TenantId: tid}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"GetTenantUsage", principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"GetTenantUsage", err)
		return
	}
	resp, err := h.srv.GetTenantUsage(ctx, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"GetTenantUsage", err)
		return
	}
	used, limit := 0, 0
	if resp.Applications != nil { // 本 in-process 服务恒设，防御性 nil 守卫
		used = int(resp.Applications.Used)
		limit = int(resp.Applications.Limit)
	}
	h.renderPage(w, r, "usage.html", http.StatusOK, map[string]any{
		"Nav":       "tenants",
		"TenantID":  tid,
		"PlanLabel": planLabel(resp.PlanName),
		"Used":      used,
		"Limit":     limit,
		"AtLimit":   used >= limit,
		"ShowMeter": limit > 0, // max="0" 无效（须 > min）；limit 为 0 时仅文本
	})
}
