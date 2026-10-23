package bootstrap

import (
	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/requestaudit"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/response"
)

// auditOutboxStatusHandler 仅向已有客户审计读取权限的用户
// 提供租户范围内的聚合投递状态。
// 它不执行远程检查，也不返回客户端凭据、令牌、
// 载荷、请求头或请求体。
func auditOutboxStatusHandler(store *requestaudit.Store, config Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		principal, ok := auth.FromContext(c.Request.Context())
		if !ok {
			response.Error(c, apperror.ErrUnauthenticated)
			return
		}
		status, err := store.Status(c.Request.Context(), principal.TenantID)
		if err != nil {
			response.Error(c, err)
			return
		}
		c.Header("Cache-Control", "private, no-store")
		response.OK(c, gin.H{
			"application_code": config.PlatformApplicationCode,
			"environment_code": config.PlatformEnvironmentCode,
			"status":           status,
		})
	}
}
