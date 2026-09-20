package contractreference

import (
	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/middleware"
)

func RegisterInternalRoutes(router *gin.RouterGroup, handler *Handler) {
	router.GET("/contract-references/customers/:customerID", middleware.RequirePermission("customer.contract_reference.read"), handler.Resolve)
}
