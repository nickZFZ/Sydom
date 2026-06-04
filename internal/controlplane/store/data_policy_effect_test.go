package store_test

import (
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// TestDataPolicyEffectColumn 验证 000015 迁移：effect 列默认 allow、CHECK 拒非法值、接受 deny。
func TestDataPolicyEffectColumn(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)

	var eff string
	// 不指定 effect → 默认 allow
	require.NoError(t, db.QueryRow(`
		INSERT INTO data_policy (app_id, subject_type, subject_id, resource, condition, version)
		VALUES ($1,'role','m','order','{}'::jsonb,1) RETURNING effect`, appID).Scan(&eff))
	require.Equal(t, "allow", eff)

	// 显式 deny 接受
	require.NoError(t, db.QueryRow(`
		INSERT INTO data_policy (app_id, subject_type, subject_id, resource, condition, effect, version)
		VALUES ($1,'user','a','order','{}'::jsonb,'deny',1) RETURNING effect`, appID).Scan(&eff))
	require.Equal(t, "deny", eff)

	// 非法值被 CHECK 拒
	_, err := db.Exec(`
		INSERT INTO data_policy (app_id, subject_type, subject_id, resource, condition, effect, version)
		VALUES ($1,'role','m','order','{}'::jsonb,'bogus',1)`, appID)
	require.Error(t, err)
}
