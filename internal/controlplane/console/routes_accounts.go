package console

import (
	"net/http"
	"strconv"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"google.golang.org/grpc/codes"
)

func (h *Handler) registerAccounts(mux *http.ServeMux) {
	mux.HandleFunc("GET /register", h.registerForm)  // 公开
	mux.HandleFunc("POST /register", h.registerPost) // 公开
	mux.HandleFunc("GET /tenants", h.tenantsList)
	mux.HandleFunc("GET /tenants/{tenant_id}/members", h.membersList)
	mux.HandleFunc("POST /tenants/{tenant_id}/members", h.memberInvite)
}

// registerForm：公开注册表单（无会话）。
func (h *Handler) registerForm(w http.ResponseWriter, r *http.Request) {
	h.renderPage(w, r, "register.html", http.StatusOK, map[string]any{"Error": ""})
}

// registerPost：公开。免鉴权直调 srv.RegisterTenant；一次性 secret 当场渲染（不 PRG、不日志/落盘）。
func (h *Handler) registerPost(w http.ResponseWriter, r *http.Request) {
	msg := &adminv1.RegisterTenantRequest{
		TenantName: r.FormValue("tenant_name"), OwnerPrincipal: r.FormValue("owner_principal")}
	resp, err := h.srv.RegisterTenant(r.Context(), msg)
	if err != nil {
		h.renderGRPCError(w, r, "/sydom.admin.v1.AdminService/RegisterTenant", err)
		return
	}
	h.renderPage(w, r, "register.html", http.StatusOK, map[string]any{
		"Created": true, "TenantID": resp.TenantId,
		"OwnerPrincipal": resp.OwnerPrincipal, "OwnerSecret": resp.OwnerSecret}) // 一次性展示
}

// tenantsList：ListMyTenants（self）。
func (h *Handler) tenantsList(w http.ResponseWriter, r *http.Request) {
	principal, _, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	ctx := cp.WithOperator(r.Context(), principal)
	resp, err := h.srv.ListMyTenants(ctx, &adminv1.ListMyTenantsRequest{Page: listPageFromReq(r)})
	if err != nil {
		h.renderGRPCError(w, r, "/sydom.admin.v1.AdminService/ListMyTenants", err)
		return
	}
	h.renderPage(w, r, "tenants.html", http.StatusOK, map[string]any{
		"Nav": "tenants", "Memberships": resp.Memberships, "IsOperatingPlane": resp.IsOperatingPlane,
		"Pager": pagerData(r, resp.Total)})
}

// membersList：ListMembers（tenant-target 读，经共用 AuthorizeRule）。
func (h *Handler) membersList(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	tid, err := strconv.ParseUint(r.PathValue("tenant_id"), 10, 64)
	if err != nil {
		http.Redirect(w, r, "/tenants", http.StatusSeeOther)
		return
	}
	tier, err := formInt64(r, "tier")
	if err != nil {
		tier = 0
	}
	const fm = "/sydom.admin.v1.AdminService/ListMembers"
	msg := &adminv1.ListMembersRequest{TenantId: tid, Page: listPageFromReq(r), Tier: int32(tier)}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, fm, principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, fm, err)
		return
	}
	resp, err := h.srv.ListMembers(ctx, msg)
	if err != nil {
		h.renderGRPCError(w, r, fm, err)
		return
	}
	h.renderPage(w, r, "members.html", http.StatusOK, map[string]any{
		"Nav": "tenants", "TenantID": tid, "Members": resp.Members, "CSRF": sess.CSRF,
		"Pager": pagerData(r, resp.Total)})
}

// memberInvite：InviteMember（CSRF → 授权 → 直调 → 一次性 secret 当场渲染，不 PRG）。
func (h *Handler) memberInvite(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if !h.checkCSRF(r, sess) {
		h.renderError(w, r, codes.PermissionDenied, "CSRF 校验失败", nil)
		return
	}
	tid, err := strconv.ParseUint(r.PathValue("tenant_id"), 10, 64)
	if err != nil {
		http.Redirect(w, r, "/tenants", http.StatusSeeOther)
		return
	}
	const fm = "/sydom.admin.v1.AdminService/InviteMember"
	msg := &adminv1.InviteMemberRequest{TenantId: tid, Principal: r.FormValue("principal")}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, fm, principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, fm, err)
		return
	}
	resp, err := h.srv.InviteMember(ctx, msg)
	if err != nil {
		h.renderGRPCError(w, r, fm, err)
		return
	}
	h.renderPage(w, r, "member_invited.html", http.StatusOK, map[string]any{
		"Nav": "tenants", "TenantID": tid,
		"Principal": resp.Principal, "Secret": resp.Secret}) // Secret 可能为空（既有 operator）
}
