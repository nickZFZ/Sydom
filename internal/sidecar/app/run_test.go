package app_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	authv1 "github.com/nickZFZ/Sydom/gen/sydom/auth/v1"
	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
	"github.com/nickZFZ/Sydom/internal/sidecar/app"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/structpb"
)

// fakePolicySync 是最小 PolicySync 服务端：PullSnapshot 返固定快照，Subscribe 长连保持。
type fakePolicySync struct {
	syncv1.UnimplementedPolicySyncServer
	snap *syncv1.Snapshot
}

func (f *fakePolicySync) PullSnapshot(context.Context, *syncv1.PullSnapshotRequest) (*syncv1.Snapshot, error) {
	return f.snap, nil
}

func (f *fakePolicySync) Subscribe(_ *syncv1.SubscribeRequest, s syncv1.PolicySync_SubscribeServer) error {
	<-s.Context().Done()
	return s.Context().Err()
}

// startFakeControlPlane 起真实 TCP 的 fake PolicySync，返回其监听地址。
func startFakeControlPlane(t *testing.T) string {
	t.Helper()
	snap := &syncv1.Snapshot{
		Version: 5,
		Rules: []*syncv1.PolicyRule{
			{Ptype: "g", Values: []string{"alice", "manager", "dom1"}},
			{Ptype: "p", Values: []string{"manager", "dom1", "order", "read", "allow"}},
		},
		DataPolicies: []*syncv1.DataPolicy{
			{Id: 1, SubjectType: "role", SubjectId: "manager", Resource: "order",
				Condition: `{"field":"dept","op":"EQ","value":"$user.department"}`, Effect: "allow"},
			{Id: 2, SubjectType: "role", SubjectId: "manager", Resource: "order",
				Condition: `{"field":"status","op":"IN","value":["locked","void"]}`, Effect: "deny"},
		},
	}
	g := grpc.NewServer()
	syncv1.RegisterPolicySyncServer(g, &fakePolicySync{snap: snap})
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = g.Serve(lis) }()
	t.Cleanup(g.Stop)
	return lis.Addr().String()
}

func TestRun_WiringEndToEnd(t *testing.T) {
	cpAddr := startFakeControlPlane(t)

	authLis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	cfg := app.Config{
		ControlPlaneAddr: cpAddr,
		AppKey:           "app-1",
		Domain:           "dom1",
		Secret:           []byte("secret"),
		MaxStaleness:     0,
		BackoffInitial:   time.Millisecond,
		BackoffMax:       5 * time.Millisecond,
		// AuthAddr 不设：Run 用注入的 authLis，不读 cfg.AuthAddr（仅 Main 用）。
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // 失败路径安全网：断言提前 FailNow 时也取消 Run，避免后台协程泄漏
	done := make(chan error, 1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	go func() { done <- app.Run(ctx, cfg, authLis, logger) }()

	conn, err := grpc.NewClient(authLis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer conn.Close()
	cli := authv1.NewAuthServiceClient(conn)

	// 装配链贯通：bootstrap 同步后 alice 经 manager 角色可 read order（就绪前返 Unavailable）。
	require.Eventually(t, func() bool {
		resp, err := cli.Check(context.Background(), &authv1.CheckRequest{
			Subject: "alice", Object: "order", Action: "read"})
		return err == nil && resp.GetAllowed()
	}, 10*time.Second, 50*time.Millisecond, "bootstrap 后 alice 应可 read order")

	// deny override 端到端贯通 FilterSQL。
	attrs, err := structpb.NewStruct(map[string]any{"department": "HR"})
	require.NoError(t, err)
	fres, err := cli.FilterSQL(context.Background(), &authv1.FilterRequest{
		Subject: "alice", Resource: "order", Attrs: attrs})
	require.NoError(t, err)
	require.Equal(t, "(dept = ? AND NOT (status IN (?, ?)))", fres.GetSql())
	gotArgs := make([]any, len(fres.GetArgs()))
	for i, v := range fres.GetArgs() {
		gotArgs[i] = v.AsInterface()
	}
	require.Equal(t, []any{"HR", "locked", "void"}, gotArgs)

	// 优雅关闭：取消 ctx，Run 在超时内干净返回 nil。
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err, "ctx 取消应使 Run 干净返回")
	case <-time.After(5 * time.Second):
		t.Fatal("Run 未在 ctx 取消后及时返回")
	}
}
