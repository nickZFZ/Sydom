package e2e_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"log/slog"
	"net"
	"os"
	"testing"
	"time"

	seed "github.com/nickZFZ/Sydom/examples/seed"
	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/auth"
	"github.com/nickZFZ/Sydom/internal/certtest"
	cpapp "github.com/nickZFZ/Sydom/internal/controlplane/app"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/nickZFZ/Sydom/internal/sidecar/dataperm"
	"github.com/nickZFZ/Sydom/internal/sidecar/kernel"
	"github.com/nickZFZ/Sydom/internal/sidecar/syncclient"
	"github.com/nickZFZ/Sydom/internal/tlsconfig"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// startControlPlaneMTLS 起 CP：admin/sync 两面皆 TLS，且 sync（机对机策略同步）通道要求
// 客户端证书链到 syncClientCA（mTLS）；admin 面只单向 server-auth（不要求客户端证书）。
// 返回 adminAddr、syncAddr、root secret。
func startControlPlaneMTLS(t *testing.T, dsn, redisAddr, tlsCert, tlsKey, syncClientCA string) (adminAddr, syncAddr string, rootSecret []byte) {
	t.Helper()
	rootSecret = []byte("root-secret")
	cfg := cpapp.Config{
		DatabaseDSN: dsn, RedisAddr: redisAddr, RootPrincipal: "root@sydom",
		HeartbeatInterval: 50 * time.Millisecond, RelayPollInterval: 20 * time.Millisecond,
		MasterKey: masterKey(), RootSecret: rootSecret,
		TLSCertFile: tlsCert, TLSKeyFile: tlsKey, SyncClientCAFile: syncClientCA,
	}
	adminLis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	syncLis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	go func() { _ = cpapp.Run(ctx, cfg, adminLis, syncLis, nil, nil, logger) }()
	return adminLis.Addr().String(), syncLis.Addr().String(), rootSecret
}

// dialAdminTLS 拨号 TLS admin 面：server-auth TLS（信任 roots，不出示客户端证书——admin 不要求），
// PerRPC HMAC 因传输已 TLS 用 secure=true。
func dialAdminTLS(t *testing.T, addr string, roots *x509.CertPool, principal string, secret []byte) adminv1.AdminServiceClient {
	t.Helper()
	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: roots, ServerName: "localhost"})),
		grpc.WithPerRPCCredentials(auth.NewPerRPCCredentials(principal, secret, true)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return adminv1.NewAdminServiceClient(conn)
}

// newMTLSSyncClient 构造一个真实的 sidecar syncclient（含自有 kernel.Engine），经 mTLS 连控制面
// policysync。clientCert/clientKey 皆空 = 不出示客户端证书（用于反向验证 mTLS 确实拦截）。
func newMTLSSyncClient(t *testing.T, syncAddr, appKey string, secret []byte, caFile, clientCert, clientKey string) *syncclient.SyncClient {
	t.Helper()
	cliTLS, err := tlsconfig.MutualClient(caFile, clientCert, clientKey)
	require.NoError(t, err)
	cliTLS.ServerName = "localhost"
	engine, err := kernel.New("shop", kernel.NewBoundedCache(64), dataperm.NewTable())
	require.NoError(t, err)
	sc, err := syncclient.New(syncclient.Config{
		Endpoint: syncAddr, AppID: appKey, Secret: secret, Secure: true,
		DialOptions:    []grpc.DialOption{grpc.WithTransportCredentials(credentials.NewTLS(cliTLS))},
		BackoffInitial: 20 * time.Millisecond, BackoffMax: 200 * time.Millisecond,
	}, engine)
	require.NoError(t, err)
	return sc
}

// TestEndToEnd_MutualTLSSyncChannel 封住组装接缝：把「控制面 policysync 要求客户端证书」与「边车
// syncclient 出示客户端证书」两端拼成一次真正的 mTLS gRPC 连接，跑通传输层握手 + HMAC 认证 + 全量
// 快照引导；再用「不出示证书的边车绝不连通」反向验证 mTLS 确实在端到端强制（断言有齿）。
func TestEndToEnd_MutualTLSSyncChannel(t *testing.T) {
	dsn := dbtest.MigratedDSN(t)
	redisAddr := dbtest.StartRedis(t)

	ca := certtest.NewCA(t)
	srvCert, srvKey := ca.Leaf(t, "localhost", x509.ExtKeyUsageServerAuth)
	cliCert, cliKey := ca.Leaf(t, "sidecar", x509.ExtKeyUsageClientAuth)

	adminAddr, syncAddr, rootSecret := startControlPlaneMTLS(t, dsn, redisAddr, srvCert, srvKey, ca.File())

	caPEM, err := os.ReadFile(ca.File())
	require.NoError(t, err)
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM(caPEM))

	adminCli := dialAdminTLS(t, adminAddr, roots, "root@sydom", rootSecret)
	require.Eventually(t, func() bool {
		_, err := adminCli.ListApplications(context.Background(), &adminv1.ListApplicationsRequest{})
		return err == nil
	}, 15*time.Second, 100*time.Millisecond, "TLS admin 面应就绪")

	// 供应一个 app（app_key=demo-shop、domain=shop），拿明文 secret 供边车 HMAC。
	secret, err := seed.Provision(context.Background(), adminCli, "demo", "shop", "demo-shop")
	require.NoError(t, err)
	require.NotEmpty(t, secret)

	// 正向：持 CA 签发客户端证书的边车经 mTLS 连通 policysync 并完成引导（Connected + Ready）。
	// 一次断言即覆盖：mTLS 传输层握手通过 + HMAC 身份认证通过 + PullSnapshot 全量快照应用成功。
	good := newMTLSSyncClient(t, syncAddr, "demo-shop", []byte(secret), ca.File(), cliCert, cliKey)
	goodCtx, goodCancel := context.WithCancel(context.Background())
	t.Cleanup(goodCancel)
	go func() { _ = good.Run(goodCtx) }()
	t.Cleanup(func() { _ = good.Close() })
	require.Eventually(t, func() bool {
		return good.Connected() && good.Ready()
	}, 15*time.Second, 100*time.Millisecond, "持客户端证书的边车应经 mTLS 连通 policysync 并完成引导(Connected+Ready)")

	// 反向验证（有齿）：同样 Secure=true 但不出示客户端证书 → mTLS 握手被服务端拒 → 边车无法引导，
	// 绝不 Connected 也绝不 Ready。若哪天 sync 通道退化为不要求客户端证书，此断言会立刻失败。
	noCert := newMTLSSyncClient(t, syncAddr, "demo-shop", []byte(secret), ca.File(), "", "")
	noCtx, noCancel := context.WithCancel(context.Background())
	t.Cleanup(noCancel)
	go func() { _ = noCert.Run(noCtx) }()
	t.Cleanup(func() { _ = noCert.Close() })
	require.Never(t, func() bool {
		return noCert.Connected() || noCert.Ready()
	}, 3*time.Second, 100*time.Millisecond, "无客户端证书的边车应被 mTLS 拒绝,绝不 Connected/Ready")
}
