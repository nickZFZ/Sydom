package auth

import (
	"context"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc/credentials"
)

// perRPC 实现 grpc.PerRPCCredentials，为每个 RPC 注入 HMAC 三件套 metadata。
type perRPC struct {
	appID  string
	secret []byte
	secure bool
	now    func() time.Time
}

// NewPerRPCCredentials 构造 Sidecar 侧的 HMAC 凭据。
// secure 表示底层是否 TLS：非 TLS（本地/测试）须传 false 以允许明文凭据。
func NewPerRPCCredentials(appID string, secret []byte, secure bool) credentials.PerRPCCredentials {
	return &perRPC{appID: appID, secret: secret, secure: secure, now: time.Now}
}

func (c *perRPC) GetRequestMetadata(ctx context.Context, uri ...string) (map[string]string, error) {
	// 优先从 gRPC 注入的 RequestInfo 取完整 FullMethod（形如 "/pkg.Service/Method"），
	// 与服务端 info.FullMethod 完全一致，签名串不会因 audience URI 截断而错位。
	method := ""
	if ri, ok := credentials.RequestInfoFromContext(ctx); ok && ri.Method != "" {
		method = ri.Method
	} else if len(uri) > 0 {
		// 兜底：audience URI 格式为 "https://host/pkg.Service"（无 Method 部分），
		// 直接使用将导致签名与服务端不匹配，故仅作保留路径。
		method = methodFromURI(uri[0])
	}
	ts := c.now().Unix()
	return map[string]string{
		MDAppID:     c.appID,
		MDTimestamp: strconv.FormatInt(ts, 10),
		MDSignature: Sign(c.secret, c.appID, ts, method),
	}, nil
}

func (c *perRPC) RequireTransportSecurity() bool { return c.secure }

// methodFromURI 从 grpc 传入的请求 audience URI 提取路径部分。
// 注意：grpc 传入的 audience 截断到 "/pkg.Service"，不含 "/Method"，
// 此函数仅作兜底，正常路径应走 credentials.RequestInfoFromContext。
func methodFromURI(uri string) string {
	if i := strings.Index(uri, "://"); i >= 0 {
		rest := uri[i+3:] // 去掉 scheme://
		if j := strings.IndexByte(rest, '/'); j >= 0 {
			return rest[j:] // 去掉 authority，保留路径部分
		}
	}
	return uri
}
