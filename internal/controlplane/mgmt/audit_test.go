package mgmt_test

import (
	"context"
	"database/sql"
	"testing"

	adminv1 "github.com/nickZFZ/Sydom/gen/sydom/admin/v1"
	cp "github.com/nickZFZ/Sydom/internal/controlplane"
	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/controlplane/store"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// AUD-2：RotateApplicationSecret 落审计且 diff 不含新 secret。
func TestRotateApplicationSecret_AuditsWithoutSecret(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	s := accountsSrv(db)
	ctx := cp.WithOperator(context.Background(), "root@sydom")
	resp, err := s.RotateApplicationSecret(ctx, &adminv1.RotateApplicationSecretRequest{AppId: uint64(appID)})
	require.NoError(t, err)
	var op, et string
	var diff sql.NullString
	require.NoError(t, db.QueryRow(
		`SELECT operator, entity_type, diff FROM admin_audit_log ORDER BY id DESC LIMIT 1`).Scan(&op, &et, &diff))
	require.Equal(t, "root@sydom", op)
	require.Equal(t, "application", et)
	require.NotContains(t, diff.String, resp.AppSecret) // 新 secret 绝不入 diff
}

// AUD-2：ResetOperatorSecret diff 不含新 secret。
func TestResetOperatorSecret_AuditsWithoutSecret(t *testing.T) {
	db := dbtest.SetupSchema(t)
	s := accountsSrv(db)
	ctx := cp.WithOperator(context.Background(), "root@sydom")
	co, err := s.CreateOperator(ctx, &adminv1.CreateOperatorRequest{Principal: "op1@x"})
	require.NoError(t, err)
	ro, err := s.ResetOperatorSecret(ctx, &adminv1.ResetOperatorSecretRequest{OperatorId: co.OperatorId})
	require.NoError(t, err)
	var diff sql.NullString
	require.NoError(t, db.QueryRow(
		`SELECT diff FROM admin_audit_log WHERE action='reset_secret' ORDER BY id DESC LIMIT 1`).Scan(&diff))
	require.NotContains(t, diff.String, ro.Secret)
}

// AUD-1：CreateApplication 审计失败 → 整笔回滚（app 未落库）。
func TestCreateApplication_AuditAtomic(t *testing.T) {
	db := dbtest.SetupSchema(t)
	var tenantID int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant(name) VALUES('acme') RETURNING id`).Scan(&tenantID))
	_, err := db.Exec(`DROP TABLE admin_audit_log`)
	require.NoError(t, err)
	s := accountsSrv(db)
	ctx := cp.WithOperator(context.Background(), "root@sydom")
	_, err = s.CreateApplication(ctx, &adminv1.CreateApplicationRequest{
		TenantId: uint64(tenantID), Domain: "d1", Name: "n1", AppKey: "k1"})
	require.Error(t, err) // 审计 INSERT 失败 → 整笔回滚
	var cnt int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM application WHERE app_key='k1'`).Scan(&cnt))
	require.Equal(t, 0, cnt)
}

// CreateApplication diff 含 name/app_key 但不含 secret。
func TestCreateApplication_AuditDiffNoSecret(t *testing.T) {
	db := dbtest.SetupSchema(t)
	var tenantID int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant(name) VALUES('acme') RETURNING id`).Scan(&tenantID))
	s := accountsSrv(db)
	ctx := cp.WithOperator(context.Background(), "root@sydom")
	resp, err := s.CreateApplication(ctx, &adminv1.CreateApplicationRequest{
		TenantId: uint64(tenantID), Domain: "d1", Name: "n1", AppKey: "k1"})
	require.NoError(t, err)
	var tid sql.NullInt64
	var diff sql.NullString
	require.NoError(t, db.QueryRow(
		`SELECT tenant_id, diff FROM admin_audit_log ORDER BY id DESC LIMIT 1`).Scan(&tid, &diff))
	require.Equal(t, tenantID, tid.Int64)
	require.Contains(t, diff.String, "k1")              // app_key 在
	require.NotContains(t, diff.String, resp.AppSecret) // secret 不在
}

// admin_version：GrantAdminRole 落审计且 admin_version 记新版本。
func TestGrantAdminRole_AuditsWithVersion(t *testing.T) {
	db := dbtest.SetupSchema(t)
	var roleID int64
	require.NoError(t, db.QueryRow(`INSERT INTO admin_role(code,name) VALUES('r1','R1') RETURNING id`).Scan(&roleID))
	s := accountsSrv(db)
	ctx := cp.WithOperator(context.Background(), "root@sydom")
	_, err := s.GrantAdminRole(ctx, &adminv1.GrantAdminRoleRequest{
		RoleId: roleID, Domain: "*", Resource: "role", Action: "read"})
	require.NoError(t, err)
	var ver sql.NullInt64
	var et string
	require.NoError(t, db.QueryRow(
		`SELECT entity_type, admin_version FROM admin_audit_log ORDER BY id DESC LIMIT 1`).Scan(&et, &ver))
	require.Equal(t, "admin_grant", et)
	require.True(t, ver.Valid)
}

// RegisterTenant：diff 不含 owner_secret，operator=owner_principal，tenant_id=新租户。
func TestRegisterTenant_AuditNoSecret(t *testing.T) {
	db := dbtest.SetupSchema(t)
	s := accountsSrv(db)
	resp, err := s.RegisterTenant(context.Background(), &adminv1.RegisterTenantRequest{
		TenantName: "acme", OwnerPrincipal: "owner1"})
	require.NoError(t, err)
	var op string
	var tid sql.NullInt64
	var diff sql.NullString
	require.NoError(t, db.QueryRow(
		`SELECT operator, tenant_id, diff FROM admin_audit_log WHERE entity_type='tenant' ORDER BY id DESC LIMIT 1`).
		Scan(&op, &tid, &diff))
	require.Equal(t, "owner1", op)
	require.Equal(t, int64(resp.TenantId), tid.Int64)
	require.NotContains(t, diff.String, resp.OwnerSecret)
}

// domainTenant：t:<id> 域 → 审计 tenant_id 落该租户（GrantAdminRole on tenant domain）。
func TestGrantAdminRole_TenantDomainAudit(t *testing.T) {
	db := dbtest.SetupSchema(t)
	var tenantID, roleID int64
	require.NoError(t, db.QueryRow(`INSERT INTO tenant(name) VALUES('acme') RETURNING id`).Scan(&tenantID))
	require.NoError(t, db.QueryRow(`INSERT INTO admin_role(code,name) VALUES('r1','R1') RETURNING id`).Scan(&roleID))
	s := accountsSrv(db)
	ctx := cp.WithOperator(context.Background(), "root@sydom")
	_, err := s.GrantAdminRole(ctx, &adminv1.GrantAdminRoleRequest{
		RoleId: roleID, Domain: adminauthz.TenantDomain(tenantID), Resource: "role", Action: "read"})
	require.NoError(t, err)
	var tid sql.NullInt64
	require.NoError(t, db.QueryRow(
		`SELECT tenant_id FROM admin_audit_log ORDER BY id DESC LIMIT 1`).Scan(&tid))
	require.True(t, tid.Valid)
	require.Equal(t, tenantID, tid.Int64)
}

// QueryAuditLog 返回 app 审计、不 bump 版本（AUD-5）。
func TestQueryAuditLog_ReadsAndNoBump(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)
	require.NoError(t, store.InsertAudit(context.Background(), db, appID,
		"alice", "create", "role", "1", []byte(`{"adds":[]}`), 1))
	var v0 int64
	require.NoError(t, db.QueryRow(`SELECT current_version FROM application WHERE id=$1`, appID).Scan(&v0))
	s := accountsSrv(db)
	ctx := cp.WithOperator(context.Background(), "alice")
	resp, err := s.QueryAuditLog(ctx, &adminv1.QueryAuditLogRequest{AppId: uint64(appID), Limit: 50})
	require.NoError(t, err)
	require.Len(t, resp.Entries, 1)
	require.Equal(t, "role", resp.Entries[0].EntityType)
	var v1 int64
	require.NoError(t, db.QueryRow(`SELECT current_version FROM application WHERE id=$1`, appID).Scan(&v1))
	require.Equal(t, v0, v1)
}

// QueryAdminAuditLog：超管 tenant_id=0 看全部；指定 tenant_id 仅该租户。
func TestQueryAdminAuditLog_TenantScope(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	require.NoError(t, adminauthz.InsertAdminAudit(ctx, db, sql.NullInt64{Int64: 1, Valid: true}, "root", "create", "application", "1", nil, sql.NullInt64{}))
	require.NoError(t, adminauthz.InsertAdminAudit(ctx, db, sql.NullInt64{}, "root", "reset", "operator", "9", nil, sql.NullInt64{}))
	s := accountsSrv(db)
	sctx := cp.WithOperator(ctx, "root@sydom")
	all, err := s.QueryAdminAuditLog(sctx, &adminv1.QueryAdminAuditLogRequest{TenantId: 0, Limit: 50})
	require.NoError(t, err)
	require.Len(t, all.Entries, 2)
	one, err := s.QueryAdminAuditLog(sctx, &adminv1.QueryAdminAuditLogRequest{TenantId: 1, Limit: 50})
	require.NoError(t, err)
	require.Len(t, one.Entries, 1)
}
