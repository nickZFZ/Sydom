package console

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"google.golang.org/protobuf/proto"
)

// effectivePermissions：用户为中心页面（读）。
// GET ?user_id= → 角色下拉（分配）+ 当前绑定（可解绑）+ 有效权限（功能允许集 + 数据策略符号谓词）。
// 鉴权：ListRoles（始终），ListUserBindings + GetEffectivePermissions（user_id 非空时）。
// 任一鉴权失败走 renderGRPCError（降级无枚举）。
func (h *Handler) effectivePermissions(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	appID, err := pathUint64(r, "app_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListRoles", err)
		return
	}
	userID := r.FormValue("user_id")

	// 角色下拉（供分配）。
	rolesMsg := &adminv1.ListRolesRequest{AppId: appID}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"ListRoles", principal, rolesMsg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListRoles", err)
		return
	}
	rolesResp, err := h.srv.ListRoles(ctx, rolesMsg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListRoles", err)
		return
	}

	data := map[string]any{
		"Nav":    "apps",
		"AppID":  appID,
		"Tab":    "effective",
		"UserID": userID,
		"Roles":  rolesResp.Roles,
		"CSRF":   sess.CSRF,
	}

	if userID != "" {
		// 当前角色绑定（可解绑）。
		bindMsg := &adminv1.ListUserBindingsRequest{AppId: appID, UserId: userID}
		bctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"ListUserBindings", principal, bindMsg)
		if err != nil {
			h.renderGRPCError(w, r, svc+"ListUserBindings", err)
			return
		}
		bindResp, err := h.srv.ListUserBindings(bctx, bindMsg)
		if err != nil {
			h.renderGRPCError(w, r, svc+"ListUserBindings", err)
			return
		}

		// 有效权限（功能允许集 + 数据策略符号谓词）。
		effMsg := &adminv1.GetEffectivePermissionsRequest{AppId: appID, UserId: userID}
		ectx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"GetEffectivePermissions", principal, effMsg)
		if err != nil {
			h.renderGRPCError(w, r, svc+"GetEffectivePermissions", err)
			return
		}
		effResp, err := h.srv.GetEffectivePermissions(ectx, effMsg)
		if err != nil {
			h.renderGRPCError(w, r, svc+"GetEffectivePermissions", err)
			return
		}

		data["Bindings"] = bindResp.Bindings
		data["EffRoles"] = effResp.Roles
		data["Permissions"] = effResp.Permissions
		data["DataPreviews"] = effResp.DataPreviews
		data["Queried"] = true
	}

	h.renderPage(w, r, "effective.html", http.StatusOK, data)
}

// effectiveRedirect：bind/unbind 后回到 effective 页面（保留 user_id），闭环「分配→看能做什么」。
func effectiveRedirect(r *http.Request) string {
	return fmt.Sprintf("/apps/%s/effective?user_id=%s",
		r.PathValue("app_id"), url.QueryEscape(r.FormValue("user_id")))
}

// bindUserOnEffective：复用 doWrite + BindUserRole，重定向回 effective 页。
func (h *Handler) bindUserOnEffective(w http.ResponseWriter, r *http.Request) {
	h.doWrite(w, r, svc+"BindUserRole",
		func(r *http.Request) (proto.Message, error) {
			appID, err := pathUint64(r, "app_id")
			if err != nil {
				return nil, err
			}
			roleID, err := formInt64(r, "role_id")
			if err != nil {
				return nil, err
			}
			return &adminv1.UserRoleRequest{AppId: appID, UserId: r.FormValue("user_id"), RoleId: roleID}, nil
		},
		func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
			return s.BindUserRole(ctx, m.(*adminv1.UserRoleRequest))
		},
		effectiveRedirect)
}

// unbindUserOnEffective：复用 doWrite + UnbindUserRole，重定向回 effective 页。
func (h *Handler) unbindUserOnEffective(w http.ResponseWriter, r *http.Request) {
	h.doWrite(w, r, svc+"UnbindUserRole",
		func(r *http.Request) (proto.Message, error) {
			appID, err := pathUint64(r, "app_id")
			if err != nil {
				return nil, err
			}
			roleID, err := formInt64(r, "role_id")
			if err != nil {
				return nil, err
			}
			return &adminv1.UserRoleRequest{AppId: appID, UserId: r.FormValue("user_id"), RoleId: roleID}, nil
		},
		func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
			return s.UnbindUserRole(ctx, m.(*adminv1.UserRoleRequest))
		},
		effectiveRedirect)
}
