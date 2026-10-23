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
	// DataScopes 由基础平台授权上下文提供，
	// 仅由已认证的服务端会话携带。当切片为空时，现有 CRM
	// 调用方不受影响。
	DataScopes      []DataScope
	ScopeMode       ScopeMode
	OrganizationIDs []string
	RoleConfigHash  string
	AuthzRevision   uint64
	// LoginIP 是可信服务端会话或平台授权上下文
	// 提供的规范用户登录地址。该字段可选：
	// 认证器不得从任意请求头填入该值。
	LoginIP string
}

// DataScope 为子系统业务代码保留授权上下文的传输契约，
// 无需将其耦合到 OIDC 适配器。
type DataScope struct {
	RoleCode        string `json:"role_code"`
	ScopeType       string `json:"scope_type"`
	ScopeID         string `json:"scope_id"`
	EnvironmentCode string `json:"environment_code"`
}

func (p Principal) HasPermission(permission string) bool {
	_, ok := p.Permissions[permission]
	return ok
}

// HasPermissionOrRole 是范围严格受限的路由兼容检查：路由以
// 签名角色为业务授权依据，而旧会话可能尚未
// 包含对应目录权限。调用方必须提供
// 明确的允许列表；此机制有意不作为通用的角色到权限
// 扩展机制。
func HasPermissionOrRole(p Principal, permission string, roles ...string) bool {
	if p.HasPermission(permission) {
		return true
	}
	for _, current := range p.Roles {
		for _, allowed := range roles {
			if current == allowed {
				return true
			}
		}
	}
	return false
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
