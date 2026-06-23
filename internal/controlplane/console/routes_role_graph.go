package console

import (
	"net/http"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
)

// registerRoleGraph 注册角色全景 + 反事实模拟路由（M3.3）。
func (h *Handler) registerRoleGraph(mux *http.ServeMux) {
	mux.HandleFunc("GET /ops/apps/{app_id}/roles/{role_id}/graph", h.roleGraph)
	mux.HandleFunc("GET /ops/apps/{app_id}/roles/{role_id}/simulate", h.roleSimulate)
}

// roleGraph：GET /ops/apps/{app_id}/roles/{role_id}/graph
// 角色全景分区面板：绑定用户 / 能力 / 继承 / 数据范围 + 两个「假如…」GET 表单。
// 能力名经 capabilityName（GetRoleGraph 已返回真实 name，空名合成「resource · 动词」）。
// 数据范围经 conditionPredicate 渲符号谓词（绝不裸 JSON）。
func (h *Handler) roleGraph(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	appID, err := pathUint64(r, "app_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"GetRoleGraph", err)
		return
	}
	roleID, err := pathInt64(r, "role_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"GetRoleGraph", err)
		return
	}
	msg := &adminv1.GetRoleGraphRequest{AppId: appID, RoleId: roleID}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"GetRoleGraph", principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"GetRoleGraph", err)
		return
	}
	g, err := h.srv.GetRoleGraph(ctx, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"GetRoleGraph", err)
		return
	}

	type capRow struct{ Name, Source string }
	type scopeRow struct{ Resource, Predicate string }

	caps := make([]capRow, 0, len(g.Capabilities))
	for _, c := range g.Capabilities {
		caps = append(caps, capRow{
			Name:   capabilityName(c.Name, c.Resource, c.Action),
			Source: c.Source,
		})
	}
	scopes := make([]scopeRow, 0, len(g.DataScopes))
	for _, d := range g.DataScopes {
		scopes = append(scopes, scopeRow{
			Resource:  d.Resource,
			Predicate: conditionPredicate(d.Condition),
		})
	}

	h.renderPage(w, r, "ops_role_graph.html", http.StatusOK, map[string]any{
		"AppID":      appID,
		"RoleID":     roleID,
		"RoleName":   g.RoleName,
		"BoundUsers": g.BoundUsers,
		"Caps":       caps,
		"Parents":    g.Parents,
		"Scopes":     scopes,
		"CSRF":       sess.CSRF,
		"OpsNav":     "roles",
	})
}

// roleSimulate：GET /ops/apps/{app_id}/roles/{role_id}/simulate
// 反事实 diff 页（读语义 GET，无 CSRF / status 写闸）。
// 能力名经 permNameMap.label（与 M1.4 opsPersonView 完全一致，绝不裸 resource:action，TP-8）。
// 数据范围直接渲染 SubjectDiff.AddedDataPreviews[].Predicate（符号谓词，已由 effperm 填充）。
func (h *Handler) roleSimulate(w http.ResponseWriter, r *http.Request) {
	principal, _, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	appID, err := pathUint64(r, "app_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"SimulateRoleChange", err)
		return
	}
	roleID, err := pathInt64(r, "role_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"SimulateRoleChange", err)
		return
	}
	q := r.URL.Query()
	msg := &adminv1.SimulateRoleChangeRequest{
		AppId:      appID,
		RoleId:     roleID,
		ChangeType: parseConsoleChangeType(q.Get("change_type")),
		UserId:     q.Get("user_id"),
		Resource:   q.Get("resource"),
		Action:     q.Get("action"),
	}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"SimulateRoleChange", principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"SimulateRoleChange", err)
		return
	}
	res, err := h.srv.SimulateRoleChange(ctx, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"SimulateRoleChange", err)
		return
	}

	// 业务名映射（M1.4 opsPersonView 同款：真实 name；缺名「resource · 动词」；绝不裸原语 TP-8）。
	capNames, err := h.permNameMap(r.Context(), principal, appID)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListPermissions", err)
		return
	}

	type permRow struct{ Name string }
	type scopePreviewRow struct{ Resource, Predicate string }
	type subjRow struct {
		UserID      string
		Added       []permRow
		Removed     []permRow
		AddedScopes []scopePreviewRow
		RemScopes   []scopePreviewRow
	}

	subjects := make([]subjRow, 0, len(res.Subjects))
	for _, s := range res.Subjects {
		sr := subjRow{UserID: s.UserId}
		for _, p := range s.AddedPermissions {
			sr.Added = append(sr.Added, permRow{Name: capNames.label(p.Resource, p.Action)})
		}
		for _, p := range s.RemovedPermissions {
			sr.Removed = append(sr.Removed, permRow{Name: capNames.label(p.Resource, p.Action)})
		}
		for _, v := range s.AddedDataPreviews {
			sr.AddedScopes = append(sr.AddedScopes, scopePreviewRow{
				Resource:  v.Resource,
				Predicate: v.Predicate,
			})
		}
		for _, v := range s.RemovedDataPreviews {
			sr.RemScopes = append(sr.RemScopes, scopePreviewRow{
				Resource:  v.Resource,
				Predicate: v.Predicate,
			})
		}
		subjects = append(subjects, sr)
	}

	h.renderPage(w, r, "ops_role_simulate.html", http.StatusOK, map[string]any{
		"AppID":    appID,
		"RoleID":   roleID,
		"Subjects": subjects,
		"OpsNav":   "roles",
	})
}

// parseConsoleChangeType 将表单字符串映射到 proto 枚举。
// 未知字符串回退到 BIND_USER（最常见操作）。
func parseConsoleChangeType(s string) adminv1.RoleChangeType {
	if s == "add_capability" {
		return adminv1.RoleChangeType_ADD_CAPABILITY
	}
	return adminv1.RoleChangeType_BIND_USER
}
