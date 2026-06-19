package mgmt

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/lib/pq"
	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/crypto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// isUniqueViolation 判定 err（含被 %w 包裹的）是否为 PostgreSQL 唯一约束冲突（SQLSTATE 23505）。
func isUniqueViolation(err error) bool {
	var pqErr *pq.Error
	return errors.As(err, &pqErr) && pqErr.Code == "23505"
}

// genSecret 生成 32 字节随机凭据的 hex 串。
func genSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (s *AdminServer) CreateApplication(ctx context.Context, r *adminv1.CreateApplicationRequest) (*adminv1.CreateApplicationResponse, error) {
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
	var appID int64
	err = tx.QueryRowContext(ctx,
		`INSERT INTO application (tenant_id, domain, name, app_key, app_secret_enc)
		 VALUES ($1,$2,$3,$4,$5) RETURNING id`,
		int64(r.TenantId), r.Domain, r.Name, r.AppKey, enc).Scan(&appID)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, status.Errorf(codes.AlreadyExists, "create application: %v", err)
		}
		if isForeignKeyViolation(err) { // 目标租户不存在
			return nil, status.Error(codes.InvalidArgument, "unknown tenant")
		}
		return nil, status.Errorf(codes.Internal, "create application: %v", err)
	}
	if err := adminauthz.InsertAdminAudit(ctx, tx,
		sql.NullInt64{Int64: int64(r.TenantId), Valid: true}, cp.OperatorFromContext(ctx),
		"create", "application", fmt.Sprintf("%d", appID),
		auditJSON(map[string]any{"after": map[string]any{
			"name": r.Name, "app_key": r.AppKey, "domain": r.Domain, "tenant_id": r.TenantId}}),
		sql.NullInt64{}); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "commit: %v", err)
	}
	return &adminv1.CreateApplicationResponse{AppId: uint64(appID), AppSecret: secret}, nil
}

// isForeignKeyViolation 判定是否外键冲突（SQLSTATE 23503）。
func isForeignKeyViolation(err error) bool {
	var pqErr *pq.Error
	return errors.As(err, &pqErr) && pqErr.Code == "23503"
}

func (s *AdminServer) SetApplicationStatus(ctx context.Context, r *adminv1.SetApplicationStatusRequest) (*adminv1.WriteResponse, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin: %v", err)
	}
	defer tx.Rollback()
	var before int16
	var tid sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		`SELECT status, tenant_id FROM application WHERE id=$1`, int64(r.AppId)).Scan(&before, &tid); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "unknown application")
		}
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE application SET status=$1 WHERE id=$2`, int16(r.Status), int64(r.AppId))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "set status: %v", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, status.Error(codes.NotFound, "unknown application")
	}
	if err := adminauthz.InsertAdminAudit(ctx, tx, tid, cp.OperatorFromContext(ctx),
		"set_status", "application", fmt.Sprintf("%d", r.AppId),
		auditJSON(map[string]any{"before": before, "after": r.Status}),
		sql.NullInt64{}); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "commit: %v", err)
	}
	return &adminv1.WriteResponse{Changed: true}, nil
}

func (s *AdminServer) ListApplications(ctx context.Context, r *adminv1.ListApplicationsRequest) (*adminv1.ListApplicationsResponse, error) {
	var rows *sql.Rows
	var err error
	if r.TenantId == 0 { // 运营平面：列全量（授权已确保仅超管可达 tenant_id=0）
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, domain, name, app_key, status, current_version FROM application ORDER BY id`)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, domain, name, app_key, status, current_version FROM application WHERE tenant_id=$1 ORDER BY id`, int64(r.TenantId))
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list: %v", err)
	}
	defer rows.Close()
	out := &adminv1.ListApplicationsResponse{}
	for rows.Next() {
		var a adminv1.ApplicationSummary
		var id, ver int64
		var st int16
		if err := rows.Scan(&id, &a.Domain, &a.Name, &a.AppKey, &st, &ver); err != nil {
			return nil, status.Errorf(codes.Internal, "scan: %v", err)
		}
		a.AppId, a.Status, a.CurrentVersion = uint64(id), uint32(st), uint64(ver)
		out.Applications = append(out.Applications, &a)
	}
	if err := rows.Err(); err != nil {
		return nil, status.Errorf(codes.Internal, "rows: %v", err)
	}
	return out, nil
}

// —— 管理员自管：写后 bump admin_policy_version 触发 enforcer 重载 ——

func (s *AdminServer) CreateOperator(ctx context.Context, r *adminv1.CreateOperatorRequest) (*adminv1.CreateOperatorResponse, error) {
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
	id, err := adminauthz.InsertOperator(ctx, tx, r.Principal, enc)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, status.Errorf(codes.AlreadyExists, "%v", err)
		}
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := adminauthz.BumpPolicyVersion(ctx, tx); err != nil {
		return nil, status.Errorf(codes.Internal, "bump: %v", err)
	}
	ver, err := adminauthz.ReadPolicyVersion(ctx, tx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := adminauthz.InsertAdminAudit(ctx, tx, sql.NullInt64{}, cp.OperatorFromContext(ctx),
		"create", "operator", fmt.Sprintf("%d", id),
		auditJSON(map[string]any{"after": map[string]any{"principal": r.Principal}}),
		sql.NullInt64{Int64: ver, Valid: true}); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "commit: %v", err)
	}
	return &adminv1.CreateOperatorResponse{OperatorId: id, Secret: secret}, nil
}

// 修正 A：原子事务 + 不忽略 bump 错误（一致性红线）。
func (s *AdminServer) SetOperatorStatus(ctx context.Context, r *adminv1.SetOperatorStatusRequest) (*adminv1.WriteResponse, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin: %v", err)
	}
	defer tx.Rollback()
	var before int16
	if err := tx.QueryRowContext(ctx,
		`SELECT status FROM admin_operator WHERE id=$1`, r.OperatorId).Scan(&before); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "unknown operator")
		}
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE admin_operator SET status=$1 WHERE id=$2`, int16(r.Status), r.OperatorId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "set operator status: %v", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, status.Error(codes.NotFound, "unknown operator")
	}
	if err := adminauthz.BumpPolicyVersion(ctx, tx); err != nil {
		return nil, status.Errorf(codes.Internal, "bump: %v", err)
	}
	ver, err := adminauthz.ReadPolicyVersion(ctx, tx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := adminauthz.InsertAdminAudit(ctx, tx, sql.NullInt64{}, cp.OperatorFromContext(ctx),
		"set_status", "operator", fmt.Sprintf("%d", r.OperatorId),
		auditJSON(map[string]any{"before": before, "after": r.Status}),
		sql.NullInt64{Int64: ver, Valid: true}); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "commit: %v", err)
	}
	return &adminv1.WriteResponse{Changed: true}, nil
}

func (s *AdminServer) CreateAdminRole(ctx context.Context, r *adminv1.CreateAdminRoleRequest) (*adminv1.CreateAdminRoleResponse, error) {
	id, err := adminauthz.InsertRole(ctx, s.db, r.Code, r.Name)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, status.Errorf(codes.AlreadyExists, "%v", err)
		}
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	return &adminv1.CreateAdminRoleResponse{RoleId: id}, nil
}

func (s *AdminServer) GrantAdminRole(ctx context.Context, r *adminv1.GrantAdminRoleRequest) (*adminv1.WriteResponse, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin: %v", err)
	}
	defer tx.Rollback()
	if err := adminauthz.InsertRoleGrant(ctx, tx, r.RoleId, r.Domain, r.Resource, r.Action); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := adminauthz.BumpPolicyVersion(ctx, tx); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	ver, err := adminauthz.ReadPolicyVersion(ctx, tx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := adminauthz.InsertAdminAudit(ctx, tx, domainTenant(r.Domain), cp.OperatorFromContext(ctx),
		"grant", "admin_grant", fmt.Sprintf("%d", r.RoleId),
		auditJSON(map[string]any{"after": map[string]any{
			"role_id": r.RoleId, "domain": r.Domain, "resource": r.Resource, "action": r.Action}}),
		sql.NullInt64{Int64: ver, Valid: true}); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "commit: %v", err)
	}
	return &adminv1.WriteResponse{Changed: true}, nil
}

func (s *AdminServer) BindOperatorRole(ctx context.Context, r *adminv1.BindOperatorRoleRequest) (*adminv1.WriteResponse, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin: %v", err)
	}
	defer tx.Rollback()
	if err := adminauthz.InsertSubjectRole(ctx, tx, r.OperatorId, r.RoleId, r.Domain); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := adminauthz.BumpPolicyVersion(ctx, tx); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	ver, err := adminauthz.ReadPolicyVersion(ctx, tx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := adminauthz.InsertAdminAudit(ctx, tx, domainTenant(r.Domain), cp.OperatorFromContext(ctx),
		"bind", "admin_binding", fmt.Sprintf("%d", r.OperatorId),
		auditJSON(map[string]any{"after": map[string]any{
			"operator_id": r.OperatorId, "role_id": r.RoleId, "domain": r.Domain}}),
		sql.NullInt64{Int64: ver, Valid: true}); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "commit: %v", err)
	}
	return &adminv1.WriteResponse{Changed: true}, nil
}

// RevokeAdminGrant 撤一条管理授权（GrantAdminRole 的逆）。原子事务 + 必 bump（撤权立即生效）。
// 撤不存在的授权 → 回滚 + NotFound（严格，不幂等；防版本跳变 / 幽灵 delta）。
func (s *AdminServer) RevokeAdminGrant(ctx context.Context, r *adminv1.RevokeAdminGrantRequest) (*adminv1.WriteResponse, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin: %v", err)
	}
	defer tx.Rollback()
	if err := adminauthz.DeleteRoleGrant(ctx, tx, r.RoleId, r.Domain, r.Resource, r.Action); err != nil {
		if errors.Is(err, adminauthz.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "grant not found")
		}
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := adminauthz.BumpPolicyVersion(ctx, tx); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	ver, err := adminauthz.ReadPolicyVersion(ctx, tx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := adminauthz.InsertAdminAudit(ctx, tx, domainTenant(r.Domain), cp.OperatorFromContext(ctx),
		"revoke", "admin_grant", fmt.Sprintf("%d", r.RoleId),
		auditJSON(map[string]any{"before": map[string]any{
			"role_id": r.RoleId, "domain": r.Domain, "resource": r.Resource, "action": r.Action}}),
		sql.NullInt64{Int64: ver, Valid: true}); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "commit: %v", err)
	}
	return &adminv1.WriteResponse{Changed: true}, nil
}

// RotateApplicationSecret 硬切换 app 的 HMAC 凭据：生成新 secret、加密、覆盖 app_secret_enc、
// 旧 secret 即刻失效（resolver 每请求查库，无缓存）。不 bump（secret 非 casbin 策略）。
// 单语句 UPDATE 本身原子，无需事务（镜像 SetApplicationStatus）。新 secret 一次性返回。
func (s *AdminServer) RotateApplicationSecret(ctx context.Context, r *adminv1.RotateApplicationSecretRequest) (*adminv1.RotateApplicationSecretResponse, error) {
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
	res, err := tx.ExecContext(ctx,
		`UPDATE application SET app_secret_enc=$1 WHERE id=$2`, enc, int64(r.AppId))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "rotate app secret: %v", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, status.Error(codes.NotFound, "unknown application")
	}
	var tid sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		`SELECT tenant_id FROM application WHERE id=$1`, int64(r.AppId)).Scan(&tid); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := adminauthz.InsertAdminAudit(ctx, tx, tid, cp.OperatorFromContext(ctx),
		"rotate_secret", "application", fmt.Sprintf("%d", r.AppId),
		auditJSON(map[string]any{"rotated": true}), sql.NullInt64{}); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "commit: %v", err)
	}
	return &adminv1.RotateApplicationSecretResponse{AppSecret: secret}, nil
}

// ResetOperatorSecret 硬切换 operator 的 HMAC 凭据：生成新 secret、加密、覆盖 secret_enc、
// 旧 secret 即刻失效。不 bump。单语句 UPDATE 本身原子（镜像 SetOperatorStatus 去掉 bump/tx）。
func (s *AdminServer) ResetOperatorSecret(ctx context.Context, r *adminv1.ResetOperatorSecretRequest) (*adminv1.ResetOperatorSecretResponse, error) {
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
	res, err := tx.ExecContext(ctx,
		`UPDATE admin_operator SET secret_enc=$1 WHERE id=$2`, enc, r.OperatorId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "reset operator secret: %v", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, status.Error(codes.NotFound, "unknown operator")
	}
	if err := adminauthz.InsertAdminAudit(ctx, tx, sql.NullInt64{}, cp.OperatorFromContext(ctx),
		"reset_secret", "operator", fmt.Sprintf("%d", r.OperatorId),
		auditJSON(map[string]any{"reset": true}), sql.NullInt64{}); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "commit: %v", err)
	}
	return &adminv1.ResetOperatorSecretResponse{Secret: secret}, nil
}

// UnbindOperatorRole 解绑操作员与管理角色（BindOperatorRole 的逆）。原子事务 + 必 bump。
// 解绑不存在的绑定 → 回滚 + NotFound。
func (s *AdminServer) UnbindOperatorRole(ctx context.Context, r *adminv1.UnbindOperatorRoleRequest) (*adminv1.WriteResponse, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "begin: %v", err)
	}
	defer tx.Rollback()
	if err := adminauthz.DeleteSubjectRole(ctx, tx, r.OperatorId, r.RoleId, r.Domain); err != nil {
		if errors.Is(err, adminauthz.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "binding not found")
		}
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := adminauthz.BumpPolicyVersion(ctx, tx); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	ver, err := adminauthz.ReadPolicyVersion(ctx, tx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := adminauthz.InsertAdminAudit(ctx, tx, domainTenant(r.Domain), cp.OperatorFromContext(ctx),
		"unbind", "admin_binding", fmt.Sprintf("%d", r.OperatorId),
		auditJSON(map[string]any{"before": map[string]any{
			"operator_id": r.OperatorId, "role_id": r.RoleId, "domain": r.Domain}}),
		sql.NullInt64{Int64: ver, Valid: true}); err != nil {
		return nil, status.Errorf(codes.Internal, "%v", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "commit: %v", err)
	}
	return &adminv1.WriteResponse{Changed: true}, nil
}
