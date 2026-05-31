package db

import (
	"testing"

	"github.com/stretchr/testify/require"
)

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

	// 外键：不存在的 app_id 应被拒绝
	_, err = db.Exec(`INSERT INTO role_permission (app_id, role_id, permission_id)
		VALUES (999999, $1, $2)`, roleID, permID)
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

	// 外键：不存在的 app_id 应被拒绝
	_, err = db.Exec(`INSERT INTO role_inheritance (app_id, parent_role_id, child_role_id)
		VALUES (999999, $1, $2)`, parentID, childID)
	require.Error(t, err)
}

func TestUserRoleBinding_Unique(t *testing.T) {
	db := setupSchema(t)
	appID := seedApp(t, db)

	var roleID int64
	require.NoError(t, db.QueryRow(
		`INSERT INTO role (app_id, code, name) VALUES ($1, 'manager', '经理') RETURNING id`,
		appID).Scan(&roleID))

	_, err := db.Exec(`INSERT INTO user_role_binding (app_id, user_id, role_id)
		VALUES ($1, 'alice', $2)`, appID, roleID)
	require.NoError(t, err)

	// (app_id, user_id, role_id) 唯一
	_, err = db.Exec(`INSERT INTO user_role_binding (app_id, user_id, role_id)
		VALUES ($1, 'alice', $2)`, appID, roleID)
	require.Error(t, err)

	// 外键：role_id 必须存在
	_, err = db.Exec(`INSERT INTO user_role_binding (app_id, user_id, role_id)
		VALUES ($1, 'bob', 999999)`, appID)
	require.Error(t, err)

	// 外键：不存在的 app_id 应被拒绝
	_, err = db.Exec(`INSERT INTO user_role_binding (app_id, user_id, role_id)
		VALUES (999999, 'carol', $1)`, roleID)
	require.Error(t, err)
}
