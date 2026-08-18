package opportunity

import (
	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/middleware"
)

func RegisterRoutes(router *gin.RouterGroup, handler *Handler) {
	router.GET("/opportunity-stage-alert-rules", middleware.RequirePermission("opportunity.alert.config"), handler.StageAlertRules)
	router.PUT("/opportunity-stage-alert-rules/:stage", middleware.RequirePermission("opportunity.alert.config"), handler.UpdateStageAlertRule)
	router.GET("/opportunity-stage-alerts", middleware.RequirePermission("opportunity.read"), handler.StageAlerts)
	router.POST("/opportunity-stage-alerts/:id/read", middleware.RequirePermission("opportunity.read"), handler.ReadStageAlert)
	opportunities := router.Group("/opportunities")
	opportunities.GET("", middleware.RequirePermission("opportunity.read"), handler.List)
	opportunities.GET("/board", middleware.RequirePermission("opportunity.read"), handler.Board)
	opportunities.GET("/:id", middleware.RequirePermission("opportunity.read"), handler.Get)
	opportunities.POST("", middleware.RequirePermission("opportunity.create"), handler.Create)
	opportunities.PUT("/:id", middleware.RequirePermission("opportunity.update"), handler.Update)
	opportunities.PUT("/:id/owner", middleware.RequirePermission("opportunity.owner.change"), handler.ChangeOwner)
	opportunities.GET("/:id/members", middleware.RequirePermission("opportunity.read"), handler.GetMembers)
	opportunities.GET("/:id/member-terms", middleware.RequirePermission("opportunity.read"), handler.ListMemberTerms)
	opportunities.PUT("/:id/members", middleware.RequirePermission("opportunity.team.manage"), handler.ReplaceMembers)
	opportunities.POST("/:id/void", middleware.RequirePermission("opportunity.void"), handler.Void)
	opportunities.POST("/:id/restore", middleware.RequirePermission("opportunity.restore"), handler.Restore)
	opportunities.POST("/:id/stage-changes", middleware.RequirePermission("opportunity.stage.change"), handler.ChangeStage)
	opportunities.GET("/:id/stage-history", middleware.RequirePermission("opportunity.read"), handler.StageHistory)
	opportunities.POST("/:id/followups", middleware.RequirePermission("opportunity.update"), handler.CreateFollowup)
	opportunities.GET("/:id/followups", middleware.RequirePermission("opportunity.read"), handler.ListFollowups)
	opportunities.GET("/:id/external-status", middleware.RequirePermission("opportunity.read"), handler.ExternalStatus)
	opportunities.POST("/:id/launch/quotation", middleware.RequirePermission("opportunity.update"), handler.LaunchQuotation)
	opportunities.POST("/:id/launch/bid", middleware.RequirePermission("opportunity.update"), handler.LaunchBid)
	opportunities.PUT("/:id/terminal-todo", middleware.RequirePermission("opportunity.stage.change"), handler.CompleteTerminalTodo)
	opportunities.POST("/:id/contract-transfer", middleware.RequirePermission("opportunity.contract.transfer"), handler.ContractTransfer)
	opportunities.GET("/:id/attachment-capabilities", middleware.RequirePermission("opportunity.attachment.read"), handler.AttachmentCapabilities)
	opportunities.GET("/:id/attachments", middleware.RequirePermission("opportunity.attachment.read"), handler.ListAttachments)
	opportunities.POST("/:id/attachments", middleware.RequirePermission("opportunity.attachment.upload"), handler.CreateAttachmentUpload)
	opportunities.PUT("/:id/attachments/:attachmentID/content", middleware.RequirePermission("opportunity.attachment.upload"), handler.UploadAttachmentContent)
	opportunities.POST("/:id/attachments/:attachmentID/complete", middleware.RequirePermission("opportunity.attachment.upload"), handler.CompleteAttachmentUpload)
	opportunities.GET("/:id/attachments/:attachmentID/content", middleware.RequirePermission("opportunity.attachment.download"), handler.DownloadAttachment)
}

func RegisterIntegrationRoutes(router *gin.RouterGroup, handler *Handler) {
	router.POST("/integrations/qb/status-events", middleware.RequirePermission("opportunity.status.write"), handler.ApplyExternalStatus)
	router.POST("/opportunities/:id/contract-link", middleware.RequirePermission("opportunity.signed.write"), handler.ContractLinkCallback)
	router.POST("/integrations/opportunity-attachment/scan-events", middleware.RequirePermission("opportunity.attachment.scan.write"), handler.ApplyAttachmentScan)
}
