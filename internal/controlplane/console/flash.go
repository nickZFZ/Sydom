package console

import "net/http"

// flashMessages 是 fullMethod → 成功后 flash 文案（业务语言；缺省回退通用语）。
var flashMessages = map[string]string{
	svc + "CreateRole":            "角色已创建",
	svc + "DeleteRole":            "角色已删除",
	svc + "GrantPermission":       "权限已授予",
	svc + "RevokePermission":      "权限已撤销",
	svc + "AddRoleInheritance":    "继承已添加",
	svc + "RemoveRoleInheritance": "继承已移除",
	svc + "BindUserRole":          "已绑定角色",
	svc + "UnbindUserRole":        "已解绑角色",
	svc + "SetApplicationStatus":  "应用状态已更新",
	svc + "RevokeAdminGrant":      "管理员授权已撤销",
	svc + "UnbindOperatorRole":    "操作员角色已解绑",
	svc + "DeleteTenantTemplate":  "模板已删除",
	svc + "CreateBusinessRole":    "业务角色已创建",
	svc + "CreateAdminRole":       "管理员角色已创建",
	svc + "GrantAdminRole":        "已授予管理员角色",
	svc + "BindOperatorRole":      "已绑定操作员角色",
	svc + "SetOperatorStatus":     "操作员状态已更新",
	svc + "SaveAppAsTemplate":     "已存为模板",
	svc + "UpsertPermission":      "权限点已保存",
	svc + "UpsertDataPolicy":      "数据策略已保存",
	svc + "DeleteDataPolicy":      "数据策略已删除",
	// 一次性 secret 动作(轮换/重置)不进 flash(走专管线)。
}

// flashFor 返回该 fullMethod 的成功文案，缺省回退通用语。
func flashFor(fullMethod string) string {
	if m, ok := flashMessages[fullMethod]; ok {
		return m
	}
	return "操作成功"
}

// sessionID 从 cookie 取会话 id（无则空串）。
func (h *Handler) sessionID(r *http.Request) string {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return ""
	}
	return c.Value
}
