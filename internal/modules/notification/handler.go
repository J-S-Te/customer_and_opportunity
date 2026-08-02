package notification

import (
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/response"
)

type Handler struct{ service *Service }

func NewHandler(service *Service) *Handler { return &Handler{service: service} }

func (h *Handler) ListMine(c *gin.Context) {
	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	if err != nil || page < 1 {
		response.Error(c, apperror.New(400, "COMMON_INVALID_PAGINATION", "invalid pagination"))
		return
	}
	pageSize, err := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if err != nil || pageSize < 1 || pageSize > 100 {
		response.Error(c, apperror.New(400, "COMMON_INVALID_PAGINATION", "invalid pagination"))
		return
	}
	unreadOnly := false
	if raw := c.Query("unread_only"); raw != "" {
		unreadOnly, err = strconv.ParseBool(raw)
		if err != nil {
			response.Error(c, apperror.New(400, "COMMON_INVALID_ARGUMENT", "invalid request"))
			return
		}
	}
	result, err := h.service.ListMine(c.Request.Context(), unreadOnly, page, pageSize)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) UnreadCount(c *gin.Context) {
	count, err := h.service.UnreadCount(c.Request.Context())
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, gin.H{"count": count})
}

func (h *Handler) MarkRead(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, apperror.New(400, "COMMON_INVALID_ARGUMENT", "invalid request"))
		return
	}
	if err = h.service.MarkRead(c.Request.Context(), id); err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, gin.H{"read": true})
}
