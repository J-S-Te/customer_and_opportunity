package auth

import "context"

type ScopeMode string

const (
	ScopeSelf ScopeMode = "SELF"
	ScopeOrg  ScopeMode = "ORG"
	ScopeAll  ScopeMode = "ALL"
)

// Principal contains verified identity claims. Production implementations must populate it
// only after validating the platform OIDC token and the local server-side session.
type Principal struct {
	UserID          string
	PersonID        string
	TenantID        string
	DisplayName     string
	PrimaryOrgID    string
	Roles           []string
	Permissions     map[string]struct{}
	ScopeMode       ScopeMode
	OrganizationIDs []string
	RoleConfigHash  string
	AuthzRevision   uint64
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

// Authenticator is the boundary to the production OIDC/session implementation.
type Authenticator interface {
	Authenticate(context.Context, string) (Principal, error)
}
