package opportunity

import (
	"context"
	"io"
	"mime"
	"net/url"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/requestbody"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/response"
)

type Handler struct {
	service     *Service
	alerts      *StageAlertService
	attachments *AttachmentService
}

func (h *Handler) UseAttachments(service *AttachmentService) *Handler {
	h.attachments = service
	return h
}

func (h *Handler) AttachmentCapabilities(c *gin.Context) {
	if h.attachments == nil {
		response.Error(c, ErrAttachmentUnavailable)
		return
	}
	id, err := attachmentOpportunityID(c)
	if err != nil {
		response.Error(c, err)
		return
	}
	value, err := h.attachments.Capabilities(c.Request.Context(), id)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, value)
}

func (h *Handler) CreateAttachmentUpload(c *gin.Context) {
	id, err := attachmentOpportunityID(c)
	if err != nil {
		response.Error(c, err)
		return
	}
	var input AttachmentCreateRequest
	if err = requestbody.DecodeJSON(c, &input); err != nil {
		response.Error(c, invalidJSON())
		return
	}
	input.IdempotencyKey = c.GetHeader("Idempotency-Key")
	value, err := h.attachments.CreateUpload(c.Request.Context(), id, input)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.Created(c, value)
}

func (h *Handler) CompleteAttachmentUpload(c *gin.Context) {
	id, err := attachmentOpportunityID(c)
	if err != nil {
		response.Error(c, err)
		return
	}
	var input AttachmentCompleteRequest
	if err = requestbody.DecodeJSON(c, &input); err != nil {
		response.Error(c, invalidJSON())
		return
	}
	input.IdempotencyKey = c.GetHeader("Idempotency-Key")
	value, err := h.attachments.CompleteUpload(c.Request.Context(), id, c.Param("attachmentID"), input)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, value)
}

func (h *Handler) ListAttachments(c *gin.Context) {
	id, err := attachmentOpportunityID(c)
	if err != nil {
		response.Error(c, err)
		return
	}
	values, err := h.attachments.List(c.Request.Context(), id)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, values)
}

func (h *Handler) DownloadAttachment(c *gin.Context) {
	id, err := attachmentOpportunityID(c)
	if err != nil {
		response.Error(c, err)
		return
	}
	value, err := h.attachments.Download(c.Request.Context(), id, c.Param("attachmentID"))
	if err != nil {
		response.Error(c, err)
		return
	}
	defer value.Reader.Close()
	c.Header("Content-Type", value.Attachment.MIMEType)
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": value.Attachment.FileName}))
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Cache-Control", "private, no-store")
	c.Header("Content-Length", strconv.FormatUint(value.Attachment.SizeBytes, 10))
	written, copyErr := io.Copy(c.Writer, io.LimitReader(value.Reader, int64(value.Attachment.SizeBytes)))
	success := copyErr == nil && written == int64(value.Attachment.SizeBytes)
	done, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 3*time.Second)
	defer cancel()
	if auditErr := value.Complete(done, success); auditErr != nil && !c.Writer.Written() {
		response.Error(c, ErrAttachmentUnavailable)
		return
	}
}

func (h *Handler) ApplyAttachmentScan(c *gin.Context) {
	var input AttachmentScanEvent
	if err := requestbody.DecodeJSON(c, &input); err != nil {
		response.Error(c, invalidJSON())
		return
	}
	value, err := h.attachments.ApplyScan(c.Request.Context(), input)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, value)
}

func attachmentOpportunityID(c *gin.Context) (uint64, error) {
	if !validQueryKeys(c) {
		return 0, ErrInvalidQuery
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		return 0, apperror.New(400, "COMMON_INVALID_ARGUMENT", "invalid opportunity id")
	}
	return id, nil
}

func NewHandler(service *Service) *Handler { return &Handler{service: service} }

// UseStageAlerts attaches BM-002 rule and in-product notification APIs without
// changing the constructor used by existing opportunity tests and adapters.
func (h *Handler) UseStageAlerts(alerts *StageAlertService) *Handler {
	h.alerts = alerts
	return h
}

func (h *Handler) StageAlertRules(c *gin.Context) {
	if !validQueryKeys(c) {
		response.Error(c, ErrInvalidQuery)
		return
	}
	values, err := h.alerts.ListRules(c.Request.Context())
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, values)
}

func (h *Handler) UpdateStageAlertRule(c *gin.Context) {
	if !validQueryKeys(c) {
		response.Error(c, ErrInvalidQuery)
		return
	}
	var input UpdateStageAlertRuleRequest
	if err := requestbody.DecodeJSON(c, &input); err != nil {
		response.Error(c, invalidJSON())
		return
	}
	value, err := h.alerts.UpdateRule(c.Request.Context(), c.Param("stage"), input)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, value)
}

func (h *Handler) StageAlerts(c *gin.Context) {
	if !validQueryKeys(c, "page", "page_size", "unread_only") {
		response.Error(c, ErrInvalidQuery)
		return
	}
	page, pageSize, err := parsePage(c)
	if err != nil {
		response.Error(c, ErrInvalidQuery)
		return
	}
	unreadOnly := false
	if raw, present := c.GetQuery("unread_only"); present {
		if raw != "true" && raw != "false" {
			response.Error(c, ErrInvalidQuery)
			return
		}
		unreadOnly = raw == "true"
	}
	values, err := h.alerts.ListMine(c.Request.Context(), unreadOnly, page, pageSize)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, values)
}

func (h *Handler) ReadStageAlert(c *gin.Context) {
	if !validQueryKeys(c) {
		response.Error(c, ErrInvalidQuery)
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, apperror.New(400, "COMMON_INVALID_ARGUMENT", "invalid request"))
		return
	}
	if err = h.alerts.MarkRead(c.Request.Context(), id); err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, gin.H{"read": true})
}

func (h *Handler) Create(c *gin.Context) {
	if !validQueryKeys(c) {
		response.Error(c, ErrInvalidQuery)
		return
	}
	var input CreateRequest
	if err := requestbody.DecodeJSON(c, &input); err != nil {
		response.Error(c, invalidJSON())
		return
	}
	input.IdempotencyKey = c.GetHeader("Idempotency-Key")
	result, err := h.service.Create(c.Request.Context(), input)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.Created(c, result)
}

func (h *Handler) Get(c *gin.Context) {
	if !validQueryKeys(c) {
		response.Error(c, ErrInvalidQuery)
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, apperror.New(400, "COMMON_INVALID_ARGUMENT", "invalid request"))
		return
	}
	result, err := h.service.Get(c.Request.Context(), id)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) List(c *gin.Context) {
	if !validQueryKeys(c, "keyword", "stage", "status", "owner_id", "page", "page_size", "sort_by", "sort_order") {
		response.Error(c, ErrInvalidQuery)
		return
	}
	page, pageSize, err := parsePage(c)
	if err != nil {
		response.Error(c, ErrInvalidQuery)
		return
	}
	result, err := h.service.List(c.Request.Context(), ListQuery{Keyword: c.Query("keyword"), Stage: c.Query("stage"), Status: c.Query("status"), OwnerID: c.Query("owner_id"), Page: page, PageSize: pageSize, SortBy: c.Query("sort_by"), SortOrder: c.Query("sort_order")})
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) Update(c *gin.Context) {
	if !validQueryKeys(c) {
		response.Error(c, ErrInvalidQuery)
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, invalid(err))
		return
	}
	var input UpdateRequest
	if err = requestbody.DecodeJSON(c, &input); err != nil {
		response.Error(c, invalidJSON())
		return
	}
	result, err := h.service.Update(c.Request.Context(), id, input)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) ChangeOwner(c *gin.Context) {
	if !validQueryKeys(c) {
		response.Error(c, ErrInvalidQuery)
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, invalid(err))
		return
	}
	var input ChangeOwnerRequest
	if err = requestbody.DecodeJSON(c, &input); err != nil {
		response.Error(c, invalidJSON())
		return
	}
	input.IdempotencyKey = c.GetHeader("Idempotency-Key")
	result, err := h.service.ChangeOwner(c.Request.Context(), id, input)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) GetMembers(c *gin.Context) {
	if !validQueryKeys(c, "include_inactive") {
		response.Error(c, ErrInvalidQuery)
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, invalid(err))
		return
	}
	includeInactive := false
	if raw, present := c.GetQuery("include_inactive"); present {
		includeInactive, err = strconv.ParseBool(raw)
		if err != nil {
			response.Error(c, ErrInvalidQuery)
			return
		}
	}
	result, err := h.service.GetMembers(c.Request.Context(), id, includeInactive)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) ListMemberTerms(c *gin.Context) {
	if !validQueryKeys(c, "user_id", "active_only", "page", "page_size") {
		response.Error(c, ErrInvalidQuery)
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, invalid(err))
		return
	}
	page, pageSize, err := parsePage(c)
	if err != nil {
		response.Error(c, ErrInvalidQuery)
		return
	}
	activeOnly := false
	if raw, present := c.GetQuery("active_only"); present {
		if raw != "true" && raw != "false" {
			response.Error(c, ErrInvalidQuery)
			return
		}
		activeOnly = raw == "true"
	}
	result, err := h.service.ListMemberTerms(c.Request.Context(), id, MemberTermQuery{
		UserID: c.Query("user_id"), ActiveOnly: activeOnly, Page: page, PageSize: pageSize,
	})
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) ReplaceMembers(c *gin.Context) {
	if !validQueryKeys(c) {
		response.Error(c, ErrInvalidQuery)
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, invalid(err))
		return
	}
	var input ReplaceMembersRequest
	if err = requestbody.DecodeJSON(c, &input); err != nil {
		response.Error(c, invalidJSON())
		return
	}
	result, err := h.service.ReplaceMembers(c.Request.Context(), id, input)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) Void(c *gin.Context)    { h.changeLifecycle(c, true) }
func (h *Handler) Restore(c *gin.Context) { h.changeLifecycle(c, false) }

func (h *Handler) changeLifecycle(c *gin.Context, void bool) {
	if !validQueryKeys(c) {
		response.Error(c, ErrInvalidQuery)
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, invalid(err))
		return
	}
	var input LifecycleRequest
	if err = requestbody.DecodeJSON(c, &input); err != nil {
		response.Error(c, invalidJSON())
		return
	}
	var result *Response
	if void {
		result, err = h.service.Void(c.Request.Context(), id, input)
	} else {
		result, err = h.service.Restore(c.Request.Context(), id, input)
	}
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) ChangeStage(c *gin.Context) {
	if !validQueryKeys(c) {
		response.Error(c, ErrInvalidQuery)
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, invalid(err))
		return
	}
	var input StageChangeRequest
	if err = requestbody.DecodeJSON(c, &input); err != nil {
		response.Error(c, invalidJSON())
		return
	}
	result, err := h.service.ChangeStage(c.Request.Context(), id, input)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) ApplyExternalStatus(c *gin.Context) {
	if !validQueryKeys(c) {
		response.Error(c, ErrInvalidQuery)
		return
	}
	var input ExternalStatusRequest
	if err := requestbody.DecodeJSON(c, &input); err != nil {
		response.Error(c, invalidJSON())
		return
	}
	result, err := h.service.ApplyExternalStatus(c.Request.Context(), input)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) Board(c *gin.Context) {
	if !validQueryKeys(c, "keyword", "owner_id") {
		response.Error(c, ErrInvalidQuery)
		return
	}
	result, err := h.service.Board(c.Request.Context(), ListQuery{Keyword: c.Query("keyword"), OwnerID: c.Query("owner_id")})
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) StageHistory(c *gin.Context) {
	if !validQueryKeys(c, "page", "page_size") {
		response.Error(c, ErrInvalidQuery)
		return
	}
	id, page, pageSize, err := parseIDPage(c)
	if err != nil {
		response.Error(c, ErrInvalidQuery)
		return
	}
	result, err := h.service.StageHistory(c.Request.Context(), id, page, pageSize)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) CreateFollowup(c *gin.Context) {
	if !validQueryKeys(c) {
		response.Error(c, ErrInvalidQuery)
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, invalid(err))
		return
	}
	var input FollowupCreateRequest
	if err = requestbody.DecodeJSON(c, &input); err != nil {
		response.Error(c, invalidJSON())
		return
	}
	result, err := h.service.CreateFollowup(c.Request.Context(), id, input)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.Created(c, result)
}

func (h *Handler) ListFollowups(c *gin.Context) {
	if !validQueryKeys(c, "page", "page_size") {
		response.Error(c, ErrInvalidQuery)
		return
	}
	id, page, pageSize, err := parseIDPage(c)
	if err != nil {
		response.Error(c, ErrInvalidQuery)
		return
	}
	result, err := h.service.ListFollowups(c.Request.Context(), id, page, pageSize)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) CompleteTerminalTodo(c *gin.Context) {
	if !validQueryKeys(c) {
		response.Error(c, ErrInvalidQuery)
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		response.Error(c, invalid(err))
		return
	}
	var input TerminalTodoRequest
	if err = requestbody.DecodeJSON(c, &input); err != nil {
		response.Error(c, invalidJSON())
		return
	}
	result, err := h.service.CompleteTerminalTodo(c.Request.Context(), id, input)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) ExternalStatus(c *gin.Context) {
	if !validQueryKeys(c) {
		response.Error(c, ErrInvalidQuery)
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, apperror.New(400, "COMMON_INVALID_ARGUMENT", "invalid request"))
		return
	}
	result, err := h.service.ExternalStatus(c.Request.Context(), id)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) LaunchQuotation(c *gin.Context) { h.launchExternal(c, "报价") }

func (h *Handler) LaunchBid(c *gin.Context) { h.launchExternal(c, "投标") }

func (h *Handler) launchExternal(c *gin.Context, launchType string) {
	if !validQueryKeys(c) {
		response.Error(c, ErrInvalidQuery)
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, apperror.New(400, "COMMON_INVALID_ARGUMENT", "invalid request"))
		return
	}
	result, err := h.service.CreateExternalLaunchContext(c.Request.Context(), id, launchType)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) ContractTransfer(c *gin.Context) {
	if !validQueryKeys(c) {
		response.Error(c, ErrInvalidQuery)
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, apperror.New(400, "COMMON_INVALID_ARGUMENT", "invalid request"))
		return
	}
	var input ContractTransferRequest
	if err = requestbody.DecodeJSON(c, &input); err != nil {
		response.Error(c, invalidJSON())
		return
	}
	input.IdempotencyKey = c.GetHeader("Idempotency-Key")
	result, err := h.service.ContractTransfer(c.Request.Context(), id, input)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, result)
}

func parseIDPage(c *gin.Context) (uint64, int, int, error) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		return 0, 0, 0, ErrInvalidQuery
	}
	page, pageSize, err := parsePage(c)
	return id, page, pageSize, err
}

const maxQueryPage = 1_000_000

func parsePage(c *gin.Context) (int, int, error) {
	page, err := parsePositiveQueryInt(c, "page", 1, maxQueryPage)
	if err != nil {
		return 0, 0, err
	}
	pageSize, err := parsePositiveQueryInt(c, "page_size", 20, 100)
	if err != nil {
		return 0, 0, err
	}
	return page, pageSize, nil
}

func parsePositiveQueryInt(c *gin.Context, key string, defaultValue, maximum int) (int, error) {
	raw, present := c.GetQuery(key)
	if !present {
		return defaultValue, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > maximum {
		return 0, ErrInvalidQuery
	}
	return value, nil
}

// validQueryKeys fails closed for both unknown keys and repeated keys. Query
// values are intentionally parsed by each handler because the allowed DTO is
// different for every read endpoint.
func validQueryKeys(c *gin.Context, allowed ...string) bool {
	values, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		return false
	}
	allow := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allow[key] = struct{}{}
	}
	for key, entries := range values {
		if _, ok := allow[key]; !ok || len(entries) != 1 {
			return false
		}
	}
	return true
}

func invalid(err error) error {
	return apperror.WithDetails(apperror.New(400, "COMMON_INVALID_ARGUMENT", "invalid request"), err.Error())
}

func invalidJSON() error {
	return apperror.New(400, "COMMON_INVALID_ARGUMENT", "invalid request")
}
