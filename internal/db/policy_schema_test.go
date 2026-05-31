package db

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDataPolicy_JSONBCondition(t *testing.T) {
	db := setupSchema(t)
	appID := seedApp(t, db)

	cond := `{"op":"AND","children":[{"field":"department","op":"EQ","value":"$user.department"}]}`
	_, err := db.Exec(`INSERT INTO data_policy (app_id, subject_type, subject_id, resource, condition, version)
		VALUES ($1, 'role', 'manager', 'order', $2::jsonb, 1)`, appID, cond)
	require.NoError(t, err)

	// jsonb 路径查询可用，证明确为 jsonb 而非纯文本
	var op string
	require.NoError(t, db.QueryRow(
		`SELECT condition->>'op' FROM data_policy WHERE app_id = $1 AND subject_id = 'manager'`,
		appID).Scan(&op))
	require.Equal(t, "AND", op)

	// app_id 外键：不存在的应用应被拒绝
	_, err = db.Exec(`INSERT INTO data_policy (app_id, subject_type, subject_id, resource, condition, version)
		VALUES (999999, 'role', 'x', 'y', '{}'::jsonb, 1)`)
	require.Error(t, err)
}

func TestCasbinRule_DefaultsAndUnique(t *testing.T) {
	db := setupSchema(t)

	// casbin_rule 是派生表、无外键，app_id 取任意值即可（此处用 1），无需预先存在的 application 行
	_, err := db.Exec(`INSERT INTO casbin_rule (app_id, ptype, v0, v1, v2, version)
		VALUES (1, 'g', 'alice', 'manager', 'order-system', 1)`)
	require.NoError(t, err)

	var v3, v4, v5 string
	require.NoError(t, db.QueryRow(
		`SELECT v3, v4, v5 FROM casbin_rule WHERE app_id = 1 AND v0 = 'alice'`).Scan(&v3, &v4, &v5))
	require.Equal(t, "", v3)
	require.Equal(t, "", v4)
	require.Equal(t, "", v5)

	// 完整 v 元组去重：同 (app_id, ptype, v0..v5) 再插入应失败
	_, err = db.Exec(`INSERT INTO casbin_rule (app_id, ptype, v0, v1, v2, version)
		VALUES (1, 'g', 'alice', 'manager', 'order-system', 2)`)
	require.Error(t, err)
}

func TestPolicyAuditLog_Insert(t *testing.T) {
	db := setupSchema(t)
	appID := seedApp(t, db)

	diff := `{"before":null,"after":{"code":"manager"}}`
	_, err := db.Exec(`INSERT INTO policy_audit_log
		(app_id, operator, action, entity_type, entity_id, diff, version)
		VALUES ($1, 'admin@acme', 'create', 'role', '1', $2::jsonb, 1)`, appID, diff)
	require.NoError(t, err)

	// entity_id 允许为 NULL（某些变更无单一实体）
	_, err = db.Exec(`INSERT INTO policy_audit_log
		(app_id, operator, action, entity_type, version)
		VALUES ($1, 'admin@acme', 'update', 'role', 2)`, appID)
	require.NoError(t, err)

	// diff 也允许为 NULL（上一条未带 diff）
	var diffVal sql.NullString
	require.NoError(t, db.QueryRow(
		`SELECT diff FROM policy_audit_log WHERE app_id = $1 AND version = 2`,
		appID).Scan(&diffVal))
	require.False(t, diffVal.Valid)

	var cnt int
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM policy_audit_log WHERE app_id = $1`, appID).Scan(&cnt))
	require.Equal(t, 2, cnt)
}
