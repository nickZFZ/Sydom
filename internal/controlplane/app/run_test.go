package app_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/auth"
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

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	go func() { done <- app.Run(ctx, cfg, adminLis, syncLis, restLis, logger) }()

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

	// 优雅关闭。
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err, "ctx 取消应使 Run 干净返回")
	case <-time.After(5 * time.Second):
		t.Fatal("Run 未在 ctx 取消后及时返回")
	}
}
