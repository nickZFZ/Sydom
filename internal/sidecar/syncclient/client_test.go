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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

func TestSyncClient_SteadyDeltas_AppliedMonotonically(t *testing.T) {
	addP := func(act string) *syncv1.PolicyChange {
		return &syncv1.PolicyChange{
			Op:   syncv1.ChangeOp_CHANGE_OP_ADD,
			Rule: &syncv1.PolicyRule{Ptype: "p", Values: []string{"manager", "dom1", "order", act, "allow"}},
		}
	}
	f := &fakeServer{
		snapFn: func(int) (*syncv1.Snapshot, error) { return snapV5(), nil },
		subscribeFn: sendThenBlock(
			deltaEv(&syncv1.Delta{Version: 6, PolicyChanges: []*syncv1.PolicyChange{addP("read")}}),
			deltaEv(&syncv1.Delta{Version: 7, PolicyChanges: []*syncv1.PolicyChange{addP("write")}}),
			deltaEv(&syncv1.Delta{Version: 8, PolicyChanges: []*syncv1.PolicyChange{addP("delete")}}),
		),
	}
	c, engine, _ := startFake(t, f)
	runClient(t, c)

	require.Eventually(t, func() bool { return engine.Version() == 8 }, time.Second, 5*time.Millisecond)
	require.Equal(t, 1, f.pullCount(), "连续 delta 不应触发任何重拉")

	allow, err := engine.Enforce("alice", "dom1", "order", "write") // 经 manager 角色
	require.NoError(t, err)
	require.True(t, allow)
}

// REMOVE delta 端到端：内核读 pc.Rule 删行，证明翻译层把 old_rule 搬进了 Rule。
func TestSyncClient_RemoveDelta_RevokesGrant(t *testing.T) {
	snap := &syncv1.Snapshot{
		Version: 5,
		Rules: []*syncv1.PolicyRule{
			{Ptype: "g", Values: []string{"alice", "manager", "dom1"}},
			{Ptype: "p", Values: []string{"manager", "dom1", "order", "read", "allow"}},
		},
	}
	f := &fakeServer{
		snapFn: func(int) (*syncv1.Snapshot, error) { return snap, nil },
		subscribeFn: sendThenBlock(
			deltaEv(&syncv1.Delta{Version: 6, PolicyChanges: []*syncv1.PolicyChange{{
				Op:      syncv1.ChangeOp_CHANGE_OP_REMOVE,
				OldRule: &syncv1.PolicyRule{Ptype: "p", Values: []string{"manager", "dom1", "order", "read", "allow"}},
			}}}),
		),
	}
	c, engine, _ := startFake(t, f)
	runClient(t, c)

	require.Eventually(t, func() bool { return engine.Version() == 6 }, time.Second, 5*time.Millisecond)
	require.Equal(t, 1, f.pullCount(), "REMOVE 不应被误判越域而触发重拉")

	allow, err := engine.Enforce("alice", "dom1", "order", "read")
	require.NoError(t, err)
	require.False(t, allow, "REMOVE 后该授权必须被真正撤销")
}

// snapStep 第 1 次 PullSnapshot 返回 v5，第 2 次起返回 vHi（模拟重拉对齐到更高版本）。
func snapStep(hi uint64) func(int) (*syncv1.Snapshot, error) {
	return func(call int) (*syncv1.Snapshot, error) {
		if call == 1 {
			return snapV5(), nil
		}
		return &syncv1.Snapshot{Version: hi, Rules: snapV5().Rules}, nil
	}
}

func TestSyncClient_HeartbeatAhead_TriggersResync(t *testing.T) {
	f := &fakeServer{
		snapFn:      snapStep(9),
		subscribeFn: sendThenBlock(heartbeatEv(9)), // 9 > 引导版本 5 → 漏包
	}
	c, engine, _ := startFake(t, f)
	runClient(t, c)

	require.Eventually(t, func() bool { return engine.Version() == 9 }, time.Second, 5*time.Millisecond)
	require.GreaterOrEqual(t, f.pullCount(), 2, "心跳超前应触发重拉")
}

func TestSyncClient_HeartbeatLevel_NoResync(t *testing.T) {
	addP := &syncv1.PolicyChange{
		Op:   syncv1.ChangeOp_CHANGE_OP_ADD,
		Rule: &syncv1.PolicyRule{Ptype: "p", Values: []string{"manager", "dom1", "order", "read", "allow"}},
	}
	f := &fakeServer{
		snapFn: func(int) (*syncv1.Snapshot, error) { return snapV5(), nil },
		subscribeFn: sendThenBlock(
			heartbeatEv(5), // 持平，不重拉
			deltaEv(&syncv1.Delta{Version: 6, PolicyChanges: []*syncv1.PolicyChange{addP}}),
		),
	}
	c, engine, _ := startFake(t, f)
	runClient(t, c)

	require.Eventually(t, func() bool { return engine.Version() == 6 }, time.Second, 5*time.Millisecond)
	require.Equal(t, 1, f.pullCount(), "持平心跳不应触发重拉，流照常消费后续 delta")
}

func TestSyncClient_SnapshotRequired_TriggersResync(t *testing.T) {
	f := &fakeServer{
		snapFn:      snapStep(8),
		subscribeFn: sendThenBlock(snapshotRequiredEv()),
	}
	c, engine, _ := startFake(t, f)
	runClient(t, c)

	require.Eventually(t, func() bool { return engine.Version() == 8 }, time.Second, 5*time.Millisecond)
	require.GreaterOrEqual(t, f.pullCount(), 2, "SnapshotRequired 应触发重拉")
}

func TestSyncClient_GapDelta_TriggersResync(t *testing.T) {
	f := &fakeServer{
		snapFn:      snapStep(10),
		subscribeFn: sendThenBlock(deltaEv(&syncv1.Delta{Version: 12})), // 12 > 5+1 → gap
	}
	c, engine, _ := startFake(t, f)
	runClient(t, c)

	require.Eventually(t, func() bool { return engine.Version() == 10 }, time.Second, 5*time.Millisecond)
	require.GreaterOrEqual(t, f.pullCount(), 2, "非连续 delta 必须重拉，绝不 apply 跳版变更")
}

// 重放/重复 delta（version ≤ 当前）必须丢弃，绝不触发重拉。
func TestSyncClient_ReplayDelta_Discarded(t *testing.T) {
	addP := func(act string) *syncv1.PolicyChange {
		return &syncv1.PolicyChange{
			Op:   syncv1.ChangeOp_CHANGE_OP_ADD,
			Rule: &syncv1.PolicyRule{Ptype: "p", Values: []string{"manager", "dom1", "order", act, "allow"}},
		}
	}
	f := &fakeServer{
		snapFn: func(int) (*syncv1.Snapshot, error) { return snapV5(), nil },
		subscribeFn: sendThenBlock(
			deltaEv(&syncv1.Delta{Version: 6, PolicyChanges: []*syncv1.PolicyChange{addP("read")}}),
			deltaEv(&syncv1.Delta{Version: 5}),                                                       // 重放（==引导版本）
			deltaEv(&syncv1.Delta{Version: 6}),                                                       // 重复（==当前）
			deltaEv(&syncv1.Delta{Version: 7, PolicyChanges: []*syncv1.PolicyChange{addP("write")}}), // 继续推进
		),
	}
	c, engine, _ := startFake(t, f)
	runClient(t, c)

	require.Eventually(t, func() bool { return engine.Version() == 7 }, time.Second, 5*time.Millisecond)
	require.Equal(t, 1, f.pullCount(), "重放/重复 delta 必须静默丢弃，绝不重拉")
}

func TestSyncClient_StreamError_ReconnectsAndRebootstraps(t *testing.T) {
	addP := &syncv1.PolicyChange{
		Op:   syncv1.ChangeOp_CHANGE_OP_ADD,
		Rule: &syncv1.PolicyRule{Ptype: "p", Values: []string{"manager", "dom1", "order", "read", "allow"}},
	}
	f := &fakeServer{
		snapFn: func(int) (*syncv1.Snapshot, error) { return snapV5(), nil },
		subscribeFn: func(call int, s syncv1.PolicySync_SubscribeServer) error {
			if call == 1 {
				return status.Error(codes.Unavailable, "boom") // 首连立即断
			}
			// 重连后正常推送一条连续 delta，然后保持
			if err := s.Send(deltaEv(&syncv1.Delta{Version: 6, PolicyChanges: []*syncv1.PolicyChange{addP}})); err != nil {
				return err
			}
			<-s.Context().Done()
			return s.Context().Err()
		},
	}
	c, engine, _ := startFake(t, f)
	runClient(t, c)

	require.Eventually(t, func() bool { return engine.Version() == 6 }, 2*time.Second, 5*time.Millisecond,
		"重连后应重新引导并消费新流 delta")
	require.GreaterOrEqual(t, f.pullCount(), 2, "重连必然重新 PullSnapshot 引导")
	require.True(t, c.Connected(), "稳态后 Connected 应为 true")
}
