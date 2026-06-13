package mgmt

import (
	"context"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	"github.com/nickZFZ/Sydom/internal/auth"
	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/crypto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// RegisterTenant 自助注册（免鉴权）：建 tenant + owner operator + membership(owner) +
// tenant-admin 角色/grant/绑定，一事务。owner_secret 明文仅当场返回，绝不日志/落盘。
func (s *AdminServer) RegisterTenant(ctx context.Context, r *adminv1.RegisterTenantRequest) (*adminv1.RegisterTenantResponse, error) {
	if r.TenantName == "" || !auth.ValidPrincipal(r.OwnerPrincipal) {
		return nil, status.Error(codes.InvalidArgument, "tenant_name and valid owner_principal required")
	}
	secret, err := genSecret()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "gen secret: %v", err)
	}
	enc, err := crypto.Encrypt(s.masterKey, []byte(secret))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encrypt: %v", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin: %v", err)
	}
	defer tx.Rollback()

	var tenantID int64
	if err := tx.QueryRowContext(ctx,
		`INSERT INTO tenant (name) VALUES ($1) RETURNING id`, r.TenantName).Scan(&tenantID); err != nil {
		if isUniqueViolation(err) {
			return nil, status.Error(codes.AlreadyExists, "tenant name taken")
		}
		return nil, status.Errorf(codes.Internal, "create tenant: %v", err)
	}
	opID, err := adminauthz.InsertOperator(ctx, tx, r.OwnerPrincipal, enc)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, status.Error(codes.AlreadyExists, "principal taken")
		}
		return nil, status.Errorf(codes.Internal, "create owner: %v", err)
	}
	if _, err := adminauthz.InsertMembership(ctx, tx, tenantID, opID, adminauthz.TierOwner); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := adminauthz.BindTenantAdminTx(ctx, tx, tenantID, opID); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := adminauthz.BumpPolicyVersion(ctx, tx); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "commit: %v", err)
	}
	return &adminv1.RegisterTenantResponse{
		TenantId: uint64(tenantID), OwnerPrincipal: r.OwnerPrincipal, OwnerSecret: secret}, nil
}

// ListMyTenants（self）：返回 ctx principal 的租户归属 + 运营平面标志。
func (s *AdminServer) ListMyTenants(ctx context.Context, _ *adminv1.ListMyTenantsRequest) (*adminv1.ListMyTenantsResponse, error) {
	principal := cp.OperatorFromContext(ctx)
	ms, err := adminauthz.TenantsOfOperator(ctx, s.db, principal)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list my tenants: %v", err)
	}
	op, err := adminauthz.IsOperatingPlane(ctx, s.db, principal)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "operating plane: %v", err)
	}
	out := &adminv1.ListMyTenantsResponse{IsOperatingPlane: op}
	for _, m := range ms {
		out.Memberships = append(out.Memberships, &adminv1.TenantMembershipSummary{
			TenantId: uint64(m.TenantID), TenantName: m.TenantName, Tier: uint32(m.Tier)})
	}
	return out, nil
}
