package console

import (
	"net/http"

	"google.golang.org/grpc/codes"
)

// confirmPrompts 是破坏性 fullMethod → 业务语言确认问句（缺省回退通用语）。
var confirmPrompts = map[string]string{
	svc + "DeleteRole":              "确定删除该业务角色吗？此操作不可撤销。",
	svc + "RemoveRoleInheritance":   "确定移除该继承关系吗？",
	svc + "RevokeAdminGrant":        "确定撤销该管理员授权吗？此操作立即生效。",
	svc + "UnbindOperatorRole":      "确定解绑该操作员角色吗？此操作立即生效。",
	svc + "RotateApplicationSecret": "确定轮换应用凭据吗？旧凭据将立即失效。",
	svc + "ResetOperatorSecret":     "确定重置该操作员凭据吗？旧凭据将立即失效。",
	svc + "DeleteDataPolicy":        "确定删除该数据策略吗？此操作不可撤销。",
	svc + "DeleteTenantTemplate":    "确定删除该模板吗？此操作不可撤销。",
	svc + "SetApplicationStatus":    "确定停用该应用吗？停用后将拒绝该应用的写操作。",
}

func confirmPrompt(fullMethod string) string {
	if p, ok := confirmPrompts[fullMethod]; ok {
		return p
	}
	return "确定执行此操作吗？"
}

// requireConfirm 是破坏性动作的二次确认门。
//
// 缺 confirmed=1 → 校验会话+CSRF 后渲染通用确认页（回显原 POST 非 csrf/confirmed 表单值为隐藏字段），
// 返回 false（调用方应 return）；
// 有 confirmed=1 → 返回 true，调用方继续（后续 doWrite/专管线再次校验 CSRF/授权/status）。
func (h *Handler) requireConfirm(w http.ResponseWriter, r *http.Request, fullMethod string) bool {
	_, sess, ok := h.requireSession(w, r)
	if !ok {
		return false
	}
	if !h.checkCSRF(r, sess) {
		h.renderError(w, r, codes.PermissionDenied, "CSRF 校验失败", nil)
		return false
	}
	if r.PostFormValue("confirmed") == "1" {
		return true
	}
	if err := r.ParseForm(); err != nil {
		h.renderError(w, r, codes.InvalidArgument, "表单解析失败", nil)
		return false
	}
	type kv struct{ Name, Value string }
	var hidden []kv
	for name, vals := range r.PostForm {
		if name == "csrf_token" || name == "confirmed" {
			continue
		}
		for _, v := range vals {
			hidden = append(hidden, kv{Name: name, Value: v})
		}
	}
	h.renderPage(w, r, "ops_confirm.html", http.StatusOK, map[string]any{
		"Action": r.URL.Path,
		"Prompt": confirmPrompt(fullMethod),
		"Hidden": hidden,
		"CSRF":   sess.CSRF,
		"Flash":  "", // 确认页是过渡页，不消费待显示的 flash（留给后续目标页）
	})
	return false
}
