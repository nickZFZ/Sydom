// Package crypto 提供司域控制面对敏感字段（如 AppSecret）的对称加解密。
// 主密钥由进程外部（环境变量 / KMS）注入，绝不入库。
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"io"
)

// KeySize 是 AES-256 要求的主密钥字节数。
const KeySize = 32

var (
	// ErrKeySize 表示主密钥长度不是 32 字节。
	ErrKeySize = errors.New("crypto: master key must be 32 bytes")
	// ErrCiphertext 表示密文长度不足以容纳 nonce。
	ErrCiphertext = errors.New("crypto: ciphertext too short")
)

// Encrypt 用 AES-256-GCM 加密 plaintext，返回 nonce||ciphertext||tag。
func Encrypt(key, plaintext []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	// Seal 把密文追加到 dst(=nonce) 之后，得到 nonce||ciphertext||tag。
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// Decrypt 解密 Encrypt 产出的 nonce||ciphertext||tag。
func Decrypt(key, blob []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	ns := gcm.NonceSize()
	// 合法密文至少含 nonce + GCM 认证标签；短于此必不可能解密成功。
	if len(blob) < ns+gcm.Overhead() {
		return nil, ErrCiphertext
	}
	nonce, ct := blob[:ns], blob[ns:]
	return gcm.Open(nil, nonce, ct, nil)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != KeySize {
		return nil, ErrKeySize
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
