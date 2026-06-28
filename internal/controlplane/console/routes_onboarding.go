package console

import (
	"net/http"
	"net/url"
	"strconv"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/controlplane/presets"
	"google.golang.org/grpc/codes"
)

// onboardingPack 是选包步骤的渲染视图（业务名 + 策展）。
type onboardingPack struct {
	ID, Name, Description, Intro string
	Recommended                  bool
	PermCount, RoleCount         int
}

// onboardingSelect：GET /ops/apps/{app_id}/onboarding —— 列官方预设包（推荐置顶 + intro）。
func (h *Handler) onboardingSelect(w http.ResponseWriter, r *http.Request) {
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
	var recommended, others []onboardingPack
	for _, t := range resp.Templates {
		p := onboardingPack{ID: t.Id, Name: t.Name, Description: t.Description,
			PermCount: len(t.Permissions), RoleCount: len(t.Roles)}
		if ob := onboardingOf(t.Id); ob != nil {
			p.Intro = ob.Intro
			p.Recommended = ob.Recommended
		}
		if p.Recommended {
			recommended = append(recommended, p)
		} else {
			others = append(others, p)
		}
	}
	h.renderPage(w, r, "onboarding_select.html", http.StatusOK, map[string]any{
		"AppID": appID, "Packs": append(recommended, others...), // 推荐置顶 + 其余，badge 区分
		"CSRF": sess.CSRF, "OpsNav": "onboarding",
	})
}

// onboardingOf 取内嵌预设包的 onboarding 策展（nil 安全）。
func onboardingOf(id string) *presets.Onboarding {
	t, ok := presets.Get(id)
	if !ok {
		return nil
	}
	return t.Onboarding
}

// onboardingApply：POST /ops/apps/{app_id}/onboarding/apply —— 一键 bootstrap。
// 安全管线镜像 doWrite：会话→CSRF→AuthorizeRule→status 闸→ApplyTemplate；幂等故 PRG 重定向到分配步骤。
func (h *Handler) onboardingApply(w http.ResponseWriter, r *http.Request) {
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
	templateID := r.FormValue("template_id")
	msg := &adminv1.ApplyTemplateRequest{AppId: appID, TemplateId: templateID}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"ApplyTemplate", principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ApplyTemplate", err)
		return
	}
	if err := mgmt.CheckStatusWrite(ctx, h.db, svc+"ApplyTemplate", msg); err != nil {
		h.renderGRPCError(w, r, svc+"ApplyTemplate", err)
		return
	}
	if _, err := h.srv.ApplyTemplate(ctx, msg); err != nil {
		h.renderGRPCError(w, r, svc+"ApplyTemplate", err)
		return
	}
	http.Redirect(w, r, "/ops/apps/"+strconv.FormatUint(appID, 10)+"/onboarding/assign?template_id="+url.QueryEscape(templateID), http.StatusSeeOther)
}

// onboardingDone：GET /ops/apps/{app_id}/onboarding/done —— 完成页（next_steps 指向运营台）。
func (h *Handler) onboardingDone(w http.ResponseWriter, r *http.Request) {
	_, _, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	appID, err := pathUint64(r, "app_id")
	if err != nil {
		h.renderGRPCError(w, r, "console/onboarding/done", err) // 纯渲染页，无 RPC：标签忠实反映 handler
		return
	}
	var nextSteps []string
	if ob := onboardingOf(r.FormValue("template_id")); ob != nil {
		nextSteps = ob.NextSteps
	}
	h.renderPage(w, r, "onboarding_done.html", http.StatusOK, map[string]any{
		"AppID": appID, "NextSteps": nextSteps, "OpsNav": "onboarding",
	})
}

// registerOnboarding 注册新 app 首次引导向导（复用既有 RPC + AuthorizeRule，零新增鉴权）。
func (h *Handler) registerOnboarding(mux *http.ServeMux) {
	mux.HandleFunc("GET /ops/apps/{app_id}/onboarding", h.onboardingSelect)
	mux.HandleFunc("POST /ops/apps/{app_id}/onboarding/apply", h.onboardingApply)
	mux.HandleFunc("GET /ops/apps/{app_id}/onboarding/done", h.onboardingDone)
	// 分配步骤（GET/POST .../onboarding/assign）由任务 3 加入本函数。
}
