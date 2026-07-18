package mgmt

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/auth"
	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/policy"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/obs"
	"github.com/nickZFZ/Sydom/internal/sidecar/dataperm"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const maxMsgSize = 16 * 1024 * 1024

// maxBatchItems 是单次批量写允许的最大条目数（防止超大批把单事务/单锁窗口拖得过长）。
const maxBatchItems = 1000

// AdminServer 实现 adminv1.AdminServiceServer。
type AdminServer struct {
	adminv1.UnimplementedAdminServiceServer
	db        *sql.DB
	mgr       *policy.PolicyManager
	masterKey []byte // 加密新建的 app/operator 凭据（任务 11 用）
}

// NewAdminServer 构造。masterKey 用于加密 CreateApplication/CreateOperator 生成的凭据。
func NewAdminServer(db *sql.DB, mgr *policy.PolicyManager, masterKey []byte) *AdminServer {
	k := make([]byte, len(masterKey))
	copy(k, masterKey)
	return &AdminServer{db: db, mgr: mgr, masterKey: k}
}

// writeResp 把 (delta, err) 归一为 WriteResponse；delta==nil 表示无策略影响。
func writeResp(d *cp.Delta, err error) (*adminv1.WriteResponse, error) {
	if err != nil {
		return nil, mapWriteErr("write", err)
	}
	if d == nil {
		return &adminv1.WriteResponse{Changed: false}, nil
	}
	return &adminv1.WriteResponse{Version: uint64(d.Version), Changed: true}, nil
}

// mapWriteErr 把 PolicyManager 写方法的错误按领域 sentinel 细化为 gRPC status，取代先前
// 一律 codes.Internal 的粗粒度映射（调用方由此拿到可操作的语义码而非笼统「internal error」）。
//
// 安全边界：分类码（AlreadyExists/FailedPrecondition/NotFound）不经 SanitizeErrorUnaryInterceptor
// 脱敏、直达客户端，故一律回干净固定文案，绝不把 raw 驱动详情（约束名/表名/SQL）插值进 message。
// ErrInvalidInput 的内容全部来自我方 curated 校验串（含调用方自身的非法输入回显），可安全带上下文。
// 未分类错误落 Internal，由脱敏拦截器统一转通用文案、原始详情仅入日志（与既有行为一致）。
func mapWriteErr(what string, err error) error {
	switch {
	case errors.Is(err, policy.ErrInvalidInput):
		return status.Errorf(codes.InvalidArgument, "%s: %v", what, err)
	case errors.Is(err, policy.ErrConflict):
		return status.Error(codes.AlreadyExists, what+"：目标已存在或与现有记录冲突")
	case errors.Is(err, policy.ErrNotFound):
		return status.Error(codes.NotFound, what+"：目标不存在")
	case errors.Is(err, policy.ErrPrecondition):
		return status.Error(codes.FailedPrecondition, what+"：当前状态不满足操作前提（引用缺失或将形成角色环）")
	default:
		return status.Errorf(codes.Internal, "%s: %v", what, err)
	}
}

func (s *AdminServer) CreateRole(ctx context.Context, r *adminv1.CreateRoleRequest) (*adminv1.CreateRoleResponse, error) {
	roleID, d, err := s.mgr.CreateRole(ctx, int64(r.AppId), r.Code, r.Name)
	if err != nil {
		return nil, mapWriteErr("create role", err)
	}
	resp := &adminv1.CreateRoleResponse{RoleId: roleID}
	if d != nil {
		resp.Version, resp.Changed = uint64(d.Version), true
	}
	return resp, nil
}

func (s *AdminServer) DeleteRole(ctx context.Context, r *adminv1.DeleteRoleRequest) (*adminv1.WriteResponse, error) {
	return writeResp(s.mgr.DeleteRole(ctx, int64(r.AppId), r.RoleId))
}

func (s *AdminServer) UpsertPermission(ctx context.Context, r *adminv1.UpsertPermissionRequest) (*adminv1.UpsertPermissionResponse, error) {
	permID, d, err := s.mgr.UpsertPermission(ctx, int64(r.AppId), r.Code, r.Resource, r.Action, r.Ptype, r.Name)
	if err != nil {
		return nil, mapWriteErr("upsert permission", err)
	}
	resp := &adminv1.UpsertPermissionResponse{PermissionId: permID}
	if d != nil {
		resp.Version, resp.Changed = uint64(d.Version), true
	}
	return resp, nil
}

func (s *AdminServer) GrantPermission(ctx context.Context, r *adminv1.GrantPermissionRequest) (*adminv1.WriteResponse, error) {
	return writeResp(s.mgr.GrantPermission(ctx, int64(r.AppId), r.RoleId, r.PermissionId, r.Eft))
}
func (s *AdminServer) RevokePermission(ctx context.Context, r *adminv1.RevokePermissionRequest) (*adminv1.WriteResponse, error) {
	return writeResp(s.mgr.RevokePermission(ctx, int64(r.AppId), r.RoleId, r.PermissionId))
}
func (s *AdminServer) AddRoleInheritance(ctx context.Context, r *adminv1.RoleInheritanceRequest) (*adminv1.WriteResponse, error) {
	return writeResp(s.mgr.AddRoleInheritance(ctx, int64(r.AppId), r.ChildRoleId, r.ParentRoleId))
}
func (s *AdminServer) RemoveRoleInheritance(ctx context.Context, r *adminv1.RoleInheritanceRequest) (*adminv1.WriteResponse, error) {
	return writeResp(s.mgr.RemoveRoleInheritance(ctx, int64(r.AppId), r.ChildRoleId, r.ParentRoleId))
}
func (s *AdminServer) BindUserRole(ctx context.Context, r *adminv1.UserRoleRequest) (*adminv1.WriteResponse, error) {
	return writeResp(s.mgr.BindUserRole(ctx, int64(r.AppId), r.UserId, r.RoleId))
}
func (s *AdminServer) UnbindUserRole(ctx context.Context, r *adminv1.UserRoleRequest) (*adminv1.WriteResponse, error) {
	return writeResp(s.mgr.UnbindUserRole(ctx, int64(r.AppId), r.UserId, r.RoleId))
}
func (s *AdminServer) UpsertDataPolicy(ctx context.Context, r *adminv1.UpsertDataPolicyRequest) (*adminv1.UpsertDataPolicyResponse, error) {
	eff := r.Effect
	if eff == "" {
		eff = cp.EffectAllow
	}
	if eff != cp.EffectAllow && eff != cp.EffectDeny {
		return nil, status.Errorf(codes.InvalidArgument, "invalid effect %q (want allow|deny)", r.Effect)
	}
	if err := dataperm.ValidateCondition(r.Condition); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid condition: %v", err)
	}
	d, err := s.mgr.UpsertDataPolicy(ctx, int64(r.AppId), cp.DataPolicy{
		ID: r.Id, SubjectType: r.SubjectType, SubjectID: r.SubjectId, Resource: r.Resource, Condition: r.Condition, Effect: eff, Description: r.Description,
	})
	if err != nil {
		return nil, mapWriteErr("upsert data policy", err)
	}
	resp := &adminv1.UpsertDataPolicyResponse{}
	if d != nil && len(d.DataChanges) > 0 {
		resp.DataPolicyId = d.DataChanges[0].Policy.ID
		resp.Version, resp.Changed = uint64(d.Version), true
	}
	return resp, nil
}
func (s *AdminServer) DeleteDataPolicy(ctx context.Context, r *adminv1.DeleteDataPolicyRequest) (*adminv1.WriteResponse, error) {
	return writeResp(s.mgr.DeleteDataPolicy(ctx, int64(r.AppId), r.DataPolicyId))
}

func (s *AdminServer) ExportAppPolicy(ctx context.Context, r *adminv1.ExportAppPolicyRequest) (*adminv1.ExportAppPolicyResponse, error) {
	content, err := s.mgr.ExportAppPolicy(ctx, int64(r.AppId), r.Format)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "export policy: %v", err)
	}
	return &adminv1.ExportAppPolicyResponse{Content: content}, nil
}

func (s *AdminServer) ImportAppPolicy(ctx context.Context, r *adminv1.ImportAppPolicyRequest) (*adminv1.ImportAppPolicyResponse, error) {
	plan, version, _, err := s.mgr.ImportAppPolicy(ctx, int64(r.AppId), []byte(r.Content), r.DryRun)
	if err != nil {
		switch {
		case errors.Is(err, policy.ErrImportConflict):
			return nil, status.Errorf(codes.FailedPrecondition, "import policy: %v", err)
		case errors.Is(err, policy.ErrImportInvalid):
			return nil, status.Errorf(codes.InvalidArgument, "import policy: %v", err)
		default:
			return nil, status.Errorf(codes.Internal, "import policy: %v", err)
		}
	}
	resp := &adminv1.ImportAppPolicyResponse{
		Applied: !r.DryRun,
		Version: version,
	}
	for _, it := range plan.Items {
		resp.Diff = append(resp.Diff, &adminv1.PolicyDiffEntry{
			Kind: it.Kind, EntityType: it.EntityType, Identity: it.Identity, Detail: it.Detail,
		})
	}
	resp.Creates = int32(plan.Count("create"))
	resp.Adopts = int32(plan.Count("adopt"))
	resp.Updates = int32(plan.Count("update"))
	resp.Deletes = int32(plan.Count("delete"))
	resp.Conflicts = int32(plan.Count("conflict"))
	return resp, nil
}

// batchResp 把 (delta, requested, applied, err) 归一为 BatchWriteResponse；delta==nil 表示无策略影响。
// 形参顺序刻意对齐 BatchWriteResponse 的字段顺序（requested 先于 applied），二者同为 int 时防手滑传反。
func batchResp(d *cp.Delta, requested, applied int, err error) (*adminv1.BatchWriteResponse, error) {
	if err != nil {
		return nil, mapWriteErr("batch write", err)
	}
	resp := &adminv1.BatchWriteResponse{
		Requested: uint32(requested),
		Applied:   uint32(applied),
	}
	if d != nil {
		resp.Version, resp.Changed = uint64(d.Version), true
	}
	return resp, nil
}

func (s *AdminServer) BatchUnbindUserRole(ctx context.Context, r *adminv1.BatchUnbindUserRoleRequest) (*adminv1.BatchWriteResponse, error) {
	if len(r.Items) == 0 || len(r.Items) > maxBatchItems {
		return nil, status.Errorf(codes.InvalidArgument, "items 数须在 1..%d", maxBatchItems)
	}
	pairs := make([]store.UserRolePair, len(r.Items))
	for i, it := range r.Items {
		pairs[i] = store.UserRolePair{UserID: it.UserId, RoleID: it.RoleId}
	}
	d, applied, err := s.mgr.BatchUnbindUserRole(ctx, int64(r.AppId), pairs)
	return batchResp(d, len(r.Items), applied, err)
}

func (s *AdminServer) BatchRevokePermission(ctx context.Context, r *adminv1.BatchRevokePermissionRequest) (*adminv1.BatchWriteResponse, error) {
	if len(r.Items) == 0 || len(r.Items) > maxBatchItems {
		return nil, status.Errorf(codes.InvalidArgument, "items 数须在 1..%d", maxBatchItems)
	}
	pairs := make([]store.GrantPair, len(r.Items))
	for i, it := range r.Items {
		pairs[i] = store.GrantPair{RoleID: it.RoleId, PermissionID: it.PermissionId}
	}
	d, applied, err := s.mgr.BatchRevokePermission(ctx, int64(r.AppId), pairs)
	return batchResp(d, len(r.Items), applied, err)
}

func (s *AdminServer) BatchRemoveRoleInheritance(ctx context.Context, r *adminv1.BatchRemoveRoleInheritanceRequest) (*adminv1.BatchWriteResponse, error) {
	if len(r.Items) == 0 || len(r.Items) > maxBatchItems {
		return nil, status.Errorf(codes.InvalidArgument, "items 数须在 1..%d", maxBatchItems)
	}
	pairs := make([]store.InheritancePair, len(r.Items))
	for i, it := range r.Items {
		pairs[i] = store.InheritancePair{ChildRoleID: it.ChildRoleId, ParentRoleID: it.ParentRoleId}
	}
	d, applied, err := s.mgr.BatchRemoveRoleInheritance(ctx, int64(r.AppId), pairs)
	return batchResp(d, len(r.Items), applied, err)
}

func (s *AdminServer) BatchDeleteRole(ctx context.Context, r *adminv1.BatchDeleteRoleRequest) (*adminv1.BatchWriteResponse, error) {
	if len(r.RoleIds) == 0 || len(r.RoleIds) > maxBatchItems {
		return nil, status.Errorf(codes.InvalidArgument, "role_ids 数须在 1..%d", maxBatchItems)
	}
	d, applied, err := s.mgr.BatchDeleteRole(ctx, int64(r.AppId), r.RoleIds)
	return batchResp(d, len(r.RoleIds), applied, err)
}

func (s *AdminServer) BatchDeleteDataPolicy(ctx context.Context, r *adminv1.BatchDeleteDataPolicyRequest) (*adminv1.BatchWriteResponse, error) {
	if len(r.DataPolicyIds) == 0 || len(r.DataPolicyIds) > maxBatchItems {
		return nil, status.Errorf(codes.InvalidArgument, "data_policy_ids 数须在 1..%d", maxBatchItems)
	}
	d, applied, err := s.mgr.BatchDeleteDataPolicy(ctx, int64(r.AppId), r.DataPolicyIds)
	return batchResp(d, len(r.DataPolicyIds), applied, err)
}

// NewGRPCServer 装配观测→脱敏→认证→鉴权→status 五拦截器（按序）并注册 AdminService。
// opts 供调用方注入额外 ServerOption（如 grpc.Creds 启用 TLS）。logger 供错误脱敏边界记录原始细节。
// m 记 RED 指标 + 访问日志，挂在链最外层（计入被 auth/authz 拒绝的请求）；m==nil 时退化为纯访问日志。
func NewGRPCServer(srv *AdminServer, resolver auth.SecretResolver, enf *adminauthz.Enforcer, db *sql.DB, logger *slog.Logger, m *obs.Metrics, opts ...grpc.ServerOption) *grpc.Server {
	chain := grpc.ChainUnaryInterceptor(
		m.UnaryServerInterceptor(logger),                                    // 0. 最外层：RED 指标 + 访问日志（含被拒请求）
		SanitizeErrorUnaryInterceptor(logger),                               // 1. Internal/Unknown 错误脱敏边界（细节仅日志）
		auth.UnaryServerInterceptorExempt(resolver, UnauthenticatedMethods), // 2. HMAC 认证（RegisterTenant 免鉴权）→ 注入 principal
		AuthzUnaryInterceptor(enf),                                          // 3. 元-RBAC 鉴权 → 注入 cp.WithOperator
		StatusWriteUnaryInterceptor(db),                                     // 4. status 写拦截
	)
	base := []grpc.ServerOption{grpc.MaxRecvMsgSize(maxMsgSize), grpc.MaxSendMsgSize(maxMsgSize), chain}
	g := grpc.NewServer(append(base, opts...)...)
	adminv1.RegisterAdminServiceServer(g, srv)
	return g
}
