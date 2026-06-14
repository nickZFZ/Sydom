package console

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// registerOps 注册运营台路由（URL 前缀 /ops/）。
// 业务向界面：隐藏技术原语，给非技术业务管理员看「某人能做什么」。
// 业务角色页（任务 8）路由将在此追加。
func (h *Handler) registerOps(mux *http.ServeMux) {
	mux.HandleFunc("GET /ops/apps/{app_id}/people", h.opsPeople)
	mux.HandleFunc("GET /ops/apps/{app_id}/people/view", h.opsPersonView)
	mux.HandleFunc("GET /ops/apps/{app_id}/roles", h.opsRoles)
	mux.HandleFunc("GET /ops/apps/{app_id}/roles/new", h.opsRoleNewForm)
	mux.HandleFunc("POST /ops/apps/{app_id}/roles", h.opsCreateRole)
	mux.HandleFunc("POST /ops/apps/{app_id}/people/assign", h.opsAssignRole)
	mux.HandleFunc("POST /ops/apps/{app_id}/people/unassign", h.opsUnassignRole)
}

// ---- 业务语言映射辅助 ----

// capName 是 (resource, action) → 业务名称 映射。
// 缺 name 时，label() 回退 resource:action（设计内最深回退，不泄露其他技术原语）。
type capName map[[2]string]string

// permNameMap 调 ListPermissions 建 (resource,action)→name 映射。
// 鉴权由 AuthorizeRule 完成；失败直接返回 error（降级无枚举）。
func (h *Handler) permNameMap(ctx context.Context, principal string, appID uint64) (capName, error) {
	msg := &adminv1.ListPermissionsRequest{AppId: appID}
	actx, err := mgmt.AuthorizeRule(ctx, h.enf, svc+"ListPermissions", principal, msg)
	if err != nil {
		return nil, err
	}
	resp, err := h.srv.ListPermissions(actx, msg)
	if err != nil {
		return nil, err
	}
	m := capName{}
	for _, p := range resp.Permissions {
		if p.Name != "" {
			m[[2]string{p.Resource, p.Action}] = p.Name
		}
	}
	return m, nil
}

func (m capName) label(resource, action string) string {
	if n, ok := m[[2]string{resource, action}]; ok {
		return n
	}
	return resource + ":" + action
}

// roleNameMap 调 ListRoles 建 code→name 映射（显示业务角色名）。
// eff.Roles 返回 casbin 角色码（code），需映射到展示名。
func (h *Handler) roleNameMap(ctx context.Context, principal string, appID uint64) (map[string]string, error) {
	msg := &adminv1.ListRolesRequest{AppId: appID}
	actx, err := mgmt.AuthorizeRule(ctx, h.enf, svc+"ListRoles", principal, msg)
	if err != nil {
		return nil, err
	}
	resp, err := h.srv.ListRoles(actx, msg)
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	for _, r := range resp.Roles {
		if r.Name != "" {
			m[r.Code] = r.Name // 无 name 不入 map；roleName() 统一回退到 code（单一回退点）
		}
	}
	return m, nil
}

// roleName 从 map 取显示名，缺省返回 code 自身（不回退到技术 id）。
func roleName(m map[string]string, code string) string {
	if n, ok := m[code]; ok {
		return n
	}
	return code
}

// ---- 人员列表页 ----

// opsPeople：GET /ops/apps/{app_id}/people
// 列出所有绑定用户（去重），链到 /ops/apps/{id}/people/view?user_id=xxx。
func (h *Handler) opsPeople(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	appID, err := pathUint64(r, "app_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListUserBindings", err)
		return
	}
	msg := &adminv1.ListUserBindingsRequest{AppId: appID}
	actx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"ListUserBindings", principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListUserBindings", err)
		return
	}
	resp, err := h.srv.ListUserBindings(actx, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListUserBindings", err)
		return
	}
	seen := map[string]bool{}
	var people []string
	for _, b := range resp.Bindings {
		if !seen[b.UserId] {
			seen[b.UserId] = true
			people = append(people, b.UserId)
		}
	}
	h.renderPage(w, r, "ops_people.html", http.StatusOK, map[string]any{
		"AppID":  appID,
		"People": people,
		"CSRF":   sess.CSRF,
		"OpsNav": "people",
	})
}

// ---- 人员详情页 ----

// capView 是渲染用的单条能力行（只含业务名，绝不含原语）。
type capView struct{ Capability string }

// opsPersonView：GET /ops/apps/{app_id}/people/view?user_id=xxx
// 展示某人的业务角色名、能力（权限点 name）、数据范围简记（业务说明）。
// 鉴权：ListPermissions(→capName) + ListRoles(→roleNames) + GetEffectivePermissions + ListUserBindings；
// 任一失败走 renderGRPCError（降级无枚举）。
func (h *Handler) opsPersonView(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	appID, err := pathUint64(r, "app_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"GetEffectivePermissions", err)
		return
	}
	userID := r.FormValue("user_id")

	// 业务名称映射（鉴权在内部完成，失败直接 error）。
	caps, err := h.permNameMap(r.Context(), principal, appID)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListPermissions", err)
		return
	}
	roleNames, err := h.roleNameMap(r.Context(), principal, appID)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListRoles", err)
		return
	}

	data := map[string]any{
		"AppID":  appID,
		"UserID": userID,
		"CSRF":   sess.CSRF,
		"OpsNav": "people",
	}

	if userID != "" {
		// 有效权限（功能允许集，含隐式角色闭包）。
		effMsg := &adminv1.GetEffectivePermissionsRequest{AppId: appID, UserId: userID}
		ectx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"GetEffectivePermissions", principal, effMsg)
		if err != nil {
			h.renderGRPCError(w, r, svc+"GetEffectivePermissions", err)
			return
		}
		eff, err := h.srv.GetEffectivePermissions(ectx, effMsg)
		if err != nil {
			h.renderGRPCError(w, r, svc+"GetEffectivePermissions", err)
			return
		}

		// 能力列表：原语 → 业务名。
		var capRows []capView
		for _, p := range eff.Permissions {
			capRows = append(capRows, capView{Capability: caps.label(p.Resource, p.Action)})
		}

		// 数据范围简记（业务说明，不回退原始谓词）。
		notes := h.dataScopeNotes(r.Context(), principal, appID, userID, eff.Roles)

		// 角色名列表（code → 业务名）。
		var roleDisplayNames []string
		for _, code := range eff.Roles {
			roleDisplayNames = append(roleDisplayNames, roleName(roleNames, code))
		}

		// 当前绑定（供运营台显示用户属于哪些角色）。
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

		data["Queried"] = true
		data["RoleNames"] = roleDisplayNames // 业务角色名列表（已映射）
		data["Capabilities"] = capRows
		data["DataNotes"] = notes
		data["Bindings"] = bindResp.Bindings
	}
	h.renderPage(w, r, "ops_person.html", http.StatusOK, data)
}

// ---- 数据范围简记 ----

// dataScopeNote 是渲染用的数据范围业务简记行（resource + 业务说明，无原始谓词）。
type dataScopeNote struct {
	Resource string
	Note     string
}

// ---- 业务角色页 ----

// opsRoles：GET /ops/apps/{app_id}/roles
// 列出业务角色（只渲染业务名，绝不渲染 code/role_id）。
func (h *Handler) opsRoles(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	appID, err := pathUint64(r, "app_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListRoles", err)
		return
	}
	msg := &adminv1.ListRolesRequest{AppId: appID}
	actx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"ListRoles", principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListRoles", err)
		return
	}
	resp, err := h.srv.ListRoles(actx, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListRoles", err)
		return
	}
	h.renderPage(w, r, "ops_roles.html", http.StatusOK, map[string]any{
		"AppID": appID, "Roles": resp.Roles, "CSRF": sess.CSRF, "OpsNav": "roles",
	})
}

// opsRoleNewForm：GET /ops/apps/{app_id}/roles/new
// 新建业务角色表单（名称 + 能力复选框，展示权限点 name，隐藏技术原语）。
func (h *Handler) opsRoleNewForm(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	appID, err := pathUint64(r, "app_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListPermissions", err)
		return
	}
	msg := &adminv1.ListPermissionsRequest{AppId: appID}
	actx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"ListPermissions", principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListPermissions", err)
		return
	}
	resp, err := h.srv.ListPermissions(actx, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListPermissions", err)
		return
	}
	h.renderPage(w, r, "ops_role_new.html", http.StatusOK, map[string]any{
		"AppID": appID, "Permissions": resp.Permissions, "CSRF": sess.CSRF, "OpsNav": "roles",
	})
}

// opsCreateRole：POST /ops/apps/{app_id}/roles
// 建业务角色（doWrite + CreateBusinessRole 原子：建角色+批量授权）。
// code 由后端生成；前端只传 name + permission_ids。
func (h *Handler) opsCreateRole(w http.ResponseWriter, r *http.Request) {
	h.doWrite(w, r, svc+"CreateBusinessRole",
		func(r *http.Request) (proto.Message, error) {
			appID, err := pathUint64(r, "app_id")
			if err != nil {
				return nil, err
			}
			var permIDs []int64
			for _, s := range r.Form["permission_ids"] {
				id, err := strconv.ParseInt(s, 10, 64)
				if err != nil {
					return nil, status.Errorf(codes.InvalidArgument, "invalid permission_id %q", s)
				}
				permIDs = append(permIDs, id)
			}
			return &adminv1.CreateBusinessRoleRequest{
				AppId:         appID,
				Name:          r.FormValue("name"),
				PermissionIds: permIDs,
			}, nil
		},
		func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
			return s.CreateBusinessRole(ctx, m.(*adminv1.CreateBusinessRoleRequest))
		},
		func(r *http.Request) string {
			return fmt.Sprintf("/ops/apps/%s/roles", r.PathValue("app_id"))
		})
}

// opsAssignRole：POST /ops/apps/{app_id}/people/assign
// 分配业务角色给用户（doWrite + BindUserRole，复用 decodeUserRoleRequest），
// 成功后重定向回人员视图形成闭环。
func (h *Handler) opsAssignRole(w http.ResponseWriter, r *http.Request) {
	h.doWrite(w, r, svc+"BindUserRole",
		decodeUserRoleRequest,
		func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
			return s.BindUserRole(ctx, m.(*adminv1.UserRoleRequest))
		},
		opsPersonRedirect)
}

// opsUnassignRole：POST /ops/apps/{app_id}/people/unassign
// 移除业务角色（doWrite + UnbindUserRole），成功后重定向回人员视图。
func (h *Handler) opsUnassignRole(w http.ResponseWriter, r *http.Request) {
	h.doWrite(w, r, svc+"UnbindUserRole",
		decodeUserRoleRequest,
		func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
			return s.UnbindUserRole(ctx, m.(*adminv1.UserRoleRequest))
		},
		opsPersonRedirect)
}

// opsPersonRedirect 生成 /ops/apps/{id}/people/view?user_id=xxx 重定向 URL。
// path 权威（app_id 取自 path），user_id 取自 form（经 URL 编码防注入）。
func opsPersonRedirect(r *http.Request) string {
	return fmt.Sprintf("/ops/apps/%s/people/view?user_id=%s",
		r.PathValue("app_id"), url.QueryEscape(r.FormValue("user_id")))
}

// dataScopeNotes 查 ListDataPolicies，筛选适用于本人（subject=user:userID 或其隐式角色 code）
// 的数据策略，按 resource 聚合业务说明（Description 字段，缺省"受限范围（详见建模台）"）。
// 绝不回退到原始谓词（Condition），以保证运营台无技术原语。
// 任一鉴权/查询失败 → 静默返回 nil（数据范围为可选展示项，不阻塞主渲染）。
func (h *Handler) dataScopeNotes(ctx context.Context, principal string, appID uint64, userID string, roles []string) []dataScopeNote {
	msg := &adminv1.ListDataPoliciesRequest{AppId: appID}
	actx, err := mgmt.AuthorizeRule(ctx, h.enf, svc+"ListDataPolicies", principal, msg)
	if err != nil {
		return nil
	}
	resp, err := h.srv.ListDataPolicies(actx, msg)
	if err != nil {
		return nil
	}
	roleSet := map[string]bool{}
	for _, rr := range roles {
		roleSet[rr] = true
	}
	byRes := map[string][]string{}
	var order []string
	for _, dp := range resp.DataPolicies {
		applies := (dp.SubjectType == "user" && dp.SubjectId == userID) ||
			(dp.SubjectType == "role" && roleSet[dp.SubjectId])
		if !applies {
			continue
		}
		note := dp.Description
		if note == "" {
			note = "受限范围（详见建模台）"
		}
		if _, ok := byRes[dp.Resource]; !ok {
			order = append(order, dp.Resource)
		}
		byRes[dp.Resource] = append(byRes[dp.Resource], note)
	}
	var out []dataScopeNote
	for _, res := range order {
		joined := ""
		for i, n := range byRes[res] {
			if i > 0 {
				joined += "；"
			}
			joined += n
		}
		out = append(out, dataScopeNote{Resource: res, Note: joined})
	}
	return out
}
