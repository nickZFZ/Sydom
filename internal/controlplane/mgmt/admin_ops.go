package mgmt

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"

	"github.com/lib/pq"
	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
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
	var appID int64
	err = s.db.QueryRowContext(ctx,
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
	return &adminv1.CreateApplicationResponse{AppId: uint64(appID), AppSecret: secret}, nil
}

// isForeignKeyViolation 判定是否外键冲突（SQLSTATE 23503）。
func isForeignKeyViolation(err error) bool {
	var pqErr *pq.Error
	return errors.As(err, &pqErr) && pqErr.Code == "23503"
}

func (s *AdminServer) SetApplicationStatus(ctx context.Context, r *adminv1.SetApplicationStatusRequest) (*adminv1.WriteResponse, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE application SET status=$1 WHERE id=$2`, int16(r.Status), int64(r.AppId))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "set status: %v", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, status.Error(codes.NotFound, "unknown application")
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
	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "commit: %v", err)
	}
	return &adminv1.WriteResponse{Changed: true}, nil
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
	if err := tx.Commit(); err != nil {
		return nil, status.Errorf(codes.Internal, "commit: %v", err)
	}
	return &adminv1.WriteResponse{Changed: true}, nil
}
