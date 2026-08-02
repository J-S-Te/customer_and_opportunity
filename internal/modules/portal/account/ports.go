package account

import (
	"context"
	"time"
)

type Claims struct {
	Subject        string
	TenantID       string
	Roles          []string
	Permissions    []string
	RoleConfigHash string
	AuthzRevision  uint64
	ExpiresAt      time.Time
	AccessToken    string
}

// OIDCClient owns authorization URL creation, code exchange and cryptographic
// validation of issuer, audience, signature, nonce and token_use.
type OIDCClient interface {
	AuthorizationURL(state, nonce, codeChallenge, returnPath string) (string, error)
	ExchangeAndValidate(context.Context, string, string, string) (Claims, error)
	UserInfo(context.Context, string) (Claims, error)
}

type VerifiedInvite struct {
	TenantID               string
	ExpectedPlatformUserID string
	CustomerID             uint64
	ContactID              *uint64
}

type InviteClient interface {
	Verify(context.Context, string) (VerifiedInvite, error)
	Consume(context.Context, string, string) error
}

type SecretProtector interface {
	Encrypt(context.Context, []byte) ([]byte, error)
	Decrypt(context.Context, []byte) ([]byte, error)
}

type Repository interface {
	WithTransaction(context.Context, func(context.Context) error) error
	UpsertPendingLink(context.Context, *IdentityLink) (*IdentityLink, error)
	FindLink(context.Context, string, string) (*IdentityLink, error)
	ActivateLink(context.Context, string, uint64, uint64, string, time.Time) error
	RevertActivation(context.Context, string, uint64, uint64, string, time.Time) error
	CreateActivation(context.Context, *ActivationContext) error
	ConsumeActivation(context.Context, string, time.Time) (*ActivationContext, error)
	CreateSession(context.Context, *Session) error
	FindSession(context.Context, string, string, time.Time) (*Session, error)
	ListSessions(context.Context, string, string, time.Time) ([]Session, error)
	FindOwnedSession(context.Context, string, string, string, time.Time) (*Session, error)
	RevokeSession(context.Context, string, string, string, time.Time) error
	RevokeSessionsForSubject(context.Context, string, string, time.Time) error
	TouchSession(context.Context, string, string, time.Time, time.Time) error
	MarkLinkVerified(context.Context, string, uint64, uint64, time.Time) error
	WriteAuthEvent(context.Context, *AuthEvent) error
	CreateSecurityEvent(context.Context, *SecurityEvent) error
	ListSecurityEvents(context.Context, string, string, int) ([]SecurityEvent, error)
	AcknowledgeSecurityEvent(context.Context, string, string, string, time.Time) error
}

type AccessDisableRepository interface {
	DisableLink(context.Context, DisableCommand, time.Time) (DisableResult, error)
}

type Clock interface{ Now() time.Time }
type RandomSource interface{ Bytes(int) ([]byte, error) }
