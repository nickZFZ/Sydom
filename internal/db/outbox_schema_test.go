package db_test

import (
	"testing"

	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestOutboxSchema_TableExists(t *testing.T) {
	conn := dbtest.SetupSchema(t)
	var exists bool
	require.NoError(t, conn.QueryRow(
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name='policy_outbox')`).Scan(&exists))
	require.True(t, exists)

	// 可插入一行并默认 published_at 为 NULL
	_, err := conn.Exec(
		`INSERT INTO policy_outbox (app_id, version, delta_proto) VALUES (1, 1, '\x00'::bytea)`)
	require.NoError(t, err)
	var pubNull bool
	require.NoError(t, conn.QueryRow(
		`SELECT published_at IS NULL FROM policy_outbox LIMIT 1`).Scan(&pubNull))
	require.True(t, pubNull)
}
