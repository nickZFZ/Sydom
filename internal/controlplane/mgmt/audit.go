package mgmt

import (
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"
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
