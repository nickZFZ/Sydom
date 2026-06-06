package sydom_test

import (
	"context"
	"net"
	"sync"
	"testing"

	authv1 "github.com/nickZFZ/Sydom/gen/sydom/auth/v1"
	"github.com/nickZFZ/Sydom/sdk/go/sydom"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func TestClient_ReportPermissions(t *testing.T) {
	got, client := startReportFake(t, &authv1.ReportPermissionsResponse{Upserted: 2, Skipped: 1})
	res, err := client.ReportPermissions(context.Background(), []sydom.Permission{
		{Code: "p.read", Resource: "order", Action: "read", Type: "api", Name: "读"},
		{Code: "p.write", Resource: "order", Action: "write"},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Upserted != 2 || res.Skipped != 1 {
		t.Fatalf("got %+v", res)
	}
	if len(*got) != 2 || (*got)[0].GetCode() != "p.read" {
		t.Fatalf("server got %v", *got)
	}
}

func TestRegistry_RegisterThenReport(t *testing.T) {
	var mu sync.Mutex
	var captured []sydom.Permission
	stub := reporterFunc(func(_ context.Context, ps []sydom.Permission) (sydom.ReportResult, error) {
		mu.Lock()
		defer mu.Unlock()
		captured = ps
		return sydom.ReportResult{Upserted: len(ps)}, nil
	})

	reg := sydom.NewRegistry()
	reg.Register(sydom.Permission{Code: "p.a", Resource: "order", Action: "read"})
	reg.Register(sydom.Permission{Code: "p.b", Resource: "order", Action: "write"})
	res, err := reg.Report(context.Background(), stub)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Upserted != 2 || len(captured) != 2 {
		t.Fatalf("got res=%+v captured=%d", res, len(captured))
	}
}

func TestRegistry_EmptyReport_NoOp(t *testing.T) {
	called := false
	stub := reporterFunc(func(_ context.Context, ps []sydom.Permission) (sydom.ReportResult, error) {
		called = true
		return sydom.ReportResult{}, nil
	})
	reg := sydom.NewRegistry()
	res, err := reg.Report(context.Background(), stub)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if called {
		t.Fatal("空注册表不应调用 reporter（应 no-op）")
	}
	if res.Upserted != 0 || res.Skipped != 0 {
		t.Fatalf("空集应返回零值，got %+v", res)
	}
}

// reporterFunc 把函数适配为 sydom.PermissionReporter。
type reporterFunc func(context.Context, []sydom.Permission) (sydom.ReportResult, error)

func (f reporterFunc) ReportPermissions(ctx context.Context, ps []sydom.Permission) (sydom.ReportResult, error) {
	return f(ctx, ps)
}

// startReportFake 起一个仅实现 ReportPermissions 的 bufconn AuthService，返回收到的入参指针与拨号好的 Client。
func startReportFake(t *testing.T, resp *authv1.ReportPermissionsResponse) (*[]*authv1.PermissionPoint, *sydom.Client) {
	t.Helper()
	var got []*authv1.PermissionPoint
	fake := &reportOnlyAuth{resp: resp, got: &got}
	g := grpc.NewServer()
	authv1.RegisterAuthServiceServer(g, fake)
	lis := bufconn.Listen(1024 * 1024)
	go func() { _ = g.Serve(lis) }()
	t.Cleanup(g.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client, err := sydom.New("bufnet", sydom.WithConn(conn))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return &got, client
}

type reportOnlyAuth struct {
	authv1.UnimplementedAuthServiceServer
	resp *authv1.ReportPermissionsResponse
	got  *[]*authv1.PermissionPoint
}

func (a *reportOnlyAuth) ReportPermissions(_ context.Context, req *authv1.ReportPermissionsRequest) (*authv1.ReportPermissionsResponse, error) {
	*a.got = req.GetPermissions()
	return a.resp, nil
}
