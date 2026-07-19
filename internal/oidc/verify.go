package oidc

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// leewaySeconds 是 exp/iat 容许的时钟偏移（≤60s，spec §5.2）。
const leewaySeconds = 60

var (
	// ErrUnknownKID：JWKS 无匹配 kid。调用方可刷新 JWKS 重试一次后再拒。
	ErrUnknownKID = errors.New("oidc: unknown kid")
	// ErrUnsupportedAlg：alg 非 RS256/ES256（含拒 none/HS*）。
	ErrUnsupportedAlg = errors.New("oidc: unsupported alg")
	// ErrBadSignature：签名不通过。
	ErrBadSignature = errors.New("oidc: bad signature")
)

// VerifyParams 是验签断言的期望值。
type VerifyParams struct{ Issuer, ClientID, Nonce string }

// IDTokenClaims 是校验通过后返回的声明子集。
type IDTokenClaims struct {
	Iss, Sub, Email string
	EmailVerified   bool
	Exp, Iat        int64
	Aud             []string
	Nonce           string
}

// audience 兼容 OIDC aud 既可为 string 也可为 []string。
type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*a = audience{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*a = many
	return nil
}

// VerifyIDToken 逐条固定的验签算法（spec §5.2）：alg 白名单→kid+kty 绑定→验签→声明校验。
func VerifyIDToken(rawIDToken string, keys JWKS, p VerifyParams, now time.Time) (IDTokenClaims, error) {
	parts := strings.Split(rawIDToken, ".")
	if len(parts) != 3 {
		return IDTokenClaims{}, errors.New("oidc: malformed jwt")
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return IDTokenClaims{}, fmt.Errorf("oidc: header decode: %w", err)
	}
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(headerBytes, &hdr); err != nil {
		return IDTokenClaims{}, fmt.Errorf("oidc: header parse: %w", err)
	}
	// 2. alg 白名单：仅 RS256/ES256；none/HS* 显式拒。
	if hdr.Alg != "RS256" && hdr.Alg != "ES256" {
		return IDTokenClaims{}, ErrUnsupportedAlg
	}
	// 3. 按 kid 选公钥。
	key, ok := keys.keys[hdr.Kid]
	if !ok {
		return IDTokenClaims{}, ErrUnknownKID
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return IDTokenClaims{}, ErrBadSignature
	}
	// 4. 验签于 header.payload 原文；kty 须与 alg 族匹配。
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	switch hdr.Alg {
	case "RS256":
		pub, ok := key.(*rsa.PublicKey)
		if !ok {
			return IDTokenClaims{}, ErrUnsupportedAlg // kid 对应 EC，与 RS256 不匹配
		}
		if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
			return IDTokenClaims{}, ErrBadSignature
		}
	case "ES256":
		pub, ok := key.(*ecdsa.PublicKey)
		if !ok {
			return IDTokenClaims{}, ErrUnsupportedAlg
		}
		if len(sig) != 64 {
			return IDTokenClaims{}, ErrBadSignature
		}
		r := new(big.Int).SetBytes(sig[:32])
		s := new(big.Int).SetBytes(sig[32:])
		if !ecdsa.Verify(pub, digest[:], r, s) {
			return IDTokenClaims{}, ErrBadSignature
		}
	}
	// 5. 解并校验声明。
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return IDTokenClaims{}, fmt.Errorf("oidc: payload decode: %w", err)
	}
	var rc struct {
		Iss           string   `json:"iss"`
		Sub           string   `json:"sub"`
		Email         string   `json:"email"`
		EmailVerified bool     `json:"email_verified"`
		Exp           int64    `json:"exp"`
		Iat           int64    `json:"iat"`
		Aud           audience `json:"aud"`
		Nonce         string   `json:"nonce"`
	}
	if err := json.Unmarshal(payloadBytes, &rc); err != nil {
		return IDTokenClaims{}, fmt.Errorf("oidc: payload parse: %w", err)
	}
	if rc.Iss != p.Issuer {
		return IDTokenClaims{}, errors.New("oidc: issuer mismatch")
	}
	if !contains(rc.Aud, p.ClientID) {
		return IDTokenClaims{}, errors.New("oidc: audience mismatch")
	}
	nowUnix := now.Unix()
	if nowUnix > rc.Exp+leewaySeconds {
		return IDTokenClaims{}, errors.New("oidc: token expired")
	}
	if rc.Iat != 0 && rc.Iat > nowUnix+leewaySeconds {
		return IDTokenClaims{}, errors.New("oidc: token issued in future")
	}
	if rc.Nonce != p.Nonce {
		return IDTokenClaims{}, errors.New("oidc: nonce mismatch")
	}
	return IDTokenClaims{
		Iss: rc.Iss, Sub: rc.Sub, Email: rc.Email, EmailVerified: rc.EmailVerified,
		Exp: rc.Exp, Iat: rc.Iat, Aud: []string(rc.Aud), Nonce: rc.Nonce,
	}, nil
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
