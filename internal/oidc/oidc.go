// Package oidc 实现纯 stdlib 手写 OIDC Relying Party 原语：
// discovery / auth-code URL / token 交换 / JWKS 解析 / ID Token 验签。
// 无外部依赖、无隐式全局状态；HTTP 客户端与时钟均注入，完全离线可测。
package oidc

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ProviderConfig 是 discovery 得到的端点集合。
type ProviderConfig struct {
	Issuer, AuthorizationEndpoint, TokenEndpoint, JWKSURI string
}

// Discover 拉取 issuer 的 openid-configuration，校验 issuer 字段防 mix-up。
func Discover(ctx context.Context, hc *http.Client, issuer string) (ProviderConfig, error) {
	u := strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return ProviderConfig{}, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return ProviderConfig{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ProviderConfig{}, fmt.Errorf("oidc: discovery status %d", resp.StatusCode)
	}
	var doc struct {
		Issuer                string `json:"issuer"`
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
		JWKSURI               string `json:"jwks_uri"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc); err != nil {
		return ProviderConfig{}, fmt.Errorf("oidc: discovery decode: %w", err)
	}
	if doc.Issuer != issuer {
		return ProviderConfig{}, fmt.Errorf("oidc: discovery issuer mismatch")
	}
	if doc.AuthorizationEndpoint == "" || doc.TokenEndpoint == "" || doc.JWKSURI == "" {
		return ProviderConfig{}, fmt.Errorf("oidc: discovery missing endpoints")
	}
	return ProviderConfig{
		Issuer: doc.Issuer, AuthorizationEndpoint: doc.AuthorizationEndpoint,
		TokenEndpoint: doc.TokenEndpoint, JWKSURI: doc.JWKSURI,
	}, nil
}

// AuthCodeURL 构造授权码流跳转 URL（PKCE S256）。
func AuthCodeURL(p ProviderConfig, clientID, redirectURI, state, nonce, codeChallenge string) string {
	v := url.Values{}
	v.Set("response_type", "code")
	v.Set("scope", "openid email")
	v.Set("client_id", clientID)
	v.Set("redirect_uri", redirectURI)
	v.Set("state", state)
	v.Set("nonce", nonce)
	v.Set("code_challenge", codeChallenge)
	v.Set("code_challenge_method", "S256")
	sep := "?"
	if strings.Contains(p.AuthorizationEndpoint, "?") {
		sep = "&"
	}
	return p.AuthorizationEndpoint + sep + v.Encode()
}

// PKCEChallenge 返回 base64url(sha256(verifier))。
func PKCEChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Exchange 用授权码换 token，取 id_token。客户端认证=client_secret_basic。
func Exchange(ctx context.Context, hc *http.Client, p ProviderConfig,
	clientID, clientSecret, redirectURI, code, codeVerifier string) (string, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("code_verifier", codeVerifier)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(clientID, clientSecret)
	resp, err := hc.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("oidc: token status %d", resp.StatusCode)
	}
	var tok struct {
		IDToken string `json:"id_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tok); err != nil {
		return "", fmt.Errorf("oidc: token decode: %w", err)
	}
	if tok.IDToken == "" {
		return "", fmt.Errorf("oidc: token response missing id_token")
	}
	return tok.IDToken, nil
}
