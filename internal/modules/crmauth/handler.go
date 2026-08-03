package crmauth

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/response"
)

type HTTPOptions struct {
	PathPrefix, PublicOrigin, CookieName, Issuer, ClientID string
	CookieSecure                                           bool
	PostLogoutRedirectURI                                  string
}

type Handler struct {
	service *Service
	options HTTPOptions
}

func NewHandler(service *Service, options HTTPOptions) *Handler {
	return &Handler{service: service, options: options}
}

func (h *Handler) Login(c *gin.Context) {
	result, err := h.service.BeginLogin(c.Request.Context(), c.Query("return_to"))
	if err != nil {
		response.Error(c, err)
		return
	}
	c.Redirect(http.StatusFound, result.AuthorizationURL)
}

func (h *Handler) Callback(c *gin.Context) {
	if strings.TrimSpace(c.Query("error")) != "" {
		response.Error(c, apperror.ErrUnauthenticated)
		return
	}
	result, err := h.service.CompleteLogin(c.Request.Context(), c.Query("state"), c.Query("code"))
	if err != nil {
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
	if h.options.Issuer != "" {
		endpoint, err := url.Parse(strings.TrimRight(h.options.Issuer, "/") + "/oauth2/logout")
		if err == nil {
			query := endpoint.Query()
			query.Set("client_id", h.options.ClientID)
			if h.options.PostLogoutRedirectURI != "" {
				query.Set("post_logout_redirect_uri", h.options.PostLogoutRedirectURI)
			}
			endpoint.RawQuery = query.Encode()
			c.Redirect(http.StatusFound, endpoint.String())
			return
		}
	}
	c.Redirect(http.StatusFound, strings.TrimRight(h.options.PathPrefix, "/")+"/")
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

func (h *Handler) cookiePath() string {
	if h.options.PathPrefix == "" {
		return "/"
	}
	return h.options.PathPrefix
}

var timeUnixOne = time.Unix(1, 0)
