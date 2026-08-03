package account

import (
	"crypto/rand"
	"time"
)

type SystemClock struct{}

// 领域时间统一为 UTC，避免数据库比较和令牌有效期判断受部署时区影响。
func (SystemClock) Now() time.Time { return time.Now().UTC() }

type CryptoRandom struct{}

func (CryptoRandom) Bytes(size int) ([]byte, error) {
	// 登录 state、nonce 和会话令牌必须来自系统密码学随机源，不能使用可预测伪随机数。
	value := make([]byte, size)
	_, err := rand.Read(value)
	return value, err
}
