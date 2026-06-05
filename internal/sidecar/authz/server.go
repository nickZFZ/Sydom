package authz

import (
	"context"
	"errors"

	authv1 "github.com/nickZFZ/Sydom/gen/sydom/auth/v1"
	"github.com/nickZFZ/Sydom/internal/sidecar/dataperm"
	"github.com/nickZFZ/Sydom/internal/sidecar/kernel"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)

// Server 把 Authorizer 适配为 gRPC AuthService。
type Server struct {
	authv1.UnimplementedAuthServiceServer
	a *Authorizer
}

// NewServer 包装 Authorizer 为 gRPC handler。
func NewServer(a *Authorizer) *Server { return &Server{a: a} }

// NewGRPCServer 装配带 AuthService 的 grpc.Server（供 cmd 监听本地端点）。
func NewGRPCServer(a *Authorizer) *grpc.Server {
	g := grpc.NewServer()
	authv1.RegisterAuthServiceServer(g, NewServer(a))
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
