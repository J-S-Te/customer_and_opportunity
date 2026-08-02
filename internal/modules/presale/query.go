package presale

import (
	"context"
	"strings"
	"time"
)

const (
	boardDefaultColumnLimit = 20
	boardMaxColumnLimit     = 50
	filterOptionLimit       = 100
)

var requestStatuses = []RequestStatus{
	StatusApprovalStarting,
	StatusPendingApproval,
	StatusApprovedPendingAssignment,
	StatusExecuting,
	StatusCompleted,
	StatusRejected,
	StatusCancelled,
}

func requestScope(actor Actor) RequestQueryScope {
	scope := RequestQueryScope{TenantID: actor.TenantID}
	scope.All = actor.HasRole("sales_director") || actor.HasRole("team_lead") ||
		actor.HasRole("technical_lead") || actor.HasRole("auditor")
	if scope.All {
		return scope
	}
	// Non-manager views are a union of the authenticated user's application
	// ownership and authoritative PMS person assignment. Assignment roles are
	// PMS business roles, so they must not be guessed from CRM OIDC role names.
	if actor.HasRole("sales") {
		scope.ApplicantID = actor.UserID
	}
	if actor.PersonID != "" {
		scope.AssigneeID = actor.PersonID
	}
	return scope
}

// ListRequests applies the TS-007 role scope before any caller-supplied
// filters. A filter can narrow this scope but can never expand it.
func (s *Service) ListRequests(ctx context.Context, actor Actor, query RequestListQuery) (RequestListPage, error) {
	if !actor.Can("presale.read") {
		return RequestListPage{}, ErrForbidden
	}
	query, err := prepareRequestListQuery(query)
	if err != nil {
		return RequestListPage{}, err
	}
	page, err := s.repo.ListRequests(ctx, requestScope(actor), query, s.clock.Now())
	if err != nil {
		return RequestListPage{}, err
	}
	for index := range page.Items {
		page.Items[index].AvailableActions = localAvailableActions(actor, page.Items[index].Status, page.Items[index].ApplicantID, page.Items[index].CurrentAssignees)
	}
	return page, nil
}

// Board uses the exact ListRequests query boundary for each finite status lane.
// It is deliberately bounded per lane and never accepts a caller-provided scope.
func (s *Service) Board(ctx context.Context, actor Actor, query RequestListQuery, columnLimit int) (RequestBoardView, error) {
	if !actor.Can("presale.read") {
		return RequestBoardView{}, ErrForbidden
	}
	if columnLimit == 0 {
		columnLimit = boardDefaultColumnLimit
	}
	if columnLimit < 1 || columnLimit > boardMaxColumnLimit {
		return RequestBoardView{}, ErrInvalidFilter
	}
	query.Page, query.PageSize = 1, columnLimit
	query, err := prepareRequestListQuery(query)
	if err != nil {
		return RequestBoardView{}, err
	}
	selectedStatus := query.Status
	columns := make([]RequestBoardColumn, 0, len(requestStatuses))
	for _, status := range requestStatuses {
		column := RequestBoardColumn{Status: status, Items: []RequestListItem{}}
		if selectedStatus != "" && selectedStatus != status {
			columns = append(columns, column)
			continue
		}
		statusQuery := query
		statusQuery.Status = status
		page, listErr := s.repo.ListRequests(ctx, requestScope(actor), statusQuery, s.clock.Now())
		if listErr != nil {
			return RequestBoardView{}, listErr
		}
		for index := range page.Items {
			page.Items[index].AvailableActions = localAvailableActions(actor, page.Items[index].Status, page.Items[index].ApplicantID, page.Items[index].CurrentAssignees)
		}
		column.Items, column.Total = page.Items, page.Total
		columns = append(columns, column)
	}
	return RequestBoardView{Columns: columns, ColumnLimit: columnLimit}, nil
}

// FilterOptions derives bounded options only from locally authoritative rows
// that survive the caller's server-side scope and the shared request filters.
func (s *Service) FilterOptions(ctx context.Context, actor Actor, query RequestListQuery) (RequestFilterOptions, error) {
	if !actor.Can("presale.read") {
		return RequestFilterOptions{}, ErrForbidden
	}
	query.Page, query.PageSize = 1, 1
	query, err := prepareRequestListQuery(query)
	if err != nil {
		return RequestFilterOptions{}, err
	}
	return s.repo.ListRequestFilterOptions(ctx, requestScope(actor), query, s.clock.Now(), filterOptionLimit)
}

func prepareRequestListQuery(query RequestListQuery) (RequestListQuery, error) {
	query.RequestNo = strings.TrimSpace(query.RequestNo)
	query.ApplicantID = strings.TrimSpace(query.ApplicantID)
	query.AssigneeID = strings.TrimSpace(query.AssigneeID)
	query.SortBy = strings.ToLower(strings.TrimSpace(query.SortBy))
	query.SortOrder = strings.ToLower(strings.TrimSpace(query.SortOrder))
	if len(query.RequestNo) > 32 || len(query.ApplicantID) > 64 || len(query.AssigneeID) > 64 {
		return RequestListQuery{}, ErrInvalidFilter
	}
	if query.Status != "" && !containsRequestStatus(query.Status) {
		return RequestListQuery{}, ErrInvalidFilter
	}
	if query.Venue != "" && query.Venue != VenueOnsite && query.Venue != VenueRemote {
		return RequestListQuery{}, ErrInvalidFilter
	}
	if query.Urgency != "" && query.Urgency != UrgencyNormal && query.Urgency != UrgencyUrgent {
		return RequestListQuery{}, ErrInvalidFilter
	}
	switch query.PushStatus {
	case "", PushPending, PushSending, PushSuccess, PushRetryWait, PushDeadLetter:
	default:
		return RequestListQuery{}, ErrInvalidFilter
	}
	if query.SortBy != "" && query.SortBy != "created_at" && query.SortBy != "updated_at" && query.SortBy != "expected_end" && query.SortBy != "request_no" {
		return RequestListQuery{}, ErrInvalidFilter
	}
	if query.SortOrder != "" && query.SortOrder != "asc" && query.SortOrder != "desc" {
		return RequestListQuery{}, ErrInvalidFilter
	}
	if invalidHalfOpenRange(query.CreatedFrom, query.CreatedTo) || invalidHalfOpenRange(query.ExpectedFrom, query.ExpectedTo) {
		return RequestListQuery{}, ErrInvalidFilter
	}
	if query.Page < 1 || query.PageSize < 1 || query.PageSize > 100 {
		return RequestListQuery{}, ErrInvalidFilter
	}
	return query, nil
}

func containsRequestStatus(status RequestStatus) bool {
	for _, value := range requestStatuses {
		if status == value {
			return true
		}
	}
	return false
}

func invalidHalfOpenRange(from, to *time.Time) bool {
	return from != nil && to != nil && !from.Before(*to)
}

// RequestDetail performs the resource-level scope check before loading child
// projections, preventing tenant-local IDOR on guessed request identifiers.
func (s *Service) RequestDetail(ctx context.Context, actor Actor, id uint64) (RequestDetailView, error) {
	if !actor.Can("presale.read") {
		return RequestDetailView{}, ErrForbidden
	}
	requestValue, err := s.repo.FindRequest(ctx, actor.TenantID, id)
	if err != nil {
		return RequestDetailView{}, err
	}
	if err = s.requireReadable(ctx, actor, requestValue); err != nil {
		return RequestDetailView{}, err
	}
	assignmentMap, err := s.repo.ListCurrentAssignments(ctx, actor.TenantID, []uint64{id})
	if err != nil {
		return RequestDetailView{}, err
	}
	aggregate, err := s.repo.RequestAggregate(ctx, actor.TenantID, id)
	if err != nil {
		return RequestDetailView{}, err
	}
	alertAggregate, err := s.repo.AlertAggregate(ctx, actor.TenantID, id)
	if err != nil {
		return RequestDetailView{}, err
	}
	if requestValue.Status == StatusCompleted || requestValue.Status == StatusRejected || requestValue.Status == StatusCancelled {
		alertAggregate = AlertAggregate{Level: "NONE"}
	}
	assignees := assigneeSummaries(assignmentMap[id])
	canViewContactPhone, err := s.canViewContactPhone(ctx, actor, requestValue)
	if err != nil {
		return RequestDetailView{}, err
	}
	return RequestDetailView{
		Request: requestView(requestValue), CurrentAssignees: assignees,
		TotalWorkHours: aggregate.TotalWorkHours, PushExceptionCount: aggregate.PushExceptionCount,
		Overdue:    isOverdue(requestValue.Status, requestValue.ExpectedEnd, s.clock.Now()),
		AlertLevel: alertAggregate.Level, AlertDueAt: alertAggregate.DueAt, AlertBasisAt: alertAggregate.BasisAt,
		AvailableActions:    localAvailableActions(actor, requestValue.Status, requestValue.ApplicantID, assignees),
		CanViewContactPhone: canViewContactPhone,
	}, nil
}

// Worklogs returns only explicit public DTOs and checks the parent request's
// resource scope before querying its children.
func (s *Service) Worklogs(ctx context.Context, actor Actor, requestID uint64) ([]WorklogView, error) {
	if !actor.Can("presale.read") {
		return nil, ErrForbidden
	}
	requestValue, err := s.repo.FindRequest(ctx, actor.TenantID, requestID)
	if err != nil {
		return nil, err
	}
	if err = s.requireReadable(ctx, actor, requestValue); err != nil {
		return nil, err
	}
	values, err := s.repo.ListWorklogs(ctx, actor.TenantID, requestID)
	if err != nil {
		return nil, err
	}
	views := make([]WorklogView, 0, len(values))
	for index := range values {
		views = append(views, worklogView(&values[index]))
	}
	return views, nil
}

// ListForOpportunity is the public TS-010 query boundary for bootstrap's
// opportunity route adapter. Bootstrap must first validate opportunity scope;
// this method then applies the independent presale scope.
func (s *Service) ListForOpportunity(ctx context.Context, actor Actor, opportunityID uint64, page, pageSize int) (OpportunityPresalePage, error) {
	requests, err := s.repo.ListOpportunityRequests(ctx, actor.TenantID, opportunityID, page, pageSize, s.clock.Now())
	if err != nil {
		return OpportunityPresalePage{}, err
	}
	ids := make([]uint64, 0, len(requests.Items))
	for _, item := range requests.Items {
		ids = append(ids, item.ID)
	}
	latest, err := s.repo.LatestProgressByRequest(ctx, actor.TenantID, ids)
	if err != nil {
		return OpportunityPresalePage{}, err
	}
	historical, err := s.repo.HistoricalAssignmentRequestIDs(ctx, actor.TenantID, actor.PersonID, ids)
	if err != nil {
		return OpportunityPresalePage{}, err
	}
	manager := actor.HasRole("sales_director") || actor.HasRole("team_lead") || actor.HasRole("technical_lead") || actor.HasRole("auditor")
	sales := actor.HasRole("sales")
	items := make([]OpportunityPresaleItem, 0, len(requests.Items))
	for _, item := range requests.Items {
		canViewDetail := actor.Can("presale.read") && (manager || sales && item.ApplicantID == actor.UserID || historical[item.ID])
		items = append(items, OpportunityPresaleItem{
			ID: item.ID, RequestNo: item.RequestNo, CreatedAt: item.CreatedAt,
			Status: item.Status, Urgency: item.Urgency, Venue: item.Venue,
			CurrentAssignees: item.CurrentAssignees, LatestProgress: truncate(strings.TrimSpace(latest[item.ID]), 200),
			TotalWorkHours: item.TotalWorkHours, ExpectedEnd: item.ExpectedEnd,
			Overdue: item.Overdue, CanViewDetail: canViewDetail,
		})
	}
	return OpportunityPresalePage{Items: items, Page: requests.Page, PageSize: requests.PageSize, Total: requests.Total}, nil
}

func assigneeSummaries(values []Assignment) []AssigneeSummaryView {
	result := make([]AssigneeSummaryView, 0, len(values))
	for _, value := range values {
		if !value.IsCurrent {
			continue
		}
		result = append(result, AssigneeSummaryView{
			PersonID: value.AssigneeID, PersonName: value.AssigneeNameSnapshot, Role: value.AssigneeRole,
		})
	}
	return result
}

func isOverdue(status RequestStatus, expectedEnd, now time.Time) bool {
	if status == StatusCompleted || status == StatusRejected || status == StatusCancelled {
		return false
	}
	return expectedEnd.Before(now)
}

func localAvailableActions(actor Actor, status RequestStatus, applicantID string, current []AssigneeSummaryView) []string {
	actions := make([]string, 0, 4)
	if (status == StatusApprovedPendingAssignment || status == StatusExecuting) &&
		actor.Can("presale.assign") && actor.HasRole("team_lead") {
		actions = append(actions, "ASSIGN")
	}
	currentAssignee := false
	for _, assignee := range current {
		if assignee.PersonID == actor.PersonID {
			currentAssignee = true
			break
		}
	}
	if status == StatusExecuting && currentAssignee {
		if actor.Can("presale.progress") {
			actions = append(actions, "ADD_PROGRESS")
		}
		if actor.Can("presale.worklog") {
			actions = append(actions, "ADD_WORKLOG")
		}
	}
	if status == StatusPendingApproval && applicantID == actor.UserID {
		actions = append(actions, "CANCEL")
	} else if (status == StatusApprovedPendingAssignment || status == StatusExecuting) &&
		(actor.HasRole("team_lead") || actor.Can("presale.cancel")) {
		actions = append(actions, "CANCEL")
	}
	return actions
}
