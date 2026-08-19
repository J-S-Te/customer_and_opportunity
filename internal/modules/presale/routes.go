package presale

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/middleware"
)

// 售前路由挂载在宿主应用已认证的 /api/v1 分组下；中间件权限是第一道门槛，
// 服务层仍会校验状态、角色和资源关系，不能把路由权限视为最终授权。
func RegisterRoutes(api *gin.RouterGroup, handler *Handler) {
	presale := api.Group("/presale")
	presale.POST("/requests", middleware.RequirePermission("presale.create"), handler.CreateRequest)
	presale.POST("/requests/:id/reopen", middleware.RequirePermission("presale.create"), handler.ReopenRequest)
	presale.GET("/execution-departments", middleware.RequirePermission("presale.read"), handler.ExecutionDepartments)
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
	presale.PUT("/requests/:id/execution-department", middleware.RequirePermission("presale.assign"), handler.SelectExecutionDepartment)
	presale.GET("/requests/:id/assignments", middleware.RequirePermission("presale.read"), handler.Assignments)
	presale.POST("/requests/:id/progress", middleware.RequirePermission("presale.progress"), handler.AddProgress)
	presale.POST("/requests/:id/cancel", handler.Cancel)
	presale.POST("/requests/:id/worklogs", middleware.RequirePermission("presale.worklog"), handler.AddWorklog)
	presale.GET("/requests/:id/worklogs", middleware.RequirePermission("presale.read"), handler.Worklogs)
	presale.GET("/alert-rules", middleware.RequirePermission("presale.alert.config"), handler.AlertRules)
	presale.PUT("/alert-rules/:type", middleware.RequirePermission("presale.alert.config"), handler.UpdateAlertRule)
	presale.GET("/approval-rules", middleware.RequirePermission("presale.read"), handler.ApprovalRules)
	presale.POST("/approval-rules", middleware.RequirePermission("presale.approval_rule.manage"), handler.CreateApprovalRule)
	presale.PUT("/approval-rules/:id", middleware.RequirePermission("presale.approval_rule.manage"), handler.UpdateApprovalRule)
	presale.DELETE("/approval-rules/:id", middleware.RequirePermission("presale.approval_rule.manage"), handler.DeleteApprovalRule)
	presale.GET("/alerts", middleware.RequirePermission("presale.read"), handler.Alerts)
	presale.POST("/alerts/:id/read", middleware.RequirePermission("presale.read"), handler.ReadAlert)
	presale.GET("/reports/summary", middleware.RequirePermission("presale.report"), handler.ReportSummary)
	presale.GET("/reports/trend", middleware.RequirePermission("presale.report"), handler.ReportTrend)
	presale.GET("/reports/distribution", middleware.RequirePermission("presale.report"), handler.ReportDistribution)
	presale.GET("/reports/filter-options", middleware.RequirePermission("presale.report"), handler.ReportFilterOptions)
	presale.POST("/reports/exports", middleware.RequirePermission("presale.report"), handler.RequestReportExport)
}

// 电话接口的 no-store 中间件在认证前挂载，因此成功响应以及认证、权限拒绝响应都不可缓存，
// 防止共享浏览器或代理根据响应差异残留敏感访问痕迹。
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

// 审批回调单独挂载到内部路由，便于宿主统一校验机器身份、audience、scope、时间戳和防重放信息。
func RegisterInternalRoutes(internal *gin.RouterGroup, handler *Handler) {
	internal.POST("/approval/callbacks/presale", middleware.RequirePermission("approval.callback.write"), handler.ApprovalCallback)
}
