package portalbootstrap

import (
	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/account"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/response"
)

// centralLifecycle 保留客户及项目隔离检查，只向当前已认证门户会话代理中央生命周期。
// 参数：deps为已装配门户依赖，interact为是否上报真实交互。
// 返回值：状态或活动处理器，业务权限与客户边界不由中央接口授予。
func centralLifecycle(deps RouterDependencies, interact bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		cookie, err := c.Request.Cookie(deps.Config.SessionCookieName)
		if err != nil || cookie.Value == "" {
			response.Error(c, account.ErrInvalidLoginState)
			return
		}
		state, err := deps.Account.Lifecycle(c.Request.Context(), deps.Config.TenantID, cookie.Value, interact)
		if err != nil {
			response.Error(c, err)
			return
		}
		response.OK(c, state)
	}
}
