package contractreference

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/response"
)

type Handler struct{ service *Service }

func NewHandler(service *Service) *Handler { return &Handler{service: service} }

func (handler *Handler) Resolve(c *gin.Context) {
	if handler == nil || handler.service == nil {
		response.Error(c, apperror.New(http.StatusServiceUnavailable, "CRM_CONTRACT_REFERENCE_UNAVAILABLE", "contract reference directory is unavailable"))
		return
	}
	customerID, err := strconv.ParseUint(c.Param("customerID"), 10, 64)
	actorID := strings.TrimSpace(c.GetHeader("X-Actor-Identity-ID"))
	if err != nil || customerID == 0 || actorID == "" || len(actorID) > 128 || len(c.Request.URL.Query()["opportunity_id"]) > 1 {
		response.Error(c, ErrInvalidReference)
		return
	}
	var opportunityID *uint64
	if raw := c.Query("opportunity_id"); raw != "" {
		parsed, parseErr := strconv.ParseUint(raw, 10, 64)
		if parseErr != nil || parsed == 0 {
			response.Error(c, ErrInvalidReference)
			return
		}
		opportunityID = &parsed
	}
	result, err := handler.service.Resolve(c.Request.Context(), customerID, opportunityID, actorID)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, result)
}
