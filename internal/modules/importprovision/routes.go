package importprovision

import (
	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/middleware"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/customer"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/opportunity"
)

// RegisterInternalRoutes 在机器认证路由组内注册历史合同导入开立通道。
// 参数：router 为已挂载机器认证的 /api/v1/internal 路由组，handler 为通道处理器。
// 返回值：无。端点各自要求专用 scope：
// customer.import_provision 覆盖客户查询与建档，opportunity.import_provision 覆盖商机建档与推进；
// 普通 POST /customers、POST /opportunities 的权限（customer.create、opportunity.create）保持不变。
// 路径直接写到 router 上（不额外 Group），使 OpenAPI 漂移检查能按源码核对完整路径。
func RegisterInternalRoutes(router *gin.RouterGroup, handler *Handler) {
	router.POST("/import-provision/customers/lookup", middleware.RequirePermission(customer.ProvisionScope), handler.LookupCustomer)
	router.POST("/import-provision/customers", middleware.RequirePermission(customer.ProvisionScope), handler.CreateCustomer)
	router.POST("/import-provision/opportunities", middleware.RequirePermission(opportunity.ProvisionScope), handler.CreateOpportunity)
	router.POST("/import-provision/opportunities/:id/sign", middleware.RequirePermission(opportunity.ProvisionScope), handler.SignOpportunity)
}
