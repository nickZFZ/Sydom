package console

import (
	"context"
	"encoding/json"
	"net/http"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/sidecar/dataperm"
	"google.golang.org/protobuf/proto"
)

// registerDataPolicy 注册数据策略读写 + condition 构建器路由。
// 路由段为 data-policies（连字符）；_appnav 的 active 判定用 Tab=="datapolicies"（无连字符）。
func (h *Handler) registerDataPolicy(mux *http.ServeMux) {
	mux.HandleFunc("GET /apps/{app_id}/data-policies", h.listDataPolicies)
	mux.HandleFunc("POST /apps/{app_id}/data-policies", h.upsertDataPolicy)
	mux.HandleFunc("POST /apps/{app_id}/data-policies/{id}/delete", h.deleteDataPolicy)
	mux.HandleFunc("POST /apps/{app_id}/data-policies/batch-delete", h.batchDeleteDataPolicy) // 任务5：多选批量删除数据策略
	mux.HandleFunc("POST /apps/{app_id}/data-policies/preview-condition", h.previewCondition) // 任务4：条件构建器服务端谓词预览
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
	msg := &adminv1.ListDataPoliciesRequest{AppId: appID, Resource: r.FormValue("resource"), Page: listPageFromReq(r)}
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
		"Nav": "apps", "AppID": appID, "Tab": "datapolicies", "DataPolicies": resp.DataPolicies,
		"CSRF": sess.CSRF, "Pager": pagerData(r, resp.Total)})
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
				Description: r.FormValue("description"),
			}, nil
		},
		func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
			return s.UpsertDataPolicy(ctx, m.(*adminv1.UpsertDataPolicyRequest))
		},
		appListRedirect("data-policies"))
}

// deleteDataPolicy：写动作走 doWrite。app_id 先解码（错则直接返回），再取 path 的 id。
func (h *Handler) deleteDataPolicy(w http.ResponseWriter, r *http.Request) {
	if !h.requireConfirm(w, r, svc+"DeleteDataPolicy") {
		return
	}
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

// batchDeleteDataPolicy：多选批量删除数据策略。requireConfirm 二次确认门 + doWrite PRG
// （同构 routes_rbac.go 的 batchDeleteRole；parseInt64s 定义于该文件，同包直接复用）。
func (h *Handler) batchDeleteDataPolicy(w http.ResponseWriter, r *http.Request) {
	if !h.requireConfirm(w, r, svc+"BatchDeleteDataPolicy") {
		return
	}
	h.doWrite(w, r, svc+"BatchDeleteDataPolicy",
		func(r *http.Request) (proto.Message, error) {
			appID, err := pathUint64(r, "app_id")
			if err != nil {
				return nil, err
			}
			ids := parseInt64s(r.PostForm["ids"])
			if len(ids) == 0 {
				return nil, errNoSelection
			}
			return &adminv1.BatchDeleteDataPolicyRequest{AppId: appID, DataPolicyIds: ids}, nil
		},
		func(ctx context.Context, s *mgmt.AdminServer, m proto.Message) (proto.Message, error) {
			return s.BatchDeleteDataPolicy(ctx, m.(*adminv1.BatchDeleteDataPolicyRequest))
		},
		appListRedirect("data-policies"))
}

// conditionPreviewResp 是预览端点的 JSON 契约：成功填 Predicate，任何失败（鉴权/校验）填 Error。
// 具名类型固化契约，供任务5 前端 fetch → resp.json() 消费（omitempty 保证互斥字段只出现一个）。
type conditionPreviewResp struct {
	Predicate string `json:"predicate,omitempty"`
	Error     string `json:"error,omitempty"`
}

// previewCondition 服务端渲染条件的符号谓词预览（幂等只读，单一真相源：复用 dataperm 校验 +
// conditionPredicate 渲染）。只解析请求体提交的条件 JSON，不读任何 app 数据、不泄露 app
// 存在性，故会话+CSRF 鉴权足够（无对应 RPC 方法，不施加 AuthorizeRule）。
// 本端点被前端 JS fetch → resp.json() 消费，故所有分支必须返回 JSON（Content-Type 顶部设一次）：
// 鉴权失败 401/403 JSON，校验成功/失败恒 200（校验错误是业务结果内联展示，非服务器错误）；
// 不落库/不 bump/不写审计。绝不用 requireSession/renderError（会重定向/回 HTML，破坏 JSON 契约）。
func (h *Handler) previewCondition(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	sess, ok := h.lookupSession(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(conditionPreviewResp{Error: "会话已过期，请刷新页面后重试"})
		return
	}
	if !h.checkCSRF(r, sess) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(conditionPreviewResp{Error: "CSRF 校验失败，请刷新页面后重试"})
		return
	}
	cond := r.FormValue("condition")
	var resp conditionPreviewResp
	if err := dataperm.ValidateCondition(cond); err != nil {
		resp.Error = err.Error()
	} else {
		resp.Predicate = conditionPredicate(cond)
	}
	_ = json.NewEncoder(w).Encode(resp)
}
