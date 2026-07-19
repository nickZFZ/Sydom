package oidc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDiscover_IssuerMismatchRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"issuer":"https://evil","authorization_endpoint":"a","token_endpoint":"t","jwks_uri":"j"}`))
	}))
	defer srv.Close()
	_, err := Discover(context.Background(), srv.Client(), srv.URL)
	require.Error(t, err, "issuer 字段与请求不符须拒（防 mix-up）")
}

func TestDiscover_Good(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.True(t, strings.HasSuffix(r.URL.Path, "/.well-known/openid-configuration"))
		_, _ = w.Write([]byte(`{"issuer":"` + srv.URL + `","authorization_endpoint":"` + srv.URL + `/auth","token_endpoint":"` + srv.URL + `/token","jwks_uri":"` + srv.URL + `/jwks"}`))
	}))
	defer srv.Close()
	pc, err := Discover(context.Background(), srv.Client(), srv.URL)
	require.NoError(t, err)
	require.Equal(t, srv.URL+"/token", pc.TokenEndpoint)
}

func TestExchange_BasicAuthAndIDToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, pw, ok := r.BasicAuth()
		require.True(t, ok)
		require.Equal(t, "cid", u)
		require.Equal(t, "sec", pw)
		require.NoError(t, r.ParseForm())
		require.Equal(t, "authorization_code", r.Form.Get("grant_type"))
		require.Equal(t, "the-code", r.Form.Get("code"))
		require.Equal(t, "v3rifier", r.Form.Get("code_verifier"))
		_, _ = w.Write([]byte(`{"id_token":"HEADER.PAYLOAD.SIG"}`))
	}))
	defer srv.Close()
	raw, err := Exchange(context.Background(), srv.Client(),
		ProviderConfig{TokenEndpoint: srv.URL}, "cid", "sec", "https://rp/cb", "the-code", "v3rifier")
	require.NoError(t, err)
	require.Equal(t, "HEADER.PAYLOAD.SIG", raw)
}

func TestAuthCodeURL_Fields(t *testing.T) {
	got := AuthCodeURL(ProviderConfig{AuthorizationEndpoint: "https://idp/auth"},
		"cid", "https://rp/cb", "st", "no", PKCEChallenge("v"))
	for _, want := range []string{"response_type=code", "scope=openid+email", "client_id=cid",
		"code_challenge_method=S256", "state=st", "nonce=no"} {
		require.Contains(t, got, want)
	}
}

func TestFetchJWKS_ParsesRSA(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// n/e 为任意合法 base64url；仅验证 FetchJWKS→ParseJWKS 贯通、kid 可寻址。
		_, _ = w.Write([]byte(`{"keys":[{"kty":"RSA","kid":"k1","n":"AQAB","e":"AQAB"}]}`))
	}))
	defer srv.Close()
	ks, err := FetchJWKS(context.Background(), srv.Client(), srv.URL)
	require.NoError(t, err)
	_, ok := ks.keys["k1"]
	require.True(t, ok)
}
