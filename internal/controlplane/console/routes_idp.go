package console

import (
	"context"
	"net/http"
	"strings"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/oidc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

func (h *Handler) registerIdP(mux *http.ServeMux) {
	mux.HandleFunc("GET /tenants/{tenant_id}/idp", h.idpConfig)
	mux.HandleFunc("POST /tenants/{tenant_id}/idp", h.idpSave)
	mux.HandleFunc("POST /tenants/{tenant_id}/idp/delete", h.idpDelete)
	mux.HandleFunc("POST /tenants/{tenant_id}/idp/test", h.idpTest)
}

// idpDelete 删除 IdP（二次确认 + doWrite）。
func (h *Handler) idpDelete(w http.ResponseWriter, r *http.Request) {
	if !h.requireConfirm(w, r, svc+"DeleteTenantIdp") {
		return
	}
	h.doWrite(w, r, svc+"DeleteTenantIdp",
		func(r *http.Request) (proto.Message, error) {
			tid, err := pathUint64(r, "tenant_id")
			if err != nil {
				return nil, err
			}
			return &adminv1.DeleteTenantIdpRequest{TenantId: tid}, nil
		},
		func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
			return s.DeleteTenantIdp(ctx, m.(*adminv1.DeleteTenantIdpRequest))
		},
		func(r *http.Request) string { return "/tenants/" + r.PathValue("tenant_id") + "/idp" })
}

// idpTest 连通性测试：探已保存 issuer 的 discovery+JWKS（SSRF-安全，不探表单 URL）。
func (h *Handler) idpTest(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if !h.checkCSRF(r, sess) {
		h.renderError(w, r, codes.PermissionDenied, "CSRF 校验失败", nil)
		return
	}
	tid, err := pathUint64(r, "tenant_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"GetTenantIdp", err)
		return
	}
	dest := "/tenants/" + r.PathValue("tenant_id") + "/idp"
	msg := &adminv1.GetTenantIdpRequest{TenantId: tid}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"GetTenantIdp", principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"GetTenantIdp", err)
		return
	}
	resp, err := h.srv.GetTenantIdp(ctx, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"GetTenantIdp", err)
		return
	}
	var flash string
	if !resp.Configured {
		flash = "请先配置 IdP 再测试连接"
	} else if pc, derr := oidc.Discover(r.Context(), h.oidcHTTP, resp.Issuer); derr != nil {
		flash = "连通测试失败：无法访问 IdP discovery 端点"
	} else if _, jerr := oidc.FetchJWKS(r.Context(), h.oidcHTTP, pc.JWKSURI); jerr != nil {
		flash = "连通测试失败：无法访问/解析 JWKS 端点"
	} else {
		flash = "连通正常：discovery 与 JWKS 端点可达"
	}
	if id := h.sessionID(r); id != "" {
		_ = h.sessions.SetFlash(r.Context(), id, flash)
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// idpConfig 渲染租户 OIDC IdP 配置表单（读，经 AuthorizeRule scopeTenant）。client_secret 绝不回填（INV-1）。
func (h *Handler) idpConfig(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	tid, err := pathUint64(r, "tenant_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"GetTenantIdp", err)
		return
	}
	msg := &adminv1.GetTenantIdpRequest{TenantId: tid}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"GetTenantIdp", principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"GetTenantIdp", err)
		return
	}
	resp, err := h.srv.GetTenantIdp(ctx, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"GetTenantIdp", err)
		return
	}
	h.renderPage(w, r, "idp.html", http.StatusOK, map[string]any{
		"Nav": "tenants", "TenantID": tid, "CSRF": sess.CSRF,
		"Configured": resp.Configured, "Issuer": resp.Issuer, "ClientID": resp.ClientId,
		"Domains": strings.Join(resp.Domains, "\n"), "Enabled": resp.Enabled, "JitEnabled": resp.JitEnabled,
	})
}

// idpSave 保存 IdP 配置（写，doWrite 管线）。client_secret 留空=保持不变（后端语义）。
func (h *Handler) idpSave(w http.ResponseWriter, r *http.Request) {
	h.doWrite(w, r, svc+"ConfigureTenantIdp",
		func(r *http.Request) (proto.Message, error) {
			tid, err := pathUint64(r, "tenant_id")
			if err != nil {
				return nil, err
			}
			return &adminv1.ConfigureTenantIdpRequest{
				TenantId:     tid,
				Issuer:       strings.TrimSpace(r.PostFormValue("issuer")),
				ClientId:     strings.TrimSpace(r.PostFormValue("client_id")),
				ClientSecret: r.PostFormValue("client_secret"), // 不 trim：secret 原样；空=保持
				Domains:      splitLines(r.PostFormValue("domains")),
				Enabled:      r.PostFormValue("enabled") != "",
				JitEnabled:   r.PostFormValue("jit_enabled") != "",
			}, nil
		},
		func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
			return s.ConfigureTenantIdp(ctx, m.(*adminv1.ConfigureTenantIdpRequest))
		},
		func(r *http.Request) string { return "/tenants/" + r.PathValue("tenant_id") + "/idp" })
}

// splitLines 把 textarea 文本按行拆、trim、去空。
func splitLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if v := strings.TrimSpace(line); v != "" {
			out = append(out, v)
		}
	}
	return out
}
