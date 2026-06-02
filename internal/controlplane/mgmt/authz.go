package mgmt

import (
	"context"
	"database/sql"
	"strconv"

	"github.com/nickZFZ/Sydom/internal/auth"
	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// appIDGetter 是带 app_id 的请求消息（业务策略写与应用 status 写均含 app_id）。
type appIDGetter interface{ GetAppId() uint64 }

// rpcRule 描述某 RPC 的鉴权要素。
type rpcRule struct {
	resource string
	action   string
	isWrite  bool // 是否受 status 写拦截（仅针对具体 app 的业务策略写）
	system   bool // true=system 域（"*"），不取请求 app_id
}

// ruleTable 把 FullMethod 映射到鉴权要素。集中维护，避免散落判断。
// 只读：初始化后不得修改（无锁并发读安全）。
var ruleTable = map[string]rpcRule{
	"/sydom.admin.v1.AdminService/CreateRole":            {"role", "create", true, false},
	"/sydom.admin.v1.AdminService/DeleteRole":            {"role", "delete", true, false},
	"/sydom.admin.v1.AdminService/UpsertPermission":      {"permission", "update", true, false},
	"/sydom.admin.v1.AdminService/GrantPermission":       {"grant", "create", true, false},
	"/sydom.admin.v1.AdminService/RevokePermission":      {"grant", "delete", true, false},
	"/sydom.admin.v1.AdminService/AddRoleInheritance":    {"inheritance", "create", true, false},
	"/sydom.admin.v1.AdminService/RemoveRoleInheritance": {"inheritance", "delete", true, false},
	"/sydom.admin.v1.AdminService/BindUserRole":          {"binding", "create", true, false},
	"/sydom.admin.v1.AdminService/UnbindUserRole":        {"binding", "delete", true, false},
	"/sydom.admin.v1.AdminService/UpsertDataPolicy":      {"data_policy", "update", true, false},
	"/sydom.admin.v1.AdminService/DeleteDataPolicy":      {"data_policy", "delete", true, false},
	"/sydom.admin.v1.AdminService/CreateApplication":     {"application", "create", false, true},
	"/sydom.admin.v1.AdminService/SetApplicationStatus":  {"application", "update", false, false}, // system=false：在目标 app 自身域校验（本域管理员可停/启自身应用，跨 app 需 * 域超管）
	"/sydom.admin.v1.AdminService/ListApplications":      {"application", "read", false, true},
	"/sydom.admin.v1.AdminService/CreateOperator":        {"admin", "create", false, true},
	"/sydom.admin.v1.AdminService/SetOperatorStatus":     {"admin", "update", false, true},
	"/sydom.admin.v1.AdminService/CreateAdminRole":       {"admin", "create", false, true},
	"/sydom.admin.v1.AdminService/GrantAdminRole":        {"admin", "update", false, true},
	"/sydom.admin.v1.AdminService/BindOperatorRole":      {"admin", "update", false, true},
}

// DomainOfAppID 把 app_id 转成 casbin domain 字符串。
func DomainOfAppID(appID int64) string { return strconv.FormatInt(appID, 10) }

// AuthzUnaryInterceptor 据 ruleTable + 请求 app_id 做元-RBAC 校验，并注入 operator 到 cp.WithOperator。
func AuthzUnaryInterceptor(enf *adminauthz.Enforcer) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		principal, ok := auth.AppIDFromContext(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "missing operator identity")
		}
		rule, known := ruleTable[info.FullMethod]
		if !known {
			return nil, status.Error(codes.PermissionDenied, "unknown method")
		}
		domain := "*"
		if !rule.system {
			g, ok := req.(appIDGetter)
			if !ok {
				return nil, status.Error(codes.Internal, "request missing app_id")
			}
			domain = DomainOfAppID(int64(g.GetAppId()))
		}
		allow, err := enf.Enforce(ctx, principal, domain, rule.resource, rule.action)
		// TODO(observability): Enforce 内部错误（DB/策略加载故障）当前与"权限不足"一并 fail-close 为 PermissionDenied；接入日志/metric 后在此区分并记录。
		if err != nil || !allow {
			return nil, status.Error(codes.PermissionDenied, "permission denied")
		}
		return handler(cp.WithOperator(ctx, principal), req)
	}
}

// StatusWriteUnaryInterceptor 对"具体 app 的业务策略写"拦截 disabled app。
// 装配契约：必须排在 AuthzUnaryInterceptor 之后——本拦截器不校验 app 归属，若先于鉴权执行，会借 NotFound/FailedPrecondition 差异泄露 app 存在性。
func StatusWriteUnaryInterceptor(db *sql.DB) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		rule, known := ruleTable[info.FullMethod]
		if !known || !rule.isWrite {
			return handler(ctx, req)
		}
		g, ok := req.(appIDGetter)
		if !ok {
			return handler(ctx, req)
		}
		var st int16
		err := db.QueryRowContext(ctx,
			`SELECT status FROM application WHERE id=$1`, int64(g.GetAppId())).Scan(&st)
		if err != nil {
			return nil, status.Error(codes.NotFound, "unknown application")
		}
		if st != 1 {
			return nil, status.Error(codes.FailedPrecondition, "application disabled")
		}
		return handler(ctx, req)
	}
}
