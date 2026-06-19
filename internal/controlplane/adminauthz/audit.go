package adminauthz

import (
	"context"
	"database/sql"
	"fmt"

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
