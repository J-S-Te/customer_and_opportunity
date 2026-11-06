package portalbootstrap

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/account"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/localidentity"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/response"
)

// localAdminSession 精确识别无客户上下文的本地门户管理身份。
// 参数：config为固定模式，session为已验证平台会话。
// 返回值：是否为本地门户超管管理会话。
func localAdminSession(config Config, session *account.Session) bool {
	return config.directoryMode() == localidentity.ModeLocal && session != nil && session.CustomerID == 0 && len(session.Roles) == 1 && session.Roles[0] == "portal_super_admin"
}

// managementMetadata 将客户能力豁免限制在本地管理和会话元数据。
// 参数：c为当前已匹配路由，config/session为固定配置和可信会话。
// 返回值：是否可跳过客户功能交集，业务路由不会命中。
func managementMetadata(c *gin.Context, config Config, session *account.Session) bool {
	if !localAdminSession(config, session) {
		return false
	}
	route := strings.TrimPrefix(c.FullPath(), config.PathPrefix)
	switch route {
	case "/api/v1/auth/me", "/api/v1/capabilities", "/auth/session", "/auth/activity", "/auth/logout", "/api/v1/account/security", "/api/v1/account/sessions", "/api/v1/account/sessions/:id", "/api/v1/account/security-events/:id/ack":
		return true
	}
	return strings.HasPrefix(route, "/api/v1/local-admin/")
}

// localAdminGuard 仅允许显式本地模式的真实超管进入管理组。
// 参数：config为受控启动配置。
// 返回值：鉴权中间件，不给管理员创建任意客户上下文。
func localAdminGuard(config Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !localAdminSession(config, currentSession(c)) {
			response.Error(c, apperror.ErrForbidden)
			c.Abort()
			return
		}
		c.Next()
	}
}

// csrfOnWrites 保留既有Origin及CSRF写保护，管理只读元数据不要求写标记。
// 参数：config为门户固定公开源。
// 返回值：仅对写方法执行既有CSRF验证的中间件。
func csrfOnWrites(config Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead || c.Request.Method == http.MethodOptions {
			c.Next()
			return
		}
		originAndCSRF(config)(c)
	}
}

// customerBusinessBoundary 禁止无客户管理员会话进入旧客户业务路由。
// 参数：无。
// 返回值：只接受真实正客户边界的中间件。
func customerBusinessBoundary() gin.HandlerFunc {
	return func(c *gin.Context) {
		session := currentSession(c)
		if session == nil || session.CustomerID == 0 {
			response.Error(c, apperror.ErrForbidden)
			c.Abort()
			return
		}
		c.Next()
	}
}

// crmSourceOnly 限定旧无命名空间机器协议为CRM权威。
// 参数：deps为固定组合根与数据库来源锁。
// 返回值：来源中间件，独立本地模式不接受旧CRM数字输入。
func crmSourceOnly(deps RouterDependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		if deps.Config.directoryMode() != localidentity.ModeCRM {
			response.Error(c, localidentity.ErrNamespace)
			c.Abort()
			return
		}
		if deps.CustomerAuthority != nil {
			if err := deps.CustomerAuthority.CheckMode(c.Request.Context(), deps.Config.TenantID, localidentity.ModeCRM); err != nil {
				response.Error(c, err)
				c.Abort()
				return
			}
		}
		c.Next()
	}
}

// localCustomerReport 仅为本地来源追加精确报告ACL，不修改原CRM下载语义。
// 参数：c/deps为当前客户请求，id/permission为原报告路由坐标。
// 返回值：可继续原报告安全链；失败已输出拒绝。
func localCustomerReport(c *gin.Context, deps RouterDependencies, id uint64, permission string) bool {
	if deps.Config.directoryMode() != localidentity.ModeLocal {
		return true
	}
	if deps.LocalIdentity == nil {
		response.Error(c, localidentity.ErrUnavailable)
		return false
	}
	if err := deps.LocalIdentity.AssertCustomerReport(c.Request.Context(), actor(c), id, permission); err != nil {
		response.Error(c, err)
		return false
	}
	return true
}
