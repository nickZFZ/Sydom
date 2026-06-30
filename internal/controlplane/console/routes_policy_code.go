package console

import (
	"fmt"
	"net/http"
	"strconv"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"google.golang.org/grpc/codes"
)

// registerPolicyCode 注册策略即代码三条路由（建模台 tab）。
func (h *Handler) registerPolicyCode(mux *http.ServeMux) {
	mux.HandleFunc("GET /apps/{app_id}/policy-code", h.policyCodePage)
	mux.HandleFunc("GET /apps/{app_id}/policy-code/export", h.exportPolicyCode)
	mux.HandleFunc("POST /apps/{app_id}/policy-code/import", h.importPolicyCode)
}

// policyCodePage：读页。requireSession → pathUint64 → 渲 policy_code.html（无 gRPC 调用）。
func (h *Handler) policyCodePage(w http.ResponseWriter, r *http.Request) {
	_, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	appID, err := pathUint64(r, "app_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"ExportAppPolicy", err)
		return
	}
	h.renderPage(w, r, "policy_code.html", http.StatusOK, map[string]any{
		"Nav": "apps", "AppID": appID, "Tab": "policycode", "CSRF": sess.CSRF,
	})
}

// exportPolicyCode：下载策略文件。requireSession → pathUint64 → AuthorizeRule → ExportAppPolicy
// → 写 Content-Disposition + Content-Type 直接推文件（不渲页）。GET 读不校验 CSRF。
func (h *Handler) exportPolicyCode(w http.ResponseWriter, r *http.Request) {
	principal, _, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	appID, err := pathUint64(r, "app_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"ExportAppPolicy", err)
		return
	}
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "yaml"
	}
	msg := &adminv1.ExportAppPolicyRequest{AppId: appID, Format: format}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"ExportAppPolicy", principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ExportAppPolicy", err)
		return
	}
	resp, err := h.srv.ExportAppPolicy(ctx, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ExportAppPolicy", err)
		return
	}
	filename := "policy.yaml"
	contentType := "application/x-yaml; charset=utf-8"
	if format == "json" {
		filename = "policy.json"
		contentType = "application/json; charset=utf-8"
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(resp.Content))
}

// importPolicyCode：预览/应用二段式。管线顺序铁律：会话→CSRF→pathUint64→AuthorizeRule→CheckStatusWrite→invoke。
// confirmed=false → 预览（dry_run=true）→ 渲 policy_code_diff.html；
// confirmed=true  → 应用（dry_run=false）→ 写 flash + PRG 303。
func (h *Handler) importPolicyCode(w http.ResponseWriter, r *http.Request) {
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
		h.renderGRPCError(w, r, svc+"ImportAppPolicy", err)
		return
	}
	content := r.FormValue("content")
	confirmed := r.PostFormValue("confirmed") == "1"
	msg := &adminv1.ImportAppPolicyRequest{AppId: appID, Content: content, DryRun: !confirmed}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"ImportAppPolicy", principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ImportAppPolicy", err)
		return
	}
	if err := mgmt.CheckStatusWrite(ctx, h.db, svc+"ImportAppPolicy", msg); err != nil {
		h.renderGRPCError(w, r, svc+"ImportAppPolicy", err)
		return
	}
	resp, err := h.srv.ImportAppPolicy(ctx, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ImportAppPolicy", err)
		return
	}
	if confirmed {
		if id := h.sessionID(r); id != "" {
			if err := h.sessions.SetFlash(ctx, id, flashFor(svc+"ImportAppPolicy")); err != nil {
				h.logger.Warn("console set flash", "err", err)
			}
		}
		http.Redirect(w, r, "/apps/"+strconv.FormatUint(appID, 10)+"/policy-code", http.StatusSeeOther)
		return
	}
	h.renderPage(w, r, "policy_code_diff.html", http.StatusOK, map[string]any{
		"Nav":       "apps",
		"AppID":     appID,
		"Tab":       "policycode",
		"CSRF":      sess.CSRF,
		"Content":   content,
		"Diff":      resp.Diff,
		"Creates":   resp.Creates,
		"Adopts":    resp.Adopts,
		"Updates":   resp.Updates,
		"Deletes":   resp.Deletes,
		"Conflicts": resp.Conflicts,
	})
}
