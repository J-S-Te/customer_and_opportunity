package report

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"io"
)

type AESDescriptorProtector struct{ aead cipher.AEAD }

func NewAESDescriptorProtector(key []byte) (*AESDescriptorProtector, error) {
	if len(key) != 32 {
		return nil, errors.New("report ingest descriptor key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &AESDescriptorProtector{aead: aead}, nil
}

func (p *AESDescriptorProtector) Encrypt(_ context.Context, plaintext []byte) ([]byte, error) {
	if len(plaintext) == 0 {
		return nil, errors.New("report ingest descriptor is empty")
	}
	nonce := make([]byte, p.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return p.aead.Seal(nonce, nonce, plaintext, nil), nil
}

func (p *AESDescriptorProtector) Decrypt(_ context.Context, ciphertext []byte) ([]byte, error) {
	if len(ciphertext) <= p.aead.NonceSize() {
		return nil, errors.New("encrypted report ingest descriptor is malformed")
	}
	return p.aead.Open(nil, ciphertext[:p.aead.NonceSize()], ciphertext[p.aead.NonceSize():], nil)
}
