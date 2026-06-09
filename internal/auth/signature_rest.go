package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

// REST-HMAC HTTP 头部（小写 canonical；net/http Header.Get 大小写不敏感）。
const (
	HdrPrincipal = "X-Sydom-Principal"
	HdrTimestamp = "X-Sydom-Timestamp"
	HdrSignature = "X-Sydom-Signature"
)

// signingStringREST 拼装 REST 待签名串，绑定到完整 HTTP 请求：
//
//	<principal>\n<unix_ts>\n<HTTP-METHOD>\n<request-target>\n<hex(sha256(body))>
//
// 绑定 method+target+body 防跨端点/改 body 重放（区别于 gRPC 的方法绑定串）。
func signingStringREST(principal string, unixTS int64, httpMethod, target, bodySHA256Hex string) string {
	var b strings.Builder
	b.WriteString(principal)
	b.WriteByte('\n')
	b.WriteString(strconv.FormatInt(unixTS, 10))
	b.WriteByte('\n')
	b.WriteString(httpMethod)
	b.WriteByte('\n')
	b.WriteString(target)
	b.WriteByte('\n')
	b.WriteString(bodySHA256Hex)
	return b.String()
}

// SignREST 用 operator secret 对 REST 请求计算 HMAC-SHA256，返回小写 hex（64 字符）。
// 调用方须保证 secret 非空（空密钥的 HMAC 无安全意义）。
func SignREST(secret []byte, principal string, unixTS int64, httpMethod, target, bodySHA256Hex string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signingStringREST(principal, unixTS, httpMethod, target, bodySHA256Hex)))
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyREST 以常量时间比对 REST 签名（防时序侧信道）。gotHex 须为小写 hex。
func VerifyREST(secret []byte, principal string, unixTS int64, httpMethod, target, bodySHA256Hex, gotHex string) bool {
	want := SignREST(secret, principal, unixTS, httpMethod, target, bodySHA256Hex)
	return hmac.Equal([]byte(want), []byte(gotHex))
}

// ValidPrincipal 限定 principal 为 ASCII 可打印非空格字符（0x21..0x7e）且非空。
// 拒绝控制字符/换行（防签名串分隔符歧义）、空格、全部非 ASCII（挡 Unicode 同形字欺骗）。
// 与 gRPC 端 app_id 校验同一字符集，两路复用。
func ValidPrincipal(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	return true
}
