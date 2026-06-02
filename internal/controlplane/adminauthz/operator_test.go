package adminauthz_test

import (
	"context"
	"testing"

	"github.com/nickZFZ/Sydom/internal/controlplane/adminauthz"
	"github.com/nickZFZ/Sydom/internal/crypto"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func masterKey() []byte {
	k := make([]byte, crypto.KeySize)
	for i := range k {
		k[i] = 0x2a
	}
	return k
}

func TestOperatorResolver_ResolveSecret(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	r, err := adminauthz.NewOperatorResolver(db, masterKey())
	require.NoError(t, err)

	enc, err := crypto.Encrypt(masterKey(), []byte("alice-secret"))
	require.NoError(t, err)
	_, err = adminauthz.InsertOperator(ctx, db, "alice", enc)
	require.NoError(t, err)

	got, err := r.ResolveSecret(ctx, "alice")
	require.NoError(t, err)
	require.Equal(t, []byte("alice-secret"), got)

	// 未知 operator → fail-close（返回 error）
	_, err = r.ResolveSecret(ctx, "ghost")
	require.Error(t, err)
}

func TestOperatorResolver_DisabledOperatorFailClose(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()
	r, _ := adminauthz.NewOperatorResolver(db, masterKey())
	enc, _ := crypto.Encrypt(masterKey(), []byte("s"))
	_, err := adminauthz.InsertOperator(ctx, db, "alice", enc)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE admin_operator SET status=2 WHERE principal='alice'`)
	require.NoError(t, err)

	_, err = r.ResolveSecret(ctx, "alice")
	require.Error(t, err, "停用 operator 必须 fail-close")
}

func TestOperatorResolver_DecryptFailureFailClose(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()

	// 用正确主密钥加密并写入凭据。
	enc, err := crypto.Encrypt(masterKey(), []byte("alice-secret"))
	require.NoError(t, err)
	_, err = adminauthz.InsertOperator(ctx, db, "alice", enc)
	require.NoError(t, err)

	// 用不同的错误主密钥构造 resolver（同样 32 字节，全 0x55）。
	wrongKey := make([]byte, crypto.KeySize)
	for i := range wrongKey {
		wrongKey[i] = 0x55
	}
	r, err := adminauthz.NewOperatorResolver(db, wrongKey)
	require.NoError(t, err)

	_, err = r.ResolveSecret(ctx, "alice")
	require.Error(t, err, "解密失败必须 fail-close")
}

func TestEnsureRootOperator_Idempotent(t *testing.T) {
	db := dbtest.SetupSchema(t)
	ctx := context.Background()

	require.NoError(t, adminauthz.EnsureRootOperator(ctx, db, masterKey(), "root", []byte("root-secret")))
	require.NoError(t, adminauthz.EnsureRootOperator(ctx, db, masterKey(), "root", []byte("root-secret")))

	// 恰一个 root operator，且绑定 super-admin@*
	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM admin_operator WHERE principal='root'`).Scan(&n))
	require.Equal(t, 1, n)
	require.NoError(t, db.QueryRow(
		`SELECT count(*) FROM admin_subject_role sr
		 JOIN admin_operator o ON o.id=sr.operator_id
		 JOIN admin_role r ON r.id=sr.role_id
		 WHERE o.principal='root' AND r.code='super-admin' AND sr.domain='*'`).Scan(&n))
	require.Equal(t, 1, n)

	// 凭据可解密
	r, _ := adminauthz.NewOperatorResolver(db, masterKey())
	got, err := r.ResolveSecret(ctx, "root")
	require.NoError(t, err)
	require.Equal(t, []byte("root-secret"), got)
}
