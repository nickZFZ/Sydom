// Package auth 实现司域控制面与 Sidecar 之间的 HMAC 认证：
// 客户端签名、服务端验签拦截器，以及对 app_id 的强制隔离。
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

// gRPC metadata key（均为小写）。
const (
	MDAppID     = "x-sydom-app-id"
	MDTimestamp = "x-sydom-timestamp"
	MDSignature = "x-sydom-signature"
)

// signingString 拼装待签名串：<app_id>\n<unix_ts>\n<full_method>。
func signingString(appID string, unixTS int64, method string) string {
	var b strings.Builder
	b.WriteString(appID)
	b.WriteByte('\n')
	b.WriteString(strconv.FormatInt(unixTS, 10))
	b.WriteByte('\n')
	b.WriteString(method)
	return b.String()
}

// Sign 用 AppSecret 对 (appID, ts, method) 计算 HMAC-SHA256，返回小写 hex（64 字符）。
// 调用方须保证 secret 非空（空密钥的 HMAC 无安全意义）。
func Sign(secret []byte, appID string, unixTS int64, method string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signingString(appID, unixTS, method)))
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify 以常量时间比对签名是否匹配（防时序侧信道）。
// gotHex 必须为小写 hex（与 Sign 输出一致）；大写或非 hex 一律判定不匹配。
func Verify(secret []byte, appID string, unixTS int64, method, gotHex string) bool {
	want := Sign(secret, appID, unixTS, method)
	return hmac.Equal([]byte(want), []byte(gotHex))
}
