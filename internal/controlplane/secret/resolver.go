// Package secret 实现控制面对 AppSecret 的加解密：解密 application.app_secret_enc。
package secret

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/nickZFZ/Sydom/internal/auth"
	"github.com/nickZFZ/Sydom/internal/crypto"
)

// 编译期断言：*Resolver 实现 auth.SecretResolver。
var _ auth.SecretResolver = (*Resolver)(nil)

// Resolver 从 application.app_secret_enc 解密取 AppSecret 原文，实现 auth.SecretResolver。
type Resolver struct {
	db        *sql.DB
	masterKey []byte // 32 字节 AES-256 主密钥，外部注入，绝不入库
}

// NewResolver 构造 Resolver；主密钥长度非法即报错（fail-close）。
// 复制传入的主密钥：防止调用方后续修改/擦除原 slice 静默篡改本 Resolver 的密钥，
// 导致后续全量解密无声失败——安全敏感字段的最小防御。
func NewResolver(db *sql.DB, masterKey []byte) (*Resolver, error) {
	if len(masterKey) != crypto.KeySize {
		return nil, fmt.Errorf("secret: master key must be %d bytes", crypto.KeySize)
	}
	mk := make([]byte, len(masterKey))
	copy(mk, masterKey)
	return &Resolver{db: db, masterKey: mk}, nil
}

// ResolveSecret 按 app_key=appID 查 application → 解密 app_secret_enc。实现 auth.SecretResolver。
func (r *Resolver) ResolveSecret(ctx context.Context, appID string) ([]byte, error) {
	var enc []byte
	err := r.db.QueryRowContext(ctx,
		`SELECT app_secret_enc FROM application WHERE app_key=$1`, appID).Scan(&enc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("secret: unknown app_key %q", appID)
	}
	if err != nil {
		return nil, err
	}
	return crypto.Decrypt(r.masterKey, enc)
}

// EncryptSecret 供建应用时加密 AppSecret 写入 app_secret_enc。
func (r *Resolver) EncryptSecret(plaintext []byte) ([]byte, error) {
	return crypto.Encrypt(r.masterKey, plaintext)
}
