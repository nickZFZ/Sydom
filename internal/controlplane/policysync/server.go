package policysync

import (
	"context"
	"database/sql"
	"time"

	syncv1 "github.com/nickZFZ/Sydom/gen/sydom/sync/v1"
	"github.com/nickZFZ/Sydom/internal/auth"
	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/broadcast"
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

// permissionReporter 是 Server 对策略管理器的窄依赖（*policy.PolicyManager 满足）。
type permissionReporter interface {
	ReportPermissions(ctx context.Context, appID int64, points []cp.PermissionPoint) (cp.ReportResult, error)
}

// Server 实现 syncv1.PolicySyncServer：PullSnapshot 全量快照 + Subscribe 流式下发。
type Server struct {
	syncv1.UnimplementedPolicySyncServer
	db       *sql.DB
	hub      *Hub
	cfg      Config
	reporter permissionReporter
}

// NewServer 构造 Server。
func NewServer(db *sql.DB, cfg Config, reporter permissionReporter) *Server {
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = 30 * time.Second
	}
	if cfg.BufSize <= 0 {
		cfg.BufSize = 256
	}
	return &Server{db: db, hub: NewHub(cfg.BufSize), cfg: cfg, reporter: reporter}
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
// opts 供注入额外 ServerOption（如 grpc.Creds）。
func NewGRPCServer(srv *Server, res auth.SecretResolver, opts ...grpc.ServerOption) *grpc.Server {
	base := []grpc.ServerOption{
		grpc.MaxRecvMsgSize(maxMsgSize),
		grpc.MaxSendMsgSize(maxMsgSize),
		grpc.UnaryInterceptor(auth.UnaryServerInterceptor(res)),
		grpc.StreamInterceptor(auth.StreamServerInterceptor(res)),
	}
	g := grpc.NewServer(append(base, opts...)...)
	syncv1.RegisterPolicySyncServer(g, srv)
	return g
}

// RunDispatchLoop 跑广播订阅循环：收到的每条 Delta 包成 SyncEvent 投给本地对应 app 的流。
// 阻塞直至 ctx 取消。每副本启动一次。
func (s *Server) RunDispatchLoop(ctx context.Context, sub broadcast.Subscriber) error {
	return sub.Run(ctx, func(appID int64, delta *syncv1.Delta) {
		s.hub.Dispatch(appID, &syncv1.SyncEvent{
			Event: &syncv1.SyncEvent_Delta{Delta: delta},
		})
	})
}

// ReportPermissions 接收权限点批量上报：app_id 由凭据强制解析，校验后委托 reporter。
func (s *Server) ReportPermissions(ctx context.Context, req *syncv1.ReportPermissionsRequest) (*syncv1.ReportPermissionsResponse, error) {
	appKey, ok := auth.AppIDFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "missing app identity")
	}
	points := make([]cp.PermissionPoint, 0, len(req.GetPermissions()))
	for _, p := range req.GetPermissions() {
		if p.GetCode() == "" || p.GetResource() == "" || p.GetAction() == "" {
			return nil, status.Error(codes.InvalidArgument, "permission code/resource/action 不可为空")
		}
		points = append(points, cp.PermissionPoint{
			Code: p.GetCode(), Resource: p.GetResource(), Action: p.GetAction(),
			Type: p.GetType(), Name: p.GetName(), Description: p.GetDescription(),
		})
	}
	appID, err := store.ResolveAppIDByKey(ctx, s.db, appKey)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "resolve app: %v", err)
	}
	res, err := s.reporter.ReportPermissions(ctx, appID, points)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "report permissions: %v", err)
	}
	return &syncv1.ReportPermissionsResponse{
		Upserted: uint32(res.Upserted), Skipped: uint32(res.Skipped),
	}, nil
}
