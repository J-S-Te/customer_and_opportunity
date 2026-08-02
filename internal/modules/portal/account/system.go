package account

import (
	"crypto/rand"
	"time"
)

type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now().UTC() }

type CryptoRandom struct{}

func (CryptoRandom) Bytes(size int) ([]byte, error) {
	value := make([]byte, size)
	_, err := rand.Read(value)
	return value, err
}
