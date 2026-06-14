package console

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"

	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

// NewHandler 装配 Console 的 ServeMux（方法感知路由 + 静态文件）。
func NewHandler(srv *mgmt.AdminServer, resolver secretResolver, enf *adminauthz.Enforcer,
	db *sql.DB, sessions *RedisStore, logger *slog.Logger, cookieSecure bool) http.Handler {
	h := &Handler{srv: srv, resolver: resolver, enf: enf, db: db,
		sessions: sessions, logger: logger, cookieSecure: cookieSecure, templates: mustTemplates()}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /login", h.handleLoginGet)
	mux.HandleFunc("POST /login", h.handleLoginPost)
	mux.HandleFunc("POST /logout", h.handleLogout)
	mux.Handle("GET /static/", http.FileServerFS(staticFS))

	h.registerApps(mux)       // 任务 4/8
	h.registerRBAC(mux)       // 任务 5/6
	h.registerDataPolicy(mux) // 任务 7
	h.registerSystem(mux)     // 任务 9
	h.registerAccounts(mux)   // M1.2 账户层：注册/我的租户/成员
	h.registerOps(mux)        // M1.4 运营台：业务向人员/业务角色旅程
	return mux
}

// doWrite 是写动作的共享管线（POST）：
// 会话 → CSRF → 解码 → 授权 → status 写闸 → 直调 → PRG 重定向。
//
// 管线顺序铁律：认证 → 授权 → status 闸；status 闸绝不能在 authz 之前
// （否则会泄露 app 是否存在）。每个写动作必过 CSRF。
func (h *Handler) doWrite(w http.ResponseWriter, r *http.Request, fullMethod string,
	decode func(*http.Request) (proto.Message, error),
	invoke func(context.Context, *mgmt.AdminServer, proto.Message) (proto.Message, error),
	redirectTo func(*http.Request) string) {
	principal, sess, ok := h.requireSession(w, r)
	if !ok {
		return
	}
	if !h.checkCSRF(r, sess) {
		h.renderError(w, r, codes.PermissionDenied, "CSRF 校验失败", nil)
		return
	}
	msg, err := decode(r)
	if err != nil {
		h.renderGRPCError(w, r, fullMethod, err)
		return
	}
	ctx, err := mgmt.AuthorizeRule(r.Context(), h.enf, fullMethod, principal, msg)
	if err != nil {
		h.renderGRPCError(w, r, fullMethod, err)
		return
	}
	if err := mgmt.CheckStatusWrite(ctx, h.db, fullMethod, msg); err != nil {
		h.renderGRPCError(w, r, fullMethod, err)
		return
	}
	if _, err := invoke(ctx, h.srv, msg); err != nil {
		h.renderGRPCError(w, r, fullMethod, err)
		return
	}
	http.Redirect(w, r, redirectTo(r), http.StatusSeeOther)
}
