package portalbootstrap

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"io"
)

// 使用每次随机 nonce 的 AES-256-GCM 加密短期 OIDC 秘密和报告敏感字段。生产轮换密钥时需保留
// 旧版本，直到仍引用旧密文的登录事务或业务记录完成迁移/过期。
type AEADCodec struct{ aead cipher.AEAD }

func NewAEADCodec(key []byte) (*AEADCodec, error) {
	if len(key) != 32 {
		return nil, errors.New("AES-256-GCM key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &AEADCodec{aead: aead}, nil
}
func (c *AEADCodec) Encrypt(value []byte) ([]byte, error) {
	if len(value) == 0 {
		return nil, nil
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return c.aead.Seal(nonce, nonce, value, nil), nil
}
func (c *AEADCodec) Decrypt(value []byte) ([]byte, error) {
	if len(value) < c.aead.NonceSize() {
		return nil, errors.New("encrypted value is malformed")
	}
	return c.aead.Open(nil, value[:c.aead.NonceSize()], value[c.aead.NonceSize():], nil)
}
