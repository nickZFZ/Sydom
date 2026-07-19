// Package ssologin 是 SSO 登录的生产 resolver：把「域/租户→IdP 登录配置（解密后 client_secret）」
// 与「email→严格映射 operator」封装在控制面（持 masterKey）。INV-1：secret 解密不出本包。
package ssologin

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"

	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/crypto"
)

// IdPLogin 是登录编排消费的 IdP 配置（含解密后的 ClientSecret 明文，仅进程内用）。
type IdPLogin struct {
	TenantID     int64
	Issuer       string
	ClientID     string
	ClientSecret string
	Enabled      bool
	JITEnabled   bool
}

// Resolver 持 db+masterKey，实现 console 的窄接口（结构性满足）。
type Resolver struct {
	db        *sql.DB
	masterKey []byte
}

// NewResolver 构造，校验主密钥长度（fail-close）。
func NewResolver(db *sql.DB, masterKey []byte) (*Resolver, error) {
	if len(masterKey) != crypto.KeySize {
		return nil, crypto.ErrKeySize
	}
	k := make([]byte, len(masterKey))
	copy(k, masterKey)
	return &Resolver{db: db, masterKey: k}, nil
}

func (r *Resolver) decrypt(row store.IdPLoginRow) (IdPLogin, error) {
	plain, err := crypto.Decrypt(r.masterKey, row.ClientSecretEnc)
	if err != nil {
		return IdPLogin{}, err
	}
	return IdPLogin{
		TenantID: row.TenantID, Issuer: row.Issuer, ClientID: row.ClientID,
		ClientSecret: string(plain), Enabled: row.Enabled, JITEnabled: row.JITEnabled,
	}, nil
}

// ResolveIdPByDomain 按 email 域路由 + 解密。
func (r *Resolver) ResolveIdPByDomain(ctx context.Context, domain string) (IdPLogin, bool, error) {
	row, ok, err := store.IdPLoginByDomain(ctx, r.db, domain)
	if err != nil || !ok {
		return IdPLogin{}, ok, err
	}
	out, err := r.decrypt(row)
	if err != nil {
		return IdPLogin{}, false, err
	}
	return out, true, nil
}

// ResolveIdPByTenant 按 tenantID 取 + 解密（回调用）。
func (r *Resolver) ResolveIdPByTenant(ctx context.Context, tenantID int64) (IdPLogin, bool, error) {
	row, ok, err := store.IdPLoginByTenant(ctx, r.db, tenantID)
	if err != nil || !ok {
		return IdPLogin{}, ok, err
	}
	out, err := r.decrypt(row)
	if err != nil {
		return IdPLogin{}, false, err
	}
	return out, true, nil
}

// MatchOperatorForLogin 委托 store 严格映射。
func (r *Resolver) MatchOperatorForLogin(ctx context.Context, tenantID int64, email string) (string, bool, error) {
	return store.OperatorEmailMatch(ctx, r.db, tenantID, email)
}

// ProvisionOperatorForLogin 仅在 email 完全未知时 JIT 开通零权限成员：
// 建 operator(principal=email, 随机 secret, status=1) + membership(TierMember) + 审计，不 bump 策略版本。
// 既有 email（含既有非成员）→ ok=false（fail-close，防跨租户账户接管）。
func (r *Resolver) ProvisionOperatorForLogin(ctx context.Context, tenantID int64, email string) (string, bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()

	var exists bool
	if err := tx.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM admin_operator WHERE email=$1)`, email).Scan(&exists); err != nil {
		return "", false, err
	}
	if exists {
		return "", false, nil // 既有 email → 不 JIT
	}

	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", false, err
	}
	enc, err := crypto.Encrypt(r.masterKey, secret)
	if err != nil {
		return "", false, err
	}
	opID, err := store.InsertJITOperatorTx(ctx, tx, email, enc)
	if err != nil {
		return "", false, err // UNIQUE 竞态 / email>128 长度 → fail-close
	}
	if _, err := adminauthz.InsertMembership(ctx, tx, tenantID, opID, adminauthz.TierMember); err != nil {
		return "", false, err
	}
	diff, err := json.Marshal(map[string]any{"tenant_id": tenantID, "via": "sso_jit"})
	if err != nil {
		return "", false, err
	}
	if err := adminauthz.InsertAdminAudit(ctx, tx,
		sql.NullInt64{Int64: tenantID, Valid: true}, "sso_jit", "jit_provision", "operator", email,
		diff, sql.NullInt64{}); err != nil {
		return "", false, err
	}
	// 不 BumpPolicyVersion：零 casbin 绑定=零策略变更，enforcer 无需重载。
	if err := tx.Commit(); err != nil {
		return "", false, err
	}
	return email, true, nil
}
