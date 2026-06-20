package console

import (
	"net/http"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"google.golang.org/grpc/codes"
)

// registerTemplates 注册运营台模板库路由。
func (h *Handler) registerTemplates(mux *http.ServeMux) {
	mux.HandleFunc("GET /ops/apps/{app_id}/templates", h.opsTemplates)
	mux.HandleFunc("POST /ops/apps/{app_id}/templates/apply", h.opsApplyTemplate)
}

// opsTemplates：GET /ops/apps/{app_id}/templates —— 模板库 + 预览（经 bizterm 渲染业务名）。
func (h *Handler) opsTemplates(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	appID, err := pathUint64(r, "app_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListTemplates", err)
		return
	}
	msg := &adminv1.ListTemplatesRequest{AppId: appID}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"ListTemplates", principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListTemplates", err)
		return
	}
	resp, err := h.srv.ListTemplates(ctx, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListTemplates", err)
		return
	}
	// 渲染视图：每模板的权限点/角色经 bizterm 渲染业务名（capabilityName 兜底缺名）。
	type capRow struct{ Name string }
	type roleRow struct {
		Name string
		Caps []string
	}
	type tplView struct {
		ID, Name, Description string
		PermCount, RoleCount  int
		Caps                  []capRow
		Roles                 []roleRow
	}
	var views []tplView
	for _, t := range resp.Templates {
		v := tplView{ID: t.Id, Name: t.Name, Description: t.Description,
			PermCount: len(t.Permissions), RoleCount: len(t.Roles)}
		nameByCode := map[string]string{}
		for _, p := range t.Permissions {
			cn := capabilityName(p.Name, p.Resource, p.Action)
			v.Caps = append(v.Caps, capRow{Name: cn})
			nameByCode[p.Code] = cn
		}
		for _, role := range t.Roles {
			rr := roleRow{Name: role.Name}
			for _, pc := range role.PermissionCodes {
				cn := nameByCode[pc]
				if cn == "" {
					cn = "（未知能力）" // 防御：role 引用了不在本模板的 code（理论不达，不渲染空行）
				}
				rr.Caps = append(rr.Caps, cn)
			}
			v.Roles = append(v.Roles, rr)
		}
		views = append(views, v)
	}
	h.renderPage(w, r, "ops_templates.html", http.StatusOK, map[string]any{
		"AppID": appID, "Templates": views, "CSRF": sess.CSRF, "OpsNav": "templates",
	})
}

// opsApplyTemplate：POST /ops/apps/{app_id}/templates/apply —— 应用后直接渲染摘要（非 PRG，
// 因 ApplyTemplate 幂等、刷新重提交无害；安全管线镜像 doWrite：会话→CSRF→鉴权→status 闸→调用）。
func (h *Handler) opsApplyTemplate(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if !h.checkCSRF(r, sess) {
		h.renderError(w, r, codes.PermissionDenied, "CSRF 校验失败", nil)
		return
	}
	appID, err := pathUint64(r, "app_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"ApplyTemplate", err)
		return
	}
	msg := &adminv1.ApplyTemplateRequest{AppId: appID, TemplateId: r.FormValue("template_id")}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"ApplyTemplate", principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ApplyTemplate", err)
		return
	}
	if err := mgmt.CheckStatusWrite(ctx, h.db, svc+"ApplyTemplate", msg); err != nil {
		h.renderGRPCError(w, r, svc+"ApplyTemplate", err)
		return
	}
	resp, err := h.srv.ApplyTemplate(ctx, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ApplyTemplate", err)
		return
	}
	h.renderPage(w, r, "ops_template_applied.html", http.StatusOK, map[string]any{
		"AppID":         appID,
		"PermsUpserted": resp.PermissionsUpserted,
		"PermsSkipped":  resp.PermissionsSkipped,
		"RolesCreated":  resp.RolesCreated,
		"RolesSkipped":  resp.RolesSkipped,
		"OpsNav":        "templates",
	})
}
