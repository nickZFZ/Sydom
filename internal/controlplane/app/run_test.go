package app_test

import (
	"context"
	"io"
	"log/slog"
	"net"
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

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	go func() { done <- app.Run(ctx, cfg, adminLis, syncLis, logger) }()

	conn, err := grpc.NewClient(adminLis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(auth.NewPerRPCCredentials(cfg.RootPrincipal, rootSecret, false)))
	require.NoError(t, err)
	defer conn.Close()
	cli := adminv1.NewAdminServiceClient(conn)

	// 装配链贯通：root 凭据经 operator 认证 + 元-RBAC 调 ListApplications 成功。
	require.Eventually(t, func() bool {
		_, err := cli.ListApplications(context.Background(), &adminv1.ListApplicationsRequest{})
		return err == nil
	}, 10*time.Second, 100*time.Millisecond, "装配后 root 应能调通 AdminService")

	// 优雅关闭：取消 ctx，Run 在超时内干净返回 nil。
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err, "ctx 取消应使 Run 干净返回")
	case <-time.After(5 * time.Second):
		t.Fatal("Run 未在 ctx 取消后及时返回")
	}
}
