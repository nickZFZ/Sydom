package console

import (
	"context"
	"net/http"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"google.golang.org/protobuf/proto"
)

// registerDataPolicy 注册数据策略读写 + condition 构建器路由。
// 路由段为 data-policies（连字符）；_appnav 的 active 判定用 Tab=="datapolicies"（无连字符）。
func (h *Handler) registerDataPolicy(mux *http.ServeMux) {
	mux.HandleFunc("GET /apps/{app_id}/data-policies", h.listDataPolicies)
	mux.HandleFunc("POST /apps/{app_id}/data-policies", h.upsertDataPolicy)
	mux.HandleFunc("POST /apps/{app_id}/data-policies/{id}/delete", h.deleteDataPolicy)
}

// listDataPolicies：读页内联范式。可选 ?resource= 过滤（""→全部）。
// 注意 Tab 用 "datapolicies"（无连字符）以匹配 _appnav 的 active 判定；
// 路由段/重定向用 "data-policies"（连字符）。
func (h *Handler) listDataPolicies(w http.ResponseWriter, r *http.Request) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	appID, err := pathUint64(r, "app_id")
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListDataPolicies", err)
		return
	}
	msg := &adminv1.ListDataPoliciesRequest{AppId: appID, Resource: r.FormValue("resource")}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, svc+"ListDataPolicies", principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListDataPolicies", err)
		return
	}
	resp, err := h.srv.ListDataPolicies(ctx, msg)
	if err != nil {
		h.renderGRPCError(w, r, svc+"ListDataPolicies", err)
		return
	}
	h.renderPage(w, r, "datapolicies.html", http.StatusOK, map[string]any{
		"Nav": "apps", "AppID": appID, "Tab": "datapolicies", "DataPolicies": resp.DataPolicies, "CSRF": sess.CSRF})
}

// upsertDataPolicy：写动作走 doWrite。id=0 即插入；condition 原样透传（绝不预解析/校验）。
func (h *Handler) upsertDataPolicy(w http.ResponseWriter, r *http.Request) {
	h.doWrite(w, r, svc+"UpsertDataPolicy",
		func(r *http.Request) (proto.Message, error) {
			appID, err := pathUint64(r, "app_id")
			if err != nil {
				return nil, err
			}
			id, err := formInt64(r, "id")
			if err != nil {
				return nil, err
			}
			return &adminv1.UpsertDataPolicyRequest{
				AppId:       appID,
				Id:          id,
				SubjectType: r.FormValue("subject_type"),
				SubjectId:   r.FormValue("subject_id"),
				Resource:    r.FormValue("resource"),
				Condition:   r.FormValue("condition"), // 原始 JSON 串：后端 fail-close
				Effect:      r.FormValue("effect"),
			}, nil
		},
		func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
			return s.UpsertDataPolicy(ctx, m.(*adminv1.UpsertDataPolicyRequest))
		},
		appListRedirect("data-policies"))
}

// deleteDataPolicy：写动作走 doWrite。app_id 先解码（错则直接返回），再取 path 的 id。
func (h *Handler) deleteDataPolicy(w http.ResponseWriter, r *http.Request) {
	h.doWrite(w, r, svc+"DeleteDataPolicy",
		func(r *http.Request) (proto.Message, error) {
			appID, err := pathUint64(r, "app_id")
			if err != nil {
				return nil, err
			}
			dpID, err := pathInt64(r, "id")
			return &adminv1.DeleteDataPolicyRequest{AppId: appID, DataPolicyId: dpID}, err
		},
		func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
			return s.DeleteDataPolicy(ctx, m.(*adminv1.DeleteDataPolicyRequest))
		},
		appListRedirect("data-policies"))
}
