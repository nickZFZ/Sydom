package sydom

import (
	"context"
	"fmt"

	authv1 "github.com/nickZFZ/Sydom/gen/sydom/auth/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)

// CheckReq 是 BatchCheck 的单条请求。
type CheckReq struct {
	Subject string
	Object  string
	Action  string
}

// FilterResult 是数据权限的参数化 SQL 片段：值全在 Args，绝不进 SQL 文本。
type FilterResult struct {
	SQL  string // 无过滤=空串；deny-all="1=0"；否则参数化片段
	Args []any  // 占位符实参（JSON 标量）
}

// Client 封装与同机 Sidecar AuthService 的 gRPC 连接。并发安全（底层 gRPC 连接并发安全）。
type Client struct {
	conn     *grpc.ClientConn
	cli      authv1.AuthServiceClient
	ownsConn bool
}

// New 连接 target（loopback，如 "127.0.0.1:8090"，对齐 Sidecar auth_addr）。
// 默认 insecure 传输——业务→Sidecar 本地回环 v1 不加 HMAC。用 WithConn 可注入既有连接。
func New(target string, opts ...Option) (*Client, error) {
	var cfg config
	for _, o := range opts {
		o(&cfg)
	}

	if cfg.conn != nil {
		return &Client{conn: cfg.conn, cli: authv1.NewAuthServiceClient(cfg.conn), ownsConn: false}, nil
	}

	dialOpts := append([]grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}, cfg.dialOpts...)
	conn, err := grpc.NewClient(target, dialOpts...)
	if err != nil {
		return nil, fmt.Errorf("sydom: dial %q: %w", target, err)
	}
	return &Client{conn: conn, cli: authv1.NewAuthServiceClient(conn), ownsConn: true}, nil
}

// Close 关闭自持连接；注入连接（WithConn）时为 no-op。
func (c *Client) Close() error {
	if c.ownsConn {
		return c.conn.Close()
	}
	return nil
}

// Check 判定单条功能权限。出错恒返回 (false, err)，绝不 (true, err)——fail-close。
func (c *Client) Check(ctx context.Context, subject, object, action string) (bool, error) {
	resp, err := c.cli.Check(ctx, &authv1.CheckRequest{Subject: subject, Object: object, Action: action})
	if err != nil {
		return false, mapErr(err)
	}
	return resp.GetAllowed(), nil
}

// BatchCheck 批量判定，结果与请求等长同序。出错返回 (nil, err)。
func (c *Client) BatchCheck(ctx context.Context, reqs []CheckReq) ([]bool, error) {
	in := &authv1.BatchCheckRequest{Requests: make([]*authv1.CheckRequest, len(reqs))}
	for i, r := range reqs {
		in.Requests[i] = &authv1.CheckRequest{Subject: r.Subject, Object: r.Object, Action: r.Action}
	}
	resp, err := c.cli.BatchCheck(ctx, in)
	if err != nil {
		return nil, mapErr(err)
	}
	got := resp.GetAllowed()
	if len(got) != len(reqs) {
		return nil, fmt.Errorf("sydom: BatchCheck 响应长度 %d 与请求数 %d 不一致", len(got), len(reqs))
	}
	return got, nil
}

// FilterSQL 返回数据权限的参数化 SQL 片段。attrs 为 $user.xxx 变量取值（JSON 标量）。
func (c *Client) FilterSQL(ctx context.Context, subject, resource string, attrs map[string]any) (FilterResult, error) {
	s, err := structpb.NewStruct(attrs)
	if err != nil {
		return FilterResult{}, fmt.Errorf("sydom: encode attrs: %w", err)
	}
	resp, err := c.cli.FilterSQL(ctx, &authv1.FilterRequest{Subject: subject, Resource: resource, Attrs: s})
	if err != nil {
		return FilterResult{}, mapErr(err)
	}
	args := make([]any, len(resp.GetArgs()))
	for i, v := range resp.GetArgs() {
		args[i] = v.AsInterface()
	}
	return FilterResult{SQL: resp.GetSql(), Args: args}, nil
}

// mapErr 把 gRPC status 译为 SDK 错误：Unavailable→ErrUnavailable（无法判定，与传输断线统一）；
// 其它码原样返回（保留 gRPC status，调用方可 status.FromError）。
func mapErr(err error) error {
	if status.Code(err) == codes.Unavailable {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return err
}
