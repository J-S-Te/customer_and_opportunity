package credit

import (
	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/auth"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/response"
	"net/http"
	"strconv"
)

type Handler struct{ service *Service }

func NewHandler(s *Service) *Handler { return &Handler{service: s} }
func (h *Handler) Level(c *gin.Context) {
	id, e := strconv.ParseUint(c.Param("id"), 10, 64)
	if e != nil {
		c.Status(http.StatusBadRequest)
		return
	}
	r, e := h.service.GetLevel(c.Request.Context(), id)
	if e != nil {
		response.Error(c, e)
		return
	}
	response.OK(c, r)
}
func (h *Handler) Payment(c *gin.Context) {
	p, ok := auth.FromContext(c.Request.Context())
	if !ok || !p.HasPermission("customer.credit.payment.ingest") {
		response.Error(c, apperror.ErrForbidden)
		return
	}
	var in PaymentEvent
	if c.ShouldBindJSON(&in) != nil {
		c.Status(http.StatusBadRequest)
		return
	}
	r, e := h.service.ProcessPayment(c.Request.Context(), in)
	if e != nil {
		response.Error(c, e)
		return
	}
	response.OK(c, r)
}
func customerID(c *gin.Context) (uint64, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, apperror.New(400, "COMMON_INVALID_ARGUMENT", "invalid customer id"))
		return 0, false
	}
	return id, true
}
func applicationID(c *gin.Context) (uint64, bool) {
	id, err := strconv.ParseUint(c.Param("applicationID"), 10, 64)
	if err != nil {
		response.Error(c, apperror.New(400, "COMMON_INVALID_ARGUMENT", "invalid application id"))
		return 0, false
	}
	return id, true
}
func (h *Handler) History(c *gin.Context) {
	id, ok := customerID(c)
	if !ok {
		return
	}
	items, err := h.service.History(c.Request.Context(), id)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, gin.H{"items": items})
}
func (h *Handler) PaymentRecords(c *gin.Context) {
	id, ok := customerID(c)
	if !ok {
		return
	}
	items, err := h.service.Payments(c.Request.Context(), id)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, gin.H{"items": items})
}
func (h *Handler) Apply(c *gin.Context) {
	id, ok := customerID(c)
	if !ok {
		return
	}
	var in ApplyRequest
	if c.ShouldBindJSON(&in) != nil {
		response.Error(c, apperror.New(400, "CRM_CREDIT_APPLICATION_INVALID", "invalid credit application"))
		return
	}
	out, err := h.service.Apply(c.Request.Context(), id, in)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.Created(c, out)
}
func (h *Handler) Withdraw(c *gin.Context) {
	customer, ok := customerID(c)
	if !ok {
		return
	}
	app, ok := applicationID(c)
	if !ok {
		return
	}
	out, err := h.service.Withdraw(c.Request.Context(), customer, app)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, out)
}
func (h *Handler) Pending(c *gin.Context) {
	items, err := h.service.Pending(c.Request.Context())
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, gin.H{"items": items})
}
func (h *Handler) Approve(c *gin.Context) { h.decide(c, true) }
func (h *Handler) Reject(c *gin.Context)  { h.decide(c, false) }
func (h *Handler) decide(c *gin.Context, approve bool) {
	id, ok := applicationID(c)
	if !ok {
		return
	}
	var in DecisionRequest
	if c.ShouldBindJSON(&in) != nil {
		response.Error(c, apperror.New(400, "CRM_CREDIT_APPLICATION_INVALID", "invalid decision"))
		return
	}
	out, err := h.service.Decide(c.Request.Context(), id, approve, in)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, out)
}
