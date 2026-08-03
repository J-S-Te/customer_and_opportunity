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

// 浏览器身份只来自不透明 Cookie 对应的服务端 CRM 会话；请求头里的用户、角色和权限声明
// 不参与认证，避免客户端伪造授权上下文。
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

// Cookie 认证的写请求必须同时匹配精确 Origin 和自定义 CSRF 头。安全方法保持可读，机器接口
// 位于独立路由组，不借此规则把任意 Authorization 头当作 CSRF 豁免。
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

// 内部集成独立验证平台应用令牌，并把验签后的主体放入同一 Principal 边界；浏览器会话不能
// 复用到该路由组。
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

// 开发认证有意使用请求头，仅用于本地联调；它仍构造与正式服务端会话相同的 Principal，
// 但这些头没有密码学可信度，部署环境必须通过配置校验阻止启用。
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
	// 此处只判断已验证主体的应用权限码；服务层还需根据 ScopeMode、组织和资源归属收窄数据范围。
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

// 只读前置接口可在明确列出的能力中满足任意一个；权限含义不同的写操作不能复用“任一满足”，
// 否则较弱权限可能间接获得较强操作权。
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
