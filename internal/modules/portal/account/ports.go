package account

import (
	"context"
	"time"

	sharedauth "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
)

type Claims struct {
	Subject        string
	PersonID       string
	TenantID       string
	Roles          []string
	Permissions    []string
	DataScopes     []DataScope
	RoleConfigHash string
	AuthzRevision  uint64
	ExpiresAt      time.Time
	AccessToken    string
}

// DataScope is the application-scoped data boundary resolved online by the
// basic platform. It is intentionally kept outside the OIDC token and is
// persisted only with the Portal's server-side session snapshot.
//
// The Portal remains customer-bound through IdentityLink.CustomerID; these
// scopes are forwarded to the request principal so future Portal capabilities
// can make explicit, role-aware data decisions without minting a second
// authorization source.
type DataScope = sharedauth.DataScope

// OIDCClient 负责授权地址、授权码兑换，以及发行方、受众、签名、nonce 与 token_use 的密码学校验。
// 业务服务只接收已验证声明，不能把浏览器回传字段直接提升为登录主体。
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

// SecretProtector 隔离持久化层与明文访问令牌、邀请令牌和 PKCE 材料，降低数据库泄露后的重放风险。
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

// AccessDisableRepository 单独表达需要强事务保证的下线协议；旧仓储不支持时必须显式失败。
type AccessDisableRepository interface {
	DisableLink(context.Context, DisableCommand, time.Time) (DisableResult, error)
}

type Clock interface{ Now() time.Time }
type RandomSource interface{ Bytes(int) ([]byte, error) }
