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
	"google.golang.org/protobuf/types/known/structpb"
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

func TestServer_BatchCheck(t *testing.T) {
	a := newAuthorizer(t, Config{}, fakeFresh{ready: true, last: time.Now()})
	cli := startAuthServer(t, a)
	resp, err := cli.BatchCheck(context.Background(), &authv1.BatchCheckRequest{
		Requests: []*authv1.CheckRequest{
			{Subject: "alice", Object: "order", Action: "read"},
			{Subject: "alice", Object: "order", Action: "delete"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, []bool{true, false}, resp.GetAllowed())
}

func TestServer_FilterSQL_DenyOverride_WKTRoundTrip(t *testing.T) {
	a := newAuthorizer(t, Config{}, fakeFresh{ready: true, last: time.Now()})
	cli := startAuthServer(t, a)
	attrs, err := structpb.NewStruct(map[string]any{"department": "HR"})
	require.NoError(t, err)
	resp, err := cli.FilterSQL(context.Background(), &authv1.FilterRequest{
		Subject: "alice", Resource: "order", Attrs: attrs,
	})
	require.NoError(t, err)
	require.Equal(t, "(dept = ? AND NOT (status IN (?, ?)))", resp.GetSql())
	gotArgs := make([]any, len(resp.GetArgs()))
	for i, v := range resp.GetArgs() {
		gotArgs[i] = v.AsInterface()
	}
	require.Equal(t, []any{"HR", "locked", "void"}, gotArgs)
}

func TestServer_FilterSQL_MissingVar_InvalidArgument(t *testing.T) {
	a := newAuthorizer(t, Config{}, fakeFresh{ready: true, last: time.Now()})
	cli := startAuthServer(t, a)
	empty, err := structpb.NewStruct(map[string]any{})
	require.NoError(t, err)
	_, err = cli.FilterSQL(context.Background(), &authv1.FilterRequest{
		Subject: "alice", Resource: "order", Attrs: empty, // 缺 department
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}
