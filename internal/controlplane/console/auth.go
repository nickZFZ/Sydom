package console

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"log/slog"
	"net/http"

	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/mgmt"
	"github.com/nickZFZ/Sydom/internal/controlplane/ssologin"
)

const sessionCookieName = "sydom_console_session"

// secretResolver 是登录验证所需的窄接口（生产由 *adminauthz.OperatorResolver 满足）。
type secretResolver interface {
	ResolveSecret(ctx context.Context, principal string) ([]byte, error)
}

// idpLoginResolver 是发起/回调解析 IdP 登录配置的窄接口（生产由 *ssologin.Resolver 满足）。
type idpLoginResolver interface {
	ResolveIdPByDomain(ctx context.Context, domain string) (ssologin.IdPLogin, bool, error)
	ResolveIdPByTenant(ctx context.Context, tenantID int64) (ssologin.IdPLogin, bool, error)
}

// operatorMatcher 是 email→严格映射 operator + JIT 开通的窄接口（生产由 *ssologin.Resolver 满足）。
type operatorMatcher interface {
	MatchOperatorForLogin(ctx context.Context, tenantID int64, email string) (string, bool, error)
	ProvisionOperatorForLogin(ctx context.Context, tenantID int64, email string) (string, bool, error)
}

// Handler 是 Console BFF 的核心结构，持有所有依赖。
// srv/enf/db 本任务不调用，task4 的 NewHandler 会填。
type Handler struct {
	srv          *mgmt.AdminServer    // task4 用
	enf          *adminauthz.Enforcer // task4 用
	db           *sql.DB              // task4 用
	resolver     secretResolver
	sessions     *RedisStore
	logger       *slog.Logger
	cookieSecure bool
	templates    pageSet

	// M6-sso-2 企业 SSO 登录（可选注入；未装配时 SSO 路由 fail-close）。
	idpResolver    idpLoginResolver
	operatorMatch  operatorMatcher
	oidcHTTP       *http.Client
	consoleBaseURL string
}

func (h *Handler) handleLoginGet(w http.ResponseWriter, r *http.Request) {
	h.renderPage(w, r, "login.html", http.StatusOK, map[string]any{"Error": ""})
}

// handleLoginPost：secret 当密码，常量时间比对；
// 任一失败一律通用「凭据无效」+401（无枚举 oracle）。
func (h *Handler) handleLoginPost(w http.ResponseWriter, r *http.Request) {
	principal := r.FormValue("principal")
	secret := r.FormValue("secret")
	stored, err := h.resolver.ResolveSecret(r.Context(), principal)
	if err != nil || subtle.ConstantTimeCompare([]byte(secret), stored) != 1 {
		h.renderPage(w, r, "login.html", http.StatusUnauthorized, map[string]any{"Error": "凭据无效"})
		return
	}
	id, _, err := h.sessions.Create(r.Context(), principal)
	if err != nil {
		h.renderError(w, r, codeInternal, "会话创建失败", err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.cookieSecure,
		SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil {
		_ = h.sessions.Delete(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   h.cookieSecure,
		SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// lookupSession 查会话但不写任何响应（供 JSON 端点自行决定失败响应格式）。
func (h *Handler) lookupSession(r *http.Request) (Session, bool) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return Session{}, false
	}
	sess, err := h.sessions.Get(r.Context(), c.Value)
	if err != nil {
		return Session{}, false
	}
	return sess, true
}

func (h *Handler) requireSession(w http.ResponseWriter, r *http.Request) (string, Session, bool) {
	sess, ok := h.lookupSession(r)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return "", Session{}, false
	}
	return sess.Principal, sess, true
}

func (h *Handler) checkCSRF(r *http.Request, sess Session) bool {
	return subtle.ConstantTimeCompare([]byte(r.FormValue("csrf_token")), []byte(sess.CSRF)) == 1
}
