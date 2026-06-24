package console

import (
	"context"
	"net/http"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

// registerTenantTemplates 注册运营台「我的模板」（租户自有模板）路由。
// 路径段刻意区分官方预设模板（/templates、/templates/apply），避免冲突。
func (h *Handler) registerTenantTemplates(mux *http.ServeMux) {
	mux.HandleFunc("POST /ops/apps/{app_id}/template-captures", h.opsSaveTemplate)
	mux.HandleFunc("GET /ops/apps/{app_id}/tenant-templates/{template_id}", h.opsTenantTemplate)
	mux.HandleFunc("POST /ops/apps/{app_id}/tenant-templates/{template_id}/apply", h.opsApplyTenantTemplate)
	mux.HandleFunc("POST /ops/apps/{app_id}/tenant-templates/{template_id}/delete", h.opsDeleteTenantTemplate)
}

// tenantOfApp 由 app 派生其租户（List/Get/Delete TenantTemplate 的 scopeTenant 域）。
// Console 会话不含 tenant_id，本页锚定在 app 上，故从 app_id 查库派生。
func (h *Handler) tenantOfApp(ctx context.Context, appID uint64) (uint64, error) {
	var tid uint64
	err := h.db.QueryRowContext(ctx, `SELECT tenant_id FROM application WHERE id=$1`, appID).Scan(&tid)
	return tid, err
}

// opsSaveTemplate：POST /ops/apps/{app_id}/template-captures —— 把本 app 全模型存为本租户模板。
// 走 doWrite 共享管线（会话→CSRF→鉴权→status 闸→调用→PRG 回模板库）。
func (h *Handler) opsSaveTemplate(w http.ResponseWriter, r *http.Request) {
	h.doWrite(w, r, svc+"SaveAppAsTemplate",
		func(r *http.Request) (proto.Message, error) {
			appID, err := pathUint64(r, "app_id")
			if err != nil {
				return nil, err
			}
			return &adminv1.SaveAppAsTemplateRequest{
				AppId:       appID,
				Name:        r.FormValue("name"),
				Description: r.FormValue("description"),
			}, nil
		},
		func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
			return s.SaveAppAsTemplate(ctx, m.(*adminv1.SaveAppAsTemplateRequest))
		},
		func(r *http.Request) string { return "/ops/apps/" + r.PathValue("app_id") + "/templates" },
	)
}

// opsTenantTemplate：GET /ops/apps/{app_id}/tenant-templates/{template_id} —— 预览（bizterm 渲染业务名）。
// 租户派生自 app；GetTenantTemplate scopeTenant 自然 fail-close（跨租户/未知模板→NotFound 错误页）。
func (h *Handler) opsTenantTemplate(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	appID, err := pathUint64(r, "app_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"GetTenantTemplate", err)
		return
	}
	tid, err := h.tenantOfApp(r.Context(), appID)
	if err != nil {
		h.renderGRPCError(w, r, svc+"GetTenantTemplate", err)
		return
	}
	tplID, err := pathUint64(r, "template_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"GetTenantTemplate", err)
		return
	}
	msg := &adminv1.GetTenantTemplateRequest{TenantId: tid, TemplateId: tplID}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"GetTenantTemplate", principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"GetTenantTemplate", err)
		return
	}
	t, err := h.srv.GetTenantTemplate(ctx, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"GetTenantTemplate", err)
		return
	}

	// 渲染视图：权限点/角色经 bizterm 渲染业务名；数据范围渲为只读符号谓词（$user. 保留，TT-5）。
	type roleRow struct {
		Name   string
		Caps   []string
		Scopes []string
	}
	caps := make([]string, 0, len(t.Permissions))
	nameByCode := map[string]string{}
	for _, p := range t.Permissions {
		cn := capabilityName(p.Name, p.Resource, p.Action)
		caps = append(caps, cn)
		nameByCode[p.Code] = cn
	}
	roles := make([]roleRow, 0, len(t.Roles))
	for _, role := range t.Roles {
		rr := roleRow{Name: role.Name}
		for _, pc := range role.PermissionCodes {
			cn := nameByCode[pc]
			if cn == "" {
				cn = "（未知能力）"
			}
			rr.Caps = append(rr.Caps, cn)
		}
		for _, ds := range role.DataScopes {
			rr.Scopes = append(rr.Scopes, ds.Resource+"：仅 "+conditionPredicate(ds.Condition))
		}
		roles = append(roles, rr)
	}

	h.renderPage(w, r, "ops_tenant_template.html", http.StatusOK, map[string]any{
		"AppID":       appID,
		"TemplateID":  tplID,
		"Name":        t.Name,
		"Description": t.Description,
		"Caps":        caps,
		"Roles":       roles,
		"CSRF":        sess.CSRF,
		"OpsNav":      "templates",
	})
}

// opsApplyTenantTemplate：POST /ops/apps/{app_id}/tenant-templates/{template_id}/apply —— 应用后渲染摘要
// （非 PRG，镜像 opsApplyTemplate：ApplyTenantTemplate 幂等，刷新重提交无害；安全管线同 doWrite）。
func (h *Handler) opsApplyTenantTemplate(w http.ResponseWriter, r *http.Request) {
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
		h.renderGRPCError(w, r, svc+"ApplyTenantTemplate", err)
		return
	}
	tplID, err := pathUint64(r, "template_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"ApplyTenantTemplate", err)
		return
	}
	msg := &adminv1.ApplyTenantTemplateRequest{AppId: appID, TemplateId: tplID}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"ApplyTenantTemplate", principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ApplyTenantTemplate", err)
		return
	}
	if err := mgmt.CheckStatusWrite(ctx, h.db, svc+"ApplyTenantTemplate", msg); err != nil {
		h.renderGRPCError(w, r, svc+"ApplyTenantTemplate", err)
		return
	}
	resp, err := h.srv.ApplyTenantTemplate(ctx, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ApplyTenantTemplate", err)
		return
	}
	h.renderPage(w, r, "ops_template_applied.html", http.StatusOK, map[string]any{
		"AppID":             appID,
		"PermsUpserted":     resp.PermissionsUpserted,
		"PermsSkipped":      resp.PermissionsSkipped,
		"RolesCreated":      resp.RolesCreated,
		"RolesSkipped":      resp.RolesSkipped,
		"DataScopesCreated": resp.DataScopesCreated,
		"OpsNav":            "templates",
	})
}

// opsDeleteTenantTemplate：POST /ops/apps/{app_id}/tenant-templates/{template_id}/delete —— 删本租户模板。
// 走 doWrite；租户派生自 app（scopeTenant），PRG 回模板库。
func (h *Handler) opsDeleteTenantTemplate(w http.ResponseWriter, r *http.Request) {
	if !h.requireConfirm(w, r, svc+"DeleteTenantTemplate") {
		return
	}
	h.doWrite(w, r, svc+"DeleteTenantTemplate",
		func(r *http.Request) (proto.Message, error) {
			appID, err := pathUint64(r, "app_id")
			if err != nil {
				return nil, err
			}
			tid, err := h.tenantOfApp(r.Context(), appID)
			if err != nil {
				return nil, err
			}
			tplID, err := pathUint64(r, "template_id")
			if err != nil {
				return nil, err
			}
			return &adminv1.DeleteTenantTemplateRequest{TenantId: tid, TemplateId: tplID}, nil
		},
		func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
			return s.DeleteTenantTemplate(ctx, m.(*adminv1.DeleteTenantTemplateRequest))
		},
		func(r *http.Request) string { return "/ops/apps/" + r.PathValue("app_id") + "/templates" },
	)
}
