package handover

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/middleware"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/requestbody"
)

// HandoverMachineScope 是平台为人员离职交接签发的专用机器 scope。
// 交接端点在机器身份认证之外还必须持有该 scope（BIZ-XAPI-01），
// 否则任何已登记的机器凭据都能驱动离职冻结/责任转移。
const HandoverMachineScope = "handover.machine"

// handoverWriteBodyLimit 限制交接写接口请求体。交接载荷是固定字段的小对象，
// 32 KiB 足够覆盖重试元数据，同时关闭无界读取；解码仍走全站统一严格语义。
const handoverWriteBodyLimit int64 = 32 << 10

// Handler 暴露人员离职交接的机器协议端点。租户边界来自机器身份，不接受请求体覆盖。
type Handler struct{ service *Service }

func NewHandler(service *Service) *Handler { return &Handler{service: service} }

// RegisterInternalRoutes 在已通过机器身份认证的内部路由组上注册五类操作。
// 每条路由额外要求 handover.machine scope；权限码来自签名 claims，不读请求头。
func RegisterInternalRoutes(group *gin.RouterGroup, handler *Handler) {
	if group == nil || handler == nil {
		return
	}
	requireMachineScope := middleware.RequirePermission(HandoverMachineScope)
	group.POST("/handover/freeze", requireMachineScope, handler.freeze)
	group.GET("/handover/responsibilities", requireMachineScope, handler.responsibilities)
	group.POST("/handover/transfers", requireMachineScope, handler.transfer)
	group.GET("/handover/transfers/:itemID", requireMachineScope, handler.receipt)
	group.POST("/handover/complete", requireMachineScope, handler.complete)
}

type freezePayload struct {
	TenantID  string `json:"tenant_id"`
	BatchID   string `json:"batch_id"`
	RequestID string `json:"request_id"`
	UserID    string `json:"user_id"`
}

type transferPayload struct {
	TenantID        string `json:"tenant_id"`
	BatchID         string `json:"batch_id"`
	ItemID          string `json:"item_id"`
	RequestID       string `json:"request_id"`
	ResourceType    string `json:"resource_type"`
	ResourceID      string `json:"resource_id"`
	FromUserID      string `json:"from_user_id"`
	ToUserID        string `json:"to_user_id"`
	ExpectedVersion uint64 `json:"expected_version"`
	IdempotencyKey  string `json:"idempotency_key"`
}

type completePayload struct {
	TenantID string `json:"tenant_id"`
	BatchID  string `json:"batch_id"`
	Outcome  string `json:"outcome"`
}

// tenantOf 只接受机器身份里的租户；请求体若填写必须一致。
func tenantOf(c *gin.Context, requested string) (string, bool) {
	principal, ok := auth.FromContext(c.Request.Context())
	if !ok || strings.TrimSpace(principal.TenantID) == "" {
		c.JSON(http.StatusForbidden, gin.H{"code": "TENANT_REQUIRED", "message": "交接请求缺少机器身份租户"})
		return "", false
	}
	tenant := strings.TrimSpace(principal.TenantID)
	if strings.TrimSpace(requested) != "" && strings.TrimSpace(requested) != tenant {
		c.JSON(http.StatusForbidden, gin.H{"code": "TENANT_MISMATCH", "message": "请求租户与机器身份租户不一致"})
		return "", false
	}
	return tenant, true
}

func (h *Handler) freeze(c *gin.Context) {
	var payload freezePayload
	if err := requestbody.DecodeJSONWithLimit(c, &payload, handoverWriteBodyLimit); err != nil {
		writeHandoverError(c, ErrValidation)
		return
	}
	tenant, ok := tenantOf(c, payload.TenantID)
	if !ok {
		return
	}
	freeze, count, err := h.service.Freeze(c.Request.Context(), tenant, payload.BatchID, payload.RequestID, payload.UserID)
	if err != nil {
		writeHandoverError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": "OK", "message": "冻结已确认", "data": gin.H{
		"system": "crm", "frozen": freeze.Status == FreezeFrozen, "frozen_at": freeze.FrozenAt, "responsibility_count": count,
	}})
}

func (h *Handler) responsibilities(c *gin.Context) {
	tenant, ok := tenantOf(c, c.Query("tenant_id"))
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	items, next, err := h.service.Responsibilities(c.Request.Context(), tenant, c.Query("user_id"), c.Query("cursor"), limit)
	if err != nil {
		writeHandoverError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": "OK", "message": "责任清单", "data": gin.H{
		"system": "crm", "items": items, "next_cursor": next,
	}})
}

func (h *Handler) transfer(c *gin.Context) {
	var payload transferPayload
	if err := requestbody.DecodeJSONWithLimit(c, &payload, handoverWriteBodyLimit); err != nil {
		writeHandoverError(c, ErrValidation)
		return
	}
	tenant, ok := tenantOf(c, payload.TenantID)
	if !ok {
		return
	}
	receipt, err := h.service.Transfer(c.Request.Context(), TransferInput{
		TenantID: tenant, BatchID: payload.BatchID, ItemID: payload.ItemID, RequestID: payload.RequestID,
		ResourceType: payload.ResourceType, ResourceID: payload.ResourceID,
		FromUserID: payload.FromUserID, ToUserID: payload.ToUserID,
		ExpectedVersion: payload.ExpectedVersion, IdempotencyKey: payload.IdempotencyKey,
	})
	if err != nil {
		writeHandoverError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": "OK", "message": "转移回执", "data": gin.H{
		"system": "crm", "item_id": receipt.ItemID, "idempotency_key": receipt.IdempotencyKey,
		"status": receipt.Status, "applied_version": receipt.AppliedVersion,
		"reason_code": receipt.ReasonCode, "evidence_ref": receipt.EvidenceRef, "received_at": receipt.ReceivedAt,
	}})
}

func (h *Handler) receipt(c *gin.Context) {
	tenant, ok := tenantOf(c, c.Query("tenant_id"))
	if !ok {
		return
	}
	receipt, err := h.service.Receipt(c.Request.Context(), tenant, c.Query("idempotency_key"))
	if err != nil {
		writeHandoverError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": "OK", "message": "转移回执", "data": gin.H{
		"system": "crm", "item_id": receipt.ItemID, "idempotency_key": receipt.IdempotencyKey,
		"status": receipt.Status, "applied_version": receipt.AppliedVersion,
		"reason_code": receipt.ReasonCode, "evidence_ref": receipt.EvidenceRef, "received_at": receipt.ReceivedAt,
	}})
}

func (h *Handler) complete(c *gin.Context) {
	var payload completePayload
	if err := requestbody.DecodeJSONWithLimit(c, &payload, handoverWriteBodyLimit); err != nil {
		writeHandoverError(c, ErrValidation)
		return
	}
	tenant, ok := tenantOf(c, payload.TenantID)
	if !ok {
		return
	}
	if err := h.service.Complete(c.Request.Context(), tenant, payload.BatchID, payload.Outcome); err != nil {
		writeHandoverError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"code": "OK", "message": "交接已结束", "data": gin.H{"system": "crm", "batch_id": payload.BatchID, "outcome": strings.ToUpper(payload.Outcome)}})
}

// writeHandoverError 把交接错误映射为确定性状态码，平台据此判断是否重试。
func writeHandoverError(c *gin.Context, err error) {
	switch {
	case err == nil:
		return
	case errors.Is(err, ErrValidation):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"code": "HANDOVER_VALIDATION", "message": "交接请求参数不合法"})
	case errors.Is(err, ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"code": "HANDOVER_NOT_FOUND", "message": "交接记录不存在"})
	case errors.Is(err, ErrFrozen):
		c.JSON(http.StatusConflict, gin.H{"code": "HANDOVER_SUCCESSOR_FROZEN", "message": "承接人处于交接冻结状态"})
	case errors.Is(err, ErrBatchNotFrozen):
		// 源负责人未被该批次冻结：不写回执、不改数据，由平台退避或人工受控恢复。
		c.JSON(http.StatusConflict, gin.H{"code": "HANDOVER_BATCH_NOT_FROZEN", "message": "源负责人不处于该批次交接冻结状态"})
	case errors.Is(err, ErrConflict):
		c.JSON(http.StatusConflict, gin.H{"code": "HANDOVER_CONFLICT", "message": "交接状态或版本冲突"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"code": "HANDOVER_INTERNAL", "message": "交接处理失败"})
	}
}
