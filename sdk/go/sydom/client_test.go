package sydom_test

import (
	"context"
	"net"
	"testing"

	authv1 "github.com/nickZFZ/Sydom/gen/sydom/auth/v1"
	"github.com/nickZFZ/Sydom/sdk/go/sydom"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/structpb"
)

// fakeAuth 是可编程的 AuthService 假实现：每个方法走注入的闭包。
type fakeAuth struct {
	authv1.UnimplementedAuthServiceServer
	check  func(*authv1.CheckRequest) (*authv1.CheckResponse, error)
	batch  func(*authv1.BatchCheckRequest) (*authv1.BatchCheckResponse, error)
	filter func(*authv1.FilterRequest) (*authv1.FilterSQLResponse, error)
}

func (f *fakeAuth) Check(_ context.Context, r *authv1.CheckRequest) (*authv1.CheckResponse, error) {
	return f.check(r)
}
func (f *fakeAuth) BatchCheck(_ context.Context, r *authv1.BatchCheckRequest) (*authv1.BatchCheckResponse, error) {
	return f.batch(r)
}
func (f *fakeAuth) FilterSQL(_ context.Context, r *authv1.FilterRequest) (*authv1.FilterSQLResponse, error) {
	return f.filter(r)
}

// startFake 在 bufconn 上起 fakeAuth，返回经 WithConn 注入的 *sydom.Client 与底层 conn。
func startFake(t *testing.T, f *fakeAuth) (*sydom.Client, *grpc.ClientConn) {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	g := grpc.NewServer()
	authv1.RegisterAuthServiceServer(g, f)
	go func() { _ = g.Serve(lis) }()
	t.Cleanup(g.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	c, err := sydom.New("", sydom.WithConn(conn))
	require.NoError(t, err)
	return c, conn
}

func TestClient_Check_AllowAndDeny(t *testing.T) {
	f := &fakeAuth{check: func(r *authv1.CheckRequest) (*authv1.CheckResponse, error) {
		// 透传断言：请求字段正确组装。
		return &authv1.CheckResponse{Allowed: r.GetSubject() == "alice"}, nil
	}}
	c, _ := startFake(t, f)

	allowed, err := c.Check(context.Background(), "alice", "order", "read")
	require.NoError(t, err)
	require.True(t, allowed)

	allowed, err = c.Check(context.Background(), "bob", "order", "read")
	require.NoError(t, err)
	require.False(t, allowed)
}

func TestClient_Check_Unavailable_MapsToSentinel(t *testing.T) {
	f := &fakeAuth{check: func(*authv1.CheckRequest) (*authv1.CheckResponse, error) {
		return nil, status.Error(codes.Unavailable, "not ready")
	}}
	c, _ := startFake(t, f)

	allowed, err := c.Check(context.Background(), "alice", "order", "read")
	require.False(t, allowed) // fail-close：出错恒 false
	require.ErrorIs(t, err, sydom.ErrUnavailable)
}

func TestClient_Check_HardError_NotUnavailable(t *testing.T) {
	f := &fakeAuth{check: func(*authv1.CheckRequest) (*authv1.CheckResponse, error) {
		return nil, status.Error(codes.Internal, "boom")
	}}
	c, _ := startFake(t, f)

	allowed, err := c.Check(context.Background(), "alice", "order", "read")
	require.False(t, allowed)
	require.NotErrorIs(t, err, sydom.ErrUnavailable)
	require.Equal(t, codes.Internal, status.Code(err)) // 保留原 gRPC status
}

func TestClient_BatchCheck_PreservesOrder(t *testing.T) {
	f := &fakeAuth{batch: func(r *authv1.BatchCheckRequest) (*authv1.BatchCheckResponse, error) {
		out := make([]bool, len(r.GetRequests()))
		for i, req := range r.GetRequests() {
			out[i] = req.GetAction() == "read"
		}
		return &authv1.BatchCheckResponse{Allowed: out}, nil
	}}
	c, _ := startFake(t, f)

	got, err := c.BatchCheck(context.Background(), []sydom.CheckReq{
		{Subject: "alice", Object: "order", Action: "read"},
		{Subject: "alice", Object: "order", Action: "write"},
	})
	require.NoError(t, err)
	require.Equal(t, []bool{true, false}, got)
}

func TestClient_FilterSQL_RoundTrip(t *testing.T) {
	var gotAttrs map[string]any
	f := &fakeAuth{filter: func(r *authv1.FilterRequest) (*authv1.FilterSQLResponse, error) {
		gotAttrs = r.GetAttrs().AsMap()
		arg, _ := structpb.NewValue("HR")
		return &authv1.FilterSQLResponse{Sql: "dept = ?", Args: []*structpb.Value{arg}}, nil
	}}
	c, _ := startFake(t, f)

	res, err := c.FilterSQL(context.Background(), "alice", "order", map[string]any{"department": "HR"})
	require.NoError(t, err)
	require.Equal(t, "dept = ?", res.SQL)
	require.Equal(t, []any{"HR"}, res.Args)
	require.Equal(t, map[string]any{"department": "HR"}, gotAttrs) // attrs 正确编码送达
}

func TestClient_Close_InjectedConn_NotClosed(t *testing.T) {
	f := &fakeAuth{check: func(*authv1.CheckRequest) (*authv1.CheckResponse, error) {
		return &authv1.CheckResponse{Allowed: true}, nil
	}}
	c, conn := startFake(t, f)

	require.NoError(t, c.Close()) // 注入连接：Close 应不关闭它

	// 连接仍可用：再发一次 RPC 成功，证明 Close 未关注入连接。
	c2, err := sydom.New("", sydom.WithConn(conn))
	require.NoError(t, err)
	allowed, err := c2.Check(context.Background(), "alice", "order", "read")
	require.NoError(t, err)
	require.True(t, allowed)
}

func TestClient_BatchCheck_LengthMismatch_Errors(t *testing.T) {
	f := &fakeAuth{batch: func(*authv1.BatchCheckRequest) (*authv1.BatchCheckResponse, error) {
		// 请求 2 条，却只返回 1 个结果——长度不一致。
		return &authv1.BatchCheckResponse{Allowed: []bool{true}}, nil
	}}
	c, _ := startFake(t, f)

	got, err := c.BatchCheck(context.Background(), []sydom.CheckReq{
		{Subject: "alice", Object: "order", Action: "read"},
		{Subject: "alice", Object: "order", Action: "write"},
	})
	require.Error(t, err) // fail-close：响应长度错位必须报错
	require.Nil(t, got)
}
