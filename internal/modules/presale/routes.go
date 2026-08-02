package presale

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/middleware"
)

// RegisterRoutes mounts presale endpoints beneath the host application's
// already-authenticated /api/v1 route group.
func RegisterRoutes(api *gin.RouterGroup, handler *Handler) {
	presale := api.Group("/presale")
	presale.POST("/requests", middleware.RequirePermission("presale.create"), handler.CreateRequest)
	presale.GET("/engineers", middleware.RequirePermission("presale.read"), handler.Engineers)
	presale.POST("/engineers/sync", middleware.RequirePermission("presale.engineer.sync"), handler.SyncEngineers)
	presale.GET("/requests", middleware.RequirePermission("presale.read"), handler.ListRequests)
	presale.GET("/board", middleware.RequirePermission("presale.read"), handler.Board)
	presale.GET("/filter-options", middleware.RequirePermission("presale.read"), handler.FilterOptions)
	presale.GET("/requests/:id", middleware.RequirePermission("presale.read"), handler.RequestDetail)
	presale.GET("/requests/:id/contact-phone", middleware.RequirePermission("presale.contact_phone.read"), handler.ContactPhone)
	presale.GET("/requests/:id/timeline", middleware.RequirePermission("presale.read"), handler.Timeline)
	presale.GET("/requests/:id/available-actions", middleware.RequirePermission("presale.read"), handler.AvailableActions)
	presale.POST("/requests/:id/approval-actions", middleware.RequirePermission("presale.approve"), handler.ApprovalAction)
	presale.GET("/requests/:id/approval-history", middleware.RequirePermission("presale.read"), handler.ApprovalHistory)
	presale.PUT("/requests/:id/assignments", middleware.RequirePermission("presale.assign"), handler.ReplaceAssignments)
	presale.GET("/requests/:id/assignments", middleware.RequirePermission("presale.read"), handler.Assignments)
	presale.POST("/requests/:id/progress", middleware.RequirePermission("presale.progress"), handler.AddProgress)
	presale.POST("/requests/:id/cancel", handler.Cancel)
	presale.POST("/requests/:id/worklogs", middleware.RequirePermission("presale.worklog"), handler.AddWorklog)
	presale.GET("/requests/:id/worklogs", middleware.RequirePermission("presale.read"), handler.Worklogs)
	presale.POST("/worklogs/:id/retry", middleware.RequirePermission("presale.worklog.retry"), handler.RetryDelivery)
	presale.GET("/worklogs/:id/delivery", middleware.RequirePermission("presale.read"), handler.Delivery)
	presale.GET("/alert-rules", middleware.RequirePermission("presale.alert.config"), handler.AlertRules)
	presale.PUT("/alert-rules/:type", middleware.RequirePermission("presale.alert.config"), handler.UpdateAlertRule)
	presale.GET("/alerts", middleware.RequirePermission("presale.read"), handler.Alerts)
	presale.POST("/alerts/:id/read", middleware.RequirePermission("presale.read"), handler.ReadAlert)
	presale.GET("/reports/summary", middleware.RequirePermission("presale.report"), handler.ReportSummary)
	presale.GET("/reports/trend", middleware.RequirePermission("presale.report"), handler.ReportTrend)
	presale.GET("/reports/distribution", middleware.RequirePermission("presale.report"), handler.ReportDistribution)
	presale.POST("/reports/exports", middleware.RequirePermission("presale.report"), handler.RequestReportExport)
}

// ContactPhoneNoStore is mounted before authentication at bootstrap. It marks
// every response from the sensitive endpoint non-cacheable, including an
// authentication or permission rejection that occurs before the handler.
func ContactPhoneNoStore() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		if strings.Contains(path, "/api/v1/presale/requests/") && strings.HasSuffix(path, "/contact-phone") {
			c.Header("Cache-Control", "no-store, private")
			c.Header("Pragma", "no-cache")
		}
		c.Next()
	}
}

// RegisterInternalRoutes is separated so bootstrap can apply machine identity,
// audience, scope, timestamp and replay-protection middleware.
func RegisterInternalRoutes(internal *gin.RouterGroup, handler *Handler) {
	internal.POST("/approval/callbacks/presale", middleware.RequirePermission("approval.callback.write"), handler.ApprovalCallback)
}
