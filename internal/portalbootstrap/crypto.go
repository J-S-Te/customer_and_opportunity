package portalbootstrap

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"io"
)

// AEADCodec encrypts short-lived OIDC secrets and sensitive report fields with
// a fresh AES-256-GCM nonce. Production deployments should rotate this key via
// a managed secret and retain old key versions until activation rows expire.
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
