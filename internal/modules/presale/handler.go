package presale

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/apperror"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/requestbody"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/response"
)

type Handler struct {
	service   *Service
	alerts    *AlertService
	reports   *ReportService
	engineers *EngineerService
	actors    ActorResolver
}

func NewHandler(service *Service, alerts *AlertService, actors ActorResolver) *Handler {
	return &Handler{service: service, alerts: alerts, actors: actors}
}

// UseReports attaches the TS-009 query boundary without changing the existing
// TS handler constructor used by earlier modules.
func (h *Handler) UseReports(reports *ReportService) *Handler {
	h.reports = reports
	return h
}

// UseEngineers attaches the PMS-backed tenant personnel directory.
func (h *Handler) UseEngineers(engineers *EngineerService) *Handler {
	h.engineers = engineers
	return h
}

func (h *Handler) Engineers(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	page, pageSize, ok := bindPagination(c)
	if !ok {
		return
	}
	if h.engineers == nil {
		response.Error(c, apiError(ErrDependencyUnavailable))
		return
	}
	value, err := h.engineers.List(c.Request.Context(), actor, EngineerListQuery{
		Keyword: c.Query("keyword"), Department: c.Query("department"), Role: c.Query("role"),
		Skill: c.Query("skill"), Page: page, PageSize: pageSize,
	})
	if err != nil {
		response.Error(c, apiError(err))
		return
	}
	response.OK(c, value)
}

func (h *Handler) SyncEngineers(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	if h.engineers == nil {
		response.Error(c, apiError(ErrDependencyUnavailable))
		return
	}
	value, err := h.engineers.EnqueueSync(c.Request.Context(), actor, c.GetHeader("Idempotency-Key"))
	if err != nil {
		response.Error(c, apiError(err))
		return
	}
	c.JSON(http.StatusAccepted, response.Envelope{Code: "OK", Message: "accepted", RequestID: actor.RequestID, Data: value})
}

func (h *Handler) ReportSummary(c *gin.Context) {
	actor, query, ok := h.reportRequest(c)
	if !ok {
		return
	}
	value, err := h.reports.Summary(c.Request.Context(), actor, query)
	if err != nil {
		response.Error(c, apiError(err))
		return
	}
	response.OK(c, value)
}

func (h *Handler) ReportTrend(c *gin.Context) {
	actor, query, ok := h.reportRequest(c)
	if !ok {
		return
	}
	value, err := h.reports.Trend(c.Request.Context(), actor, query)
	if err != nil {
		response.Error(c, apiError(err))
		return
	}
	response.OK(c, value)
}

func (h *Handler) ReportDistribution(c *gin.Context) {
	actor, query, ok := h.reportRequest(c)
	if !ok {
		return
	}
	value, err := h.reports.Distribution(c.Request.Context(), actor, query, c.DefaultQuery("dimension", "PERSON"))
	if err != nil {
		response.Error(c, apiError(err))
		return
	}
	response.OK(c, value)
}

func (h *Handler) RequestReportExport(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	var input struct {
		From           string `json:"from" binding:"required"`
		To             string `json:"to" binding:"required"`
		OrganizationID string `json:"organization_id"`
		PersonID       string `json:"person_id"`
		OpportunityID  uint64 `json:"opportunity_id"`
	}
	if !bindJSON(c, &input) {
		return
	}
	query, valid := parseReportQuery(input.From, input.To, input.OrganizationID, input.PersonID, input.OpportunityID)
	if !valid {
		response.Error(c, apperror.New(http.StatusBadRequest, "CRM_PRESALE_REPORT_INVALID_FILTER", "invalid report filter"))
		return
	}
	if err := h.reports.RequestExport(actor, query); err != nil {
		response.Error(c, apiError(err))
		return
	}
	response.Error(c, apiError(ErrReportExportUnavailable))
}

func (h *Handler) reportRequest(c *gin.Context) (Actor, ReportQuery, bool) {
	actor, ok := h.actor(c)
	if !ok {
		return Actor{}, ReportQuery{}, false
	}
	var opportunityID uint64
	if raw := strings.TrimSpace(c.Query("opportunity_id")); raw != "" {
		value, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || value == 0 {
			response.Error(c, apperror.New(http.StatusBadRequest, "CRM_PRESALE_REPORT_INVALID_FILTER", "invalid report filter"))
			return Actor{}, ReportQuery{}, false
		}
		opportunityID = value
	}
	query, valid := parseReportQuery(c.Query("from"), c.Query("to"), c.Query("organization_id"), c.Query("person_id"), opportunityID)
	if !valid || h.reports == nil {
		response.Error(c, apperror.New(http.StatusBadRequest, "CRM_PRESALE_REPORT_INVALID_FILTER", "invalid report filter"))
		return Actor{}, ReportQuery{}, false
	}
	return actor, query, true
}

func parseReportQuery(from, to, organizationID, personID string, opportunityID uint64) (ReportQuery, bool) {
	fromValue, err := time.Parse(time.RFC3339, strings.TrimSpace(from))
	if err != nil {
		return ReportQuery{}, false
	}
	toValue, err := time.Parse(time.RFC3339, strings.TrimSpace(to))
	if err != nil {
		return ReportQuery{}, false
	}
	return ReportQuery{From: fromValue.UTC(), To: toValue.UTC(), OrganizationID: organizationID, PersonID: personID, OpportunityID: opportunityID}, true
}

func (h *Handler) AlertRules(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	values, err := h.alerts.ListRules(c.Request.Context(), actor)
	if err != nil {
		response.Error(c, apiError(err))
		return
	}
	response.OK(c, values)
}

func (h *Handler) UpdateAlertRule(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	alertType := AlertType(c.Param("type"))
	var input UpdateAlertRuleInput
	if !bindJSON(c, &input) {
		return
	}
	value, err := h.alerts.UpdateRule(c.Request.Context(), actor, alertType, input)
	if err != nil {
		response.Error(c, apiError(err))
		return
	}
	response.OK(c, value)
}

func (h *Handler) Alerts(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	page, pageSize, ok := bindPagination(c)
	if !ok {
		return
	}
	unreadOnly := false
	if raw := c.Query("unread_only"); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			response.Error(c, apperror.New(http.StatusBadRequest, "CRM_PRESALE_INVALID_FILTER", "invalid unread flag"))
			return
		}
		unreadOnly = value
	}
	values, err := h.alerts.ListAlerts(c.Request.Context(), actor, unreadOnly, page, pageSize)
	if err != nil {
		response.Error(c, apiError(err))
		return
	}
	response.OK(c, values)
}

func (h *Handler) ReadAlert(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	id, ok := bindID(c, "id")
	if !ok {
		return
	}
	if err := h.alerts.MarkRead(c.Request.Context(), actor, id); err != nil {
		response.Error(c, apiError(err))
		return
	}
	response.OK(c, gin.H{"read": true})
}

func (h *Handler) actor(c *gin.Context) (Actor, bool) {
	actor, err := h.actors.Resolve(c.Request.Context())
	if err != nil {
		response.Error(c, err)
		return Actor{}, false
	}
	return actor, true
}

func bindID(c *gin.Context, name string) (uint64, bool) {
	id, err := strconv.ParseUint(c.Param(name), 10, 64)
	if err != nil || id == 0 {
		response.Error(c, apperror.New(http.StatusBadRequest, "CRM_PRESALE_INVALID_ID", "invalid resource id"))
		return 0, false
	}
	return id, true
}

func bindJSON(c *gin.Context, value any) bool {
	if err := requestbody.DecodeJSON(c, value); err != nil {
		response.Error(c, apperror.New(http.StatusBadRequest, "COMMON_INVALID_ARGUMENT", "invalid request"))
		return false
	}
	return true
}

func (h *Handler) CreateRequest(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	var in CreateRequestInput
	if !bindJSON(c, &in) {
		return
	}
	value, err := h.service.CreateRequest(c.Request.Context(), actor, c.GetHeader("Idempotency-Key"), in)
	if err != nil {
		response.Error(c, apiError(err))
		return
	}
	response.Created(c, requestView(value))
}

func (h *Handler) ListRequests(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	if !onlyQueryKeys(c, requestListQueryKeys...) {
		response.Error(c, apperror.New(http.StatusBadRequest, "CRM_PRESALE_INVALID_FILTER", "invalid presale query filter"))
		return
	}
	page, pageSize, ok := bindPagination(c)
	if !ok {
		return
	}
	query, ok := bindRequestListQuery(c, page, pageSize)
	if !ok {
		return
	}
	value, err := h.service.ListRequests(c.Request.Context(), actor, query)
	if err != nil {
		response.Error(c, apiError(err))
		return
	}
	response.OK(c, value)
}

var requestFilterQueryKeys = []string{
	"request_no", "opportunity_id", "applicant_id", "assignee_id", "status", "venue", "urgency",
	"created_from", "created_to", "expected_from", "expected_to", "overdue", "push_status", "sort_by", "sort_order",
}

var requestListQueryKeys = append(append([]string{}, requestFilterQueryKeys...), "page", "page_size")

func (h *Handler) Board(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	allowed := append(append([]string{}, requestFilterQueryKeys...), "column_limit")
	if !onlyQueryKeys(c, allowed...) {
		response.Error(c, apperror.New(http.StatusBadRequest, "CRM_PRESALE_INVALID_FILTER", "invalid presale query filter"))
		return
	}
	columnLimit := boardDefaultColumnLimit
	if raw := strings.TrimSpace(c.Query("column_limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			response.Error(c, apiError(ErrInvalidFilter))
			return
		}
		columnLimit = value
	}
	query, ok := bindRequestListQuery(c, 1, columnLimit)
	if !ok {
		return
	}
	value, err := h.service.Board(c.Request.Context(), actor, query, columnLimit)
	if err != nil {
		response.Error(c, apiError(err))
		return
	}
	response.OK(c, value)
}

func (h *Handler) FilterOptions(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	if !onlyQueryKeys(c, requestFilterQueryKeys...) {
		response.Error(c, apperror.New(http.StatusBadRequest, "CRM_PRESALE_INVALID_FILTER", "invalid presale query filter"))
		return
	}
	query, ok := bindRequestListQuery(c, 1, 1)
	if !ok {
		return
	}
	value, err := h.service.FilterOptions(c.Request.Context(), actor, query)
	if err != nil {
		response.Error(c, apiError(err))
		return
	}
	response.OK(c, value)
}

func (h *Handler) RequestDetail(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	id, ok := bindID(c, "id")
	if !ok {
		return
	}
	value, err := h.service.RequestDetail(c.Request.Context(), actor, id)
	if err != nil {
		response.Error(c, apiError(err))
		return
	}
	response.OK(c, value)
}

// ContactPhone is a separate no-store read path so opening ordinary task
// detail never decrypts sensitive contact data in the browser or service.
func (h *Handler) ContactPhone(c *gin.Context) {
	c.Header("Cache-Control", "no-store, private")
	c.Header("Pragma", "no-cache")
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	if !onlyQueryKeys(c) {
		response.Error(c, apperror.New(http.StatusBadRequest, "COMMON_INVALID_ARGUMENT", "invalid request"))
		return
	}
	id, ok := bindID(c, "id")
	if !ok {
		return
	}
	value, err := h.service.ContactPhone(c.Request.Context(), actor, id)
	if err != nil {
		response.Error(c, apiError(err))
		return
	}
	response.OK(c, value)
}

func (h *Handler) Timeline(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	id, ok := bindID(c, "id")
	if !ok {
		return
	}
	if !onlyQueryKeys(c, "cursor", "limit") {
		response.Error(c, apperror.New(http.StatusBadRequest, "COMMON_INVALID_ARGUMENT", "invalid request"))
		return
	}
	limit := 20
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 100 {
			response.Error(c, apperror.New(http.StatusBadRequest, "COMMON_INVALID_PAGINATION", "invalid page size"))
			return
		}
		limit = value
	}
	value, err := h.service.Timeline(c.Request.Context(), actor, id, strings.TrimSpace(c.Query("cursor")), limit)
	if err != nil {
		response.Error(c, apiError(err))
		return
	}
	response.OK(c, value)
}

func (h *Handler) AvailableActions(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	id, ok := bindID(c, "id")
	if !ok {
		return
	}
	if !onlyQueryKeys(c) {
		response.Error(c, apperror.New(http.StatusBadRequest, "COMMON_INVALID_ARGUMENT", "invalid request"))
		return
	}
	value, err := h.service.AvailableActions(c.Request.Context(), actor, id)
	if err != nil {
		response.Error(c, apiError(err))
		return
	}
	response.OK(c, value)
}

func onlyQueryKeys(c *gin.Context, allowed ...string) bool {
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

func (h *Handler) Worklogs(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	id, ok := bindID(c, "id")
	if !ok {
		return
	}
	value, err := h.service.Worklogs(c.Request.Context(), actor, id)
	if err != nil {
		response.Error(c, apiError(err))
		return
	}
	response.OK(c, value)
}

func bindPagination(c *gin.Context) (int, int, bool) {
	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	if err != nil || page < 1 {
		response.Error(c, apperror.New(http.StatusBadRequest, "COMMON_INVALID_PAGINATION", "invalid page"))
		return 0, 0, false
	}
	pageSize, err := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if err != nil || pageSize < 1 || pageSize > 100 {
		response.Error(c, apperror.New(http.StatusBadRequest, "COMMON_INVALID_PAGINATION", "invalid page size"))
		return 0, 0, false
	}
	return page, pageSize, true
}

func bindRequestListQuery(c *gin.Context, page, pageSize int) (RequestListQuery, bool) {
	query := RequestListQuery{
		RequestNo: strings.TrimSpace(c.Query("request_no")), ApplicantID: strings.TrimSpace(c.Query("applicant_id")),
		AssigneeID: strings.TrimSpace(c.Query("assignee_id")), Status: RequestStatus(c.Query("status")),
		Venue: Venue(c.Query("venue")), Urgency: Urgency(c.Query("urgency")), PushStatus: PushStatus(c.Query("push_status")),
		Page: page, PageSize: pageSize, SortBy: c.Query("sort_by"), SortOrder: c.Query("sort_order"),
	}
	if raw := c.Query("opportunity_id"); raw != "" {
		value, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || value == 0 {
			response.Error(c, apperror.New(http.StatusBadRequest, "CRM_PRESALE_INVALID_FILTER", "invalid opportunity id"))
			return RequestListQuery{}, false
		}
		query.OpportunityID = value
	}
	var ok bool
	if query.CreatedFrom, ok = bindOptionalTime(c, "created_from"); !ok {
		return RequestListQuery{}, false
	}
	if query.CreatedTo, ok = bindOptionalTime(c, "created_to"); !ok {
		return RequestListQuery{}, false
	}
	if query.ExpectedFrom, ok = bindOptionalTime(c, "expected_from"); !ok {
		return RequestListQuery{}, false
	}
	if query.ExpectedTo, ok = bindOptionalTime(c, "expected_to"); !ok {
		return RequestListQuery{}, false
	}
	if raw := c.Query("overdue"); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			response.Error(c, apperror.New(http.StatusBadRequest, "CRM_PRESALE_INVALID_FILTER", "invalid overdue flag"))
			return RequestListQuery{}, false
		}
		query.Overdue = &value
	}
	return query, true
}

func bindOptionalTime(c *gin.Context, name string) (*time.Time, bool) {
	raw := c.Query(name)
	if raw == "" {
		return nil, true
	}
	value, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		response.Error(c, apperror.New(http.StatusBadRequest, "CRM_PRESALE_INVALID_FILTER", "invalid time filter"))
		return nil, false
	}
	return &value, true
}

func (h *Handler) ApprovalAction(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	id, ok := bindID(c, "id")
	if !ok {
		return
	}
	var in ApprovalActionInput
	if !bindJSON(c, &in) {
		return
	}
	if err := h.service.RequestApprovalAction(c.Request.Context(), actor, id, c.GetHeader("Idempotency-Key"), in); err != nil {
		response.Error(c, apiError(err))
		return
	}
	c.JSON(http.StatusAccepted, response.Envelope{Code: "OK", Message: "accepted", RequestID: actor.RequestID, Data: gin.H{"queued": true}})
}

func (h *Handler) ApprovalHistory(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	id, ok := bindID(c, "id")
	if !ok {
		return
	}
	values, err := h.service.ApprovalHistory(c.Request.Context(), actor, id)
	if err != nil {
		response.Error(c, apiError(err))
		return
	}
	response.OK(c, values)
}

func (h *Handler) ReplaceAssignments(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	id, ok := bindID(c, "id")
	if !ok {
		return
	}
	var in ReplaceAssignmentsInput
	if !bindJSON(c, &in) {
		return
	}
	value, err := h.service.ReplaceAssignments(c.Request.Context(), actor, id, c.GetHeader("Idempotency-Key"), in)
	if err != nil {
		response.Error(c, apiError(err))
		return
	}
	response.OK(c, requestView(value))
}

func (h *Handler) Assignments(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	id, ok := bindID(c, "id")
	if !ok {
		return
	}
	values, err := h.service.Assignments(c.Request.Context(), actor, id)
	if err != nil {
		response.Error(c, apiError(err))
		return
	}
	response.OK(c, values)
}

func (h *Handler) AddProgress(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	id, ok := bindID(c, "id")
	if !ok {
		return
	}
	var in AddProgressInput
	if !bindJSON(c, &in) {
		return
	}
	if _, err := h.service.AddProgress(c.Request.Context(), actor, id, c.GetHeader("Idempotency-Key"), in); err != nil {
		response.Error(c, apiError(err))
		return
	}
	response.Created(c, gin.H{"created": true})
}

func (h *Handler) Cancel(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	id, ok := bindID(c, "id")
	if !ok {
		return
	}
	var in CancelInput
	if !bindJSON(c, &in) {
		return
	}
	if err := h.service.Cancel(c.Request.Context(), actor, id, c.GetHeader("Idempotency-Key"), in); err != nil {
		response.Error(c, apiError(err))
		return
	}
	response.OK(c, gin.H{"cancelled": true})
}

func (h *Handler) AddWorklog(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	id, ok := bindID(c, "id")
	if !ok {
		return
	}
	var in AddWorklogInput
	if !bindJSON(c, &in) {
		return
	}
	value, err := h.service.AddWorklog(c.Request.Context(), actor, id, c.GetHeader("Idempotency-Key"), in)
	if err != nil {
		response.Error(c, apiError(err))
		return
	}
	response.Created(c, worklogView(value))
}

func (h *Handler) RetryDelivery(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	id, ok := bindID(c, "id")
	if !ok {
		return
	}
	if err := h.service.RetryDelivery(c.Request.Context(), actor, id); err != nil {
		response.Error(c, apiError(err))
		return
	}
	response.OK(c, gin.H{"queued": true})
}

func (h *Handler) Delivery(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	id, ok := bindID(c, "id")
	if !ok {
		return
	}
	value, err := h.service.Delivery(c.Request.Context(), actor, id)
	if err != nil {
		response.Error(c, apiError(err))
		return
	}
	response.OK(c, value)
}

// ApprovalCallback accepts only machine-authenticated calls. The callback's
// tenant must be supplied by verified machine claims, never by request JSON.
func (h *Handler) ApprovalCallback(c *gin.Context) {
	actor, ok := h.actor(c)
	if !ok {
		return
	}
	if !actor.Can("approval.callback.write") {
		response.Error(c, apiError(ErrForbidden))
		return
	}
	var in ApprovalCallbackInput
	if !bindJSON(c, &in) {
		return
	}
	if err := h.service.HandleApprovalCallback(c.Request.Context(), actor.TenantID, in); err != nil {
		response.Error(c, apiError(err))
		return
	}
	response.OK(c, gin.H{"accepted": true})
}
