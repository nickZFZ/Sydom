package store_test

import (
	"database/sql"
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest" // blank-imports lib/pq，注册 postgres 驱动
	"github.com/stretchr/testify/require"
)

// role/data_policy 新增 source 列，既有插入默认 'manual'（向后兼容）。
func TestEntitySource_DefaultsManual(t *testing.T) {
	dsn := dbtest.MigratedDSN(t)
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	_, app := dbtest.SeedAppInTenant(t, db, "src", "src", "src-key")
	var roleSrc string
	require.NoError(t, db.QueryRow(
		`INSERT INTO role (app_id, code, name) VALUES ($1,'r','R') RETURNING source`, app).Scan(&roleSrc))
	require.Equal(t, "manual", roleSrc)

	var dpSrc string
	require.NoError(t, db.QueryRow(
		`INSERT INTO data_policy (app_id, subject_type, subject_id, resource, condition, effect, version)
		 VALUES ($1,'role','r','order','{}'::jsonb,'allow',1) RETURNING source`, app).Scan(&dpSrc))
	require.Equal(t, "manual", dpSrc)
}
