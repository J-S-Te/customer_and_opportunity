package portalreportreference

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/middleware"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/response"
)

type Handler struct{ Reader Reader }

func RegisterRoutes(group *gin.RouterGroup, reader Reader) {
	handler := &Handler{Reader: reader}
	group.GET("/portal-report/download-identity", middleware.RequirePermission(Scope), handler.GetDownloadIdentity)
}

func (h *Handler) GetDownloadIdentity(c *gin.Context) {
	principal, ok := auth.FromContext(c.Request.Context())
	if !ok || principal.TenantID == "" {
		response.Error(c, apperror.ErrUnauthenticated)
		return
	}
	customerID, err := strconv.ParseUint(c.Query("customer_id"), 10, 64)
	accountID := strings.TrimSpace(c.Query("account_id"))
	if err != nil || customerID == 0 || accountID == "" || len(accountID) > 128 || len(c.Request.URL.Query()["customer_id"]) != 1 || len(c.Request.URL.Query()["account_id"]) != 1 {
		response.Error(c, apperror.New(400, "CRM_PORTAL_REPORT_IDENTITY_INVALID", "invalid identity reference"))
		return
	}
	if h == nil || h.Reader == nil {
		response.Error(c, apperror.New(http.StatusServiceUnavailable, "CRM_PORTAL_REPORT_IDENTITY_UNAVAILABLE", "report identity unavailable"))
		return
	}
	result, err := h.Reader.Resolve(c.Request.Context(), principal.TenantID, customerID, accountID)
	if err != nil {
		response.Error(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	response.OK(c, result)
}
