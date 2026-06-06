package sydom

import (
	"context"
	"sync"

	authv1 "github.com/nickZFZ/Sydom/gen/sydom/auth/v1"
)

// Permission 是一条要上报的权限点目录元数据（功能权限定义）。
type Permission struct {
	Code        string
	Resource    string
	Action      string
	Type        string
	Name        string
	Description string
}

// ReportResult 是一次上报的写入统计。
type ReportResult struct {
	Upserted int // 新增或刷新（source=auto）
	Skipped  int // 命中人工配置（source=manual）被保留
}

// PermissionReporter 是 Registry 对上报端的窄依赖；*Client 自动满足。
type PermissionReporter interface {
	ReportPermissions(ctx context.Context, perms []Permission) (ReportResult, error)
}

// ReportPermissions 把权限点上报到本地 Sidecar（Sidecar 中继到控制面）。
// 上报是目录元数据、非鉴权决策：失败返回 error（codes.Unavailable→ErrUnavailable），
// 业务通常记日志后继续，不应因上报失败阻塞启动（fail-soft）。
func (c *Client) ReportPermissions(ctx context.Context, perms []Permission) (ReportResult, error) {
	in := &authv1.ReportPermissionsRequest{Permissions: make([]*authv1.PermissionPoint, len(perms))}
	for i, p := range perms {
		in.Permissions[i] = &authv1.PermissionPoint{
			Code: p.Code, Resource: p.Resource, Action: p.Action,
			Type: p.Type, Name: p.Name, Description: p.Description,
		}
	}
	resp, err := c.cli.ReportPermissions(ctx, in)
	if err != nil {
		return ReportResult{}, mapErr(err)
	}
	return ReportResult{Upserted: int(resp.GetUpserted()), Skipped: int(resp.GetSkipped())}, nil
}

// Registry 在进程内收集权限点，供启动时一次性上报。并发安全。
type Registry struct {
	mu    sync.Mutex
	perms []Permission
}

// NewRegistry 新建空注册表。
func NewRegistry() *Registry { return &Registry{} }

// Register 登记一条权限点（可在 init/启动期多处调用）。
func (r *Registry) Register(p Permission) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.perms = append(r.perms, p)
}

// Report 把已登记的权限点一次性上报。空集为 no-op，返回零值结果。
func (r *Registry) Report(ctx context.Context, reporter PermissionReporter) (ReportResult, error) {
	r.mu.Lock()
	snapshot := append([]Permission(nil), r.perms...)
	r.mu.Unlock()
	if len(snapshot) == 0 {
		return ReportResult{}, nil
	}
	return reporter.ReportPermissions(ctx, snapshot)
}
