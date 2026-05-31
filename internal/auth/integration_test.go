package auth_test

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
	"github.com/nickZFZ/Sydom/internal/auth"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type fakeResolver map[string][]byte

func (f fakeResolver) ResolveSecret(_ context.Context, appID string) ([]byte, error) {
	s, ok := f[appID]
	if !ok {
		return nil, errors.New("not found")
	}
	return s, nil
}

// stubServer 是最小 PolicySync 实现，只把已认证 app_id 记录下来供断言。
// 字段由服务端 goroutine 写、测试 goroutine 读，用 mu 保护以满足 Go 内存模型（-race 干净）。
type stubServer struct {
	syncv1.UnimplementedPolicySyncServer
	mu          sync.Mutex
	unaryAppID  string
	streamAppID string
}

func (s *stubServer) PullSnapshot(ctx context.Context, _ *syncv1.PullSnapshotRequest) (*syncv1.Snapshot, error) {
	id, _ := auth.AppIDFromContext(ctx)
	s.mu.Lock()
	s.unaryAppID = id
	s.mu.Unlock()
	return &syncv1.Snapshot{Version: 1}, nil
}

func (s *stubServer) Subscribe(_ *syncv1.SubscribeRequest, ss syncv1.PolicySync_SubscribeServer) error {
	id, _ := auth.AppIDFromContext(ss.Context())
	s.mu.Lock()
	s.streamAppID = id
	s.mu.Unlock()
	return nil // 立即结束流，本测试只验证认证与 app_id 注入
}

func (s *stubServer) getUnaryAppID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.unaryAppID
}

func (s *stubServer) getStreamAppID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.streamAppID
}

func startServer(t *testing.T, res auth.SecretResolver) (*stubServer, *bufconn.Listener) {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	srv := grpc.NewServer(
		grpc.UnaryInterceptor(auth.UnaryServerInterceptor(res)),
		grpc.StreamInterceptor(auth.StreamServerInterceptor(res)),
	)
	stub := &stubServer{}
	syncv1.RegisterPolicySyncServer(srv, stub)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return stub, lis
}

func dial(t *testing.T, lis *bufconn.Listener, creds grpc.DialOption) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.DialContext(context.Background(), "bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		creds,
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestIntegration_UnarySuccess(t *testing.T) {
	secret := []byte("s3cr3t")
	stub, lis := startServer(t, fakeResolver{"AK_order": secret})
	conn := dial(t, lis, grpc.WithPerRPCCredentials(
		auth.NewPerRPCCredentials("AK_order", secret, false)))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err := syncv1.NewPolicySyncClient(conn).PullSnapshot(ctx, &syncv1.PullSnapshotRequest{})
	require.NoError(t, err)
	require.Equal(t, uint64(1), resp.GetVersion())
	require.Equal(t, "AK_order", stub.getUnaryAppID())
}

func TestIntegration_StreamSuccess(t *testing.T) {
	secret := []byte("s3cr3t")
	stub, lis := startServer(t, fakeResolver{"AK_order": secret})
	conn := dial(t, lis, grpc.WithPerRPCCredentials(
		auth.NewPerRPCCredentials("AK_order", secret, false)))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream, err := syncv1.NewPolicySyncClient(conn).Subscribe(ctx, &syncv1.SubscribeRequest{LastAppliedVersion: 0})
	require.NoError(t, err)
	_, err = stream.Recv()
	require.ErrorIs(t, err, io.EOF)
	require.Equal(t, "AK_order", stub.getStreamAppID())
}

func TestIntegration_WrongSecretRejected(t *testing.T) {
	stub, lis := startServer(t, fakeResolver{"AK_order": []byte("real")})
	conn := dial(t, lis, grpc.WithPerRPCCredentials(
		auth.NewPerRPCCredentials("AK_order", []byte("wrong"), false)))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := syncv1.NewPolicySyncClient(conn).PullSnapshot(ctx, &syncv1.PullSnapshotRequest{})
	require.Equal(t, codes.Unauthenticated, status.Code(err))
	require.Empty(t, stub.getUnaryAppID())
}

func TestIntegration_StreamWrongSecretRejected(t *testing.T) {
	stub, lis := startServer(t, fakeResolver{"AK_order": []byte("real")})
	conn := dial(t, lis, grpc.WithPerRPCCredentials(
		auth.NewPerRPCCredentials("AK_order", []byte("wrong"), false)))

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stream, err := syncv1.NewPolicySyncClient(conn).Subscribe(ctx, &syncv1.SubscribeRequest{LastAppliedVersion: 0})
	require.NoError(t, err)
	_, err = stream.Recv() // 认证失败在首次 Recv 时返回
	require.Equal(t, codes.Unauthenticated, status.Code(err))
	require.Empty(t, stub.getStreamAppID()) // handler 未被调用
}

func TestIntegration_NoCredentialsRejected(t *testing.T) {
	_, lis := startServer(t, fakeResolver{"AK_order": []byte("real")})
	conn := dial(t, lis, grpc.EmptyDialOption{})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := syncv1.NewPolicySyncClient(conn).PullSnapshot(ctx, &syncv1.PullSnapshotRequest{})
	require.Equal(t, codes.Unauthenticated, status.Code(err))
}
