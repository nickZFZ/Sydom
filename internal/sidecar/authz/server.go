package authz

import (
	"context"
	"errors"

	authv1 "github.com/nickZFZ/Sydom/gen/sydom/auth/v1"
	"github.com/nickZFZ/Sydom/internal/sidecar/dataperm"
	"github.com/nickZFZ/Sydom/internal/sidecar/kernel"
	"github.com/nickZFZ/Sydom/internal/sidecar/syncclient"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)

// Server 把 Authorizer 适配为 gRPC AuthService，并中继权限点上报到控制面。
type Server struct {
	authv1.UnimplementedAuthServiceServer
	a     *Authorizer
	relay PermissionRelay
}

// PermissionRelay 是 Server 对上报中继的窄依赖（*syncclient.SyncClient 满足）。
type PermissionRelay interface {
	ReportPermissions(ctx context.Context, points []syncclient.PermissionPoint) (syncclient.ReportResult, error)
}

// NewServer 包装 Authorizer 为 gRPC handler，relay 转发权限点上报。
func NewServer(a *Authorizer, relay PermissionRelay) *Server { return &Server{a: a, relay: relay} }

// NewGRPCServer 装配带 AuthService 的 grpc.Server（供 cmd 监听本地端点）。
func NewGRPCServer(a *Authorizer, relay PermissionRelay) *grpc.Server {
	g := grpc.NewServer()
	authv1.RegisterAuthServiceServer(g, NewServer(a, relay))
	return g
}

func (s *Server) Check(_ context.Context, req *authv1.CheckRequest) (*authv1.CheckResponse, error) {
	allowed, err := s.a.Check(req.GetSubject(), req.GetObject(), req.GetAction())
	if err != nil {
		return nil, toStatus(err)
	}
	return &authv1.CheckResponse{Allowed: allowed}, nil
}

func (s *Server) BatchCheck(_ context.Context, req *authv1.BatchCheckRequest) (*authv1.BatchCheckResponse, error) {
	reqs := make([]CheckReq, len(req.GetRequests()))
	for i, r := range req.GetRequests() {
		reqs[i] = CheckReq{Subject: r.GetSubject(), Object: r.GetObject(), Action: r.GetAction()}
	}
	allowed, err := s.a.BatchCheck(reqs)
	if err != nil {
		return nil, toStatus(err)
	}
	return &authv1.BatchCheckResponse{Allowed: allowed}, nil
}

func (s *Server) FilterSQL(_ context.Context, req *authv1.FilterRequest) (*authv1.FilterSQLResponse, error) {
	res, err := s.a.FilterSQL(req.GetSubject(), req.GetResource(), req.GetAttrs().AsMap())
	if err != nil {
		return nil, toStatus(err)
	}
	args := make([]*structpb.Value, len(res.Args))
	for i, v := range res.Args {
		val, verr := structpb.NewValue(v)
		if verr != nil {
			return nil, status.Errorf(codes.Internal, "encode arg %d: %v", i, verr)
		}
		args[i] = val
	}
	return &authv1.FilterSQLResponse{Sql: res.SQL, Args: args}, nil
}

// ReportPermissions 把业务进程的权限点上报译为域中性点、委托 relay 转发到控制面。
// 上报是 fail-soft 的目录元数据写入：失败返回 error 交业务自定处理，不影响鉴权。
func (s *Server) ReportPermissions(ctx context.Context, req *authv1.ReportPermissionsRequest) (*authv1.ReportPermissionsResponse, error) {
	points := make([]syncclient.PermissionPoint, len(req.GetPermissions()))
	for i, p := range req.GetPermissions() {
		points[i] = syncclient.PermissionPoint{
			Code: p.GetCode(), Resource: p.GetResource(), Action: p.GetAction(),
			Type: p.GetType(), Name: p.GetName(), Description: p.GetDescription(),
		}
	}
	res, err := s.relay.ReportPermissions(ctx, points)
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "report permissions: %v", err)
	}
	return &authv1.ReportPermissionsResponse{
		Upserted: uint32(res.Upserted), Skipped: uint32(res.Skipped),
	}, nil
}

// toStatus 把领域错误映射为 gRPC status：
// not-ready/too-stale→Unavailable（无法判定，调用方自定 fail-open/close）；
// ErrMissingVar→InvalidArgument（调用方入参）；ErrInvalidPolicy→FailedPrecondition（服务端数据损坏）；
// ErrForeignDomain/其它→Internal（配置错/未预期）。
func toStatus(err error) error {
	switch {
	case errors.Is(err, kernel.ErrNotReady), errors.Is(err, ErrTooStale):
		return status.Error(codes.Unavailable, err.Error())
	case errors.Is(err, dataperm.ErrMissingVar):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, dataperm.ErrInvalidPolicy):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, kernel.ErrForeignDomain):
		return status.Error(codes.Internal, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}
