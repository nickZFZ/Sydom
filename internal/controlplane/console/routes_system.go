package console

import (
	"context"
	"net/http"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

// registerSystem 注册系统域 operators/admin-roles 路由（顶层，Nav:"system"，不在 /apps 之下）。
// 系统页授权域为 "*"（ruleTable 标 system=true），故只有 root@sydom 等持 * 域授权者可见。
// 红线：系统读页（listOperators/listAdminRoles）拒绝即 403，绝不降级——降级会泄露系统页存在性。
func (h *Handler) registerSystem(mux *http.ServeMux) {
	mux.HandleFunc("GET /operators", h.listOperators)
	mux.HandleFunc("GET /operators/new", h.operatorNewForm)
	mux.HandleFunc("POST /operators", h.createOperator)
	mux.HandleFunc("POST /operators/{operator_id}/status", h.setOperatorStatus)
	mux.HandleFunc("POST /operators/{operator_id}/roles", h.bindOperatorRole)
	mux.HandleFunc("GET /admin-roles", h.listAdminRoles)
	mux.HandleFunc("POST /admin-roles", h.createAdminRole)
	mux.HandleFunc("POST /admin-roles/{role_id}/grants", h.grantAdminRole)
}

// listOperators：系统读页内联范式（会话 → 授权 → 直调 → 渲染）。
// 与仪表盘不同：拒绝即 403（renderGRPCError），绝不降级——系统页不得经降级泄露。
func (h *Handler) listOperators(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	const fm = svc + "ListOperators"
	msg := &adminv1.ListOperatorsRequest{}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, fm, principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, fm, err) // PermissionDenied → 403，绝不降级
		return
	}
	resp, err := h.srv.ListOperators(ctx, msg)
	if err != nil {
		h.renderGRPCError(w, r, fm, err)
		return
	}
	h.renderPage(w, r, "operators.html", http.StatusOK, map[string]any{
		"Nav": "system", "Operators": resp.Operators, "CSRF": sess.CSRF})
}

// operatorNewForm：建操作员表单页（仅需会话；授权延后到 POST）。
func (h *Handler) operatorNewForm(w http.ResponseWriter, r *http.Request) {
	_, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	h.renderPage(w, r, "operator_new.html", http.StatusOK, map[string]any{"Nav": "system", "CSRF": sess.CSRF})
}

// createOperator：建操作员走「一次性 secret」专管线——不经 doWrite，绝不 PRG。
// 后端仅此一次返回明文 secret，故必须直接渲染 operator_created.html 当场展示；
// 一旦 PRG 重定向就会丢失。该明文密钥绝不日志、绝不落盘。
// 管线顺序：会话 → CSRF → 授权 → 直调 → 渲染。
func (h *Handler) createOperator(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if !h.checkCSRF(r, sess) {
		h.renderError(w, r, codes.PermissionDenied, "CSRF 校验失败", nil)
		return
	}
	const fm = svc + "CreateOperator"
	msg := &adminv1.CreateOperatorRequest{Principal: r.FormValue("principal")}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, fm, principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, fm, err)
		return
	}
	resp, err := h.srv.CreateOperator(ctx, msg)
	if err != nil {
		h.renderGRPCError(w, r, fm, err)
		return
	}
	h.renderPage(w, r, "operator_created.html", http.StatusOK, map[string]any{
		"Nav": "system", "OperatorID": resp.OperatorId, "Secret": resp.Secret}) // 一次性展示，绝不日志/落盘
}

// setOperatorStatus：状态切换走 doWrite。operator_id 取自 path（权威），status ∈ {1=启用, 2=停用}。
func (h *Handler) setOperatorStatus(w http.ResponseWriter, r *http.Request) {
	h.doWrite(w, r, svc+"SetOperatorStatus",
		func(r *http.Request) (proto.Message, error) {
			opID, err := pathInt64(r, "operator_id")
			if err != nil {
				return nil, err
			}
			st, err := formInt64(r, "status")
			if err != nil {
				return nil, err
			}
			return &adminv1.SetOperatorStatusRequest{OperatorId: opID, Status: uint32(st)}, nil
		},
		func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
			return s.SetOperatorStatus(ctx, m.(*adminv1.SetOperatorStatusRequest))
		},
		func(*http.Request) string { return "/operators" })
}

// bindOperatorRole：绑角色走 doWrite。operator_id 取自 path（权威），role_id/domain 取表单。
func (h *Handler) bindOperatorRole(w http.ResponseWriter, r *http.Request) {
	h.doWrite(w, r, svc+"BindOperatorRole",
		func(r *http.Request) (proto.Message, error) {
			opID, err := pathInt64(r, "operator_id")
			if err != nil {
				return nil, err
			}
			roleID, err := formInt64(r, "role_id")
			if err != nil {
				return nil, err
			}
			return &adminv1.BindOperatorRoleRequest{OperatorId: opID, RoleId: roleID, Domain: r.FormValue("domain")}, nil
		},
		func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
			return s.BindOperatorRole(ctx, m.(*adminv1.BindOperatorRoleRequest))
		},
		func(*http.Request) string { return "/operators" })
}

// listAdminRoles：系统读页内联范式（同 listOperators）。拒绝即 403，绝不降级。
func (h *Handler) listAdminRoles(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	const fm = svc + "ListAdminRoles"
	msg := &adminv1.ListAdminRolesRequest{}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, fm, principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, fm, err)
		return
	}
	resp, err := h.srv.ListAdminRoles(ctx, msg)
	if err != nil {
		h.renderGRPCError(w, r, fm, err)
		return
	}
	h.renderPage(w, r, "admin_roles.html", http.StatusOK, map[string]any{
		"Nav": "system", "Roles": resp.Roles, "CSRF": sess.CSRF})
}

// createAdminRole：建管理角色走 doWrite（无一次性 secret，故走 PRG）。
func (h *Handler) createAdminRole(w http.ResponseWriter, r *http.Request) {
	h.doWrite(w, r, svc+"CreateAdminRole",
		func(r *http.Request) (proto.Message, error) {
			return &adminv1.CreateAdminRoleRequest{Code: r.FormValue("code"), Name: r.FormValue("name")}, nil
		},
		func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
			return s.CreateAdminRole(ctx, m.(*adminv1.CreateAdminRoleRequest))
		},
		func(*http.Request) string { return "/admin-roles" })
}

// grantAdminRole：管理角色授权走 doWrite。role_id 取自 path（权威），domain/resource/action 取表单。
func (h *Handler) grantAdminRole(w http.ResponseWriter, r *http.Request) {
	h.doWrite(w, r, svc+"GrantAdminRole",
		func(r *http.Request) (proto.Message, error) {
			roleID, err := pathInt64(r, "role_id")
			if err != nil {
				return nil, err
			}
			return &adminv1.GrantAdminRoleRequest{
				RoleId:   roleID,
				Domain:   r.FormValue("domain"),
				Resource: r.FormValue("resource"),
				Action:   r.FormValue("action"),
			}, nil
		},
		func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
			return s.GrantAdminRole(ctx, m.(*adminv1.GrantAdminRoleRequest))
		},
		func(*http.Request) string { return "/admin-roles" })
}
