package restgw

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"time"

	"github.com/nickZFZ/Sydom/internal/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// authenticateHTTP 校验 REST-HMAC 凭据，成功返回 principal。
// body 为已缓存的原始请求体字节（handler 管线第 1 步读出，GET/DELETE 传 nil/空）。
// 失败一律 codes.Unauthenticated + 通用文案，防 operator 存在性枚举 oracle（与 gRPC 层一致）。
func authenticateHTTP(r *http.Request, body []byte, resolver auth.SecretResolver, now time.Time) (string, error) {
	principal := r.Header.Get(auth.HdrPrincipal)
	tsStr := r.Header.Get(auth.HdrTimestamp)
	sig := r.Header.Get(auth.HdrSignature)
	if principal == "" || tsStr == "" || sig == "" {
		return "", status.Error(codes.Unauthenticated, "missing auth fields")
	}
	if !auth.ValidPrincipal(principal) {
		return "", status.Error(codes.Unauthenticated, "invalid principal")
	}
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return "", status.Error(codes.Unauthenticated, "bad timestamp")
	}
	if d := now.Sub(time.Unix(ts, 0)); d > auth.MaxClockSkew || d < -auth.MaxClockSkew {
		return "", status.Error(codes.Unauthenticated, "timestamp out of window")
	}
	secret, err := resolver.ResolveSecret(r.Context(), principal)
	// 统一通用错误 + len==0 fail-close（空密钥 HMAC 人人可算），与 gRPC authenticate 同策略。
	if err != nil || len(secret) == 0 {
		return "", status.Error(codes.Unauthenticated, "authentication failed")
	}
	sum := sha256.Sum256(body)
	bodyHex := hex.EncodeToString(sum[:])
	if !auth.VerifyREST(secret, principal, ts, r.Method, r.URL.RequestURI(), bodyHex, sig) {
		return "", status.Error(codes.Unauthenticated, "authentication failed")
	}
	return principal, nil
}
