package db

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMigration000025_OperatorEmail(t *testing.T) {
	dsn := startPostgres(t)
	require.NoError(t, RunMigrations(dsn, migrationsSource))
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	// 存量 operator 无 email（NULL）不受影响。
	var o1 int64
	require.NoError(t, db.QueryRow(
		`INSERT INTO admin_operator (principal, secret_enc) VALUES ('op1','\xab'::bytea) RETURNING id`).Scan(&o1))
	require.NoError(t, db.QueryRow(
		`INSERT INTO admin_operator (principal, secret_enc, email) VALUES ('op2','\xab'::bytea,'a@acme.com') RETURNING id`).Scan(new(int64)))

	// 全局唯一：第二 operator 抢同 email→冲突。
	_, err = db.Exec(`UPDATE admin_operator SET email='a@acme.com' WHERE id=$1`, o1)
	require.Error(t, err, "admin_operator.email 全局唯一应拒重复")

	// 多个 NULL 共存（Postgres UNIQUE 允许多 NULL）。
	_, err = db.Exec(`INSERT INTO admin_operator (principal, secret_enc) VALUES ('op3','\xab'::bytea)`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO admin_operator (principal, secret_enc) VALUES ('op4','\xab'::bytea)`)
	require.NoError(t, err, "多个 NULL email 应共存")

	// down：列被删。
	require.NoError(t, MigrateDown(dsn, migrationsSource))
	_, err = db.Exec(`SELECT email FROM admin_operator LIMIT 1`)
	require.Error(t, err, "down 后 email 列应不存在")
}
