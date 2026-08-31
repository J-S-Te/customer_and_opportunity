package presale

import (
	"context"
	"encoding/json"
	"errors"
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
	scope.All = actor.HasRole("sales_director") || actor.HasRole("technical_director") || actor.HasRole("team_lead") ||
		actor.HasRole("crm_super_admin") || actor.HasRole("auditor")
	if scope.All {
		return scope
	}
	// 非管理角色的可见集合是“本人申请”与“本人被指派”的并集。
	// 执行人使用基础平台 user_id，不依赖外部人员系统的身份映射。
	if actor.HasRole("sales") {
		scope.ApplicantID = actor.UserID
	}
	if actor.PersonID != "" {
		scope.AssigneeID = actor.PersonID
	}
	return scope
}

// 列表先应用服务端授权范围，再叠加调用者筛选条件；筛选只能收窄，不能扩大数据范围。
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
	if err = s.enrichApplicantNames(ctx, page.Items); err != nil {
		return RequestListPage{}, err
	}
	for index := range page.Items {
		page.Items[index].AvailableActions = localAvailableActions(actor, page.Items[index].Status, page.Items[index].ApplicantID, page.Items[index].CurrentAssignees)
	}
	return page, nil
}

// 看板的每个状态泳道复用列表的授权和筛选边界，并限制单泳道条数，
// 不接受客户端范围参数，也不会为看板一次性加载全部任务。
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
		if listErr = s.enrichApplicantNames(ctx, page.Items); listErr != nil {
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

// 筛选选项只从调用者当前可见的结果关系中提取，并设定上限；
// 不可见申请中的人员、商机或推送状态不会通过下拉选项泄露。
func (s *Service) FilterOptions(ctx context.Context, actor Actor, query RequestListQuery) (RequestFilterOptions, error) {
	if !actor.Can("presale.read") {
		return RequestFilterOptions{}, ErrForbidden
	}
	query.Page, query.PageSize = 1, 1
	query, err := prepareRequestListQuery(query)
	if err != nil {
		return RequestFilterOptions{}, err
	}
	options, err := s.repo.ListRequestFilterOptions(ctx, requestScope(actor), query, s.clock.Now(), filterOptionLimit)
	if err != nil {
		return RequestFilterOptions{}, err
	}
	missing := make([]string, 0, len(options.Applicants))
	for _, option := range options.Applicants {
		if strings.TrimSpace(option.Label) == "" {
			missing = append(missing, option.Value)
		}
	}
	if len(missing) > 0 {
		names, resolveErr := resolveOwnerDisplayNames(ctx, s.ownerDirectory, missing)
		if resolveErr != nil {
			return RequestFilterOptions{}, resolveErr
		}
		for index := range options.Applicants {
			if strings.TrimSpace(options.Applicants[index].Label) == "" {
				options.Applicants[index].Label = names[options.Applicants[index].Value]
			}
		}
	}
	return options, nil
}

func (s *Service) enrichApplicantNames(ctx context.Context, items []RequestListItem) error {
	missing := make([]string, 0)
	for _, item := range items {
		if strings.TrimSpace(item.ApplicantName) == "" {
			missing = append(missing, item.ApplicantID)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	names, err := resolveOwnerDisplayNames(ctx, s.ownerDirectory, missing)
	if err != nil {
		return err
	}
	for index := range items {
		if strings.TrimSpace(items[index].ApplicantName) == "" {
			items[index].ApplicantName = names[items[index].ApplicantID]
		}
	}
	return nil
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

// 详情在读取任职、工时和预警等子投影前先校验父申请的数据范围，
// 防止通过猜测租户内 ID 绕过资源级授权。
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
	if strings.TrimSpace(requestValue.ApplicantNameSnapshot) == "" {
		names, resolveErr := resolveOwnerDisplayNames(ctx, s.ownerDirectory, []string{requestValue.ApplicantID})
		if resolveErr != nil {
			return RequestDetailView{}, resolveErr
		}
		requestValue.ApplicantNameSnapshot = names[requestValue.ApplicantID]
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
	instance, err := s.repo.FindApprovalInstance(ctx, actor.TenantID, id)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return RequestDetailView{}, err
	}
	view := requestView(requestValue)
	view.AssignmentAction, view.AssignmentRoleCode = assignmentActionForInstance(instance, actor)
	availableActions := localAvailableActions(actor, requestValue.Status, requestValue.ApplicantID, assignees)
	// Assignment authority is defined by the approval-rule snapshot.  Keep the
	// legacy list/board action calculation for requests without a snapshot, but
	// expose the configured person-assignment action to the authoritative detail
	// endpoint as well.  Otherwise a technical_director (or another configured
	// role) sees the picker but the frontend rejects the mutation locally because
	// the action list does not contain ASSIGN.
	if requestValue.Status == StatusApprovedPendingAssignment || requestValue.Status == StatusExecuting {
		if actor.Can("presale.assign") {
			if action, _ := assignmentActionForInstance(instance, actor); action == ApprovalNodePerson {
				availableActions = appendUniqueAction(availableActions, "ASSIGN")
			}
		}
	}
	return RequestDetailView{
		Request: view, CurrentAssignees: assignees,
		TotalWorkHours: aggregate.TotalWorkHours, PushExceptionCount: aggregate.PushExceptionCount,
		Overdue:    isOverdue(requestValue.Status, requestValue.ExpectedEnd, s.clock.Now()),
		AlertLevel: alertAggregate.Level, AlertDueAt: alertAggregate.DueAt, AlertBasisAt: alertAggregate.BasisAt,
		AvailableActions:    availableActions,
		CanViewContactPhone: canViewContactPhone,
	}, nil
}

func appendUniqueAction(actions []string, action string) []string {
	for _, existing := range actions {
		if existing == action {
			return actions
		}
	}
	return append(actions, action)
}

func assignmentActionForInstance(instance *ApprovalInstance, actor Actor) (ApprovalNodeType, string) {
	if instance == nil || len(instance.NodesJSON) == 0 {
		return "", ""
	}
	var nodes []ApprovalNode
	if json.Unmarshal(instance.NodesJSON, &nodes) != nil {
		return "", ""
	}
	for roleCode := range actor.Roles {
		if action, ok := AssignmentActionForRole(nodes, roleCode); ok {
			return action, strings.TrimSpace(roleCode)
		}
	}
	for _, node := range nodes {
		if node.Type == ApprovalNodeDepartment || node.Type == ApprovalNodePerson {
			return node.Type, strings.TrimSpace(node.RoleCode)
		}
	}
	return "", ""
}

// 工时列表先校验父申请，再返回显式 DTO，避免子资源接口成为绕过申请范围的入口。
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

// 商机路由由上层先校验商机访问权，此处返回不含敏感字段的售前摘要；
// 每条摘要仍单独计算能否进入详情，历史执行人只凭真实分派关系获得该能力。
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
	manager := actor.HasRole("sales_director") || actor.HasRole("technical_director") || actor.HasRole("team_lead") || actor.HasRole("crm_super_admin") || actor.HasRole("auditor")
	sales := actor.HasRole("sales")
	items := make([]OpportunityPresaleItem, 0, len(requests.Items))
	for _, item := range requests.Items {
		canViewDetail := actor.Can("presale.read") && (manager || sales && item.ApplicantID == actor.UserID || historical[item.ID])
		items = append(items, OpportunityPresaleItem{
			ID: item.ID, RequestNo: item.RequestNo, OpportunityName: item.OpportunityName, CreatedAt: item.CreatedAt,
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
		actor.Can("presale.assign") && (actor.HasRole("technical_director") || actor.HasRole("team_lead") || actor.HasRole("crm_super_admin")) {
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
		if actor.Can("presale.complete") && (actor.HasRole("technician") || actor.HasRole("project_manager") || actor.HasRole("team_lead")) {
			actions = append(actions, "COMPLETE")
		}
	}
	if status == StatusPendingApproval && applicantID == actor.UserID {
		actions = append(actions, "CANCEL")
	} else if (status == StatusApprovedPendingAssignment || status == StatusExecuting) &&
		(actor.HasRole("team_lead") || actor.HasRole("crm_super_admin") || actor.Can("presale.cancel")) {
		actions = append(actions, "CANCEL")
	}
	return actions
}
