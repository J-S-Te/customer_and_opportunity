package notification

import "github.com/gin-gonic/gin"

func RegisterRoutes(router *gin.RouterGroup, handler *Handler) {
	// The service requires opportunity.read or presale.read and then always
	// narrows rows to the authenticated principal. A single-permission route
	// middleware would incorrectly hide TS notifications from technicians.
	router.GET("/notifications", handler.ListMine)
	router.GET("/notifications/unread-count", handler.UnreadCount)
	router.POST("/notifications/:id/read", handler.MarkRead)
}
