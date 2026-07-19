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

// SSODeps 是 Console SSO 登录的注入依赖（run.go 装配；生产实现在 ssologin，持 db+masterKey）。
// 零值=未装配 SSO：SSO 路由 fail-close，principal/secret 登录不受影响。
type SSODeps struct {
	Resolver       idpLoginResolver
	Matcher        operatorMatcher
	HTTPClient     *http.Client
	ConsoleBaseURL string
}

// NewHandler 装配 Console 的 ServeMux（方法感知路由 + 静态文件）。
func NewHandler(srv *mgmt.AdminServer, resolver secretResolver, enf *adminauthz.Enforcer,
	db *sql.DB, sessions *RedisStore, logger *slog.Logger, cookieSecure bool, sso SSODeps) http.Handler {
	h := &Handler{srv: srv, resolver: resolver, enf: enf, db: db,
		sessions: sessions, logger: logger, cookieSecure: cookieSecure, templates: mustTemplates(),
		idpResolver: sso.Resolver, operatorMatch: sso.Matcher,
		oidcHTTP: sso.HTTPClient, consoleBaseURL: sso.ConsoleBaseURL}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /login", h.handleLoginGet)
	mux.HandleFunc("POST /login", h.handleLoginPost)
	mux.HandleFunc("POST /login/sso", h.handleSSOStart)          // M6-sso-2 企业 SSO 发起
	mux.HandleFunc("GET /auth/oidc/callback", h.handleOIDCCallback) // M6-sso-2 OIDC 回调
	mux.HandleFunc("POST /logout", h.handleLogout)
	mux.Handle("GET /static/", http.FileServerFS(staticFS))

	h.registerApps(mux)            // 任务 4/8
	h.registerRBAC(mux)            // 任务 5/6
	h.registerDataPolicy(mux)      // 任务 7
	h.registerPolicyCode(mux)      // M4.1 策略即代码
	h.registerDeveloper(mux)       // M4.4 开发者文档区
	h.registerDataSandbox(mux)     // M4.5 数据权限沙箱专页
	h.registerSystem(mux)          // 任务 9
	h.registerAccounts(mux)        // M1.2 账户层：注册/我的租户/成员
	h.registerUsage(mux)           // M6.1c 租户用量页（消费 GetTenantUsage）
	h.registerIdP(mux)             // M6-sso-4 租户 OIDC IdP 配置页
	h.registerOps(mux)             // M1.4 运营台：业务向人员/业务角色旅程
	h.registerTemplates(mux)       // M3.2 运营台模板库
	h.registerTenantTemplates(mux) // M3.2c-2 运营台「我的模板」（租户自有模板）
	h.registerOnboarding(mux)      // M3.4c 新 app 首次引导向导
	h.registerRoleGraph(mux)       // M3.3 角色全景 + 反事实模拟
	h.registerAudit(mux)           // M2.3 审计页：app appnav tab + admin 系统区
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
	if id := h.sessionID(r); id != "" {
		if err := h.sessions.SetFlash(ctx, id, flashFor(fullMethod)); err != nil {
			h.logger.Warn("console set flash", "err", err) // fail-soft：flash 失败不影响已成功的写
		}
	}
	http.Redirect(w, r, redirectTo(r), http.StatusSeeOther)
}
