package crmauth

import (
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/response"
	"net/http"
)

// writeSessionFailure 区分确定失效与暂时故障，避免清除尚有效的登录和编辑状态。
// 参数：c为HTTP上下文，err为业务或存储返回的错误。
// 返回值：无；确定未认证输出401，其余失败输出503。
func writeSessionFailure(c *gin.Context, err error) {
	if !errors.Is(err, ErrAuthorizationUnavailable) && errors.Is(err, ErrUnauthenticated) {
		response.Error(c, apperror.ErrUnauthenticated)
		return
	}
	response.Error(c, apperror.Wrap(err, http.StatusServiceUnavailable, "COMMON_DEPENDENCY_UNAVAILABLE", "会话服务暂不可用，请稍后重试"))
}

// SessionState 读取本Cookie精确关联的中央生命周期，不刷新业务权限或推进活动。
// 参数：c为已由CRM身份中间件校验的请求。
// 返回值：无；返回不含令牌秘密的权威窗口。
func (h *Handler) SessionState(c *gin.Context) { h.lifecycle(c, false) }

// Activity 代理通过Origin和CSRF校验的显式真实交互。
// 参数：c为已认证且通过同源写保护的请求。
// 返回值：无；中央到期不会被此接口复活。
func (h *Handler) Activity(c *gin.Context) { h.lifecycle(c, true) }

// lifecycle 统一状态及活动响应，临时故障保留浏览器登录态。
// 参数：c为当前请求，interact为真实交互标识。
// 返回值：无；确定失效401，中央或存储暂不可用503。
func (h *Handler) lifecycle(c *gin.Context, interact bool) {
	c.Header("Cache-Control", "no-store")
	cookie, err := c.Request.Cookie(h.options.CookieName)
	if err != nil || cookie.Value == "" {
		response.Error(c, apperror.ErrUnauthenticated)
		return
	}
	state, err := h.service.Lifecycle(c.Request.Context(), cookie.Value, interact)
	if err != nil {
		writeSessionFailure(c, err)
		return
	}
	response.OK(c, state)
}
