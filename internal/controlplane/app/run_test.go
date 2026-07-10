package app_test

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/auth"
	"github.com/nickZFZ/Sydom/internal/certtest"
	"github.com/nickZFZ/Sydom/internal/controlplane/app"
	"github.com/nickZFZ/Sydom/internal/crypto"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestRun_WiringEndToEnd(t *testing.T) {
	dsn := dbtest.MigratedDSN(t)
	redisAddr := dbtest.StartRedis(t)

	mk := make([]byte, crypto.KeySize)
	for i := range mk {
		mk[i] = 0x2a
	}
	rootSecret := []byte("root-secret")
	cfg := app.Config{
		DatabaseDSN:       dsn,
		RedisAddr:         redisAddr,
		RootPrincipal:     "root@sydom",
		HeartbeatInterval: 50 * time.Millisecond,
		RelayPollInterval: 20 * time.Millisecond,
		MasterKey:         mk,
		RootSecret:        rootSecret,
	}

	adminLis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	syncLis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	restLis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	consoleLis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	go func() { done <- app.Run(ctx, cfg, adminLis, syncLis, restLis, consoleLis, logger) }()

	// gRPC 链贯通（既有断言）。
	conn, err := grpc.NewClient(adminLis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(auth.NewPerRPCCredentials(cfg.RootPrincipal, rootSecret, false)))
	require.NoError(t, err)
	defer conn.Close()
	cli := adminv1.NewAdminServiceClient(conn)
	require.Eventually(t, func() bool {
		_, err := cli.ListApplications(context.Background(), &adminv1.ListApplicationsRequest{})
		return err == nil
	}, 10*time.Second, 100*time.Millisecond, "装配后 root 应能调通 gRPC AdminService")

	// REST 监听器走通认证链：root 签名 GET /v1/applications → 200。
	restBase := "http://" + restLis.Addr().String()
	require.Eventually(t, func() bool {
		target := "/v1/applications"
		ts := time.Now().Unix()
		sum := sha256.Sum256(nil)
		h := hex.EncodeToString(sum[:])
		req, _ := http.NewRequest("GET", restBase+target, nil)
		req.Header.Set(auth.HdrPrincipal, cfg.RootPrincipal)
		req.Header.Set(auth.HdrTimestamp, strconv.FormatInt(ts, 10))
		req.Header.Set(auth.HdrSignature, auth.SignREST(rootSecret, cfg.RootPrincipal, ts, "GET", target, h))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 10*time.Second, 100*time.Millisecond, "REST 监听器应走通认证链返回 200")

	// M5.2a 接线核验（REST 面）：secheaders 在链上、且为 JSON 锁死变体（非 Console 变体，SH-4）。
	{
		target := "/v1/applications"
		ts := time.Now().Unix()
		sum := sha256.Sum256(nil)
		h := hex.EncodeToString(sum[:])
		req, _ := http.NewRequest("GET", restBase+target, nil)
		req.Header.Set(auth.HdrPrincipal, cfg.RootPrincipal)
		req.Header.Set(auth.HdrTimestamp, strconv.FormatInt(ts, 10))
		req.Header.Set(auth.HdrSignature, auth.SignREST(rootSecret, cfg.RootPrincipal, ts, "GET", target, h))
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))
		require.Equal(t, "default-src 'none'; frame-ancestors 'none'", resp.Header.Get("Content-Security-Policy"))
	}

	// Console 监听器起来：未认证 GET /login → 200 登录页。
	consoleBase := "http://" + consoleLis.Addr().String()
	require.Eventually(t, func() bool {
		resp, err := http.DefaultClient.Get(consoleBase + "/login")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 10*time.Second, 100*time.Millisecond, "Console 监听器应返回登录页 200")

	// M5.2a 接线核验（Console 面）：secheaders 在链上、且为 HTML 严格 CSP 变体（含 script-src 'self' 无 unsafe-inline，SH-2/SH-4）。
	{
		resp, err := http.DefaultClient.Get(consoleBase + "/login")
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))
		require.Contains(t, resp.Header.Get("Content-Security-Policy"), "script-src 'self'")
		require.NotContains(t, resp.Header.Get("Content-Security-Policy"), "unsafe-inline")
	}

	// 优雅关闭。
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err, "ctx 取消应使 Run 干净返回")
	case <-time.After(5 * time.Second):
		t.Fatal("Run 未在 ctx 取消后及时返回")
	}
}

// caPool 从 CA 文件构造仅信任该 CA 的根池。
func caPool(t *testing.T, caFile string) *x509.CertPool {
	t.Helper()
	pem, err := os.ReadFile(caFile)
	require.NoError(t, err)
	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(pem))
	return pool
}

func TestRun_SyncChannelRequiresClientCert(t *testing.T) {
	dsn := dbtest.MigratedDSN(t)
	redisAddr := dbtest.StartRedis(t)

	ca := certtest.NewCA(t)
	srvCert, srvKey := ca.Leaf(t, "localhost", x509.ExtKeyUsageServerAuth)
	cliCert, cliKey := ca.Leaf(t, "sidecar", x509.ExtKeyUsageClientAuth)

	mk := make([]byte, crypto.KeySize)
	for i := range mk {
		mk[i] = 0x2a
	}
	cfg := app.Config{
		DatabaseDSN:       dsn,
		RedisAddr:         redisAddr,
		RootPrincipal:     "root@sydom",
		HeartbeatInterval: 50 * time.Millisecond,
		RelayPollInterval: 20 * time.Millisecond,
		MasterKey:         mk,
		RootSecret:        []byte("root-secret"),
		TLSCertFile:       srvCert,
		TLSKeyFile:        srvKey,
		SyncClientCAFile:  ca.File(),
	}

	adminLis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	syncLis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	restLis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	consoleLis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	go func() { done <- app.Run(ctx, cfg, adminLis, syncLis, restLis, consoleLis, logger) }()

	roots := caPool(t, ca.File())

	// 先等服务端起来：admin 监听器用共享 srvTLS（不要求客户端证书），无证书 TLS 握手应成功。
	adminNoCert := &tls.Config{RootCAs: roots, ServerName: "localhost"}
	require.Eventually(t, func() bool {
		c, err := tls.Dial("tcp", adminLis.Addr().String(), adminNoCert)
		if err != nil {
			return false
		}
		c.Close()
		return true
	}, 10*time.Second, 100*time.Millisecond, "admin 监听器不应要求客户端证书（分通道隔离）")

	// sync 监听器：无客户端证书 → 握手被拒。
	// 注：TLS 1.3 下客户端在观测到服务端后置证书校验结果前即完成自身握手并发出
	// Finished，tls.Dial() 可能返回 (conn, nil)；服务端校验失败后发送 fatal alert
	// 并关闭连接，客户端须靠后续 Read 才能观测到该拒绝（Go crypto/tls 既有语义，
	// 与任务 3 tlsconfig 集成测试实测一致）。实测确认：Dial 成功，随后 Read 得
	// "remote error: tls: certificate required"。故 Dial 成功时补一次带超时的
	// Read，凭该具体错误串判定服务端是否真拒绝（而非仅看 Read 出错/超时——单纯
	// 出错不足以区分「确被拒绝」与「握手其实成功但对端沉默」）。
	syncNoCert := &tls.Config{RootCAs: roots, ServerName: "localhost"}
	c0, dialErr := tls.Dial("tcp", syncLis.Addr().String(), syncNoCert)
	if dialErr == nil {
		defer c0.Close()
		c0.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 1)
		n, rerr := c0.Read(buf)
		if n > 0 {
			t.Fatal("policysync 监听器应要求客户端证书，无证书握手不该成功（收到实际数据）")
		}
		if rerr == nil || !strings.Contains(rerr.Error(), "certificate required") {
			t.Fatalf("policysync 监听器应因缺少客户端证书发送 fatal alert 拒绝连接，实际 read err=%v", rerr)
		}
	}

	// sync 监听器：持 CA 签发客户端证书 → 握手成功（gRPC/HMAC 另说，此处只验传输层）。
	pair, err := tls.LoadX509KeyPair(cliCert, cliKey)
	require.NoError(t, err)
	syncWithCert := &tls.Config{RootCAs: roots, ServerName: "localhost", Certificates: []tls.Certificate{pair}}
	c, err := tls.Dial("tcp", syncLis.Addr().String(), syncWithCert)
	require.NoError(t, err, "持 CA 签发客户端证书应通过 policysync 传输层校验")
	c.Close()

	// Console 监听器（HTTPS，共享 srvTLS，不要求客户端证书）：无证书 GET /login → 200。
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "localhost"}}}
	consoleURL := "https://localhost:" + portOf(t, consoleLis) + "/login"
	resp, err := client.Get(consoleURL)
	require.NoError(t, err, "Console 监听器不应要求客户端证书（分通道隔离）")
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Run 未在 ctx 取消后及时返回")
	}
}

// portOf 从监听器地址提取端口字符串（用于以 ServerName=localhost 构造 https URL）。
func portOf(t *testing.T, ln net.Listener) string {
	t.Helper()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	require.NoError(t, err)
	return port
}
