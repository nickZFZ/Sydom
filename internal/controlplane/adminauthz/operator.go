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

// ensureOperatorTx 幂等取/建 operator，返回 id。事务内调用。
func ensureOperatorTx(ctx context.Context, tx *sql.Tx, masterKey []byte, principal string, secret []byte) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM admin_operator WHERE principal=$1`, principal).Scan(&id)
	if err == nil {
		return id, nil // 已存在：不覆盖凭据
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("adminauthz: find operator: %w", err)
	}
	enc, err := crypto.Encrypt(masterKey, secret)
	if err != nil {
		return 0, fmt.Errorf("adminauthz: encrypt secret: %w", err)
	}
	return InsertOperator(ctx, tx, principal, enc)
}

// EnsureTenantAdmin 幂等播种某租户的租户管理员：
//   - operator(principal) 不存在则建（masterKey 加密初始 secret）；已存在不覆盖凭据；
//   - 写 membership(owner) 记录账户层归属（I-1 不变量：与 casbin 绑定同事务）；
//   - 租户专属角色（code=tenant-admin-<tenantID>）在 t:<tenantID> 域授单条通配 (*,*)；
//   - 绑定 operator → 角色 @ t:<tenantID>；
//   - bump 版本触发 enforcer 重载。
//
// 通配 (t:<id>,*,*) 经 matcher 仅命中 app-scoped 操作（system RPC 在 * 域，不被 t: 域命中），
// 故租户管理员止步于本租户业务策略，碰不到 SaaS 级 operator/admin-role 管理与 CreateApplication。
func EnsureTenantAdmin(ctx context.Context, db *sql.DB, masterKey []byte, tenantID int64, principal string, secret []byte) error {
	if len(masterKey) != crypto.KeySize {
		return crypto.ErrKeySize
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("adminauthz: begin: %w", err)
	}
	defer tx.Rollback()

	opID, err := ensureOperatorTx(ctx, tx, masterKey, principal, secret)
	if err != nil {
		return err
	}
	if _, err := InsertMembership(ctx, tx, tenantID, opID, TierOwner); err != nil {
		return err
	}
	if err := BindTenantAdminTx(ctx, tx, tenantID, opID); err != nil {
		return err
	}
	if err := BumpPolicyVersion(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("adminauthz: commit tenant admin: %w", err)
	}
	return nil
}
