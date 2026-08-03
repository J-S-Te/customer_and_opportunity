package bootstrap

import (
	"context"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/security"
)

// 将 CRM 认证加密能力适配到邀请 Saga；补偿快照离开 portalinvite 服务边界前已加密，不在任务表
// 或日志中暴露明文凭据和身份材料。
type portalInviteOperationProtector struct {
	codec *security.SensitiveCodec
}

func (p portalInviteOperationProtector) Encrypt(_ context.Context, plaintext []byte) ([]byte, error) {
	return p.codec.Encrypt(string(plaintext))
}

func (p portalInviteOperationProtector) Decrypt(_ context.Context, ciphertext []byte) ([]byte, error) {
	plaintext, err := p.codec.Decrypt(ciphertext)
	return []byte(plaintext), err
}
