package crmauth

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	sharedauthorization "github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/authorizationcontext"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/response"
)

type HTTPOptions struct {
	PathPrefix, PublicOrigin, CookieName, EndSessionEndpoint, ClientID string
	CookieSecure                                                       bool
	PostLogoutRedirectURI                                              string
}

type Handler struct {
	service *Service
	options HTTPOptions
}

func NewHandler(service *Service, options HTTPOptions) *Handler {
	return &Handler{service: service, options: options}
}

// Login 建立原state、nonce和PKCE事务并进入可信登录流程。
// 参数：c为当前HTTP请求上下文。
// 返回值：无。
func (h *Handler) Login(c *gin.Context) {
	result, err := h.service.BeginLogin(c.Request.Context(), c.Query("return_to"), strings.EqualFold(strings.TrimSpace(c.Query("prompt")), "login"))
	if err != nil {
		response.Error(c, err)
		return
	}
	if h.service.options.CentralClient != nil {
		http.SetCookie(c.Writer, &http.Cookie{Name: h.options.CookieName + "_oidc_state", Value: result.State, Path: h.cookiePath(), HttpOnly: true, Secure: h.options.CookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: 600})
	}
	c.Redirect(http.StatusFound, result.AuthorizationURL)
}

// Callback 消费原登录事务，验证身份并创建受中央期限约束的本地会话。
// 参数：c为当前HTTP请求上下文。
// 返回值：无。
func (h *Handler) Callback(c *gin.Context) {
	if h.service.options.CentralClient != nil {
		cookie, err := c.Request.Cookie(h.options.CookieName + "_oidc_state")
		if err != nil || cookie.Value == "" || cookie.Value != c.Query("state") {
			response.Error(c, apperror.ErrUnauthenticated)
			return
		}
		c.Header("Cache-Control", "no-store")
		c.Header("Referrer-Policy", "no-referrer")
		if ticket := c.Query("session_link_ticket"); ticket != "" {
			if c.Query("code") != "" {
				response.Error(c, apperror.ErrUnauthenticated)
				return
			}
			start, err := h.service.AcceptSessionLink(c.Request.Context(), cookie.Value, ticket)
			if err != nil {
				writeSessionFailure(c, err)
				return
			}
			c.Redirect(http.StatusFound, start.AuthorizationURL)
			return
		}
		http.SetCookie(c.Writer, &http.Cookie{Name: h.options.CookieName + "_oidc_state", Value: "", Path: h.cookiePath(), HttpOnly: true, Secure: h.options.CookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	}
	if providerError := strings.TrimSpace(c.Query("error")); providerError != "" {
		slog.Default().Warn("CRM OIDC provider callback rejected",
			"provider_error", providerError,
			"has_error_description", strings.TrimSpace(c.Query("error_description")) != "",
			"has_state", strings.TrimSpace(c.Query("state")) != "",
		)
		response.Error(c, apperror.ErrUnauthenticated)
		return
	}
	result, err := h.service.CompleteLogin(c.Request.Context(), c.Query("state"), c.Query("code"))
	if err != nil {
		// 浏览器响应保持通用错误，同时保留有类型的
		// 服务端原因。否则服务发现、授权码
		// 交换、nonce、签发者及授权声明失败均表现为
		// 同一未认证响应，阻碍 Broker 取证，且无法
		// 提供可操作的诊断。
		slog.Default().Warn("CRM OIDC callback failed",
			"has_state", strings.TrimSpace(c.Query("state")) != "",
			"has_code", strings.TrimSpace(c.Query("code")) != "",
			"error", err,
		)
		if errors.Is(err, sharedauthorization.ErrUnavailable) {
			response.Error(c, apperror.Wrap(err, http.StatusServiceUnavailable, "COMMON_DEPENDENCY_UNAVAILABLE", "authorization service is temporarily unavailable"))
			return
		}
		response.Error(c, apperror.ErrUnauthenticated)
		return
	}
	h.setCookie(c, result.SessionToken, result.ExpiresAt)
	target := result.ReturnPath
	if target == "/" {
		target = strings.TrimRight(h.options.PathPrefix, "/") + "/"
	}
	if !strings.HasPrefix(target, h.options.PathPrefix+"/") && target != h.options.PathPrefix {
		// service 只保证站内路径；这里再收紧到 CRM 挂载前缀，避免登录后跳入同域的其他子系统。
		target = strings.TrimRight(h.options.PathPrefix, "/") + "/"
	}
	c.Redirect(http.StatusFound, target)
}

func (h *Handler) Logout(c *gin.Context) {
	if cookie, err := c.Request.Cookie(h.options.CookieName); err == nil {
		h.service.Logout(c.Request.Context(), cookie.Value)
	}
	http.SetCookie(c.Writer, &http.Cookie{Name: h.options.CookieName, Value: "", Path: h.cookiePath(), Expires: timeUnixOne, MaxAge: -1, HttpOnly: true, Secure: h.options.CookieSecure, SameSite: http.SameSiteLaxMode})
	if h.options.EndSessionEndpoint != "" {
		endpoint, err := url.ParseRequestURI(h.options.EndSessionEndpoint)
		if err == nil && (endpoint.Scheme == "http" || endpoint.Scheme == "https") && endpoint.Host != "" && endpoint.User == nil && endpoint.Fragment == "" {
			query := endpoint.Query()
			if strings.TrimSpace(h.options.ClientID) != "" {
				query.Set("client_id", strings.TrimSpace(h.options.ClientID))
			}
			if validSameOriginRedirect(h.options.PostLogoutRedirectURI, h.options.PublicOrigin) {
				query.Set("post_logout_redirect_uri", strings.TrimSpace(h.options.PostLogoutRedirectURI))
			}
			endpoint.RawQuery = query.Encode()
			c.Redirect(http.StatusFound, endpoint.String())
			return
		}
	}
	c.Redirect(http.StatusFound, strings.TrimRight(h.options.PathPrefix, "/")+"/")
}

func (h *Handler) LocalLogout(c *gin.Context) {
	if cookie, err := c.Request.Cookie(h.options.CookieName); err == nil {
		h.service.Logout(c.Request.Context(), cookie.Value)
	}
	h.setExpiredCookie(c)
	c.Header("Cache-Control", "no-store")
	c.Status(http.StatusNoContent)
}

func validSameOriginRedirect(candidate, publicOrigin string) bool {
	redirect, err := url.ParseRequestURI(strings.TrimSpace(candidate))
	origin, originErr := url.ParseRequestURI(strings.TrimSpace(publicOrigin))
	if err != nil || originErr != nil || redirect.Scheme == "" || redirect.Host == "" || redirect.User != nil || redirect.Fragment != "" {
		return false
	}
	return (redirect.Scheme == "http" || redirect.Scheme == "https") &&
		redirect.Scheme == origin.Scheme && strings.EqualFold(redirect.Host, origin.Host)
}

// RequireSameOrigin 保护基于 Cookie 的写请求。除 Origin 必须与公开入口同源外，还要求自定义头，
// 使跨站 HTML 表单即便携带 SameSite 允许的 Cookie 也无法通过校验。
func (h *Handler) RequireSameOrigin(c *gin.Context) {
	origin, err := url.Parse(c.GetHeader("Origin"))
	expected, expectedErr := url.Parse(h.options.PublicOrigin)
	if err != nil || expectedErr != nil || origin.Scheme != expected.Scheme || origin.Host != expected.Host || c.GetHeader("X-CSRF-Token") != "1" {
		response.Error(c, apperror.ErrForbidden)
		c.Abort()
		return
	}
	c.Next()
}

func (h *Handler) setCookie(c *gin.Context, value string, expires time.Time) {
	http.SetCookie(c.Writer, &http.Cookie{Name: h.options.CookieName, Value: value, Path: h.cookiePath(), Expires: expires, HttpOnly: true, Secure: h.options.CookieSecure, SameSite: http.SameSiteLaxMode})
}

func (h *Handler) setExpiredCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{Name: h.options.CookieName, Value: "", Path: h.cookiePath(), Expires: timeUnixOne, MaxAge: -1, HttpOnly: true, Secure: h.options.CookieSecure, SameSite: http.SameSiteLaxMode})
}

func (h *Handler) cookiePath() string {
	if h.options.PathPrefix == "" {
		return "/"
	}
	return h.options.PathPrefix
}

var timeUnixOne = time.Unix(1, 0)
