package console

import (
	"net/http"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const svc = "/sydom.admin.v1.AdminService/"

func (h *Handler) registerApps(mux *http.ServeMux) {
	// 用 {$} 精确匹配根，避免 "GET /" 吃掉所有未注册路径（Go 1.22 ServeMux 语义）。
	mux.HandleFunc("GET /{$}", h.dashboard)
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
