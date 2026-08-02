package middleware

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/response"
)

// SessionAuth trusts only the opaque subsystem cookie and the server-side CRM
// session store. Browser-supplied identity or permission headers are ignored.
func SessionAuth(authenticator auth.Authenticator, cookieName string) gin.HandlerFunc {
	return func(c *gin.Context) {
		cookie, err := c.Request.Cookie(cookieName)
		if err != nil || cookie.Value == "" {
			response.Error(c, apperror.ErrUnauthenticated)
			c.Abort()
			return
		}
		principal, err := authenticator.Authenticate(c.Request.Context(), cookie.Value)
		if err != nil {
			response.Error(c, apperror.ErrUnauthenticated)
			c.Abort()
			return
		}
		c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), principal))
		c.Next()
	}
}

// RequireSameOriginWrite protects cookie-authenticated CRM writes. Safe methods
// remain readable without a CSRF header; machine routes use a separate group.
func RequireSameOriginWrite(publicOrigin string) gin.HandlerFunc {
	return func(c *gin.Context) {
		switch c.Request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			c.Next()
			return
		}
		origin, err := url.Parse(c.GetHeader("Origin"))
		expected, expectedErr := url.Parse(publicOrigin)
		if err != nil || expectedErr != nil || origin.Scheme != expected.Scheme || origin.Host != expected.Host || c.GetHeader("X-CSRF-Token") != "1" {
			response.Error(c, apperror.ErrForbidden)
			c.Abort()
			return
		}
		c.Next()
	}
}

type machineAuthenticator interface {
	Authenticate(context.Context, *http.Request) (auth.Principal, error)
}

// MachineAuth authenticates internal integrations independently from browser sessions.
func MachineAuth(authenticator machineAuthenticator) gin.HandlerFunc {
	return func(c *gin.Context) {
		principal, err := authenticator.Authenticate(c.Request.Context(), c.Request)
		if err != nil {
			response.Error(c, apperror.ErrUnauthenticated)
			c.Abort()
			return
		}
		c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), principal))
		c.Next()
	}
}

// DevelopmentAuth is deliberately header-based and must never be enabled outside local development.
// It preserves the same Principal boundary that a production OIDC server-side session will populate.
func DevelopmentAuth(enabled bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !enabled {
			response.Error(c, apperror.New(http.StatusServiceUnavailable, "COMMON_AUTH_NOT_CONFIGURED", "production authentication is not configured"))
			c.Abort()
			return
		}
		userID := strings.TrimSpace(c.GetHeader("X-Dev-User-ID"))
		tenantID := strings.TrimSpace(c.GetHeader("X-Dev-Tenant-ID"))
		if userID == "" || tenantID == "" {
			response.Error(c, apperror.ErrUnauthenticated)
			c.Abort()
			return
		}
		permissions := make(map[string]struct{})
		for _, permission := range strings.Split(c.GetHeader("X-Dev-Permissions"), ",") {
			if permission = strings.TrimSpace(permission); permission != "" {
				permissions[permission] = struct{}{}
			}
		}
		roles := make([]string, 0)
		for _, role := range strings.Split(c.GetHeader("X-Dev-Roles"), ",") {
			if role = strings.TrimSpace(role); role != "" {
				roles = append(roles, role)
			}
		}
		scope := auth.ScopeMode(strings.ToUpper(c.GetHeader("X-Dev-Scope")))
		if scope != auth.ScopeAll && scope != auth.ScopeOrg {
			scope = auth.ScopeSelf
		}
		principal := auth.Principal{
			UserID: userID, PersonID: strings.TrimSpace(c.GetHeader("X-Dev-Person-ID")), TenantID: tenantID, DisplayName: c.GetHeader("X-Dev-Display-Name"),
			Roles: roles, Permissions: permissions, ScopeMode: scope,
		}
		c.Request = c.Request.WithContext(auth.WithPrincipal(c.Request.Context(), principal))
		c.Next()
	}
}

func RequirePermission(permission string) gin.HandlerFunc {
	return func(c *gin.Context) {
		principal, ok := auth.FromContext(c.Request.Context())
		if !ok {
			response.Error(c, apperror.ErrUnauthenticated)
			c.Abort()
			return
		}
		if !principal.HasPermission(permission) {
			response.Error(c, apperror.ErrForbidden)
			c.Abort()
			return
		}
		c.Next()
	}
}

// RequireAnyPermission grants a read-only route when the principal owns at
// least one of the explicitly listed capabilities. Callers must not use it for
// mutations where each capability has different authority.
func RequireAnyPermission(permissions ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		principal, ok := auth.FromContext(c.Request.Context())
		if !ok {
			response.Error(c, apperror.ErrUnauthenticated)
			c.Abort()
			return
		}
		for _, permission := range permissions {
			if principal.HasPermission(permission) {
				c.Next()
				return
			}
		}
		response.Error(c, apperror.ErrForbidden)
		c.Abort()
	}
}
