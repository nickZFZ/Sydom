// Package ssologin 是 SSO 登录的生产 resolver：把「域/租户→IdP 登录配置（解密后 client_secret）」
// 与「email→严格映射 operator」封装在控制面（持 masterKey）。INV-1：secret 解密不出本包。
package ssologin

import (
	"context"
	"database/sql"

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
		ClientSecret: string(plain), Enabled: row.Enabled,
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
