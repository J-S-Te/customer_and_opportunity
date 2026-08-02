package portalinvite

import (
	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/middleware"
)

func RegisterRoutes(api *gin.RouterGroup, handler *Handler) {
	api.POST("/customers/:id/portal-invites", middleware.RequirePermission("portal_account.provision"), handler.Create)
	api.GET("/customers/:id/portal-invites/current", middleware.RequireAnyPermission("portal_account.provision", "portal_account.revoke"), handler.Current)
	api.POST("/portal-invites/:inviteNo/revoke", middleware.RequirePermission("portal_account.revoke"), handler.Revoke)
}

func RegisterAccessDisableRoute(api *gin.RouterGroup, handler *AccessDisableHandler) {
	api.GET("/customers/:id/portal-access", middleware.RequireAnyPermission("portal_account.provision", "portal_account.disable"), handler.Current)
	api.POST("/customers/:id/portal-access/disable", middleware.RequirePermission("portal_account.disable"), handler.Disable)
}

// RegisterInternalRoutes must receive the CRM's dedicated machine-authenticated
// group. The middleware verifies platform audience/scope and consumes the
// timestamp/nonce replay key before these handlers run.
func RegisterInternalRoutes(internal *gin.RouterGroup, handler *Handler) {
	invites := internal.Group("/portal/invites", middleware.RequirePermission("portal.invite.verify"))
	invites.POST("/verify", handler.Verify)
	invites.POST("/consume", handler.Consume)
}
