package ownerdirectory

import (
	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/middleware"
)

func RegisterRoutes(router *gin.RouterGroup, handler *Handler) {
	router.GET("/owner-directory", middleware.RequireAnyPermission(
		"customer.read", "customer.create", "customer.update",
		"opportunity.create", "opportunity.owner.change", "opportunity.team.manage",
		"presale.report",
	), handler.List)
}
