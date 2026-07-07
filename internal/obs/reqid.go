package obs

import (
	"crypto/rand"
	"encoding/hex"
)

// newRequestID 生成 16 字节随机 hex（无外部依赖；仅作关联标识，非安全令牌）。
func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b[:])
}
