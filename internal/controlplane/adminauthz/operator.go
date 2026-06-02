package adminauthz

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/nickZFZ/Sydom/internal/auth"
	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/crypto"
)

// OperatorResolver 按 operator principal 解密返回其凭据原文，供 auth HMAC 认证复用。
// 实现 auth.SecretResolver。
type OperatorResolver struct {
	db        *sql.DB
	masterKey []byte
}

// NewOperatorResolver 构造，校验主密钥长度（fail-close）。
func NewOperatorResolver(db *sql.DB, masterKey []byte) (*OperatorResolver, error) {
	if len(masterKey) != crypto.KeySize {
		return nil, crypto.ErrKeySize
	}
	k := make([]byte, len(masterKey)) // 深拷贝，防调用方后续改动
	copy(k, masterKey)
	return &OperatorResolver{db: db, masterKey: k}, nil
}

// ResolveSecret 解密 active operator 的凭据；未知/停用/解密失败一律 error（fail-close）。
func (r *OperatorResolver) ResolveSecret(ctx context.Context, principal string) ([]byte, error) {
	var enc []byte
	var status int16
	err := r.db.QueryRowContext(ctx,
		`SELECT secret_enc, status FROM admin_operator WHERE principal=$1`, principal).Scan(&enc, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("adminauthz: unknown operator %q", principal)
	}
	if err != nil {
		return nil, fmt.Errorf("adminauthz: query operator: %w", err)
	}
	if status != 1 {
		return nil, fmt.Errorf("adminauthz: operator %q disabled", principal)
	}
	plain, err := crypto.Decrypt(r.masterKey, enc)
	if err != nil {
		return nil, fmt.Errorf("adminauthz: decrypt operator secret: %w", err)
	}
	return plain, nil
}

// EnsureRootOperator 幂等播种 bootstrap 超管：principal 不存在则建并绑定 super-admin@*。
// 已存在则不动（不覆盖凭据）。masterKey 用于加密初始凭据。
func EnsureRootOperator(ctx context.Context, db *sql.DB, masterKey []byte, principal string, secret []byte) error {
	if len(masterKey) != crypto.KeySize {
		return crypto.ErrKeySize
	}
	var exists bool
	if err := db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM admin_operator WHERE principal=$1)`, principal).Scan(&exists); err != nil {
		return fmt.Errorf("adminauthz: check root: %w", err)
	}
	if exists {
		return nil
	}
	enc, err := crypto.Encrypt(masterKey, secret)
	if err != nil {
		return fmt.Errorf("adminauthz: encrypt root secret: %w", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("adminauthz: begin: %w", err)
	}
	defer tx.Rollback()
	opID, err := InsertOperator(ctx, tx, principal, enc)
	if err != nil {
		return err
	}
	var superID int64
	if err := tx.QueryRowContext(ctx,
		`SELECT id FROM admin_role WHERE code='super-admin'`).Scan(&superID); err != nil {
		return fmt.Errorf("adminauthz: find super-admin role: %w", err)
	}
	if err := InsertSubjectRole(ctx, tx, opID, superID, "*"); err != nil {
		return err
	}
	if err := BumpPolicyVersion(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("adminauthz: commit root: %w", err)
	}
	return nil
}

// 编译期断言：*OperatorResolver 实现 auth.SecretResolver。
var _ auth.SecretResolver = (*OperatorResolver)(nil)

// 确保 *sql.Tx 满足 cp.DBTX（InsertOperator 等接受 cp.DBTX）。
var _ cp.DBTX = (*sql.Tx)(nil)
