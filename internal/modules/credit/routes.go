package credit

import (
	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/middleware"
)

func RegisterRoutes(api *gin.RouterGroup, h *Handler) {
	api.GET("/customers/:id/credit", middleware.RequirePermission("customer.credit.read"), h.Level)
	api.GET("/customers/:id/credit/history", middleware.RequirePermission("customer.credit.read"), h.History)
	api.GET("/customers/:id/credit/payment-records", middleware.RequirePermission("customer.credit.read"), h.PaymentRecords)
	api.POST("/customers/:id/credit/applications", middleware.RequirePermission("customer.credit.apply"), h.Apply)
	api.POST("/customers/:id/credit/applications/:applicationID/withdraw", middleware.RequirePermission("customer.credit.apply"), h.Withdraw)
	api.GET("/credit/applications/pending", middleware.RequirePermission("customer.credit.approve"), h.Pending)
	api.POST("/credit/applications/:applicationID/approve", middleware.RequirePermission("customer.credit.approve"), h.Approve)
	api.POST("/credit/applications/:applicationID/reject", middleware.RequirePermission("customer.credit.approve"), h.Reject)
}

func RegisterInternalRoutes(internal *gin.RouterGroup, h *Handler) {
	internal.GET("/customers/:id/credit-level", middleware.RequirePermission("customer.credit.internal.read"), h.Level)
	internal.POST("/credit/payment-events", middleware.RequirePermission("customer.credit.payment.ingest"), h.Payment)
}
