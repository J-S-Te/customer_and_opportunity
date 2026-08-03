package notification

import "github.com/gin-gonic/gin"

func RegisterRoutes(router *gin.RouterGroup, handler *Handler) {
	// 通知服务允许商机读取者和售前人员共用入口，但查询始终继续收窄到当前主体。
	// 若在路由层固定要求某一个权限，会错误地隐藏另一类用户本应看到的个人通知。
	router.GET("/notifications", handler.ListMine)
	router.GET("/notifications/unread-count", handler.UnreadCount)
	router.POST("/notifications/:id/read", handler.MarkRead)
}
