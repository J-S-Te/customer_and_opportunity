package importprovision

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/requestbody"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/response"
)

// Handler 暴露历史合同导入开立通道的四个机器端点。
type Handler struct{ service *Service }

func NewHandler(service *Service) *Handler { return &Handler{service: service} }

// LookupCustomer 处理 POST /api/v1/internal/import-provision/customers/lookup。
func (h *Handler) LookupCustomer(c *gin.Context) {
	if _, err := actorIdentityID(c); err != nil {
		response.Error(c, err)
		return
	}
	var input CustomerLookupRequest
	if err := requestbody.DecodeJSON(c, &input); err != nil {
		response.Error(c, ErrInvalidRequest)
		return
	}
	result, err := h.service.LookupCustomer(c.Request.Context(), input)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, result)
}

// CreateCustomer 处理 POST /api/v1/internal/import-provision/customers。
// Idempotency-Key 与 X-Actor-Identity-ID 都是必需头：前者保证重试不重复建档，后者用于审计归属。
func (h *Handler) CreateCustomer(c *gin.Context) {
	actor, err := actorIdentityID(c)
	if err != nil {
		response.Error(c, err)
		return
	}
	key := idempotencyKey(c)
	if key == "" {
		response.Error(c, ErrIdempotencyKeyRequired)
		return
	}
	var input CustomerCreateRequest
	if err = requestbody.DecodeJSON(c, &input); err != nil {
		response.Error(c, ErrInvalidRequest)
		return
	}
	result, err := h.service.CreateCustomer(c.Request.Context(), actor, key, input)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.Created(c, result)
}

// CreateOpportunity 处理 POST /api/v1/internal/import-provision/opportunities。
func (h *Handler) CreateOpportunity(c *gin.Context) {
	actor, err := actorIdentityID(c)
	if err != nil {
		response.Error(c, err)
		return
	}
	key := idempotencyKey(c)
	if key == "" {
		response.Error(c, ErrIdempotencyKeyRequired)
		return
	}
	var input OpportunityCreateRequest
	if err = requestbody.DecodeJSON(c, &input); err != nil {
		response.Error(c, ErrInvalidRequest)
		return
	}
	result, err := h.service.CreateOpportunity(c.Request.Context(), actor, key, input)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.Created(c, result)
}

// SignOpportunity 处理 POST /api/v1/internal/import-provision/opportunities/:id/sign。
// 合同侧以 contract_ref 表达期望的推进结果，因此这里不要求 Idempotency-Key 头。
func (h *Handler) SignOpportunity(c *gin.Context) {
	actor, err := actorIdentityID(c)
	if err != nil {
		response.Error(c, err)
		return
	}
	id, parseErr := strconv.ParseUint(c.Param("id"), 10, 64)
	if parseErr != nil || id == 0 {
		response.Error(c, ErrInvalidRequest)
		return
	}
	var input OpportunitySignRequest
	if err = requestbody.DecodeJSON(c, &input); err != nil {
		response.Error(c, ErrInvalidRequest)
		return
	}
	result, err := h.service.SignOpportunity(c.Request.Context(), actor, id, input)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, result)
}

// idempotencyKey 读取并裁剪幂等键；长度与字符校验交给领域服务，保持四条写链路同一错误码。
func idempotencyKey(c *gin.Context) string {
	return strings.TrimSpace(c.GetHeader("Idempotency-Key"))
}

// actorIdentityID 读取审计归属身份头。
// 该头不作为负责人依据（负责人只认 owner_identity_id 并经目录校验），
// 因此这里只做字面校验，不查会话或数据范围。
func actorIdentityID(c *gin.Context) (string, error) {
	value := strings.TrimSpace(c.GetHeader("X-Actor-Identity-ID"))
	if value == "" || len(value) > 64 || strings.ContainsAny(value, " \t\r\n") {
		return "", ErrActorIdentityRequired
	}
	return value, nil
}
