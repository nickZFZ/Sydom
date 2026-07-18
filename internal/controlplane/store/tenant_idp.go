package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	cp "github.com/nickZFZ/Sydom/internal/controlplane"
)

// TenantIdp 是租户 OIDC IdP 配置的读视图（绝不含 client_secret——INV-1 不泄露）。
type TenantIdp struct {
	Configured bool
	Issuer     string
	ClientID   string
	Domains    []string
	Enabled    bool
}

// UpsertTenantIdpTx 在调用方事务内 upsert 本租户 IdP config + 替换其 email 域集合。
// domains 小写化写入；域被他租户占用→pq 23505（uq_tenant_idp_domain）由调用方映射。
func UpsertTenantIdpTx(ctx context.Context, tx cp.DBTX, tenantID int64,
	issuer, clientID string, secretEnc []byte, domains []string, enabled bool) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO tenant_idp (tenant_id, issuer, client_id, client_secret_enc, enabled)
		 VALUES ($1,$2,$3,$4,$5)
		 ON CONFLICT (tenant_id) DO UPDATE SET
		   issuer=EXCLUDED.issuer, client_id=EXCLUDED.client_id,
		   client_secret_enc=EXCLUDED.client_secret_enc, enabled=EXCLUDED.enabled, updated_at=now()`,
		tenantID, issuer, clientID, secretEnc, enabled); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM tenant_idp_domain WHERE tenant_id=$1`, tenantID); err != nil {
		return err
	}
	for _, d := range domains {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO tenant_idp_domain (tenant_id, domain) VALUES ($1,$2)`,
			tenantID, strings.ToLower(strings.TrimSpace(d))); err != nil {
			return err // 域全局冲突→pq 23505
		}
	}
	return nil
}

// TenantIdpOf 读租户 IdP 元数据（不查 client_secret_enc）+ 聚合域。无配置→Configured=false。
func TenantIdpOf(ctx context.Context, ex cp.DBTX, tenantID int64) (TenantIdp, error) {
	var t TenantIdp
	err := ex.QueryRowContext(ctx,
		`SELECT issuer, client_id, enabled FROM tenant_idp WHERE tenant_id=$1`, tenantID).
		Scan(&t.Issuer, &t.ClientID, &t.Enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return TenantIdp{Configured: false}, nil
	}
	if err != nil {
		return TenantIdp{}, err
	}
	t.Configured = true
	rows, err := ex.QueryContext(ctx,
		`SELECT domain FROM tenant_idp_domain WHERE tenant_id=$1 ORDER BY domain`, tenantID)
	if err != nil {
		return TenantIdp{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return TenantIdp{}, err
		}
		t.Domains = append(t.Domains, d)
	}
	return t, rows.Err()
}
