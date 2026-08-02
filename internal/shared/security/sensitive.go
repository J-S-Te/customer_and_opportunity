package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
)

type SensitiveCodec struct {
	aead    cipher.AEAD
	hmacKey []byte
}

func NewSensitiveCodec(encryptionKey, hmacKey []byte) (*SensitiveCodec, error) {
	if len(encryptionKey) != 32 || len(hmacKey) < 32 {
		return nil, errors.New("sensitive field encryption key must be 32 bytes and HMAC key at least 32 bytes")
	}
	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &SensitiveCodec{aead: aead, hmacKey: append([]byte(nil), hmacKey...)}, nil
}

func (c *SensitiveCodec) Encrypt(plaintext string) ([]byte, error) {
	if plaintext == "" {
		return nil, nil
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return c.aead.Seal(nonce, nonce, []byte(plaintext), nil), nil
}

// Decrypt reverses Encrypt for trusted service-layer adapters. Callers must not
// include the returned plaintext in API DTOs, errors, logs, metrics or audit
// payloads.
func (c *SensitiveCodec) Decrypt(ciphertext []byte) (string, error) {
	if len(ciphertext) == 0 {
		return "", nil
	}
	nonceSize := c.aead.NonceSize()
	if len(ciphertext) < nonceSize {
		return "", errors.New("sensitive field ciphertext is invalid")
	}
	plaintext, err := c.aead.Open(nil, ciphertext[:nonceSize], ciphertext[nonceSize:], nil)
	if err != nil {
		return "", errors.New("sensitive field ciphertext authentication failed")
	}
	return string(plaintext), nil
}

func (c *SensitiveCodec) HMAC(normalized string) string {
	if normalized == "" {
		return ""
	}
	mac := hmac.New(sha256.New, c.hmacKey)
	_, _ = mac.Write([]byte(strings.ToUpper(strings.TrimSpace(normalized))))
	return hex.EncodeToString(mac.Sum(nil))
}

func MaskPhone(phone string) string {
	runes := []rune(strings.TrimSpace(phone))
	if len(runes) <= 7 {
		return "****"
	}
	return string(runes[:3]) + "****" + string(runes[len(runes)-4:])
}
