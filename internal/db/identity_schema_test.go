package db

import (
	"database/sql"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

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

	_, err := db.Exec(`INSERT INTO application (tenant_id, domain, name, app_key, app_secret_enc)
		VALUES ($1, 'order-system', '订单系统', 'AK_order', '\xab'::bytea)`, tenantID)
	require.NoError(t, err)

	var ver int64
	require.NoError(t, db.QueryRow(
		`SELECT current_version FROM application WHERE app_key = 'AK_order'`).Scan(&ver))
	require.Equal(t, int64(0), ver)

	_, err = db.Exec(`INSERT INTO application (tenant_id, domain, name, app_key, app_secret_enc)
		VALUES ($1, 'other', '其他', 'AK_order', '\xab'::bytea)`, tenantID)
	require.Error(t, err)

	_, err = db.Exec(`INSERT INTO application (tenant_id, domain, name, app_key, app_secret_enc)
		VALUES ($1, 'order-system', '重复域', 'AK_dup', '\xab'::bytea)`, tenantID)
	require.Error(t, err)

	_, err = db.Exec(`INSERT INTO application (tenant_id, domain, name, app_key, app_secret_enc)
		VALUES (999999, 'x', 'x', 'AK_x', '\xab'::bytea)`)
	require.Error(t, err)
}

func TestApplication_VersionBumpSerialized(t *testing.T) {
	db := setupSchema(t)
	appID := seedApp(t, db)

	const goroutines = 10
	const bumpsEach = 20

	var wg sync.WaitGroup
	errCh := make(chan error, goroutines)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < bumpsEach; i++ {
				if err := bumpVersion(db, appID); err != nil {
					errCh <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}

	var final int64
	require.NoError(t, db.QueryRow(
		`SELECT current_version FROM application WHERE id = $1`, appID).Scan(&final))
	// 无丢失更新：最终版本号 == 总递增次数
	require.Equal(t, int64(goroutines*bumpsEach), final)
}

func TestApplication_SecretColumnIsEnc(t *testing.T) {
	db := setupSchema(t)

	// 新列 app_secret_enc 必须是 bytea 且 NOT NULL（限定 public schema，避免同名表干扰）
	var dataType, isNullable string
	require.NoError(t, db.QueryRow(`
		SELECT data_type, is_nullable FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'application'
		  AND column_name = 'app_secret_enc'`).Scan(&dataType, &isNullable))
	require.Equal(t, "bytea", dataType)
	require.Equal(t, "NO", isNullable)

	// 旧列 app_secret_hash 必须已被移除
	var n int
	require.NoError(t, db.QueryRow(`
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'application'
		  AND column_name = 'app_secret_hash'`).Scan(&n))
	require.Equal(t, 0, n)
}

// bumpVersion 自行开启事务，复现规格 §6 步骤 1-2、5：行锁读取 current_version 后递增写回。
func bumpVersion(db *sql.DB, appID int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var cur int64
	if err := tx.QueryRow(
		`SELECT current_version FROM application WHERE id = $1 FOR UPDATE`,
		appID).Scan(&cur); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`UPDATE application SET current_version = $1 WHERE id = $2`, cur+1, appID); err != nil {
		return err
	}
	return tx.Commit()
}
