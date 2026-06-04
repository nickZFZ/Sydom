package syncclient

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
	"github.com/nickZFZ/Sydom/internal/sidecar/dataperm"
	"github.com/nickZFZ/Sydom/internal/sidecar/kernel"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

// fakeServer 是脚本化 PolicySync 服务端：
// snapFn 决定第 N 次 PullSnapshot 的返回；subscribeFn 决定第 M 次 Subscribe 的行为。
type fakeServer struct {
	syncv1.UnimplementedPolicySyncServer

	mu          sync.Mutex
	pullCalls   int
	subCalls    int
	snapFn      func(call int) (*syncv1.Snapshot, error)
	subscribeFn func(call int, s syncv1.PolicySync_SubscribeServer) error
}

func (f *fakeServer) PullSnapshot(_ context.Context, _ *syncv1.PullSnapshotRequest) (*syncv1.Snapshot, error) {
	f.mu.Lock()
	f.pullCalls++
	n, fn := f.pullCalls, f.snapFn
	f.mu.Unlock()
	return fn(n)
}

func (f *fakeServer) Subscribe(_ *syncv1.SubscribeRequest, s syncv1.PolicySync_SubscribeServer) error {
	f.mu.Lock()
	f.subCalls++
	n, fn := f.subCalls, f.subscribeFn
	f.mu.Unlock()
	return fn(n, s)
}

func (f *fakeServer) pullCount() int { f.mu.Lock(); defer f.mu.Unlock(); return f.pullCalls }

// sendThenBlock 依次 Send evs，然后阻塞到 stream ctx 取消（模拟长连保持，避免触发重连）。
func sendThenBlock(evs ...*syncv1.SyncEvent) func(int, syncv1.PolicySync_SubscribeServer) error {
	return func(_ int, s syncv1.PolicySync_SubscribeServer) error {
		for _, ev := range evs {
			if err := s.Send(ev); err != nil {
				return err
			}
		}
		<-s.Context().Done()
		return s.Context().Err()
	}
}

func deltaEv(d *syncv1.Delta) *syncv1.SyncEvent {
	return &syncv1.SyncEvent{Event: &syncv1.SyncEvent_Delta{Delta: d}}
}
func heartbeatEv(v uint64) *syncv1.SyncEvent {
	return &syncv1.SyncEvent{Event: &syncv1.SyncEvent_Heartbeat{Heartbeat: &syncv1.Heartbeat{CurrentVersion: v}}}
}
func snapshotRequiredEv() *syncv1.SyncEvent {
	return &syncv1.SyncEvent{Event: &syncv1.SyncEvent_SnapshotRequired{SnapshotRequired: &syncv1.SnapshotRequired{}}}
}

// startFake 起 bufconn fake 服务端，构造真实 Engine+Table，返回拨号好的 SyncClient。
func startFake(t *testing.T, f *fakeServer) (*SyncClient, *kernel.Engine, *dataperm.Table) {
	t.Helper()
	g := grpc.NewServer()
	syncv1.RegisterPolicySyncServer(g, f)
	lis := bufconn.Listen(1024 * 1024)
	go func() { _ = g.Serve(lis) }()
	t.Cleanup(g.Stop)

	table := dataperm.NewTable()
	engine, err := kernel.New("dom1", nil, table)
	require.NoError(t, err)

	c, err := New(Config{
		Endpoint: "passthrough:///bufnet",
		AppID:    "app-1",
		Secret:   []byte("secret"),
		Secure:   false,
		DialOptions: []grpc.DialOption{
			grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		},
		BackoffInitial: time.Millisecond,
		BackoffMax:     5 * time.Millisecond,
	}, engine)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c, engine, table
}

// runClient 在后台跑 Run，测试结束自动取消。
func runClient(t *testing.T, c *SyncClient) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = c.Run(ctx) }()
}

func snapV5() *syncv1.Snapshot {
	return &syncv1.Snapshot{
		Version: 5,
		Rules:   []*syncv1.PolicyRule{{Ptype: "g", Values: []string{"alice", "manager", "dom1"}}},
	}
}

func TestSyncClient_Bootstrap_PullsSnapshotAndBecomesReady(t *testing.T) {
	f := &fakeServer{
		snapFn:      func(int) (*syncv1.Snapshot, error) { return snapV5(), nil },
		subscribeFn: sendThenBlock(),
	}
	c, engine, _ := startFake(t, f)
	runClient(t, c)

	require.Eventually(t, func() bool {
		return engine.Ready() && engine.Version() == 5
	}, time.Second, 5*time.Millisecond, "引导后应 Ready 且 Version=5")
	require.Eventually(t, func() bool { return c.Connected() }, time.Second, 5*time.Millisecond)
	require.False(t, c.LastSyncAt().IsZero(), "引导成功应刷新 LastSyncAt")
}

// 空策略 app 也应达「已同步」（Ready=true），而非永远 fail-close deny-all。
func TestSyncClient_Bootstrap_EmptySnapshotStillReady(t *testing.T) {
	f := &fakeServer{
		snapFn:      func(int) (*syncv1.Snapshot, error) { return &syncv1.Snapshot{Version: 0}, nil },
		subscribeFn: sendThenBlock(),
	}
	c, engine, _ := startFake(t, f)
	runClient(t, c)

	require.Eventually(t, func() bool {
		return engine.Ready() && engine.Version() == 0
	}, time.Second, 5*time.Millisecond, "空快照也应 Ready=true、Version=0")
}
