package bootstrap

import (
	"context"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/security"
)

// portalInviteOperationProtector adapts the CRM authenticated-encryption
// codec to the invitation saga without exposing plaintext snapshots outside
// the portalinvite service boundary.
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
