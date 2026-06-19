package adminauthz_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

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
