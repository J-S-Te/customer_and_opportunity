package portalbootstrap

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/account"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	sharedauth "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/requestbody"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/response"
)

func originAndCSRF(config Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		parsedOrigin, err := url.Parse(origin)
		expected, _ := url.Parse(config.PublicOrigin)
		if err != nil || origin == "" || parsedOrigin.Scheme != expected.Scheme || parsedOrigin.Host != expected.Host {
			response.Error(c, apperror.ErrForbidden)
			c.Abort()
			return
		}
		// 除 Origin 外还要求非简单自定义头，在不向 JavaScript 暴露 CSRF 密钥的情况下保护
		// Cookie 认证写请求。
		if c.GetHeader("X-CSRF-Token") != "1" {
			response.Error(c, apperror.ErrForbidden)
			c.Abort()
			return
		}
		c.Next()
	}
}

func requirePermission(expected string) gin.HandlerFunc {
	return func(c *gin.Context) {
		for _, permission := range currentSession(c).Permissions {
			if permission == expected {
				c.Next()
				return
			}
		}
		response.Error(c, apperror.ErrForbidden)
		c.Abort()
	}
}

// 多种能力共享的只读前置数据可接受任一权限；详情和变更接口仍由各自精确权限保护，不随此前置
// 查询扩大授权面。
func requireAnyPermission(expected ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		for _, actual := range currentSession(c).Permissions {
			for _, allowed := range expected {
				if actual == allowed {
					c.Next()
					return
				}
			}
		}
		response.Error(c, apperror.ErrForbidden)
		c.Abort()
	}
}

func machineAuth(authenticator machineAuthenticator) gin.HandlerFunc {
	return func(c *gin.Context) {
		if authenticator == nil {
			response.Error(c, apperror.ErrUnauthenticated)
			c.Abort()
			return
		}
		principal, err := authenticator.Authenticate(c.Request.Context(), c.Request)
		if err != nil {
			response.Error(c, apperror.ErrUnauthenticated)
			c.Abort()
			return
		}
		c.Request = c.Request.WithContext(sharedauth.WithPrincipal(c.Request.Context(), principal))
		c.Next()
	}
}

func requireMachineScope(expected string) gin.HandlerFunc {
	return func(c *gin.Context) {
		principal, ok := sharedauth.FromContext(c.Request.Context())
		if !ok || len(principal.Permissions) != 1 || !principal.HasPermission(expected) {
			response.Error(c, apperror.ErrForbidden)
			c.Abort()
			return
		}
		c.Next()
	}
}

// 除 scope 外再绑定精确机器客户端 subject，防止其他集成客户端因误获同名 scope 而调用高影响
// 账号接口。
func requireMachineClientSubject(expected string) gin.HandlerFunc {
	return func(c *gin.Context) {
		principal, ok := sharedauth.FromContext(c.Request.Context())
		expected = strings.TrimSpace(expected)
		if !ok || expected == "" || principal.UserID != "machine:"+expected {
			response.Error(c, apperror.ErrForbidden)
			c.Abort()
			return
		}
		c.Next()
	}
}

func secureHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Referrer-Policy", "same-origin")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Cache-Control", "no-store")
		c.Next()
	}
}

func unsupported(code string) gin.HandlerFunc {
	return func(c *gin.Context) {
		response.Error(c, apperror.New(http.StatusServiceUnavailable, code, "required external adapter is not configured"))
	}
}

func currentSession(c *gin.Context) *account.Session {
	value, _ := c.Get(sessionContextKey)
	session, _ := value.(*account.Session)
	return session
}

func sessionCookie(config Config, value string, expires time.Time) *http.Cookie {
	return &http.Cookie{Name: config.SessionCookieName, Value: value, Path: config.PathPrefix, Expires: expires, HttpOnly: true, Secure: config.SessionCookieSecure, SameSite: http.SameSiteLaxMode}
}

func safeLocalPath(value, prefix string) bool {
	return strings.HasPrefix(value, prefix+"/") && !strings.HasPrefix(value, "//") && !strings.ContainsAny(value, "\r\n")
}

func decode(c *gin.Context, target any) bool {
	if err := requestbody.DecodeJSON(c, target); err != nil {
		// 不返回解析器和请求体细节，既保持错误契约稳定，也避免未知字段名或畸形敏感值被反射给调用方。
		response.Error(c, apperror.New(http.StatusUnprocessableEntity, "COMMON_VALIDATION_ERROR", "request body is invalid"))
		return false
	}
	return true
}
