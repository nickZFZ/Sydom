package console

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/nickZFZ/Sydom/internal/oidc"
)

const oidcCallbackPath = "/auth/oidc/callback"

// ssoFail 统一通用失败（无枚举 oracle：不区分域未配/验签失败/映射失败）。
func (h *Handler) ssoFail(w http.ResponseWriter, r *http.Request) {
	h.renderPage(w, r, "login.html", http.StatusUnauthorized, map[string]any{"Error": "SSO 登录失败"})
}

// handleSSOStart：email 先行 → 域路由 → discovery → 生成 state/nonce/PKCE → 存一时态 → 302 到 IdP。
func (h *Handler) handleSSOStart(w http.ResponseWriter, r *http.Request) {
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	at := strings.LastIndex(email, "@")
	if at <= 0 || at == len(email)-1 {
		h.ssoFail(w, r)
		return
	}
	if h.consoleBaseURL == "" || h.idpResolver == nil {
		h.ssoFail(w, r) // fail-close：无 redirect_uri 基址 / 未装配 SSO
		return
	}
	idp, ok, err := h.idpResolver.ResolveIdPByDomain(r.Context(), email[at+1:])
	if err != nil || !ok || !idp.Enabled {
		h.ssoFail(w, r)
		return
	}
	pc, err := oidc.Discover(r.Context(), h.oidcHTTP, idp.Issuer)
	if err != nil {
		h.ssoFail(w, r)
		return
	}
	state, err1 := randToken()
	nonce, err2 := randToken()
	verifier, err3 := randToken()
	if err1 != nil || err2 != nil || err3 != nil {
		h.ssoFail(w, r)
		return
	}
	if err := h.sessions.PutOIDCState(r.Context(), state,
		oidcState{Nonce: nonce, Verifier: verifier, TenantID: idp.TenantID, ReturnTo: "/"},
		10*time.Minute); err != nil {
		h.ssoFail(w, r)
		return
	}
	authURL := oidc.AuthCodeURL(pc, idp.ClientID, h.consoleBaseURL+oidcCallbackPath,
		state, nonce, oidc.PKCEChallenge(verifier))
	http.Redirect(w, r, authURL, http.StatusSeeOther)
}

// handleOIDCCallback：state 一次性 → 按一时态 tenantID 重取 IdP → 换 token → 验签 → 严格映射 → 会话。
func (h *Handler) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("error") != "" {
		h.ssoFail(w, r)
		return
	}
	st, ok, err := h.sessions.TakeOIDCState(r.Context(), q.Get("state"))
	if err != nil || !ok {
		h.ssoFail(w, r) // CSRF + 一次性：未知/过期/重放
		return
	}
	if h.consoleBaseURL == "" || h.idpResolver == nil {
		h.ssoFail(w, r)
		return
	}
	idp, ok, err := h.idpResolver.ResolveIdPByTenant(r.Context(), st.TenantID)
	if err != nil || !ok || !idp.Enabled { // 期间被停用→拒
		h.ssoFail(w, r)
		return
	}
	pc, err := oidc.Discover(r.Context(), h.oidcHTTP, idp.Issuer)
	if err != nil {
		h.ssoFail(w, r)
		return
	}
	raw, err := oidc.Exchange(r.Context(), h.oidcHTTP, pc, idp.ClientID, idp.ClientSecret,
		h.consoleBaseURL+oidcCallbackPath, q.Get("code"), st.Verifier)
	if err != nil {
		h.ssoFail(w, r)
		return
	}
	vp := oidc.VerifyParams{Issuer: idp.Issuer, ClientID: idp.ClientID, Nonce: st.Nonce}
	jwks, err := oidc.FetchJWKS(r.Context(), h.oidcHTTP, pc.JWKSURI)
	if err != nil {
		h.ssoFail(w, r)
		return
	}
	claims, err := oidc.VerifyIDToken(raw, jwks, vp, time.Now())
	if errors.Is(err, oidc.ErrUnknownKID) { // kid 未知→刷新 JWKS 重试一次
		if jwks, err = oidc.FetchJWKS(r.Context(), h.oidcHTTP, pc.JWKSURI); err == nil {
			claims, err = oidc.VerifyIDToken(raw, jwks, vp, time.Now())
		}
	}
	if err != nil || !claims.EmailVerified {
		h.ssoFail(w, r)
		return
	}
	email := strings.ToLower(claims.Email)
	principal, ok, err := h.operatorMatch.MatchOperatorForLogin(r.Context(), st.TenantID, email)
	if err != nil {
		h.ssoFail(w, r)
		return
	}
	if !ok {
		// 严格映射未命中：租户显式开 JIT 则尝试自动开通（仅全新 email）。
		if idp.JITEnabled {
			principal, ok, err = h.operatorMatch.ProvisionOperatorForLogin(r.Context(), st.TenantID, email)
		}
		if err != nil || !ok {
			h.ssoFail(w, r) // JIT 关 / 既有 email / 竞态 → 通用 401（无枚举 oracle）
			return
		}
	}
	id, _, err := h.sessions.Create(r.Context(), principal)
	if err != nil {
		h.renderError(w, r, codeInternal, "会话创建失败", err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: id, Path: "/",
		HttpOnly: true, Secure: h.cookieSecure, SameSite: http.SameSiteStrictMode,
	})
	returnTo := st.ReturnTo
	if !isSafeReturnTo(returnTo) {
		returnTo = "/"
	}
	http.Redirect(w, r, returnTo, http.StatusSeeOther)
}

// isSafeReturnTo 仅允许本站相对路径（防开放重定向）。
func isSafeReturnTo(p string) bool {
	return strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "//") && !strings.Contains(p, "\\")
}
