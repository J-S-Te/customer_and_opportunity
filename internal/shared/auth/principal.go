package auth

import "context"

type ScopeMode string

const (
	ScopeSelf ScopeMode = "SELF"
	ScopeOrg  ScopeMode = "ORG"
	ScopeAll  ScopeMode = "ALL"
)

// Principal 是认证边界输出而非请求 DTO。正式环境只有在平台令牌验签、服务端会话复核及当前
// 授权重验后才能写入，业务层不得从客户端字段重建该对象。
type Principal struct {
	UserID       string
	Username     string
	PersonID     string
	TenantID     string
	DisplayName  string
	PrimaryOrgID string
	Roles        []string
	Permissions  map[string]struct{}
	// DataScopes are supplied by the basic platform authorization-context and
	// carried only by the authenticated server-side session. Existing CRM
	// callers remain unaffected when this slice is empty.
	DataScopes      []DataScope
	ScopeMode       ScopeMode
	OrganizationIDs []string
	RoleConfigHash  string
	AuthzRevision   uint64
}

// DataScope keeps the authorization-context wire contract available to
// subsystem business code without coupling it to an OIDC adapter.
type DataScope struct {
	RoleCode        string
	ScopeType       string
	ScopeID         string
	EnvironmentCode string
}

func (p Principal) HasPermission(permission string) bool {
	_, ok := p.Permissions[permission]
	return ok
}

type contextKey string

const principalKey contextKey = "principal"

func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalKey, principal)
}

func FromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalKey).(Principal)
	return principal, ok
}

// Authenticator 隔离具体 OIDC 与会话实现，中间件只消费最终主体，不缓存令牌中的旧权限快照。
type Authenticator interface {
	Authenticate(context.Context, string) (Principal, error)
}
