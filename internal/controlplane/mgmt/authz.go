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

// ruleScope 决定 AuthorizeRule 如何解析鉴权域。
type ruleScope int

const (
	scopeSystem ruleScope = iota // "*" 域：运营平面
	scopeApp                     // 域取自请求 app_id（M1.1 路径）
	scopeTenant                  // 域取自请求 tenant_id（0→"*"，否则 t:<id>）
	scopeSelf                    // 不 enforce，认证通过即放行
)

// rpcRule 描述某 RPC 的鉴权要素。
type rpcRule struct {
	resource string
	action   string
	isWrite  bool // 是否受 status 写拦截（仅针对具体 app 的业务策略写）
	scope    ruleScope
}

// appIDGetter / tenantIDGetter 取请求中的域键。
type appIDGetter interface{ GetAppId() uint64 }
type tenantIDGetter interface{ GetTenantId() uint64 }

// UnauthenticatedMethods 是免鉴权 RPC 白名单（集中真相源，auth 与 authz 拦截器、REST serve 共用）。
var UnauthenticatedMethods = map[string]bool{
	"/sydom.admin.v1.AdminService/RegisterTenant": true,
}

// ruleTable 把 FullMethod 映射到鉴权要素。集中维护，避免散落判断。
// 只读：初始化后不得修改（无锁并发读安全）。
var ruleTable = map[string]rpcRule{
	"/sydom.admin.v1.AdminService/CreateRole":            {"role", "create", true, scopeApp},
	"/sydom.admin.v1.AdminService/DeleteRole":            {"role", "delete", true, scopeApp},
	"/sydom.admin.v1.AdminService/UpsertPermission":      {"permission", "update", true, scopeApp},
	"/sydom.admin.v1.AdminService/GrantPermission":       {"grant", "create", true, scopeApp},
	"/sydom.admin.v1.AdminService/RevokePermission":      {"grant", "delete", true, scopeApp},
	"/sydom.admin.v1.AdminService/AddRoleInheritance":    {"inheritance", "create", true, scopeApp},
	"/sydom.admin.v1.AdminService/RemoveRoleInheritance": {"inheritance", "delete", true, scopeApp},
	"/sydom.admin.v1.AdminService/BindUserRole":          {"binding", "create", true, scopeApp},
	"/sydom.admin.v1.AdminService/UnbindUserRole":        {"binding", "delete", true, scopeApp},
	"/sydom.admin.v1.AdminService/UpsertDataPolicy":      {"data_policy", "update", true, scopeApp},
	"/sydom.admin.v1.AdminService/DeleteDataPolicy":      {"data_policy", "delete", true, scopeApp},
	"/sydom.admin.v1.AdminService/CreateApplication":     {"application", "create", false, scopeTenant},
	"/sydom.admin.v1.AdminService/SetApplicationStatus":  {"application", "update", false, scopeApp}, // 在目标 app 自身域校验（本域管理员可停/启自身应用，跨 app 需 * 域超管）
	"/sydom.admin.v1.AdminService/ListApplications":      {"application", "read", false, scopeTenant},
	"/sydom.admin.v1.AdminService/CreateOperator":        {"admin", "create", false, scopeSystem},
	"/sydom.admin.v1.AdminService/SetOperatorStatus":     {"admin", "update", false, scopeSystem},
	"/sydom.admin.v1.AdminService/CreateAdminRole":       {"admin", "create", false, scopeSystem},
	"/sydom.admin.v1.AdminService/GrantAdminRole":        {"admin", "update", false, scopeSystem},
	"/sydom.admin.v1.AdminService/BindOperatorRole":      {"admin", "update", false, scopeSystem},
	"/sydom.admin.v1.AdminService/ListRoles":             {"role", "read", false, scopeApp},
	"/sydom.admin.v1.AdminService/ListPermissions":       {"permission", "read", false, scopeApp},
	"/sydom.admin.v1.AdminService/ListGrants":            {"grant", "read", false, scopeApp},
	"/sydom.admin.v1.AdminService/ListRoleInheritances":  {"inheritance", "read", false, scopeApp},
	"/sydom.admin.v1.AdminService/ListUserBindings":      {"binding", "read", false, scopeApp},
	"/sydom.admin.v1.AdminService/ListDataPolicies":      {"data_policy", "read", false, scopeApp},
	"/sydom.admin.v1.AdminService/ListOperators":         {"admin", "read", false, scopeSystem},
	"/sydom.admin.v1.AdminService/ListAdminRoles":        {"admin", "read", false, scopeSystem},
	"/sydom.admin.v1.AdminService/ListMyTenants":         {"", "", false, scopeSelf},
	"/sydom.admin.v1.AdminService/InviteMember":          {"member", "create", false, scopeTenant},
	"/sydom.admin.v1.AdminService/ListMembers":           {"member", "read", false, scopeTenant},
}

// DomainOfAppID 把 app_id 转成 casbin domain 字符串。
func DomainOfAppID(appID int64) string { return strconv.FormatInt(appID, 10) }

// AuthorizeRule 据 ruleTable[fullMethod].scope 解析鉴权域并 enforce。gRPC/REST/Console 共用，唯一真相源。
func AuthorizeRule(ctx context.Context, enf *adminauthz.Enforcer, fullMethod, principal string, req any) (context.Context, error) {
	rule, known := ruleTable[fullMethod]
	if !known {
		return nil, status.Error(codes.PermissionDenied, "unknown method")
	}
	if rule.scope == scopeSelf {
		// 认证已由上游保证；自有数据由 handler 按 ctx principal 过滤。
		return cp.WithOperator(ctx, principal), nil
	}
	var domain, tdom string
	switch rule.scope {
	case scopeSystem:
		domain, tdom = "*", "*"
	case scopeApp:
		g, ok := req.(appIDGetter)
		if !ok {
			return nil, status.Error(codes.Internal, "request missing app_id")
		}
		appID := int64(g.GetAppId())
		domain = DomainOfAppID(appID)
		td, err := enf.TenantDomainOf(ctx, appID)
		if err != nil {
			// app 不存在/查询失败：fail-close 为 PermissionDenied，不泄露存在性差异。
			return nil, status.Error(codes.PermissionDenied, "permission denied")
		}
		tdom = td
	case scopeTenant:
		g, ok := req.(tenantIDGetter)
		if !ok {
			return nil, status.Error(codes.Internal, "request missing tenant_id")
		}
		tid := int64(g.GetTenantId())
		if tid == 0 {
			domain, tdom = "*", "*" // 运营平面通配（仅超管 g(sub,*,"*") 命中）
		} else {
			domain = adminauthz.TenantDomain(tid)
			tdom = domain
		}
	}
	allow, err := enf.Enforce(ctx, principal, domain, tdom, rule.resource, rule.action)
	// TODO(observability): Enforce 内部错误（DB/策略加载故障）当前与"权限不足"一并 fail-close 为 PermissionDenied；接入日志/metric 后在此区分并记录。
	if err != nil || !allow {
		return nil, status.Error(codes.PermissionDenied, "permission denied")
	}
	return cp.WithOperator(ctx, principal), nil
}

// CheckStatusWrite 对"具体 app 的业务策略写"（isWrite 规则）校验目标 app 未停用；
// 非写规则或无 app_id 的请求直接放行。返回 gRPC status 错误。
// 调用契约：必须在 AuthorizeRule 之后执行——本函数不校验 app 归属，若先于鉴权，
// 会借 NotFound/FailedPrecondition 差异泄露 app 存在性。
func CheckStatusWrite(ctx context.Context, db *sql.DB, fullMethod string, req any) error {
	rule, known := ruleTable[fullMethod]
	if !known || !rule.isWrite {
		return nil
	}
	g, ok := req.(appIDGetter)
	if !ok {
		return nil
	}
	var st int16
	err := db.QueryRowContext(ctx,
		`SELECT status FROM application WHERE id=$1`, int64(g.GetAppId())).Scan(&st)
	if err != nil {
		return status.Error(codes.NotFound, "unknown application")
	}
	if st != 1 {
		return status.Error(codes.FailedPrecondition, "application disabled")
	}
	return nil
}

// AuthzUnaryInterceptor 是 AuthorizeRule 的 gRPC 薄封装：先取认证注入的 principal。
func AuthzUnaryInterceptor(enf *adminauthz.Enforcer) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if UnauthenticatedMethods[info.FullMethod] {
			return handler(ctx, req) // 免鉴权：不取 principal、不 enforce
		}
		principal, ok := auth.AppIDFromContext(ctx)
		if !ok {
			return nil, status.Error(codes.Unauthenticated, "missing operator identity")
		}
		newCtx, err := AuthorizeRule(ctx, enf, info.FullMethod, principal, req)
		if err != nil {
			return nil, err
		}
		return handler(newCtx, req)
	}
}

// StatusWriteUnaryInterceptor 是 CheckStatusWrite 的 gRPC 薄封装。
// 装配契约：必须排在 AuthzUnaryInterceptor 之后——本拦截器不校验 app 归属，若先于鉴权执行，会借 NotFound/FailedPrecondition 差异泄露 app 存在性。
func StatusWriteUnaryInterceptor(db *sql.DB) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if err := CheckStatusWrite(ctx, db, info.FullMethod, req); err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}
