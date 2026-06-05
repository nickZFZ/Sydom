// Package syncclient 把 Sidecar 接成控制面 PolicySync 的 gRPC 订阅客户端：
// 先 PullSnapshot 建基线、再 Subscribe 持续对账，把翻译后的策略喂给注入的 *kernel.Engine。
// 一切 pull/translate/apply 错误一律 fail-close：不推进版本、退避后重拉，绝不部分应用、绝不放行。
package syncclient

import (
	"time"

	"google.golang.org/grpc"
)

const (
	defaultBackoffInitial = 500 * time.Millisecond
	defaultBackoffMax     = 30 * time.Second
	// maxRecvMsgSize 容纳全量快照（对齐 gRPC spec §8 的 64MB unary）。
	maxRecvMsgSize = 64 * 1024 * 1024
)

// Config 是 SyncClient 的连接/认证/退避参数。
type Config struct {
	Endpoint    string            // 控制面 PolicySync 地址
	AppID       string            // app_key：HMAC 认证标识 + 流路由
	Secret      []byte            // HMAC 密钥（调用方从配置/解密提供）
	Secure      bool              // 传输层是否 TLS（false=insecure；true 时由 DialOptions 提供传输凭据）
	DialOptions []grpc.DialOption // 附加 dial 选项（TLS 凭据等）

	BackoffInitial time.Duration // 退避初值（零值用 500ms）
	BackoffMax     time.Duration // 退避上限（零值用 30s）
}
