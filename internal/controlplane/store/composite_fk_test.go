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
	// 断言具体约束名（lib/pq FK 错误固定含约束名），隔离验证是 permission 侧 FK 触发，
	// 避免无关 FK 误判假绿。
	_, err = db.Exec(
		`INSERT INTO role_permission (app_id, role_id, permission_id) VALUES ($1, $2, $3)`,
		appA, roleA, permB)
	require.Error(t, err, "跨 app permission 引用必须被复合 FK 拒绝")
	require.Contains(t, err.Error(), "fk_role_permission_permission_app")
}

// role 侧复合 FK 的隔离回归保护：构造一行使 permission 侧 (app_id,permission_id) 满足、
// 唯独 role 侧 (app_id,role_id) 不匹配，从而只有 fk_role_permission_role_app 触发拒绝。
// 这样即便 permission 侧 FK 被误删，本测试仍能捕获 role 侧 FK 的缺失（有「齿」）。
func TestCompositeFK_RejectsCrossAppRole(t *testing.T) {
	dsn := dbtest.MigratedDSN(t)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, appA := dbtest.SeedAppInTenant(t, db, "fk-ra", "fk-ra", "fk-ra-key")
	_, appB := dbtest.SeedAppInTenant(t, db, "fk-rb", "fk-rb", "fk-rb-key")

	var roleB int64
	require.NoError(t, db.QueryRow(
		`INSERT INTO role (app_id, code, name) VALUES ($1, 'r', 'R') RETURNING id`, appB).Scan(&roleB))
	var permA int64
	require.NoError(t, db.QueryRow(
		`INSERT INTO permission (app_id, code, resource, action, type, name)
		 VALUES ($1, 'c', 'res', 'act', 'data', 'P') RETURNING id`, appA).Scan(&permA))

	// app_id=A、role=B 的、permission=A 的 → permission FK (appA,permA) 满足；
	// 唯独 role FK (appA,roleB) 不匹配 role(app_id=appB) → 仅 role 侧 FK 拒绝。
	_, err = db.Exec(
		`INSERT INTO role_permission (app_id, role_id, permission_id) VALUES ($1, $2, $3)`,
		appA, roleB, permA)
	require.Error(t, err, "跨 app role 引用必须被复合 FK 拒绝")
	require.Contains(t, err.Error(), "fk_role_permission_role_app")
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
