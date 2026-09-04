package customer

import (
	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/middleware"
)

func RegisterRoutes(router *gin.RouterGroup, handler *Handler) {
	customers := router.Group("/customers")
	customers.GET("", middleware.RequirePermission("customer.read"), handler.List)
	customers.POST("/duplicate-check", middleware.RequirePermission("customer.create"), handler.CheckDuplicate)
	customers.POST("/merge", middleware.RequirePermission("customer.merge"), handler.Merge)
	customers.POST("/imports/preview", middleware.RequirePermission("customer.import"), handler.PreviewImport)
	customers.GET("/imports/template", middleware.RequirePermission("customer.import"), handler.DownloadImportTemplate)
	customers.POST("/imports/:jobNo/commit", middleware.RequirePermission("customer.import"), handler.CommitImport)
	customers.GET("/imports/:jobNo/errors", middleware.RequirePermission("customer.import"), handler.ImportErrors)
	customers.POST("", middleware.RequirePermission("customer.create"), handler.Create)
	customers.GET("/:id", middleware.RequirePermission("customer.read"), handler.Get)
	customers.GET("/:id/contacts", middleware.RequirePermission("customer.read"), handler.ListContacts)
	customers.GET("/:id/stakeholders", middleware.RequirePermission("customer.read"), handler.ListStakeholders)
	customers.PUT("/:id/stakeholders", middleware.RequirePermission("customer.update"), handler.ReplaceStakeholders)
	customers.GET("/:id/systems", middleware.RequirePermission("customer.read"), handler.ListInformationSystems)
	customers.PUT("/:id/systems", middleware.RequirePermission("customer.update"), handler.ReplaceInformationSystems)
	customers.GET("/:id/opportunities", middleware.RequirePermission("customer.read"), handler.OpportunityHistory)
	customers.GET("/:id/projects", middleware.RequirePermission("customer.read"), handler.ProjectHistory)
	customers.GET("/:id/audit-logs", middleware.RequirePermission("customer.audit.read"), handler.ListChangeLogs)
	customers.PUT("/:id", middleware.RequirePermission("customer.update"), handler.Update)
	customers.POST("/:id/void", middleware.RequirePermission("customer.void"), handler.Void)
	customers.POST("/:id/restore", middleware.RequirePermission("customer.restore"), handler.Restore)
	customers.GET("/:id/followups", middleware.RequirePermission("customer.read"), handler.ListFollowups)
	customers.POST("/:id/followups", middleware.RequirePermission("customer.update"), handler.CreateFollowup)
	router.POST("/customer-exports", middleware.RequirePermission("customer.export"), handler.CreateExport)
}
