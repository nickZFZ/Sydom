package store_test

import (
	"database/sql"
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest" // blank-imports lib/pq，注册 postgres 驱动
	"github.com/stretchr/testify/require"
)

// 复合 FK 应拒绝「本行 app_id 与被引用 role/permission 的 app_id 不一致」的跨 app 引用。
func TestCompositeFK_RejectsCrossAppPermission(t *testing.T) {
	dsn := dbtest.MigratedDSN(t)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, appA := dbtest.SeedAppInTenant(t, db, "fk-a", "fk-a", "fk-a-key")
	_, appB := dbtest.SeedAppInTenant(t, db, "fk-b", "fk-b", "fk-b-key")

	var roleA int64
	require.NoError(t, db.QueryRow(
		`INSERT INTO role (app_id, code, name) VALUES ($1, 'r', 'R') RETURNING id`, appA).Scan(&roleA))
	var permB int64
	require.NoError(t, db.QueryRow(
		`INSERT INTO permission (app_id, code, resource, action, type, name)
		 VALUES ($1, 'c', 'res', 'act', 'data', 'P') RETURNING id`, appB).Scan(&permB))

	// app_id=A、role=A 的、permission=B 的 → 复合 FK (app_id,permission_id) 应拒绝。
	_, err = db.Exec(
		`INSERT INTO role_permission (app_id, role_id, permission_id) VALUES ($1, $2, $3)`,
		appA, roleA, permB)
	require.Error(t, err, "跨 app permission 引用必须被复合 FK 拒绝")
	require.Contains(t, err.Error(), "foreign key")
}

// 同 app 引用应正常通过（复合 FK 不破坏合法写入）。
func TestCompositeFK_AllowsSameApp(t *testing.T) {
	dsn := dbtest.MigratedDSN(t)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, app := dbtest.SeedAppInTenant(t, db, "fk-ok", "fk-ok", "fk-ok-key")
	var roleID, permID int64
	require.NoError(t, db.QueryRow(
		`INSERT INTO role (app_id, code, name) VALUES ($1, 'r', 'R') RETURNING id`, app).Scan(&roleID))
	require.NoError(t, db.QueryRow(
		`INSERT INTO permission (app_id, code, resource, action, type, name)
		 VALUES ($1, 'c', 'res', 'act', 'data', 'P') RETURNING id`, app).Scan(&permID))
	_, err = db.Exec(
		`INSERT INTO role_permission (app_id, role_id, permission_id) VALUES ($1, $2, $3)`,
		app, roleID, permID)
	require.NoError(t, err)
}
