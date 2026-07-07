package console

import (
	"net/http"
	"strings"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
)

// registerDataSandbox 注册数据权限沙箱专页（建模台开发者区，只读）。
func (h *Handler) registerDataSandbox(mux *http.ServeMux) {
	mux.HandleFunc("GET /apps/{app_id}/data-sandbox", h.dataSandbox)
}

// dataSandbox：数据权限沙箱页（读）。GET ?subject=&resource=&attrs= 三者齐备时调 PreviewDataFilter
// 渲染参数化 WHERE + args（与 Sidecar 数据面同源），否则只渲表单。attrs 为 "key=value 每行" 文本，
// 服务端解析（无新 JS）。幂等只读——AuthorizeRule(scopeApp read) fail-close 前置，无写/无 bump/无审计。
func (h *Handler) dataSandbox(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	appID, err := pathUint64(r, "app_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"PreviewDataFilter", err)
		return
	}
	subject := r.FormValue("subject")
	resource := r.FormValue("resource")
	attrsRaw := r.FormValue("attrs")
	data := map[string]any{
		"Nav": "apps", "AppID": appID, "Tab": "datasandbox",
		"Subject": subject, "Resource": resource, "AttrsRaw": attrsRaw, "CSRF": sess.CSRF,
	}
	if subject != "" && resource != "" {
		msg := &adminv1.PreviewDataFilterRequest{
			AppId: appID, Subject: subject, Resource: resource, Attrs: parseAttrs(attrsRaw),
		}
		ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"PreviewDataFilter", principal, msg)
		if err != nil {
			h.renderGRPCError(w, r, svc+"PreviewDataFilter", err)
			return
		}
		resp, err := h.srv.PreviewDataFilter(ctx, msg)
		if err != nil {
			h.renderGRPCError(w, r, svc+"PreviewDataFilter", err)
			return
		}
		data["Queried"] = true
		data["SQL"] = resp.Sql
		data["Args"] = resp.Args
	}
	h.renderPage(w, r, "data_sandbox.html", http.StatusOK, data)
}

// parseAttrs 把 "key=value 每行" 文本解析为 map（空行/无 = 的行跳过；只切首个 =）。
func parseAttrs(raw string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		k, v, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		out[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return out
}
