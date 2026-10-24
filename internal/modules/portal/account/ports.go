package account

import (
	"context"
	"time"

	sharedauth "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
)

type Claims struct {
	Subject        string
	OIDCSessionID  string
	IdentityID     string
	PersonID       string
	TenantID       string
	Roles          []string
	Permissions    []string
	DataScopes     []DataScope
	RoleConfigHash string
	AuthzRevision  uint64
	ExpiresAt      time.Time
	AccessToken    string
	LoginIP        string
	// CustomerRef 是平台 authorization-context 下发的客户绑定声明（Phase 4 开关
	// PORTAL_USE_PLATFORM_BINDING 打开后生效）；为空表示平台未下发或尚未开启。
	CustomerRef string
}

// DataScope 是基础平台在线解析的应用级数据边界，
// 有意放在 OIDC 令牌之外，
// 仅随 Portal 服务端会话快照持久化。
//
// Portal 始终通过 IdentityLink.CustomerID 绑定客户；
// 这些范围转交请求主体，使未来 Portal 功能
// 能按明确角色作出数据决策，而不创建第二个
// 授权来源。
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

// IdentityReconciliationRepository 仅公开 CRM 对账工作器
// 所需的最小读取模型，有意独立于
// 登录 Repository，避免测试替身及认证路径意外获得
// 跨客户列举能力。
type IdentityReconciliationRepository interface {
	FindLinksBySubjects(context.Context, string, []string) ([]IdentityLink, error)
}

type Clock interface{ Now() time.Time }
type RandomSource interface{ Bytes(int) ([]byte, error) }
