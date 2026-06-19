package mgmt

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"

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
	ver, err := adminauthz.ReadPolicyVersion(ctx, tx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := adminauthz.InsertAdminAudit(ctx, tx,
		sql.NullInt64{Int64: tenantID, Valid: true}, r.OwnerPrincipal,
		"register", "tenant", fmt.Sprintf("%d", tenantID),
		auditJSON(map[string]any{"after": map[string]any{
			"tenant_name": r.TenantName, "owner": r.OwnerPrincipal}}),
		sql.NullInt64{Int64: ver, Valid: true}); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "commit: %v", err)
	}
	return &adminv1.RegisterTenantResponse{
		TenantId: uint64(tenantID), OwnerPrincipal: r.OwnerPrincipal, OwnerSecret: secret}, nil
}

// ListMyTenants（self）：返回 ctx principal 的租户归属 + 运营平面标志。
// 纯内存分页（全量从 DB 取后过滤/排序/切片），避免把 q/sort 注入 adminauthz.TenantsOfOperator。
func (s *AdminServer) ListMyTenants(ctx context.Context, r *adminv1.ListMyTenantsRequest) (*adminv1.ListMyTenantsResponse, error) {
	principal := cp.OperatorFromContext(ctx)
	ms, err := adminauthz.TenantsOfOperator(ctx, s.db, principal)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list my tenants: %v", err)
	}
	op, err := adminauthz.IsOperatingPlane(ctx, s.db, principal)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "operating plane: %v", err)
	}

	// 1. q 子串过滤（不区分大小写）
	if q := r.Page.GetQ(); q != "" {
		lq := strings.ToLower(q)
		filtered := ms[:0]
		for _, m := range ms {
			if strings.Contains(strings.ToLower(m.TenantName), lq) {
				filtered = append(filtered, m)
			}
		}
		ms = filtered
	}

	// 2. total = 过滤后总数（分页前）
	total := uint32(len(ms))

	// 3. 排序：白名单 {tenant_id, tenant_name}，默认 tenant_id asc
	sortCol := r.Page.GetSort()
	sortDir := strings.ToLower(r.Page.GetOrder())
	desc := sortDir == "desc"
	switch sortCol {
	case "tenant_name":
		sort.Slice(ms, func(i, j int) bool {
			if desc {
				return ms[i].TenantName > ms[j].TenantName
			}
			return ms[i].TenantName < ms[j].TenantName
		})
	default: // "tenant_id" 或非白名单 → 默认 tenant_id
		sort.Slice(ms, func(i, j int) bool {
			if desc {
				return ms[i].TenantID > ms[j].TenantID
			}
			return ms[i].TenantID < ms[j].TenantID
		})
	}

	// 4. 内存分页
	limit, offset := pageOf(r.Page)
	if offset > len(ms) {
		offset = len(ms)
	}
	end := offset + limit
	if end > len(ms) {
		end = len(ms)
	}
	ms = ms[offset:end]

	out := &adminv1.ListMyTenantsResponse{IsOperatingPlane: op, Total: total}
	for _, m := range ms {
		out.Memberships = append(out.Memberships, &adminv1.TenantMembershipSummary{
			TenantId: uint64(m.TenantID), TenantName: m.TenantName, Tier: uint32(m.Tier)})
	}
	return out, nil
}

// InviteMember（tenant-target，owner/admin 可调）：在 tenant_id 建 admin 档成员。
// 新 principal 生成一次性 secret 返回；既有 operator（多租户）不返回新 secret（复用既有凭据）。
func (s *AdminServer) InviteMember(ctx context.Context, r *adminv1.InviteMemberRequest) (*adminv1.InviteMemberResponse, error) {
	if !auth.ValidPrincipal(r.Principal) {
		return nil, status.Error(codes.InvalidArgument, "valid principal required")
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

	opID, created, err := adminauthz.EnsureOperator(ctx, tx, r.Principal, enc)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "ensure operator: %v", err)
	}
	inserted, err := adminauthz.InsertMembership(ctx, tx, int64(r.TenantId), opID, adminauthz.TierAdmin)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if !inserted {
		return nil, status.Error(codes.AlreadyExists, "already a member")
	}
	if err := adminauthz.BindTenantAdminTx(ctx, tx, int64(r.TenantId), opID); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := adminauthz.BumpPolicyVersion(ctx, tx); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	ver, err := adminauthz.ReadPolicyVersion(ctx, tx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := adminauthz.InsertAdminAudit(ctx, tx,
		sql.NullInt64{Int64: int64(r.TenantId), Valid: true}, cp.OperatorFromContext(ctx),
		"invite", "membership", fmt.Sprintf("%d", opID),
		auditJSON(map[string]any{"after": map[string]any{
			"principal": r.Principal, "tier": "admin"}}),
		sql.NullInt64{Int64: ver, Valid: true}); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "commit: %v", err)
	}
	resp := &adminv1.InviteMemberResponse{OperatorId: uint64(opID), Principal: r.Principal}
	if created {
		resp.Secret = secret // 仅新建 operator 返回一次性 secret
	}
	return resp, nil
}

// ListMembers（tenant-target 读）：列 tenant_id 的成员；secret_enc 绝不出查询。
func (s *AdminServer) ListMembers(ctx context.Context, r *adminv1.ListMembersRequest) (*adminv1.ListMembersResponse, error) {
	// base cond：m.tenant_id 始终限定租户（保留 tenant scope）
	conds := []string{"m.tenant_id = $1"}
	args := []any{int64(r.TenantId)}
	add := func(cond string, val any) {
		args = append(args, val)
		conds = append(conds, cond+" $"+strconv.Itoa(len(args)))
	}
	if r.Tier != 0 {
		add("m.tier =", int16(r.Tier))
	}
	if sc, arg := searchClause(r.Page.GetQ(), []string{"o.principal"}, len(args)+1); sc != "" {
		args = append(args, arg)
		conds = append(conds, sc)
	}
	where := strings.Join(conds, " AND ")
	var total uint32
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM tenant_membership m JOIN admin_operator o ON o.id = m.operator_id WHERE `+where,
		args...).Scan(&total); err != nil {
		return nil, status.Errorf(codes.Internal, "count members: %v", err)
	}
	order := resolveOrder(r.Page.GetSort(), r.Page.GetOrder(),
		map[string]string{"operator_id": "o.id", "principal": "o.principal", "tier": "m.tier"}, "o.id")
	limit, offset := pageOf(r.Page)
	args = append(args, limit, offset)
	q := `SELECT o.id, o.principal, m.tier, o.status
		 FROM tenant_membership m JOIN admin_operator o ON o.id = m.operator_id WHERE ` + where +
		` ORDER BY ` + order + ` LIMIT $` + strconv.Itoa(len(args)-1) + ` OFFSET $` + strconv.Itoa(len(args))
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list members: %v", err)
	}
	defer rows.Close()
	out := &adminv1.ListMembersResponse{Total: total}
	for rows.Next() {
		var x adminv1.MemberSummary
		var tier, st int16
		if err := rows.Scan(&x.OperatorId, &x.Principal, &tier, &st); err != nil {
			return nil, status.Errorf(codes.Internal, "scan member: %v", err)
		}
		x.Tier, x.Status = uint32(tier), uint32(st)
		out.Members = append(out.Members, &x)
	}
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "rows member: %v", err)
	}
	return out, nil
}
