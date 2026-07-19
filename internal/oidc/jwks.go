package oidc

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
)

// JWKS 是 kid→公钥（*rsa.PublicKey | *ecdsa.PublicKey）的只读映射。
type JWKS struct{ keys map[string]any }

// ParseJWKS 解析 JWKS 文档；仅收 RSA 与 EC(P-256)，其余跳过。
func ParseJWKS(b []byte) (JWKS, error) {
	var doc struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			Crv string `json:"crv"`
			N   string `json:"n"`
			E   string `json:"e"`
			X   string `json:"x"`
			Y   string `json:"y"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return JWKS{}, fmt.Errorf("oidc: parse jwks: %w", err)
	}
	out := JWKS{keys: make(map[string]any, len(doc.Keys))}
	for _, k := range doc.Keys {
		switch k.Kty {
		case "RSA":
			nb, err := base64.RawURLEncoding.DecodeString(k.N)
			if err != nil {
				return JWKS{}, fmt.Errorf("oidc: jwk n: %w", err)
			}
			eb, err := base64.RawURLEncoding.DecodeString(k.E)
			if err != nil {
				return JWKS{}, fmt.Errorf("oidc: jwk e: %w", err)
			}
			out.keys[k.Kid] = &rsa.PublicKey{
				N: new(big.Int).SetBytes(nb),
				E: int(new(big.Int).SetBytes(eb).Int64()),
			}
		case "EC":
			if k.Crv != "P-256" {
				continue
			}
			xb, err := base64.RawURLEncoding.DecodeString(k.X)
			if err != nil {
				return JWKS{}, fmt.Errorf("oidc: jwk x: %w", err)
			}
			yb, err := base64.RawURLEncoding.DecodeString(k.Y)
			if err != nil {
				return JWKS{}, fmt.Errorf("oidc: jwk y: %w", err)
			}
			out.keys[k.Kid] = &ecdsa.PublicKey{
				Curve: elliptic.P256(),
				X:     new(big.Int).SetBytes(xb),
				Y:     new(big.Int).SetBytes(yb),
			}
		}
	}
	return out, nil
}

// FetchJWKS GET jwksURI 并解析。
func FetchJWKS(ctx context.Context, hc *http.Client, jwksURI string) (JWKS, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jwksURI, nil)
	if err != nil {
		return JWKS{}, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return JWKS{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return JWKS{}, fmt.Errorf("oidc: jwks status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return JWKS{}, err
	}
	return ParseJWKS(body)
}
