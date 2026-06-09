package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"
)

func bodyHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestSignREST_Deterministic(t *testing.T) {
	secret := []byte("s3cr3t")
	h := bodyHex([]byte(`{"code":"x"}`))
	a := SignREST(secret, "root", 1700000000, "POST", "/v1/apps/5/roles", h)
	b := SignREST(secret, "root", 1700000000, "POST", "/v1/apps/5/roles", h)
	require.Equal(t, a, b)
	require.Len(t, a, 64) // SHA-256 hex
}

func TestVerifyREST_Match(t *testing.T) {
	secret := []byte("s3cr3t")
	const p, ts, m, tgt = "root", int64(1700000000), "POST", "/v1/apps/5/roles"
	h := bodyHex([]byte(`{"code":"x"}`))
	sig := SignREST(secret, p, ts, m, tgt, h)
	require.True(t, VerifyREST(secret, p, ts, m, tgt, h, sig))
}

func TestVerifyREST_RejectsTampering(t *testing.T) {
	secret := []byte("s3cr3t")
	const p, ts, m, tgt = "root", int64(1700000000), "POST", "/v1/apps/5/roles"
	h := bodyHex([]byte(`{"code":"x"}`))
	sig := SignREST(secret, p, ts, m, tgt, h)

	require.False(t, VerifyREST([]byte("wrong"), p, ts, m, tgt, h, sig))           // 错密钥
	require.False(t, VerifyREST(secret, "other", ts, m, tgt, h, sig))              // 错 principal
	require.False(t, VerifyREST(secret, p, ts+1, m, tgt, h, sig))                  // 错时间戳
	require.False(t, VerifyREST(secret, p, ts, "GET", tgt, h, sig))                // 错 HTTP 方法
	require.False(t, VerifyREST(secret, p, ts, m, "/v1/apps/9/roles", h, sig))     // 错 target（防跨端点重放）
	require.False(t, VerifyREST(secret, p, ts, m, tgt, bodyHex([]byte("z")), sig)) // 错 body（防改 body 重放）
	require.False(t, VerifyREST(secret, p, ts, m, tgt, h, "deadbeef"))             // 错签名
}

func TestValidPrincipal(t *testing.T) {
	require.True(t, ValidPrincipal("root@sydom"))
	require.True(t, ValidPrincipal("AK-order.v2"))
	require.False(t, ValidPrincipal(""))          // 空
	require.False(t, ValidPrincipal("AK order"))  // 空格
	require.False(t, ValidPrincipal("AK\norder")) // 换行
	require.False(t, ValidPrincipal("AK\torder")) // 制表符
	require.False(t, ValidPrincipal("AK​order"))  // 零宽空格
	require.False(t, ValidPrincipal("订单"))        // 非 ASCII
}
