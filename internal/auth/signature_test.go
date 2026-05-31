package auth

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSign_Deterministic(t *testing.T) {
	secret := []byte("s3cr3t")
	a := Sign(secret, "AK_order", 1700000000, "/sydom.sync.v1.PolicySync/PullSnapshot")
	b := Sign(secret, "AK_order", 1700000000, "/sydom.sync.v1.PolicySync/PullSnapshot")
	require.Equal(t, a, b) // 同输入同输出
	require.Len(t, a, 64)  // SHA-256 hex = 64 字符
}

func TestVerify_Match(t *testing.T) {
	secret := []byte("s3cr3t")
	const appID, ts, method = "AK_order", int64(1700000000), "/svc/M"
	sig := Sign(secret, appID, ts, method)
	require.True(t, Verify(secret, appID, ts, method, sig))
}

func TestVerify_RejectsTampering(t *testing.T) {
	secret := []byte("s3cr3t")
	const appID, ts, method = "AK_order", int64(1700000000), "/svc/M"
	sig := Sign(secret, appID, ts, method)

	require.False(t, Verify([]byte("wrong"), appID, ts, method, sig)) // 错密钥
	require.False(t, Verify(secret, "AK_other", ts, method, sig))     // 错 app_id
	require.False(t, Verify(secret, appID, ts+1, method, sig))        // 错时间戳
	require.False(t, Verify(secret, appID, ts, "/svc/Other", sig))    // 错方法
	require.False(t, Verify(secret, appID, ts, method, "deadbeef"))   // 错签名
}
