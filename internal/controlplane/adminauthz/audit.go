package adminauthz

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
)

// InsertAdminAudit 写一条 system 域审计记录。tenantID/adminVersion 用 sql.NullInt64
// 承载 NULL（纯系统级动作无租户；无 bump 的动作无版本）。diff 可为 nil → 落 NULL。
// diff 绝不含 secret（由调用方构造，仅白名单非敏感字段）。
func InsertAdminAudit(ctx context.Context, ex cp.DBTX, tenantID sql.NullInt64,
	operator, action, entityType, entityID string, diff []byte, adminVersion sql.NullInt64) error {
	var diffVal interface{}
	if diff != nil {
		diffVal = diff
	}
	_, err := ex.ExecContext(ctx, `
		INSERT INTO admin_audit_log
		  (tenant_id, operator, action, entity_type, entity_id, diff, admin_version)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		tenantID, operator, action, entityType, nullStr(entityID), diffVal, adminVersion)
	if err != nil {
		return fmt.Errorf("adminauthz: insert admin audit: %w", err)
	}
	return nil
}

// nullStr 把空串转成 NULL（entity_id 可空）。
func nullStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// AdminAuditFilter 是 QueryAdminAudit 的过滤参数。零值字段不参与过滤。
// TenantID.Valid=true → WHERE tenant_id=值（租户隔离）；!Valid → 不加 tenant 过滤（超管全量，含 NULL 行）。
type AdminAuditFilter struct {
	TenantID                               sql.NullInt64
	EntityType, EntityID, Action, Operator string
	Since, Until                           time.Time
	Cursor                                 uint64 // keyset：仅返回 id < Cursor 的行（0=从头）
	Limit                                  int
}

// AdminAuditEntry 是 admin_audit_log 的一行投影。
type AdminAuditEntry struct {
	ID                           int64
	TenantID                     sql.NullInt64
	Operator, Action, EntityType string
	EntityID, Diff               sql.NullString
	AdminVersion                 sql.NullInt64
	CreatedAt                    time.Time
}

// QueryAdminAudit 按过滤做 keyset 分页（id 降序，新→旧）。
// TenantID.Valid → WHERE tenant_id=值（租户隔离）；!Valid → 不加 tenant 过滤（超管全量，含纯系统级 NULL 行）。
// 全部参数化（绝不拼接用户值）。
func QueryAdminAudit(ctx context.Context, q cp.DBTX, f AdminAuditFilter) ([]AdminAuditEntry, uint64, error) {
	var conds []string
	var args []any
	add := func(cond string, val any) {
		args = append(args, val)
		conds = append(conds, cond+" $"+strconv.Itoa(len(args)))
	}
	if f.TenantID.Valid {
		add("tenant_id =", f.TenantID.Int64)
	}
	if f.Cursor > 0 {
		add("id <", int64(f.Cursor))
	}
	if f.EntityType != "" {
		add("entity_type =", f.EntityType)
	}
	if f.EntityID != "" {
		add("entity_id =", f.EntityID)
	}
	if f.Action != "" {
		add("action =", f.Action)
	}
	if f.Operator != "" {
		add("operator =", f.Operator)
	}
	if !f.Since.IsZero() {
		add("created_at >=", f.Since)
	}
	if !f.Until.IsZero() {
		add("created_at <=", f.Until)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, f.Limit+1)
	query := `SELECT id, tenant_id, operator, action, entity_type, entity_id, diff, admin_version, created_at
		FROM admin_audit_log` + where + ` ORDER BY id DESC LIMIT $` + strconv.Itoa(len(args))

	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []AdminAuditEntry
	for rows.Next() {
		var e AdminAuditEntry
		if err := rows.Scan(&e.ID, &e.TenantID, &e.Operator, &e.Action, &e.EntityType,
			&e.EntityID, &e.Diff, &e.AdminVersion, &e.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var next uint64
	if len(out) > f.Limit {
		next = uint64(out[f.Limit-1].ID)
		out = out[:f.Limit]
	}
	return out, next, nil
}
