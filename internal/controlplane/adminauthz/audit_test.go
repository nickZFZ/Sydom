package adminauthz_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestQueryAdminAudit_TenantScopeAndAll(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	mk := func(tid sql.NullInt64, et string) {
		require.NoError(t, adminauthz.InsertAdminAudit(ctx, db, tid, "root", "x", et, "1", nil, sql.NullInt64{}))
	}
	mk(sql.NullInt64{Int64: 1, Valid: true}, "application")
	mk(sql.NullInt64{Int64: 2, Valid: true}, "application")
	mk(sql.NullInt64{}, "operator") // 纯系统级
	// 租户 1 过滤 → 仅 tenant_id=1
	e1, _, err := adminauthz.QueryAdminAudit(ctx, db,
		adminauthz.AdminAuditFilter{TenantID: sql.NullInt64{Int64: 1, Valid: true}, Limit: 50})
	require.NoError(t, err)
	require.Len(t, e1, 1)
	require.Equal(t, int64(1), e1[0].TenantID.Int64)
	// 超管全量（TenantID 不 Valid）→ 全部 3 条
	eAll, _, err := adminauthz.QueryAdminAudit(ctx, db, adminauthz.AdminAuditFilter{Limit: 50})
	require.NoError(t, err)
	require.Len(t, eAll, 3)
}

func TestInsertAdminAudit_RoundTrip(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	err := adminauthz.InsertAdminAudit(ctx, db,
		sql.NullInt64{Int64: 7, Valid: true}, "root@sydom", "create",
		"application", "42", []byte(`{"after":{"name":"x"}}`),
		sql.NullInt64{Int64: 3, Valid: true})
	require.NoError(t, err)

	var tid, ver sql.NullInt64
	var op, act, et string
	var diff sql.NullString
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT tenant_id, operator, action, entity_type, diff, admin_version
		 FROM admin_audit_log ORDER BY id DESC LIMIT 1`).
		Scan(&tid, &op, &act, &et, &diff, &ver))
	require.Equal(t, int64(7), tid.Int64)
	require.Equal(t, "root@sydom", op)
	require.Equal(t, "application", et)
	require.Contains(t, diff.String, "after")
	require.Equal(t, int64(3), ver.Int64)
}

func TestInsertAdminAudit_NullTenantAndVersion(t *testing.T) {
	db := dbtest.SetupSchema(t)
	require.NoError(t, adminauthz.InsertAdminAudit(context.Background(), db,
		sql.NullInt64{}, "root@sydom", "reset", "operator", "9", nil, sql.NullInt64{}))
	var tid, ver sql.NullInt64
	require.NoError(t, db.QueryRow(
		`SELECT tenant_id, admin_version FROM admin_audit_log ORDER BY id DESC LIMIT 1`).Scan(&tid, &ver))
	require.False(t, tid.Valid)
	require.False(t, ver.Valid)
}
