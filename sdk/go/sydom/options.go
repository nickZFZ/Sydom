package sydom

import "google.golang.org/grpc"

type config struct {
	dialOpts []grpc.DialOption
	conn     *grpc.ClientConn // 注入则 New 不自拨、Close 不关
}

// Option 配置 Client。
type Option func(*config)

// WithDialOptions 追加自定义 gRPC 拨号参数（进阶）。
func WithDialOptions(opts ...grpc.DialOption) Option {
	return func(c *config) { c.dialOpts = append(c.dialOpts, opts...) }
}

// WithConn 注入既有 *grpc.ClientConn（测试 bufconn / 连接复用）；
// 此时 New 不自拨号、忽略 target 参数，Close 也不关闭该连接（生命周期归注入方）。
func WithConn(conn *grpc.ClientConn) Option {
	return func(c *config) { c.conn = conn }
}
