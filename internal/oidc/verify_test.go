package oidc

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// signRS256 用 key 签一份 JWT（header{alg,kid} + payload），返回紧凑串。alg 可覆盖以测负路径。
func signRS256(t *testing.T, key *rsa.PrivateKey, kid, alg string, claims map[string]any) string {
	t.Helper()
	hdr := map[string]any{"alg": alg, "typ": "JWT"}
	if kid != "" {
		hdr["kid"] = kid
	}
	enc := func(v any) string {
		b, err := json.Marshal(v)
		require.NoError(t, err)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	signingInput := enc(hdr) + "." + enc(claims)
	if alg == "none" {
		return signingInput + "."
	}
	sum := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	require.NoError(t, err)
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// jwksFor 构造只含一把 RSA 公钥（kid）的 JWKS。
func jwksFor(t *testing.T, kid string, pub *rsa.PublicKey) JWKS {
	t.Helper()
	n := base64.RawURLEncoding.EncodeToString(pub.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes())
	doc := map[string]any{"keys": []map[string]any{{"kty": "RSA", "kid": kid, "n": n, "e": e}}}
	b, err := json.Marshal(doc)
	require.NoError(t, err)
	ks, err := ParseJWKS(b)
	require.NoError(t, err)
	return ks
}

func goodClaims(now time.Time) map[string]any {
	return map[string]any{
		"iss": "https://idp.example", "sub": "u-1", "aud": "client-x",
		"email": "alice@acme.com", "email_verified": true,
		"exp": now.Add(time.Hour).Unix(), "iat": now.Unix(), "nonce": "N",
	}
}

func params() VerifyParams {
	return VerifyParams{Issuer: "https://idp.example", ClientID: "client-x", Nonce: "N"}
}

func TestVerifyIDToken_Good(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	now := time.Unix(1_700_000_000, 0)
	raw := signRS256(t, key, "k1", "RS256", goodClaims(now))
	c, err := VerifyIDToken(raw, jwksFor(t, "k1", &key.PublicKey), params(), now)
	require.NoError(t, err)
	require.Equal(t, "alice@acme.com", c.Email)
	require.True(t, c.EmailVerified)
	require.Equal(t, "u-1", c.Sub)
}

func TestVerifyIDToken_Negatives(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	now := time.Unix(1_700_000_000, 0)
	ks := jwksFor(t, "k1", &key.PublicKey)

	cases := []struct {
		name  string
		build func() string
		keys  JWKS
	}{
		{"tampered-signature", func() string {
			raw := signRS256(t, key, "k1", "RS256", goodClaims(now))
			return raw[:len(raw)-2] + "AA" // 改签名尾字节
		}, ks},
		{"wrong-signing-key", func() string { return signRS256(t, other, "k1", "RS256", goodClaims(now)) }, ks},
		{"alg-none", func() string { return signRS256(t, key, "k1", "none", goodClaims(now)) }, ks},
		{"alg-hs256", func() string { return signRS256(t, key, "k1", "HS256", goodClaims(now)) }, ks},
		{"unknown-kid", func() string { return signRS256(t, key, "k9", "RS256", goodClaims(now)) }, ks},
		{"wrong-aud", func() string {
			c := goodClaims(now)
			c["aud"] = "someone-else"
			return signRS256(t, key, "k1", "RS256", c)
		}, ks},
		{"wrong-iss", func() string {
			c := goodClaims(now)
			c["iss"] = "https://evil"
			return signRS256(t, key, "k1", "RS256", c)
		}, ks},
		{"wrong-nonce", func() string {
			c := goodClaims(now)
			c["nonce"] = "X"
			return signRS256(t, key, "k1", "RS256", c)
		}, ks},
		{"expired", func() string {
			c := goodClaims(now)
			c["exp"] = now.Add(-2 * time.Hour).Unix()
			return signRS256(t, key, "k1", "RS256", c)
		}, ks},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := VerifyIDToken(tc.build(), tc.keys, params(), now)
			require.Error(t, err, "负路径必须拒绝")
		})
	}
}

func TestVerifyIDToken_UnknownKIDSentinel(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	now := time.Unix(1_700_000_000, 0)
	raw := signRS256(t, key, "k9", "RS256", goodClaims(now))
	_, err := VerifyIDToken(raw, jwksFor(t, "k1", &key.PublicKey), params(), now)
	require.True(t, errors.Is(err, ErrUnknownKID), "kid 未命中须暴露哨兵供调用方刷新 JWKS 重试")
}

// aud 兼容 string 与 []string。
func TestVerifyIDToken_AudArray(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	now := time.Unix(1_700_000_000, 0)
	c := goodClaims(now)
	c["aud"] = []string{"other", "client-x"}
	raw := signRS256(t, key, "k1", "RS256", c)
	_, err := VerifyIDToken(raw, jwksFor(t, "k1", &key.PublicKey), params(), now)
	require.NoError(t, err)
}
