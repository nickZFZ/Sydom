package authz

import (
	"context"
	"net"
	"testing"
	"time"

	authv1 "github.com/nickZFZ/Sydom/gen/sydom/auth/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// startAuthServer 起 bufconn AuthService，返回拨号好的客户端。
func startAuthServer(t *testing.T, a *Authorizer) authv1.AuthServiceClient {
	t.Helper()
	g := grpc.NewServer()
	authv1.RegisterAuthServiceServer(g, NewServer(a))
	lis := bufconn.Listen(1024 * 1024)
	go func() { _ = g.Serve(lis) }()
	t.Cleanup(g.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return authv1.NewAuthServiceClient(conn)
}

func TestServer_Check_AllowViaRole(t *testing.T) {
	a := newAuthorizer(t, Config{}, fakeFresh{ready: true, last: time.Now()})
	cli := startAuthServer(t, a)
	resp, err := cli.Check(context.Background(), &authv1.CheckRequest{Subject: "alice", Object: "order", Action: "read"})
	require.NoError(t, err)
	require.True(t, resp.GetAllowed())
}

func TestServer_Check_NotReady_Unavailable(t *testing.T) {
	a := newAuthorizer(t, Config{}, fakeFresh{ready: false})
	cli := startAuthServer(t, a)
	_, err := cli.Check(context.Background(), &authv1.CheckRequest{Subject: "alice", Object: "order", Action: "read"})
	require.Equal(t, codes.Unavailable, status.Code(err), "未就绪应映射 Unavailable，而非 allowed=false")
}
