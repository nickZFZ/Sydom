package db

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPostgresContainerStarts(t *testing.T) {
	dsn := startPostgres(t)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.Ping())
}

func TestTenant_NameUnique(t *testing.T) {
	db := setupSchema(t)

	_, err := db.Exec(`INSERT INTO tenant (name) VALUES ('acme')`)
	require.NoError(t, err)

	_, err = db.Exec(`INSERT INTO tenant (name) VALUES ('acme')`)
	require.Error(t, err)

	var status int
	require.NoError(t, db.QueryRow(
		`SELECT status FROM tenant WHERE name = 'acme'`).Scan(&status))
	require.Equal(t, 1, status)
}

func TestApplication_Constraints(t *testing.T) {
	db := setupSchema(t)

	var tenantID int64
	require.NoError(t, db.QueryRow(
		`INSERT INTO tenant (name) VALUES ('acme') RETURNING id`).Scan(&tenantID))

	_, err := db.Exec(`INSERT INTO application (tenant_id, domain, name, app_key, app_secret_hash)
		VALUES ($1, 'order-system', '订单系统', 'AK_order', 'hash1')`, tenantID)
	require.NoError(t, err)

	var ver int64
	require.NoError(t, db.QueryRow(
		`SELECT current_version FROM application WHERE app_key = 'AK_order'`).Scan(&ver))
	require.Equal(t, int64(0), ver)

	_, err = db.Exec(`INSERT INTO application (tenant_id, domain, name, app_key, app_secret_hash)
		VALUES ($1, 'other', '其他', 'AK_order', 'hash2')`, tenantID)
	require.Error(t, err)

	_, err = db.Exec(`INSERT INTO application (tenant_id, domain, name, app_key, app_secret_hash)
		VALUES ($1, 'order-system', '重复域', 'AK_dup', 'hash3')`, tenantID)
	require.Error(t, err)

	_, err = db.Exec(`INSERT INTO application (tenant_id, domain, name, app_key, app_secret_hash)
		VALUES (999999, 'x', 'x', 'AK_x', 'hashx')`)
	require.Error(t, err)
}

// seedApp 建一个租户+应用，返回 app_id。供需要 app 上下文的表测试复用。
func seedApp(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	var tenantID, appID int64
	require.NoError(t, db.QueryRow(
		`INSERT INTO tenant (name) VALUES ('acme') RETURNING id`).Scan(&tenantID))
	require.NoError(t, db.QueryRow(
		`INSERT INTO application (tenant_id, domain, name, app_key, app_secret_hash)
		 VALUES ($1, 'order-system', '订单系统', 'AK_order', 'hash1') RETURNING id`,
		tenantID).Scan(&appID))
	return appID
}

func TestRole_AppCodeUnique(t *testing.T) {
	db := setupSchema(t)
	appID := seedApp(t, db)

	_, err := db.Exec(`INSERT INTO role (app_id, code, name) VALUES ($1, 'manager', '经理')`, appID)
	require.NoError(t, err)

	_, err = db.Exec(`INSERT INTO role (app_id, code, name) VALUES ($1, 'manager', '重复')`, appID)
	require.Error(t, err)

	// app_id 外键：不存在的应用应被拒绝
	_, err = db.Exec(`INSERT INTO role (app_id, code, name) VALUES (999999, 'x', 'x')`)
	require.Error(t, err)
}

func TestPermission_AppCodeUnique(t *testing.T) {
	db := setupSchema(t)
	appID := seedApp(t, db)

	_, err := db.Exec(`INSERT INTO permission (app_id, code, resource, action, type, name)
		VALUES ($1, 'order:create', 'order', 'create', 'api', '创建订单')`, appID)
	require.NoError(t, err)

	var source string
	require.NoError(t, db.QueryRow(
		`SELECT source FROM permission WHERE app_id = $1 AND code = 'order:create'`,
		appID).Scan(&source))
	require.Equal(t, "manual", source)

	_, err = db.Exec(`INSERT INTO permission (app_id, code, resource, action, type, name)
		VALUES ($1, 'order:create', 'order', 'create', 'api', '重复')`, appID)
	require.Error(t, err)

	// app_id 外键：不存在的应用应被拒绝
	_, err = db.Exec(`INSERT INTO permission (app_id, code, resource, action, type, name)
		VALUES (999999, 'x:y', 'x', 'y', 'api', 'x')`)
	require.Error(t, err)
}

func TestRolePermission_Constraints(t *testing.T) {
	db := setupSchema(t)
	appID := seedApp(t, db)

	var roleID, permID int64
	require.NoError(t, db.QueryRow(
		`INSERT INTO role (app_id, code, name) VALUES ($1, 'manager', '经理') RETURNING id`,
		appID).Scan(&roleID))
	require.NoError(t, db.QueryRow(
		`INSERT INTO permission (app_id, code, resource, action, type, name)
		 VALUES ($1, 'order:create', 'order', 'create', 'api', '创建订单') RETURNING id`,
		appID).Scan(&permID))

	_, err := db.Exec(`INSERT INTO role_permission (app_id, role_id, permission_id)
		VALUES ($1, $2, $3)`, appID, roleID, permID)
	require.NoError(t, err)

	// eft 默认 allow
	var eft string
	require.NoError(t, db.QueryRow(
		`SELECT eft FROM role_permission WHERE role_id = $1 AND permission_id = $2`,
		roleID, permID).Scan(&eft))
	require.Equal(t, "allow", eft)

	// (app_id, role_id, permission_id) 唯一
	_, err = db.Exec(`INSERT INTO role_permission (app_id, role_id, permission_id)
		VALUES ($1, $2, $3)`, appID, roleID, permID)
	require.Error(t, err)

	// 外键：不存在的 permission_id 应被拒绝
	_, err = db.Exec(`INSERT INTO role_permission (app_id, role_id, permission_id)
		VALUES ($1, $2, 999999)`, appID, roleID)
	require.Error(t, err)
}

func TestRoleInheritance_EdgeUnique(t *testing.T) {
	db := setupSchema(t)
	appID := seedApp(t, db)

	var parentID, childID int64
	require.NoError(t, db.QueryRow(
		`INSERT INTO role (app_id, code, name) VALUES ($1, 'admin', '管理员') RETURNING id`,
		appID).Scan(&parentID))
	require.NoError(t, db.QueryRow(
		`INSERT INTO role (app_id, code, name) VALUES ($1, 'manager', '经理') RETURNING id`,
		appID).Scan(&childID))

	_, err := db.Exec(`INSERT INTO role_inheritance (app_id, parent_role_id, child_role_id)
		VALUES ($1, $2, $3)`, appID, parentID, childID)
	require.NoError(t, err)

	// 同一条继承边唯一（防重复边；环检测由控制面 detector.Check 负责，不在表层）
	_, err = db.Exec(`INSERT INTO role_inheritance (app_id, parent_role_id, child_role_id)
		VALUES ($1, $2, $3)`, appID, parentID, childID)
	require.Error(t, err)

	// 外键：不存在的角色应被拒绝
	_, err = db.Exec(`INSERT INTO role_inheritance (app_id, parent_role_id, child_role_id)
		VALUES ($1, $2, 999999)`, appID, parentID)
	require.Error(t, err)
}
