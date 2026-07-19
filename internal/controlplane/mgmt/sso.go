package mgmt

import (
	"context"
	"database/sql"
	"fmt"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/crypto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ConfigureTenantIdp upsert 本租户 OIDC IdP 配置（scopeTenant 租户 owner 自助）。
// client_secret 加密存储；域被他租户占用→AlreadyExists。
func (s *AdminServer) ConfigureTenantIdp(ctx context.Context, r *adminv1.ConfigureTenantIdpRequest) (*adminv1.ConfigureTenantIdpResponse, error) {
	if r.Issuer == "" || r.ClientId == "" || r.ClientSecret == "" || len(r.Domains) == 0 {
		return nil, status.Error(codes.InvalidArgument, "issuer, client_id, client_secret, domains required")
	}
	enc, err := crypto.Encrypt(s.masterKey, []byte(r.ClientSecret))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encrypt: %v", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin: %v", err)
	}
	defer tx.Rollback()
	if err := store.UpsertTenantIdpTx(ctx, tx, int64(r.TenantId),
		r.Issuer, r.ClientId, enc, r.Domains, r.Enabled); err != nil {
		if isUniqueViolation(err) {
			return nil, status.Error(codes.AlreadyExists, "domain already claimed by another tenant")
		}
		if isForeignKeyViolation(err) {
			return nil, status.Error(codes.NotFound, "unknown tenant")
		}
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	// 审计绝不含 client_secret（INV-1）。
	if err := adminauthz.InsertAdminAudit(ctx, tx,
		sql.NullInt64{Int64: int64(r.TenantId), Valid: true}, cp.OperatorFromContext(ctx),
		"configure_idp", "tenant_idp", fmt.Sprintf("%d", r.TenantId),
		auditJSON(map[string]any{"issuer": r.Issuer, "client_id": r.ClientId, "domains": r.Domains, "enabled": r.Enabled}),
		sql.NullInt64{}); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "commit: %v", err)
	}
	return &adminv1.ConfigureTenantIdpResponse{TenantId: r.TenantId, Enabled: r.Enabled}, nil
}

// GetTenantIdp 读本租户 IdP 元数据（脱敏，绝不回 client_secret）。
func (s *AdminServer) GetTenantIdp(ctx context.Context, r *adminv1.GetTenantIdpRequest) (*adminv1.GetTenantIdpResponse, error) {
	t, err := store.TenantIdpOf(ctx, s.db, int64(r.TenantId))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	return &adminv1.GetTenantIdpResponse{
		Configured: t.Configured, Issuer: t.Issuer, ClientId: t.ClientID,
		Domains: t.Domains, Enabled: t.Enabled,
	}, nil
}
