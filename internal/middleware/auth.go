package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	sharedauthorization "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/authorizationcontext"
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
			if errors.Is(err, sharedauthorization.ErrUnavailable) {
				response.Error(c, apperror.Wrap(err, http.StatusServiceUnavailable, "COMMON_DEPENDENCY_UNAVAILABLE", "authorization service is temporarily unavailable"))
				c.Abort()
				return
			}
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

// RequirePermissionOrRoles supports a deliberately explicit compatibility
// path for signed role claims from sessions created before a permission catalog
// update. It must only be used where the listed roles are independently
// authorized to perform the exact operation.
func RequirePermissionOrRoles(permission string, roles ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		principal, ok := auth.FromContext(c.Request.Context())
		if !ok {
			response.Error(c, apperror.ErrUnauthenticated)
			c.Abort()
			return
		}
		if !auth.HasPermissionOrRole(principal, permission, roles...) {
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
