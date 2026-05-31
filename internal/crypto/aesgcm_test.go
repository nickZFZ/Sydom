package crypto

import (
	"bytes"
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/require"
)

func mustKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, KeySize)
	_, err := rand.Read(k)
	require.NoError(t, err)
	return k
}

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	key := mustKey(t)
	plain := []byte("super-secret-app-secret")

	blob, err := Encrypt(key, plain)
	require.NoError(t, err)
	require.NotEqual(t, plain, blob) // 密文不等于明文

	got, err := Decrypt(key, blob)
	require.NoError(t, err)
	require.Equal(t, plain, got)
}

func TestEncrypt_NonceIsRandom(t *testing.T) {
	key := mustKey(t)
	plain := []byte("same-input")

	a, err := Encrypt(key, plain)
	require.NoError(t, err)
	b, err := Encrypt(key, plain)
	require.NoError(t, err)
	require.False(t, bytes.Equal(a, b)) // 相同明文两次加密因随机 nonce 而不同
}

func TestDecrypt_WrongKeyFails(t *testing.T) {
	key1 := bytes.Repeat([]byte{0x01}, KeySize)
	key2 := bytes.Repeat([]byte{0x02}, KeySize) // 确定性互异密钥，避免随机相等的理论 flaky
	blob, err := Encrypt(key1, []byte("x"))
	require.NoError(t, err)

	_, err = Decrypt(key2, blob)
	require.Error(t, err)
}

func TestDecrypt_TamperedFails(t *testing.T) {
	key := mustKey(t)
	blob, err := Encrypt(key, []byte("payload"))
	require.NoError(t, err)

	blob[len(blob)-1] ^= 0xff // 翻转最后一字节（GCM 认证标签）
	_, err = Decrypt(key, blob)
	require.Error(t, err)
}

func TestBadKeySize(t *testing.T) {
	_, err := Encrypt([]byte("short"), []byte("x"))
	require.ErrorIs(t, err, ErrKeySize)
	_, err = Decrypt([]byte("short"), []byte("x"))
	require.ErrorIs(t, err, ErrKeySize)
}

func TestDecrypt_TooShort(t *testing.T) {
	_, err := Decrypt(mustKey(t), []byte{0x01})
	require.ErrorIs(t, err, ErrCiphertext)
}

// TestDecrypt_NonceLenButNoTag 覆盖边界：长度等于 nonce、但缺少认证标签的 blob
// 应被长度前置检查拒绝（返回 ErrCiphertext），而非落到 GCM 认证失败。
func TestDecrypt_NonceLenButNoTag(t *testing.T) {
	// AES-GCM 标准 nonce 为 12 字节；构造恰好 12 字节、无 tag 的 blob。
	_, err := Decrypt(mustKey(t), make([]byte, 12))
	require.ErrorIs(t, err, ErrCiphertext)
}
