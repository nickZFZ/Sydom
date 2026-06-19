package mgmt

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// auditJSON 把审计 diff payload 序列化（绝不放 secret——调用方只传白名单非敏感字段）。
// 空 payload → nil（落 SQL NULL，由 InsertAdminAudit 处理）。
func auditJSON(payload map[string]any) []byte {
	if len(payload) == 0 {
		return nil
	}
	b, _ := json.Marshal(payload)
	return b
}

// domainTenant 把 admin 域字符串映射到审计 tenant_id：
// "t:<id>"（adminauthz.TenantDomain 生成）→ 该租户；"*"/app 域/其它 → NULL（纯系统级）。
func domainTenant(domain string) sql.NullInt64 {
	const tenantPrefix = "t:" // 与 adminauthz.TenantDomain 一致；该包未导出前缀常量，用字面量。
	if strings.HasPrefix(domain, tenantPrefix) {
		if id, err := strconv.ParseInt(strings.TrimPrefix(domain, tenantPrefix), 10, 64); err == nil {
			return sql.NullInt64{Int64: id, Valid: true}
		}
	}
	return sql.NullInt64{}
}

const auditMaxLimit = 200
const auditDefaultLimit = 50

func clampLimit(n uint32) int {
	if n == 0 {
		return auditDefaultLimit
	}
	if int(n) > auditMaxLimit {
		return auditMaxLimit
	}
	return int(n)
}

// parseTS 解析可选 RFC3339 时间；空串=零值(不过滤)；非法→InvalidArgument。
func parseTS(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, status.Error(codes.InvalidArgument, "invalid timestamp")
	}
	return t, nil
}

func nullStrToProto(s sql.NullString) string {
	if s.Valid {
		return s.String
	}
	return ""
}

// QueryAuditLog 读 app 域审计（scopeApp 已由 AuthorizeRule 鉴权）。纯读、不 bump。
func (s *AdminServer) QueryAuditLog(ctx context.Context, r *adminv1.QueryAuditLogRequest) (*adminv1.QueryAuditLogResponse, error) {
	since, err := parseTS(r.Since)
	if err != nil {
		return nil, err
	}
	until, err := parseTS(r.Until)
	if err != nil {
		return nil, err
	}
	entries, next, err := store.QueryAppAudit(ctx, s.db, int64(r.AppId), store.AppAuditFilter{
		EntityType: r.EntityType, EntityID: r.EntityId, Action: r.Action, Operator: r.Operator,
		Since: since, Until: until, Cursor: r.Cursor, Limit: clampLimit(r.Limit),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "query audit: %v", err)
	}
	out := &adminv1.QueryAuditLogResponse{NextCursor: next}
	for _, e := range entries {
		out.Entries = append(out.Entries, &adminv1.AuditEntry{
			Id: uint64(e.ID), Operator: e.Operator, Action: e.Action, EntityType: e.EntityType,
			EntityId: nullStrToProto(e.EntityID), Diff: nullStrToProto(e.Diff),
			Version: uint64(e.Version), CreatedAt: e.CreatedAt.Format(time.RFC3339),
		})
	}
	return out, nil
}

// QueryAdminAuditLog 读 admin 域审计（scopeTenant 已鉴权）。tenant_id=0→超管全量；非 0→该租户。
// WHERE 与 scope 锁步：AuthorizeRule 已保证调用者对该 tenant_id 域有权。
func (s *AdminServer) QueryAdminAuditLog(ctx context.Context, r *adminv1.QueryAdminAuditLogRequest) (*adminv1.QueryAdminAuditLogResponse, error) {
	since, err := parseTS(r.Since)
	if err != nil {
		return nil, err
	}
	until, err := parseTS(r.Until)
	if err != nil {
		return nil, err
	}
	var tenant sql.NullInt64
	if r.TenantId != 0 {
		tenant = sql.NullInt64{Int64: int64(r.TenantId), Valid: true}
	}
	entries, next, err := adminauthz.QueryAdminAudit(ctx, s.db, adminauthz.AdminAuditFilter{
		TenantID: tenant, EntityType: r.EntityType, EntityID: r.EntityId, Action: r.Action,
		Operator: r.Operator, Since: since, Until: until, Cursor: r.Cursor, Limit: clampLimit(r.Limit),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "query admin audit: %v", err)
	}
	out := &adminv1.QueryAdminAuditLogResponse{NextCursor: next}
	for _, e := range entries {
		var tid uint64
		if e.TenantID.Valid {
			tid = uint64(e.TenantID.Int64)
		}
		var ver uint64
		if e.AdminVersion.Valid {
			ver = uint64(e.AdminVersion.Int64)
		}
		out.Entries = append(out.Entries, &adminv1.AdminAuditEntry{
			Id: uint64(e.ID), TenantId: tid, Operator: e.Operator, Action: e.Action,
			EntityType: e.EntityType, EntityId: nullStrToProto(e.EntityID), Diff: nullStrToProto(e.Diff),
			AdminVersion: ver, CreatedAt: e.CreatedAt.Format(time.RFC3339),
		})
	}
	return out, nil
}
