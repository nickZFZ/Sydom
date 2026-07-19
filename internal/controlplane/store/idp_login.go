package store

import (
	"context"
	"database/sql"
	"errors"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
)

// IdPLoginRow 是登录编排所需的 IdP 配置行（client_secret 仍为密文，解密在 ssologin/控制面）。
type IdPLoginRow struct {
	TenantID        int64
	Issuer          string
	ClientID        string
	ClientSecretEnc []byte
	Enabled         bool
}

const idpLoginCols = `ti.tenant_id, ti.issuer, ti.client_id, ti.client_secret_enc, ti.enabled`

func scanIdPLogin(row *sql.Row) (IdPLoginRow, bool, error) {
	var r IdPLoginRow
	err := row.Scan(&r.TenantID, &r.Issuer, &r.ClientID, &r.ClientSecretEnc, &r.Enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return IdPLoginRow{}, false, nil
	}
	if err != nil {
		return IdPLoginRow{}, false, err
	}
	return r, true, nil
}

// IdPLoginByDomain 按 email 域路由到租户 IdP（域全局唯一）。未命中→ok=false。
func IdPLoginByDomain(ctx context.Context, ex cp.DBTX, domain string) (IdPLoginRow, bool, error) {
	return scanIdPLogin(ex.QueryRowContext(ctx,
		`SELECT `+idpLoginCols+` FROM tenant_idp ti
		 JOIN tenant_idp_domain d ON d.tenant_id = ti.tenant_id
		 WHERE d.domain = $1`, domain))
}

// IdPLoginByTenant 按 tenantID 取 IdP（回调复用一时态 tenantID，避免信任回调参数）。
func IdPLoginByTenant(ctx context.Context, ex cp.DBTX, tenantID int64) (IdPLoginRow, bool, error) {
	return scanIdPLogin(ex.QueryRowContext(ctx,
		`SELECT `+idpLoginCols+` FROM tenant_idp ti WHERE ti.tenant_id = $1`, tenantID))
}

// OperatorEmailMatch 严格映射：email 匹配的 active operator 且为 tenantID 有效成员 → principal。
// 任一不满足→ok=false（fail-close，无枚举 oracle）。email UNIQUE + 成员 PK 保至多一行。
func OperatorEmailMatch(ctx context.Context, ex cp.DBTX, tenantID int64, email string) (string, bool, error) {
	var principal string
	err := ex.QueryRowContext(ctx,
		`SELECT o.principal FROM admin_operator o
		 JOIN tenant_membership m ON m.operator_id = o.id
		 WHERE o.email = $1 AND o.status = 1 AND m.tenant_id = $2`, email, tenantID).Scan(&principal)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return principal, true, nil
}
