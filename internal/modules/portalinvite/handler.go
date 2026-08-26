package portalinvite

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/requestbody"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/response"
)

type Handler struct{ service *Service }

type AccessDisableHandler struct{ service *AccessDisableService }

func NewHandler(service *Service) *Handler { return &Handler{service: service} }
func NewAccessDisableHandler(service *AccessDisableService) *AccessDisableHandler {
	return &AccessDisableHandler{service: service}
}

func (h *Handler) Create(c *gin.Context) {
	id, ok := customerID(c)
	if !ok {
		return
	}
	value, err := h.service.Create(c.Request.Context(), id, CreateRequest{IdempotencyKey: c.GetHeader("Idempotency-Key")})
	if err != nil {
		response.Error(c, err)
		return
	}
	// 激活链接属于一次性 Bearer 凭证，禁止浏览器和中间缓存保存创建响应。
	c.Header("Cache-Control", "no-store, private")
	c.Header("Pragma", "no-cache")
	response.Created(c, value)
}

func (h *Handler) Current(c *gin.Context) {
	id, ok := customerID(c)
	if !ok {
		return
	}
	value, err := h.service.Current(c.Request.Context(), id)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, value)
}

func (h *Handler) Revoke(c *gin.Context) {
	var body RevokeRequest
	if err := requestbody.DecodeJSON(c, &body); err != nil {
		response.Error(c, apperror.New(http.StatusBadRequest, "COMMON_INVALID_ARGUMENT", "invalid request"))
		return
	}
	value, err := h.service.Revoke(c.Request.Context(), c.Param("inviteNo"), body)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, value)
}

func (h *Handler) Verify(c *gin.Context) {
	var body VerifyRequest
	if err := requestbody.DecodeJSON(c, &body); err != nil {
		response.Error(c, apperror.New(http.StatusBadRequest, "COMMON_INVALID_ARGUMENT", "invalid request"))
		return
	}
	value, err := h.service.Verify(c.Request.Context(), body.Token)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, value)
}

func (h *Handler) Consume(c *gin.Context) {
	var body ConsumeRequest
	if err := requestbody.DecodeJSON(c, &body); err != nil {
		response.Error(c, apperror.New(http.StatusBadRequest, "COMMON_INVALID_ARGUMENT", "invalid request"))
		return
	}
	if err := h.service.Consume(c.Request.Context(), body); err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, nil)
}

func (h *AccessDisableHandler) Disable(c *gin.Context) {
	id, ok := customerID(c)
	if !ok {
		return
	}
	var body DisableAccessRequest
	if err := requestbody.DecodeJSON(c, &body); err != nil {
		response.Error(c, apperror.New(http.StatusBadRequest, "COMMON_INVALID_ARGUMENT", "invalid request"))
		return
	}
	body.IdempotencyKey = c.GetHeader("Idempotency-Key")
	value, err := h.service.Disable(c.Request.Context(), id, body)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, value)
}

func (h *AccessDisableHandler) Current(c *gin.Context) {
	id, ok := customerID(c)
	if !ok {
		return
	}
	result, err := h.service.Current(c.Request.Context(), id)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, result)
}

func customerID(c *gin.Context) (uint64, bool) {
	id, err := strconv.ParseUint(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, apperror.New(http.StatusBadRequest, "COMMON_INVALID_ARGUMENT", "customer id must be a positive integer"))
		return 0, false
	}
	return id, true
}
