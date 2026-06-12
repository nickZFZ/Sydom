package console

import (
	"context"
	"net/http"
	"strconv"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const svc = "/sydom.admin.v1.AdminService/"

func (h *Handler) registerApps(mux *http.ServeMux) {
	// 用 {$} 精确匹配根，避免 "GET /" 吃掉所有未注册路径（Go 1.22 ServeMux 语义）。
	mux.HandleFunc("GET /{$}", h.dashboard)
	mux.HandleFunc("GET /apps/new", h.appNewForm)
	mux.HandleFunc("POST /apps", h.createApp)
	mux.HandleFunc("GET /apps/redirect", h.appRedirect)
	mux.HandleFunc("POST /apps/{app_id}/status", h.setAppStatus)
}

// appNewForm：建应用表单页（仅需会话；授权延后到 POST）。
func (h *Handler) appNewForm(w http.ResponseWriter, r *http.Request) {
	_, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	h.renderPage(w, r, "app_new.html", http.StatusOK, map[string]any{"Nav": "apps", "CSRF": sess.CSRF})
}

// createApp：建应用走「一次性 secret」专管线——不经 doWrite，绝不 PRG。
// 后端仅此一次返回明文 app_secret，故必须直接渲染 app_created.html 当场展示；
// 一旦 PRG 重定向就会丢失。该明文密钥绝不日志、绝不落盘。
// 管线顺序：会话 → CSRF → 授权 → 直调 → 渲染。
func (h *Handler) createApp(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if !h.checkCSRF(r, sess) {
		h.renderError(w, r, codes.PermissionDenied, "CSRF 校验失败", nil)
		return
	}
	const fm = svc + "CreateApplication"
	msg := &adminv1.CreateApplicationRequest{
		TenantName: r.FormValue("tenant_name"), Domain: r.FormValue("domain"),
		Name: r.FormValue("name"), AppKey: r.FormValue("app_key")}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, fm, principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, fm, err)
		return
	}
	resp, err := h.srv.CreateApplication(ctx, msg)
	if err != nil {
		h.renderGRPCError(w, r, fm, err)
		return
	}
	h.renderPage(w, r, "app_created.html", http.StatusOK, map[string]any{
		"Nav": "apps", "AppID": resp.AppId, "AppSecret": resp.AppSecret}) // 一次性展示，绝不日志/落盘
}

// appRedirect：降级「App ID 直达」——无枚举，绝不查库（查库会泄露 app 存在性）。
// 仅做纯语法校验：能解析则 302 到工作台，不能解析则回首页。
func (h *Handler) appRedirect(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := h.requireSession(w, r); !ok {
		return
	}
	id, err := strconv.ParseUint(r.FormValue("app_id"), 10, 64)
	if err != nil {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	http.Redirect(w, r, "/apps/"+strconv.FormatUint(id, 10)+"/roles", http.StatusFound)
}

// setAppStatus：状态切换走 doWrite（CSRF → 授权 → 直调 → PRG）。
// app_id 取自 path（权威），status ∈ {1=启用, 2=停用}。SetApplicationStatus 豁免
// status 写闸（isWrite=false），故停用一个已启用 app 不会被拦截。
func (h *Handler) setAppStatus(w http.ResponseWriter, r *http.Request) {
	h.doWrite(w, r, svc+"SetApplicationStatus",
		func(r *http.Request) (proto.Message, error) {
			appID, err := pathUint64(r, "app_id")
			if err != nil {
				return nil, err
			}
			st, err := formInt64(r, "status")
			if err != nil {
				return nil, err
			}
			return &adminv1.SetApplicationStatusRequest{AppId: appID, Status: uint32(st)}, nil
		},
		func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
			return s.SetApplicationStatus(ctx, m.(*adminv1.SetApplicationStatusRequest))
		},
		func(r *http.Request) string { return "/" })
}

// dashboard：ListApplications；PermissionDenied → 降级渲染「按 app ID 直达」表单（无枚举）。
func (h *Handler) dashboard(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	const fm = svc + "ListApplications"
	msg := &adminv1.ListApplicationsRequest{}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, fm, principal, msg)
	if err != nil {
		if status.Code(err) == codes.PermissionDenied {
			// 降级：无枚举，只给「输入 App ID 直达」表单，绝不暴露任何 app 列表/存在性。
			h.renderPage(w, r, "dashboard.html", http.StatusOK,
				map[string]any{"Nav": "apps", "Degraded": true, "CSRF": sess.CSRF})
			return
		}
		h.renderGRPCError(w, r, fm, err)
		return
	}
	resp, err := h.srv.ListApplications(ctx, msg)
	if err != nil {
		h.renderGRPCError(w, r, fm, err)
		return
	}
	h.renderPage(w, r, "dashboard.html", http.StatusOK, map[string]any{
		"Nav": "apps", "Degraded": false, "Apps": resp.Applications, "CSRF": sess.CSRF})
}
