package console

import (
	"net/http"
	"strings"

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
	svc + "DeleteTenantIdp":         "确定删除该租户的 SSO 配置吗？删除后该域 SSO 登录停用；仅 SSO 登录的 operator（含 JIT 开通、无密码）将无法登录，已开通的成员账户保留。此操作不可撤销。",
	svc + "DeleteTenantTemplate":    "确定删除该模板吗？此操作不可撤销。",
	svc + "SetApplicationStatus":    "确定停用该应用吗？停用后将拒绝该应用的写操作。",
	// M4.2 批量移除族：无 JS 确认页文案与各模板 data-confirm（有 JS 路径）逐字一致。
	svc + "BatchDeleteRole":            "确认批量移除选中的角色？将一并移除其授权与绑定。",
	svc + "BatchRevokePermission":      "确认批量撤销选中的授权？",
	svc + "BatchRemoveRoleInheritance": "确认批量移除选中的继承关系？",
	svc + "BatchUnbindUserRole":        "确认批量解绑选中的用户角色？",
	svc + "BatchDeleteDataPolicy":      "确认批量删除选中的数据策略？此操作不可撤销。",
}

func confirmPrompt(fullMethod string) string {
	if p, ok := confirmPrompts[fullMethod]; ok {
		return p
	}
	return "确定执行此操作吗？"
}

// confirmCancelURL 为确认页「取消」推导一个必定有效的返回目标（真实链接）。
// 严格 CSP（script-src 'self' 无 unsafe-inline）下 href="javascript:history.back()" 被浏览器拒、
// 点击静默失效；且 Referrer-Policy: no-referrer 下无 Referer 可回溯——故按 Action 路径的作用域
// 回落到对应列表页。无法判定时回落登录后首页 "/"（对任何已登录操作员必定可达）。
func confirmCancelURL(r *http.Request) string {
	if appID := r.PathValue("app_id"); appID != "" {
		if strings.HasPrefix(r.URL.Path, "/ops/") {
			return "/ops/apps/" + appID + "/roles"
		}
		return "/apps/" + appID + "/roles"
	}
	switch {
	case strings.HasPrefix(r.URL.Path, "/operators/"):
		return "/operators"
	case strings.HasPrefix(r.URL.Path, "/admin-roles/"):
		return "/admin-roles"
	}
	return "/"
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
		"Action":    r.URL.Path,
		"Prompt":    confirmPrompt(fullMethod),
		"Hidden":    hidden,
		"CSRF":      sess.CSRF,
		"CancelURL": confirmCancelURL(r),
		"Flash":     "", // 确认页是过渡页，不消费待显示的 flash（留给后续目标页）
	})
	return false
}
