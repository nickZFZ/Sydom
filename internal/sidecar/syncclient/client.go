package syncclient

import (
	"context"
	"sync/atomic"
	"time"

	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
	"github.com/nickZFZ/Sydom/internal/auth"
	"github.com/nickZFZ/Sydom/internal/sidecar/kernel"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// SyncClient 把 Sidecar 接成控制面 PolicySync 的订阅客户端：持续对账并把结果喂给注入的
// *kernel.Engine。不做任何鉴权决策；最终 fail-close 由 ④-4 在 !Ready() 时拒绝。
type SyncClient struct {
	cfg    Config
	engine *kernel.Engine

	conn   *grpc.ClientConn
	client syncv1.PolicySyncClient

	lastSyncAt atomic.Int64 // UnixNano；0 表示从未成功同步
	connected  atomic.Bool  // 订阅流是否在线
}

// New 拨号控制面并构造 SyncClient（不启动对账，调用方另起 goroutine 调 Run）。
// Secure=false 时注入 insecure 传输凭据；Secure=true 时传输凭据须由 cfg.DialOptions 提供。
func New(cfg Config, engine *kernel.Engine) (*SyncClient, error) {
	opts := []grpc.DialOption{
		grpc.WithPerRPCCredentials(auth.NewPerRPCCredentials(cfg.AppID, cfg.Secret, cfg.Secure)),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(maxRecvMsgSize)),
	}
	if !cfg.Secure {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}
	opts = append(opts, cfg.DialOptions...)

	conn, err := grpc.NewClient(cfg.Endpoint, opts...)
	if err != nil {
		return nil, err
	}
	return &SyncClient{
		cfg:    cfg,
		engine: engine,
		conn:   conn,
		client: syncv1.NewPolicySyncClient(conn),
	}, nil
}

// Version/Ready 透传内核；Connected/LastSyncAt 是自身连接态，供 ④-4 自定 fail-open/close 阈值。
func (c *SyncClient) Version() uint64 { return c.engine.Version() }
func (c *SyncClient) Ready() bool     { return c.engine.Ready() }
func (c *SyncClient) Connected() bool { return c.connected.Load() }

func (c *SyncClient) LastSyncAt() time.Time {
	ns := c.lastSyncAt.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

// Close 关闭底层连接（Run 退出后调用）。
func (c *SyncClient) Close() error { return c.conn.Close() }

func (c *SyncClient) markSync() { c.lastSyncAt.Store(time.Now().UnixNano()) }

// Run 阻塞式对账循环：引导 → 订阅消费 → 断连退避重连。ctx 取消即干净退出返回 nil。
func (c *SyncClient) Run(ctx context.Context) error {
	bo := newBackoff(c.cfg.BackoffInitial, c.cfg.BackoffMax)
	for {
		if ctx.Err() != nil {
			return nil
		}
		if err := c.connectAndServe(ctx, bo); err != nil {
			if !c.sleep(ctx, bo.next()) {
				return nil
			}
			continue
		}
		return nil // connectAndServe 返回 nil = ctx 取消
	}
}

// connectAndServe 引导 + 订阅消费，直到流断（返回 err 触发重连）或 ctx 取消（返回 nil）。
func (c *SyncClient) connectAndServe(ctx context.Context, bo *backoff) error {
	if err := c.bootstrap(ctx); err != nil {
		return err
	}
	stream, err := c.client.Subscribe(ctx, &syncv1.SubscribeRequest{
		LastAppliedVersion: c.engine.Version(),
	})
	if err != nil {
		return err
	}
	c.connected.Store(true)
	defer c.connected.Store(false)

	for {
		ev, err := stream.Recv()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err // 流断 → 退避重连
		}
		bo.reset() // 收到事件 = 流健康，退避归零
		if err := c.handleEvent(ctx, ev); err != nil {
			return err // resync 连接级失败 → 升级为重连
		}
	}
}

// bootstrap 显式拉全量快照建基线（PullSnapshot→ApplySnapshot），使空策略 app 也达 Ready=true。
func (c *SyncClient) bootstrap(ctx context.Context) error { return c.resync(ctx) }

// resync 重拉全量快照对齐内核：成功刷新 lastSyncAt 返回 nil（继续消费同一流）；失败返回 err。
func (c *SyncClient) resync(ctx context.Context) error {
	snap, err := c.client.PullSnapshot(ctx, &syncv1.PullSnapshotRequest{})
	if err != nil {
		return err
	}
	ks, err := snapshotFromProto(snap)
	if err != nil {
		return err
	}
	if err := c.engine.ApplySnapshot(ks); err != nil {
		return err
	}
	c.markSync()
	return nil
}

// handleEvent 按事件类型分派。
func (c *SyncClient) handleEvent(ctx context.Context, ev *syncv1.SyncEvent) error {
	switch e := ev.GetEvent().(type) {
	case *syncv1.SyncEvent_Delta:
		return c.handleDelta(ctx, e.Delta)
	case *syncv1.SyncEvent_Heartbeat:
		if e.Heartbeat.GetCurrentVersion() > c.engine.Version() {
			return c.resync(ctx) // 漏包 → 重拉
		}
		c.markSync() // 流活性证明
		return nil
	case *syncv1.SyncEvent_SnapshotRequired:
		return c.resync(ctx)
	default:
		return nil // 未知事件忽略（前向兼容）
	}
}

// handleDelta 仅当版本连续（==Version()+1）才增量 apply；非连续 → 重拉（gap 兜底）。
// 落后/重放（≤Version()）与 ErrStaleVersion 的精细处理见 Task 7。
func (c *SyncClient) handleDelta(ctx context.Context, d *syncv1.Delta) error {
	if d.GetVersion() == c.engine.Version()+1 {
		kd, err := deltaFromProto(d)
		if err != nil {
			return c.resync(ctx) // 翻译失败 → 重拉，绝不部分应用
		}
		if err := c.engine.ApplyDelta(kd); err != nil {
			return c.resync(ctx) // apply 失败 → 重拉
		}
		c.markSync()
		return nil
	}
	return c.resync(ctx) // 非连续（含 gap） → 重拉
}

// sleep 睡 d，期间 ctx 取消则返回 false（应退出）。
func (c *SyncClient) sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
