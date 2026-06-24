package console

import (
	"context"
	"fmt"
	"net/http"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"google.golang.org/protobuf/proto"
)

// registerRBAC 注册工作台角色/权限点/授权/继承/用户绑定路由。
// 本任务（任务 5）实现 角色(List/Create/Delete) + 权限点(List/Upsert)。
func (h *Handler) registerRBAC(mux *http.ServeMux) {
	mux.HandleFunc("GET /apps/{app_id}/roles", h.listRoles)
	mux.HandleFunc("POST /apps/{app_id}/roles", h.createRole)
	mux.HandleFunc("POST /apps/{app_id}/roles/{role_id}/delete", h.deleteRole)
	mux.HandleFunc("GET /apps/{app_id}/permissions", h.listPermissions)
	mux.HandleFunc("POST /apps/{app_id}/permissions", h.upsertPermission)
	mux.HandleFunc("GET /apps/{app_id}/grants", h.listGrants)
	mux.HandleFunc("POST /apps/{app_id}/grants", h.grantPermission)
	mux.HandleFunc("POST /apps/{app_id}/grants/revoke", h.revokePermission)
	mux.HandleFunc("GET /apps/{app_id}/inheritances", h.listInheritances)
	mux.HandleFunc("POST /apps/{app_id}/inheritances", h.addInheritance)
	mux.HandleFunc("POST /apps/{app_id}/inheritances/remove", h.removeInheritance)
	mux.HandleFunc("GET /apps/{app_id}/bindings", h.listBindings)
	mux.HandleFunc("POST /apps/{app_id}/bindings", h.bindUser)
	mux.HandleFunc("POST /apps/{app_id}/bindings/unbind", h.unbindUser)
	mux.HandleFunc("GET /apps/{app_id}/effective", h.effectivePermissions)
	mux.HandleFunc("POST /apps/{app_id}/effective/bind", h.bindUserOnEffective)
	mux.HandleFunc("POST /apps/{app_id}/effective/unbind", h.unbindUserOnEffective)
	mux.HandleFunc("GET /apps/{app_id}/decision", h.decisionExplainer)
}

// appListRedirect：PRG 重定向回 /apps/{app_id}/{seg}（app_id 取自 path，权威）。
func appListRedirect(seg string) func(*http.Request) string {
	return func(r *http.Request) string { return fmt.Sprintf("/apps/%s/%s", r.PathValue("app_id"), seg) }
}

// listRoles：读页内联范式（requireSession → path 取 app_id → 授权 → 直调 → 渲染）。
func (h *Handler) listRoles(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	appID, err := pathUint64(r, "app_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListRoles", err)
		return
	}
	msg := &adminv1.ListRolesRequest{AppId: appID, Page: listPageFromReq(r)}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"ListRoles", principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListRoles", err)
		return
	}
	resp, err := h.srv.ListRoles(ctx, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListRoles", err)
		return
	}
	h.renderPage(w, r, "roles.html", http.StatusOK, map[string]any{
		"Nav": "apps", "AppID": appID, "Tab": "roles", "Roles": resp.Roles,
		"CSRF": sess.CSRF, "Pager": pagerData(r, resp.Total)})
}

// createRole：写动作走 doWrite（CSRF → 授权 → status 闸 → 直调 → PRG）。
func (h *Handler) createRole(w http.ResponseWriter, r *http.Request) {
	h.doWrite(w, r, svc+"CreateRole",
		func(r *http.Request) (proto.Message, error) {
			id, err := pathUint64(r, "app_id")
			return &adminv1.CreateRoleRequest{AppId: id, Code: r.FormValue("code"), Name: r.FormValue("name")}, err
		},
		func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
			return s.CreateRole(ctx, m.(*adminv1.CreateRoleRequest))
		},
		appListRedirect("roles"))
}

// deleteRole：app_id 先解码（错则直接返回），再 role_id；均从 path 取（path 权威）。
func (h *Handler) deleteRole(w http.ResponseWriter, r *http.Request) {
	if !h.requireConfirm(w, r, svc+"DeleteRole") {
		return
	}
	h.doWrite(w, r, svc+"DeleteRole",
		func(r *http.Request) (proto.Message, error) {
			appID, err := pathUint64(r, "app_id")
			if err != nil {
				return nil, err
			}
			roleID, err := pathInt64(r, "role_id")
			return &adminv1.DeleteRoleRequest{AppId: appID, RoleId: roleID}, err
		},
		func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
			return s.DeleteRole(ctx, m.(*adminv1.DeleteRoleRequest))
		},
		appListRedirect("roles"))
}

// listPermissions：读页内联范式（同 listRoles）。
func (h *Handler) listPermissions(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	appID, err := pathUint64(r, "app_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListPermissions", err)
		return
	}
	msg := &adminv1.ListPermissionsRequest{AppId: appID, Page: listPageFromReq(r)}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"ListPermissions", principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListPermissions", err)
		return
	}
	resp, err := h.srv.ListPermissions(ctx, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListPermissions", err)
		return
	}
	h.renderPage(w, r, "permissions.html", http.StatusOK, map[string]any{
		"Nav": "apps", "AppID": appID, "Tab": "permissions", "Permissions": resp.Permissions,
		"CSRF": sess.CSRF, "Pager": pagerData(r, resp.Total)})
}

// upsertPermission：写动作走 doWrite。app_id 从 path 取；其余字段取表单。
func (h *Handler) upsertPermission(w http.ResponseWriter, r *http.Request) {
	h.doWrite(w, r, svc+"UpsertPermission",
		func(r *http.Request) (proto.Message, error) {
			id, err := pathUint64(r, "app_id")
			return &adminv1.UpsertPermissionRequest{
				AppId:    id,
				Code:     r.FormValue("code"),
				Resource: r.FormValue("resource"),
				Action:   r.FormValue("action"),
				Ptype:    r.FormValue("ptype"),
				Name:     r.FormValue("name"),
			}, err
		},
		func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
			return s.UpsertPermission(ctx, m.(*adminv1.UpsertPermissionRequest))
		},
		appListRedirect("permissions"))
}

// listGrants：读页内联范式。可选 ?role_id= 过滤（空→0→全部）。
func (h *Handler) listGrants(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	appID, err := pathUint64(r, "app_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListGrants", err)
		return
	}
	roleFilter, err := formInt64(r, "role_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListGrants", err)
		return
	}
	msg := &adminv1.ListGrantsRequest{AppId: appID, RoleId: roleFilter, Page: listPageFromReq(r)}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"ListGrants", principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListGrants", err)
		return
	}
	resp, err := h.srv.ListGrants(ctx, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListGrants", err)
		return
	}
	h.renderPage(w, r, "grants.html", http.StatusOK, map[string]any{
		"Nav": "apps", "AppID": appID, "Tab": "grants", "Grants": resp.Grants,
		"CSRF": sess.CSRF, "Pager": pagerData(r, resp.Total)})
}

// grantPermission：写动作走 doWrite。eft 空串透传，后端按 allow。
func (h *Handler) grantPermission(w http.ResponseWriter, r *http.Request) {
	h.doWrite(w, r, svc+"GrantPermission",
		func(r *http.Request) (proto.Message, error) {
			appID, err := pathUint64(r, "app_id")
			if err != nil {
				return nil, err
			}
			roleID, err := formInt64(r, "role_id")
			if err != nil {
				return nil, err
			}
			permID, err := formInt64(r, "permission_id")
			if err != nil {
				return nil, err
			}
			return &adminv1.GrantPermissionRequest{AppId: appID, RoleId: roleID, PermissionId: permID, Eft: r.FormValue("eft")}, nil
		},
		func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
			return s.GrantPermission(ctx, m.(*adminv1.GrantPermissionRequest))
		},
		appListRedirect("grants"))
}

// revokePermission：写动作走 doWrite。
func (h *Handler) revokePermission(w http.ResponseWriter, r *http.Request) {
	h.doWrite(w, r, svc+"RevokePermission",
		func(r *http.Request) (proto.Message, error) {
			appID, err := pathUint64(r, "app_id")
			if err != nil {
				return nil, err
			}
			roleID, err := formInt64(r, "role_id")
			if err != nil {
				return nil, err
			}
			permID, err := formInt64(r, "permission_id")
			if err != nil {
				return nil, err
			}
			return &adminv1.RevokePermissionRequest{AppId: appID, RoleId: roleID, PermissionId: permID}, nil
		},
		func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
			return s.RevokePermission(ctx, m.(*adminv1.RevokePermissionRequest))
		},
		appListRedirect("grants"))
}

// listInheritances：读页内联范式。无过滤。
func (h *Handler) listInheritances(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	appID, err := pathUint64(r, "app_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListRoleInheritances", err)
		return
	}
	msg := &adminv1.ListRoleInheritancesRequest{AppId: appID, Page: listPageFromReq(r)}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"ListRoleInheritances", principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListRoleInheritances", err)
		return
	}
	resp, err := h.srv.ListRoleInheritances(ctx, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListRoleInheritances", err)
		return
	}
	h.renderPage(w, r, "inheritances.html", http.StatusOK, map[string]any{
		"Nav": "apps", "AppID": appID, "Tab": "inheritances", "Inheritances": resp.Inheritances,
		"CSRF": sess.CSRF, "Pager": pagerData(r, resp.Total)})
}

// addInheritance：写动作走 doWrite。
func (h *Handler) addInheritance(w http.ResponseWriter, r *http.Request) {
	h.doWrite(w, r, svc+"AddRoleInheritance",
		func(r *http.Request) (proto.Message, error) {
			appID, err := pathUint64(r, "app_id")
			if err != nil {
				return nil, err
			}
			childID, err := formInt64(r, "child_role_id")
			if err != nil {
				return nil, err
			}
			parentID, err := formInt64(r, "parent_role_id")
			if err != nil {
				return nil, err
			}
			return &adminv1.RoleInheritanceRequest{AppId: appID, ChildRoleId: childID, ParentRoleId: parentID}, nil
		},
		func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
			return s.AddRoleInheritance(ctx, m.(*adminv1.RoleInheritanceRequest))
		},
		appListRedirect("inheritances"))
}

// removeInheritance：写动作走 doWrite（同 RoleInheritanceRequest 类型）。破坏性，先过确认门。
func (h *Handler) removeInheritance(w http.ResponseWriter, r *http.Request) {
	if !h.requireConfirm(w, r, svc+"RemoveRoleInheritance") {
		return
	}
	h.doWrite(w, r, svc+"RemoveRoleInheritance",
		func(r *http.Request) (proto.Message, error) {
			appID, err := pathUint64(r, "app_id")
			if err != nil {
				return nil, err
			}
			childID, err := formInt64(r, "child_role_id")
			if err != nil {
				return nil, err
			}
			parentID, err := formInt64(r, "parent_role_id")
			if err != nil {
				return nil, err
			}
			return &adminv1.RoleInheritanceRequest{AppId: appID, ChildRoleId: childID, ParentRoleId: parentID}, nil
		},
		func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
			return s.RemoveRoleInheritance(ctx, m.(*adminv1.RoleInheritanceRequest))
		},
		appListRedirect("inheritances"))
}

// listBindings：读页内联范式。可选 ?user_id= 过滤（""→全部）。
func (h *Handler) listBindings(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	appID, err := pathUint64(r, "app_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListUserBindings", err)
		return
	}
	msg := &adminv1.ListUserBindingsRequest{AppId: appID, UserId: r.FormValue("user_id"), Page: listPageFromReq(r)}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"ListUserBindings", principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListUserBindings", err)
		return
	}
	resp, err := h.srv.ListUserBindings(ctx, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListUserBindings", err)
		return
	}
	h.renderPage(w, r, "bindings.html", http.StatusOK, map[string]any{
		"Nav": "apps", "AppID": appID, "Tab": "bindings", "Bindings": resp.Bindings,
		"CSRF": sess.CSRF, "Pager": pagerData(r, resp.Total)})
}

// decodeUserRoleRequest 从 path(app_id) + form(role_id/user_id) 解码 UserRoleRequest。
// bindings 页与有效权限页的 bind/unbind 四处 handler 共用，消除字节级相同的 decode 闭包。
func decodeUserRoleRequest(r *http.Request) (proto.Message, error) {
	appID, err := pathUint64(r, "app_id")
	if err != nil {
		return nil, err
	}
	roleID, err := formInt64(r, "role_id")
	if err != nil {
		return nil, err
	}
	return &adminv1.UserRoleRequest{AppId: appID, UserId: r.FormValue("user_id"), RoleId: roleID}, nil
}

// bindUser：写动作走 doWrite。
func (h *Handler) bindUser(w http.ResponseWriter, r *http.Request) {
	h.doWrite(w, r, svc+"BindUserRole",
		decodeUserRoleRequest,
		func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
			return s.BindUserRole(ctx, m.(*adminv1.UserRoleRequest))
		},
		appListRedirect("bindings"))
}

// unbindUser：写动作走 doWrite（同 UserRoleRequest 类型）。
func (h *Handler) unbindUser(w http.ResponseWriter, r *http.Request) {
	h.doWrite(w, r, svc+"UnbindUserRole",
		decodeUserRoleRequest,
		func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
			return s.UnbindUserRole(ctx, m.(*adminv1.UserRoleRequest))
		},
		appListRedirect("bindings"))
}
