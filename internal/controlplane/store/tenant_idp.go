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
	JITEnabled bool
}

// UpsertTenantIdpTx 在调用方事务内 upsert 本租户 IdP config + 替换其 email 域集合。
// domains 小写化写入；域被他租户占用→pq 23505（uq_tenant_idp_domain）由调用方映射。
func UpsertTenantIdpTx(ctx context.Context, tx cp.DBTX, tenantID int64,
	issuer, clientID string, secretEnc []byte, domains []string, enabled, jitEnabled bool) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO tenant_idp (tenant_id, issuer, client_id, client_secret_enc, enabled, jit_enabled)
		 VALUES ($1,$2,$3,$4,$5,$6)
		 ON CONFLICT (tenant_id) DO UPDATE SET
		   issuer=EXCLUDED.issuer, client_id=EXCLUDED.client_id,
		   client_secret_enc=EXCLUDED.client_secret_enc, enabled=EXCLUDED.enabled,
		   jit_enabled=EXCLUDED.jit_enabled, updated_at=now()`,
		tenantID, issuer, clientID, secretEnc, enabled, jitEnabled); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM tenant_idp_domain WHERE tenant_id=$1`, tenantID); err != nil {
		return err
	}
	// 同请求内先归一（小写+去空白）再去重：否则大小写/空白不同但实为同一域的条目会
	// 逐条插入、第二条撞全局 UNIQUE（uq_tenant_idp_domain）被误判成「他租户占用」。
	seen := make(map[string]struct{}, len(domains))
	for _, d := range domains {
		norm := strings.ToLower(strings.TrimSpace(d))
		if _, dup := seen[norm]; dup {
			continue
		}
		seen[norm] = struct{}{}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO tenant_idp_domain (tenant_id, domain) VALUES ($1,$2)`,
			tenantID, norm); err != nil {
			return err // 域被他租户占用→pq 23505
		}
	}
	return nil
}

// DeleteTenantIdpTx 删除本租户 IdP 配置 + 其 email 域（域表引用 tenant 非 tenant_idp，不级联，须显式先删）。
// 返回是否真有配置被删（无配置→false，供调用方映射 NotFound）。
func DeleteTenantIdpTx(ctx context.Context, tx cp.DBTX, tenantID int64) (bool, error) {
	if _, err := tx.ExecContext(ctx, `DELETE FROM tenant_idp_domain WHERE tenant_id=$1`, tenantID); err != nil {
		return false, err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM tenant_idp WHERE tenant_id=$1`, tenantID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// TenantIdpSecretEnc 读租户 IdP 的原始加密 client_secret（密文）。无配置→ok=false。
// 仅供 ConfigureTenantIdp 编辑保留时把旧密文原样回写；从不解密、绝不出控制面（INV-1）。
func TenantIdpSecretEnc(ctx context.Context, ex cp.DBTX, tenantID int64) ([]byte, bool, error) {
	var enc []byte
	err := ex.QueryRowContext(ctx,
		`SELECT client_secret_enc FROM tenant_idp WHERE tenant_id=$1`, tenantID).Scan(&enc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return enc, true, nil
}

// TenantIdpOf 读租户 IdP 元数据（不查 client_secret_enc）+ 聚合域。无配置→Configured=false。
func TenantIdpOf(ctx context.Context, ex cp.DBTX, tenantID int64) (TenantIdp, error) {
	var t TenantIdp
	err := ex.QueryRowContext(ctx,
		`SELECT issuer, client_id, enabled, jit_enabled FROM tenant_idp WHERE tenant_id=$1`, tenantID).
		Scan(&t.Issuer, &t.ClientID, &t.Enabled, &t.JITEnabled)
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
