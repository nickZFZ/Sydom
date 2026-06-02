package policysync

import (
	"context"
	"database/sql"
	"time"

	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
	"github.com/nickZFZ/Sydom/internal/auth"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/controlplane/translate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const maxMsgSize = 64 * 1024 * 1024 // 64MB，容纳全量快照（gRPC spec §8）

// Config 配置 Server 行为。
type Config struct {
	HeartbeatInterval time.Duration // 心跳间隔（~30s 生产值）
	BufSize           int           // 每流事件缓冲容量
}

// Server 实现 syncv1.PolicySyncServer：PullSnapshot 全量快照 + Subscribe 流式下发。
type Server struct {
	syncv1.UnimplementedPolicySyncServer
	db  *sql.DB
	hub *Hub
	cfg Config
}

// NewServer 构造 Server。
func NewServer(db *sql.DB, cfg Config) *Server {
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = 30 * time.Second
	}
	if cfg.BufSize <= 0 {
		cfg.BufSize = 256
	}
	return &Server{db: db, hub: NewHub(cfg.BufSize), cfg: cfg}
}

// Hub 暴露给广播订阅循环 Dispatch（任务 8 接线）。
func (s *Server) Hub() *Hub { return s.hub }

// PullSnapshot 在只读事务内读全量策略，保证 version 与 rules/data 一致。
func (s *Server) PullSnapshot(ctx context.Context, _ *syncv1.PullSnapshotRequest) (*syncv1.Snapshot, error) {
	appKey, ok := auth.AppIDFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "missing app identity")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin read tx: %v", err)
	}
	defer tx.Rollback()

	appID, err := store.ResolveAppIDByKey(ctx, tx, appKey)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "resolve app: %v", err)
	}
	version, err := store.ReadCurrentVersion(ctx, tx, appID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read version: %v", err)
	}
	rules, err := store.ReadAppRules(ctx, tx, appID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read rules: %v", err)
	}
	dps, err := store.ReadAppDataPolicies(ctx, tx, appID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read data policies: %v", err)
	}
	return &syncv1.Snapshot{
		Version:      uint64(version),
		Rules:        translate.RulesToProto(rules),
		DataPolicies: translate.DataPoliciesToProto(dps),
	}, nil
}

// Subscribe 订阅策略变更流：对账续传 + fan-out + 心跳。
func (s *Server) Subscribe(req *syncv1.SubscribeRequest, stream syncv1.PolicySync_SubscribeServer) error {
	ctx := stream.Context()
	appKey, ok := auth.AppIDFromContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "missing app identity")
	}
	appID, err := store.ResolveAppIDByKey(ctx, s.db, appKey)
	if err != nil {
		return status.Errorf(codes.NotFound, "resolve app: %v", err)
	}
	current, err := store.ReadCurrentVersion(ctx, s.db, appID)
	if err != nil {
		return status.Errorf(codes.Internal, "read version: %v", err)
	}

	// 先注册到 Hub，避免读 current 与注册之间漏掉 Dispatch 的 Delta（重复由 Sidecar 版本去重兜底）。
	sub := s.hub.register(appID)
	defer s.hub.unregister(sub)

	// 对账：last_applied 落后于 current（含冷启动 0<current）→ 先发 SnapshotRequired。
	if uint64(current) != req.LastAppliedVersion {
		if err := stream.Send(&syncv1.SyncEvent{Event: &syncv1.SyncEvent_SnapshotRequired{
			SnapshotRequired: &syncv1.SnapshotRequired{CurrentVersion: uint64(current), Reason: "behind"},
		}}); err != nil {
			return err
		}
	}

	// send-loop：events / overflow / heartbeat 三路。
	lastVer := uint64(current)
	const reconcileEvery = 10 // 每 10 个心跳兑正一次内存版本
	ticks := 0
	ticker := time.NewTicker(s.cfg.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil // Sidecar 断开 / 服务端关闭
		case ev := <-sub.events:
			if d := ev.GetDelta(); d != nil {
				lastVer = d.Version
			}
			if err := stream.Send(ev); err != nil {
				return err
			}
		case <-sub.overflow:
			if err := stream.Send(&syncv1.SyncEvent{Event: &syncv1.SyncEvent_SnapshotRequired{
				SnapshotRequired: &syncv1.SnapshotRequired{CurrentVersion: lastVer, Reason: "overflow"},
			}}); err != nil {
				return err
			}
		case <-ticker.C:
			ticks++
			if ticks%reconcileEvery == 0 {
				if v, err := store.ReadCurrentVersion(ctx, s.db, appID); err == nil {
					lastVer = uint64(v)
				}
			}
			if err := stream.Send(&syncv1.SyncEvent{Event: &syncv1.SyncEvent_Heartbeat{
				Heartbeat: &syncv1.Heartbeat{CurrentVersion: lastVer},
			}}); err != nil {
				return err
			}
		}
	}
}

// NewGRPCServer 组装带认证拦截器与 64MB 消息上限的 grpc.Server 并注册 PolicySync。
func NewGRPCServer(srv *Server, res auth.SecretResolver) *grpc.Server {
	g := grpc.NewServer(
		grpc.MaxRecvMsgSize(maxMsgSize),
		grpc.MaxSendMsgSize(maxMsgSize),
		grpc.UnaryInterceptor(auth.UnaryServerInterceptor(res)),
		grpc.StreamInterceptor(auth.StreamServerInterceptor(res)),
	)
	syncv1.RegisterPolicySyncServer(g, srv)
	return g
}
