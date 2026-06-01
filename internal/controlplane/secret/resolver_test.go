package secret_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/nickZFZ/Sydom/internal/controlplane/secret"
	"github.com/nickZFZ/Sydom/internal/crypto"
	"github.com/nickZFZ/Sydom/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func masterKey() []byte { return bytes.Repeat([]byte{0x2a}, crypto.KeySize) }

func TestResolveSecret_RoundTrip(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)

	r, err := secret.NewResolver(db, masterKey())
	require.NoError(t, err)

	plain := []byte("the-real-app-secret")
	enc, err := r.EncryptSecret(plain)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE application SET app_secret_enc=$1 WHERE id=$2`, enc, appID)
	require.NoError(t, err)

	got, err := r.ResolveSecret(context.Background(), dbtest.SeedAppKey)
	require.NoError(t, err)
	require.Equal(t, plain, got)
}

func TestResolveSecret_UnknownApp(t *testing.T) {
	db := dbtest.SetupSchema(t)
	r, err := secret.NewResolver(db, masterKey())
	require.NoError(t, err)
	_, err = r.ResolveSecret(context.Background(), "AK_nonexistent")
	require.Error(t, err)
}

// fail-close：app 存在但 app_secret_enc 损坏 → 解密失败必须返回错误（不返回部分明文）。
func TestResolveSecret_CorruptedCiphertext(t *testing.T) {
	db := dbtest.SetupSchema(t)
	appID := dbtest.SeedApp(t, db)

	r, err := secret.NewResolver(db, masterKey())
	require.NoError(t, err)

	// 写入明显损坏的密文（非合法 AES-GCM 输出）
	_, err = db.Exec(`UPDATE application SET app_secret_enc=$1 WHERE id=$2`, []byte{0x00, 0x01, 0x02}, appID)
	require.NoError(t, err)

	got, err := r.ResolveSecret(context.Background(), dbtest.SeedAppKey)
	require.Error(t, err)
	require.Nil(t, got)
}

func TestNewResolver_BadKeyFailsClose(t *testing.T) {
	db := dbtest.SetupSchema(t)
	_, err := secret.NewResolver(db, []byte("short"))
	require.Error(t, err) // 主密钥非 32 字节 → 构造即失败
}
