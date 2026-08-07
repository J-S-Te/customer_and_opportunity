package presale

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Repository interface {
	WithTransaction(context.Context, func(context.Context) error) error
	NextRequestNo(context.Context, string, time.Time) (string, error)
	NextWorklogNo(context.Context, string, time.Time) (string, error)
	CreateRequest(context.Context, *PresaleRequest) error
	FindRequest(context.Context, string, uint64) (*PresaleRequest, error)
	FindRequestForUpdate(context.Context, string, uint64) (*PresaleRequest, error)
	FindRequestByCreateKey(context.Context, string, string) (*PresaleRequest, error)
	ListRequests(context.Context, RequestQueryScope, RequestListQuery, time.Time) (RequestListPage, error)
	ListRequestFilterOptions(context.Context, RequestQueryScope, RequestListQuery, time.Time, int) (RequestFilterOptions, error)
	ListOpportunityRequests(context.Context, string, uint64, int, int, time.Time) (RequestListPage, error)
	HistoricalAssignmentRequestIDs(context.Context, string, string, []uint64) (map[uint64]bool, error)
	RequestAggregate(context.Context, string, uint64) (RequestAggregate, error)
	AlertAggregate(context.Context, string, uint64) (AlertAggregate, error)
	ListCurrentAssignments(context.Context, string, []uint64) (map[uint64][]Assignment, error)
	LatestProgressByRequest(context.Context, string, []uint64) (map[uint64]string, error)
	UpdateRequestVersioned(context.Context, *PresaleRequest, uint64, map[string]any) error
	CreateApprovalInstance(context.Context, *ApprovalInstance) error
	FindApprovalInstance(context.Context, string, uint64) (*ApprovalInstance, error)
	FindApprovalInstanceForUpdate(context.Context, string, uint64) (*ApprovalInstance, error)
	UpdateApprovalInstance(context.Context, *ApprovalInstance, map[string]any) error
	CreateApprovalLog(context.Context, *ApprovalLog) error
	ListApprovalLogs(context.Context, string, uint64) ([]ApprovalLog, error)
	FindEngineTaskLog(context.Context, string, string) (*ApprovalLog, error)
	FindEngineers(context.Context, string, []string) ([]Engineer, error)
	FindEngineersForUpdate(context.Context, string, []string) ([]Engineer, error)
	ListCurrentAssignmentsForUpdate(context.Context, string, uint64) ([]Assignment, error)
	ListAssignments(context.Context, string, uint64) ([]Assignment, error)
	CreateAssignment(context.Context, *Assignment) error
	EndAssignment(context.Context, string, uint64, uint64, string, time.Time) error
	CreateAssignmentEvent(context.Context, *AssignmentEvent) error
	CreateProgress(context.Context, *ProgressLog) error
	CreateProgressNotificationEvent(context.Context, *ProgressNotificationEvent) error
	FindProgressByKey(context.Context, string, string) (*ProgressLog, error)
	FindProgressByKeyForUpdate(context.Context, string, string) (*ProgressLog, error)
	CreateStatusLog(context.Context, *StatusLog) error
	FindMutationReplay(context.Context, string, uint64, string, string) (*MutationReplay, error)
	CreateMutationReplay(context.Context, *MutationReplay) error
	CreateWorklog(context.Context, *Worklog) error
	FindWorklog(context.Context, string, uint64) (*Worklog, error)
	FindWorklogForUpdate(context.Context, string, uint64) (*Worklog, error)
	FindWorklogByKey(context.Context, string, string) (*Worklog, error)
	ListWorklogs(context.Context, string, uint64) ([]Worklog, error)
	ListTimeline(context.Context, string, uint64, *TimelineCursor, int) ([]TimelineRecord, error)
	HasOverlappingWorklog(context.Context, string, uint64, string, time.Time, time.Time) (bool, error)
	AssigneeIDsWithValidWorklogs(context.Context, string, uint64, []string) (map[string]bool, error)
	CreateOutbox(context.Context, *OutboxEvent) error
	RequeueOutboxByAggregate(context.Context, string, string, string) error
	UpdateWorklogDelivery(context.Context, string, uint64, map[string]any) error
	CreateIntegrationAttempt(context.Context, *IntegrationAttempt) error
}

type GORMRepository struct{ db *gorm.DB }

func NewGORMRepository(db *gorm.DB) *GORMRepository { return &GORMRepository{db: db} }

func (r *GORMRepository) tx(ctx context.Context) *gorm.DB { return database.FromContext(ctx, r.db) }
func (r *GORMRepository) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	return database.WithTransaction(ctx, r.db, fn)
}

func (r *GORMRepository) nextNumber(ctx context.Context, tenant string, at time.Time, prefix string) (string, error) {
	date := at.UTC().Format("20060102")
	var seq NumberSequence
	// 序列表按“租户 + 类型 + 日期”加行锁，确保并发创建时编号单调且不重复；
	// 首次插入与后续递增都必须运行在调用方事务中。
	err := r.tx(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("tenant_id = ? AND sequence_type = ? AND sequence_date = ?", tenant, prefix, date).Take(&seq).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		seq = NumberSequence{TenantID: tenant, SequenceType: prefix, SequenceDate: date, LastValue: 1}
		if err = r.tx(ctx).Create(&seq).Error; err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	} else {
		if seq.LastValue >= 9999 {
			return "", fmt.Errorf("daily presale sequence exhausted")
		}
		seq.LastValue++
		if err = r.tx(ctx).Model(&seq).Update("last_value", seq.LastValue).Error; err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("%s%s%04d", prefix, date, seq.LastValue), nil
}

func (r *GORMRepository) NextRequestNo(ctx context.Context, tenant string, at time.Time) (string, error) {
	return r.nextNumber(ctx, tenant, at, "TS")
}
func (r *GORMRepository) NextWorklogNo(ctx context.Context, tenant string, at time.Time) (string, error) {
	return r.nextNumber(ctx, tenant, at, "WL")
}
func (r *GORMRepository) CreateRequest(ctx context.Context, v *PresaleRequest) error {
	return r.tx(ctx).Create(v).Error
}
func (r *GORMRepository) FindRequest(ctx context.Context, tenant string, id uint64) (*PresaleRequest, error) {
	var v PresaleRequest
	err := r.tx(ctx).Where("tenant_id = ? AND id = ?", tenant, id).Take(&v).Error
	return &v, mapNotFound(err)
}
func (r *GORMRepository) FindRequestForUpdate(ctx context.Context, tenant string, id uint64) (*PresaleRequest, error) {
	var v PresaleRequest
	err := r.tx(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id = ? AND id = ?", tenant, id).Take(&v).Error
	return &v, mapNotFound(err)
}
func (r *GORMRepository) FindRequestByCreateKey(ctx context.Context, tenant, key string) (*PresaleRequest, error) {
	var v PresaleRequest
	err := r.tx(ctx).Where("tenant_id = ? AND create_idempotency_key = ?", tenant, key).Take(&v).Error
	return &v, mapNotFound(err)
}

func applyRequestScope(db *gorm.DB, scope RequestQueryScope) *gorm.DB {
	db = db.Where("crm_presale_requests.tenant_id=? AND crm_presale_requests.deleted_at IS NULL", scope.TenantID)
	if scope.All {
		return db
	}
	assignmentScope := `EXISTS (
		SELECT 1 FROM crm_presale_assignments scope_assignment
		WHERE scope_assignment.tenant_id=crm_presale_requests.tenant_id
		  AND scope_assignment.request_id=crm_presale_requests.id
		  AND scope_assignment.assignee_id=?
		  AND scope_assignment.deleted_at IS NULL)`
	switch {
	case scope.ApplicantID != "" && scope.AssigneeID != "":
		return db.Where("(crm_presale_requests.applicant_id=? OR "+assignmentScope+")", scope.ApplicantID, scope.AssigneeID)
	case scope.ApplicantID != "":
		return db.Where("crm_presale_requests.applicant_id=?", scope.ApplicantID)
	case scope.AssigneeID != "":
		return db.Where(assignmentScope, scope.AssigneeID)
	default:
		// 仅有 presale.read 但既不是申请人、也没有可信 PMS 人员身份时，返回空集合，
		// 不能把基础读权限退化成租户级全量读取。
		return db.Where("1=0")
	}
}

func applyRequestFilters(db *gorm.DB, query RequestListQuery, now time.Time) *gorm.DB {
	if query.RequestNo != "" {
		// INSTR 将 %, _ 和反斜杠按普通任务编号字符处理；既保留子串搜索，
		// 又不让用户输入改变 LIKE 通配语义。
		db = db.Where("INSTR(crm_presale_requests.request_no, ?) > 0", query.RequestNo)
	}
	if query.OpportunityID != 0 {
		db = db.Where("crm_presale_requests.opportunity_id=?", query.OpportunityID)
	}
	if query.ApplicantID != "" {
		db = db.Where("crm_presale_requests.applicant_id=?", query.ApplicantID)
	}
	if query.AssigneeID != "" {
		db = db.Where(`EXISTS (SELECT 1 FROM crm_presale_assignments filter_assignment
			WHERE filter_assignment.tenant_id=crm_presale_requests.tenant_id
			AND filter_assignment.request_id=crm_presale_requests.id
			AND filter_assignment.assignee_id=? AND filter_assignment.deleted_at IS NULL)`, query.AssigneeID)
	}
	if query.Status != "" {
		db = db.Where("crm_presale_requests.status=?", query.Status)
	}
	if query.Venue != "" {
		db = db.Where("crm_presale_requests.venue=?", query.Venue)
	}
	if query.Urgency != "" {
		db = db.Where("crm_presale_requests.urgency=?", query.Urgency)
	}
	if query.CreatedFrom != nil {
		db = db.Where("crm_presale_requests.created_at>=?", query.CreatedFrom.UTC())
	}
	if query.CreatedTo != nil {
		db = db.Where("crm_presale_requests.created_at<?", query.CreatedTo.UTC())
	}
	if query.ExpectedFrom != nil {
		db = db.Where("crm_presale_requests.expected_end>=?", query.ExpectedFrom.UTC())
	}
	if query.ExpectedTo != nil {
		db = db.Where("crm_presale_requests.expected_end<?", query.ExpectedTo.UTC())
	}
	if query.Overdue != nil {
		terminal := []RequestStatus{StatusCompleted, StatusRejected, StatusCancelled}
		if *query.Overdue {
			db = db.Where("crm_presale_requests.expected_end<? AND crm_presale_requests.status NOT IN ?", now, terminal)
		} else {
			db = db.Where("crm_presale_requests.expected_end>=? OR crm_presale_requests.status IN ?", now, terminal)
		}
	}
	if query.PushStatus != "" {
		db = db.Where(`EXISTS (SELECT 1 FROM crm_presale_worklogs filter_worklog
			WHERE filter_worklog.tenant_id=crm_presale_requests.tenant_id
			AND filter_worklog.request_id=crm_presale_requests.id
			AND filter_worklog.push_status=? AND filter_worklog.deleted_at IS NULL
			AND filter_worklog.voided_at IS NULL)`, query.PushStatus)
	}
	return db
}

func (r *GORMRepository) ListRequests(ctx context.Context, scope RequestQueryScope, query RequestListQuery, now time.Time) (RequestListPage, error) {
	query.Page, query.PageSize = pagination.Normalize(query.Page, query.PageSize)
	db := applyRequestFilters(applyRequestScope(r.tx(ctx).Model(&PresaleRequest{}), scope), query, now)
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return RequestListPage{}, err
	}
	sortFields := map[string]string{
		"created_at":   "crm_presale_requests.created_at",
		"updated_at":   "crm_presale_requests.updated_at",
		"expected_end": "crm_presale_requests.expected_end",
		"request_no":   "crm_presale_requests.request_no",
	}
	sortField := sortFields[query.SortBy]
	if sortField == "" {
		sortField = "crm_presale_requests.created_at"
	}
	direction := "DESC"
	if strings.EqualFold(query.SortOrder, "asc") {
		direction = "ASC"
	}
	idDirection := direction
	type requestListRow struct {
		ID                  uint64
		RequestNo           string
		OpportunityID       uint64
		OpportunityNo       string
		OpportunityName     string
		ApplicantID         string
		ApplicantName       string
		Status              RequestStatus
		CurrentApprovalNode uint8
		Venue               Venue
		Urgency             Urgency
		ExpectedEnd         time.Time
		CreatedAt           time.Time
		TotalWorkHours      string
		PushExceptionCount  int64
		AlertLevel          string
		AlertDueAt          *time.Time
		AlertBasisAt        *time.Time
	}
	var rows []requestListRow
	err := db.Select(`crm_presale_requests.id, crm_presale_requests.request_no,
		crm_presale_requests.opportunity_id, crm_presale_requests.opportunity_no_snapshot AS opportunity_no,
		COALESCE((SELECT o.name FROM crm_opportunities o WHERE o.tenant_id=crm_presale_requests.tenant_id
			AND o.id=crm_presale_requests.opportunity_id AND o.deleted_at IS NULL LIMIT 1), '') AS opportunity_name,
		crm_presale_requests.applicant_id, crm_presale_requests.applicant_name_snapshot AS applicant_name,
		crm_presale_requests.status, crm_presale_requests.current_approval_node,
		crm_presale_requests.venue, crm_presale_requests.urgency,
		crm_presale_requests.expected_end, crm_presale_requests.created_at,
		COALESCE((SELECT CAST(SUM(w.work_hours) AS CHAR) FROM crm_presale_worklogs w
		 WHERE w.tenant_id=crm_presale_requests.tenant_id AND w.request_id=crm_presale_requests.id
		 AND w.deleted_at IS NULL AND w.voided_at IS NULL), '0.00') AS total_work_hours,
		(SELECT COUNT(*) FROM crm_presale_worklogs ew
		 WHERE ew.tenant_id=crm_presale_requests.tenant_id AND ew.request_id=crm_presale_requests.id
		 AND ew.deleted_at IS NULL AND ew.voided_at IS NULL
		 AND ew.push_status IN ('RETRY_WAIT','DEAD_LETTER')) AS push_exception_count,
		COALESCE((SELECT CASE
		 WHEN SUM(a.alert_type IN ('APPROVAL_NODE_1_OVERDUE','APPROVAL_NODE_2_OVERDUE','ASSIGNMENT_OVERDUE','EXECUTION_OVERDUE')) > 0 THEN 'OVERDUE'
		 WHEN SUM(a.alert_type='EXECUTION_DUE_SOON') > 0 THEN 'DUE_SOON' ELSE 'NONE' END
		 FROM crm_presale_alerts a WHERE a.tenant_id=crm_presale_requests.tenant_id
		 AND a.request_id=crm_presale_requests.id AND a.status='UNREAD' AND a.deleted_at IS NULL
		 AND ((crm_presale_requests.status='PENDING_APPROVAL' AND crm_presale_requests.current_approval_node=1 AND a.alert_type='APPROVAL_NODE_1_OVERDUE')
		   OR (crm_presale_requests.status='PENDING_APPROVAL' AND crm_presale_requests.current_approval_node=2 AND a.alert_type='APPROVAL_NODE_2_OVERDUE')
		   OR (crm_presale_requests.status='APPROVED_PENDING_ASSIGNMENT' AND a.alert_type='ASSIGNMENT_OVERDUE')
		   OR (crm_presale_requests.status='EXECUTING' AND a.alert_type IN ('EXECUTION_DUE_SOON','EXECUTION_OVERDUE')))), 'NONE') AS alert_level,
		(SELECT MIN(a.due_at) FROM crm_presale_alerts a WHERE a.tenant_id=crm_presale_requests.tenant_id
		 AND a.request_id=crm_presale_requests.id AND a.status='UNREAD' AND a.deleted_at IS NULL
		 AND ((crm_presale_requests.status='PENDING_APPROVAL' AND a.alert_type LIKE 'APPROVAL_NODE_%') OR (crm_presale_requests.status='APPROVED_PENDING_ASSIGNMENT' AND a.alert_type='ASSIGNMENT_OVERDUE') OR (crm_presale_requests.status='EXECUTING' AND a.alert_type LIKE 'EXECUTION_%'))) AS alert_due_at,
		(SELECT MIN(a.basis_at) FROM crm_presale_alerts a WHERE a.tenant_id=crm_presale_requests.tenant_id
		 AND a.request_id=crm_presale_requests.id AND a.status='UNREAD' AND a.deleted_at IS NULL
		 AND ((crm_presale_requests.status='PENDING_APPROVAL' AND a.alert_type LIKE 'APPROVAL_NODE_%') OR (crm_presale_requests.status='APPROVED_PENDING_ASSIGNMENT' AND a.alert_type='ASSIGNMENT_OVERDUE') OR (crm_presale_requests.status='EXECUTING' AND a.alert_type LIKE 'EXECUTION_%'))) AS alert_basis_at`).
		Order(sortField + " " + direction).Order("crm_presale_requests.id " + idDirection).
		Offset((query.Page - 1) * query.PageSize).Limit(query.PageSize).Scan(&rows).Error
	if err != nil {
		return RequestListPage{}, err
	}
	ids := make([]uint64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	assignments, err := r.ListCurrentAssignments(ctx, scope.TenantID, ids)
	if err != nil {
		return RequestListPage{}, err
	}
	items := make([]RequestListItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, RequestListItem{
			ID: row.ID, RequestNo: row.RequestNo, OpportunityID: row.OpportunityID,
			OpportunityNo: row.OpportunityNo, OpportunityName: row.OpportunityName, ApplicantID: row.ApplicantID,
			ApplicantName: row.ApplicantName, CurrentAssignees: assigneeSummaries(assignments[row.ID]),
			Status: row.Status, CurrentApprovalNode: row.CurrentApprovalNode, Venue: row.Venue, Urgency: row.Urgency,
			ExpectedEnd: row.ExpectedEnd, CreatedAt: row.CreatedAt,
			Overdue: isOverdue(row.Status, row.ExpectedEnd, now), TotalWorkHours: row.TotalWorkHours,
			PushExceptionCount: row.PushExceptionCount, AlertLevel: row.AlertLevel,
			AlertDueAt: row.AlertDueAt, AlertBasisAt: row.AlertBasisAt,
		})
	}
	return RequestListPage{Items: items, Page: query.Page, PageSize: query.PageSize, Total: total}, nil
}

// 每个筛选维度都从与列表相同的“已授权且已过滤”关系派生；只多取一条判断是否截断，
// 不会为了构造选项加载无限集合，也不会旁路列表的数据范围。
func (r *GORMRepository) ListRequestFilterOptions(ctx context.Context, scope RequestQueryScope, query RequestListQuery, now time.Time, limit int) (RequestFilterOptions, error) {
	visible := func() *gorm.DB {
		return applyRequestFilters(applyRequestScope(r.tx(ctx).Model(&PresaleRequest{}), scope), query, now)
	}
	result := RequestFilterOptions{}
	rowLimit := limit + 1

	type opportunityOptionRow struct {
		Value uint64
		Label string
	}
	var opportunities []opportunityOptionRow
	if err := visible().Select("crm_presale_requests.opportunity_id AS value, MAX(crm_presale_requests.opportunity_no_snapshot) AS label").
		Group("crm_presale_requests.opportunity_id").Order("label,value").Limit(rowLimit).Scan(&opportunities).Error; err != nil {
		return RequestFilterOptions{}, err
	}
	if len(opportunities) > limit {
		result.Truncated = true
		opportunities = opportunities[:limit]
	}
	for _, row := range opportunities {
		result.Opportunities = append(result.Opportunities, OpportunityFilterOption{Value: row.Value, Label: row.Label})
	}

	queryRequestOptions := func(valueColumn, labelColumn string) ([]FilterOption, bool, error) {
		var rows []FilterOption
		err := visible().Select(valueColumn + " AS value, MAX(" + labelColumn + ") AS label").
			Group(valueColumn).Order("label,value").Limit(rowLimit).Scan(&rows).Error
		if err != nil {
			return nil, false, err
		}
		truncated := len(rows) > limit
		if truncated {
			rows = rows[:limit]
		}
		return rows, truncated, nil
	}
	var truncated bool
	var err error
	if result.Applicants, truncated, err = queryRequestOptions("crm_presale_requests.applicant_id", "crm_presale_requests.applicant_name_snapshot"); err != nil {
		return RequestFilterOptions{}, err
	}
	result.Truncated = result.Truncated || truncated
	if result.Statuses, truncated, err = queryRequestOptions("crm_presale_requests.status", "crm_presale_requests.status"); err != nil {
		return RequestFilterOptions{}, err
	}
	result.Truncated = result.Truncated || truncated
	if result.Venues, truncated, err = queryRequestOptions("crm_presale_requests.venue", "crm_presale_requests.venue"); err != nil {
		return RequestFilterOptions{}, err
	}
	result.Truncated = result.Truncated || truncated
	if result.Urgencies, truncated, err = queryRequestOptions("crm_presale_requests.urgency", "crm_presale_requests.urgency"); err != nil {
		return RequestFilterOptions{}, err
	}
	result.Truncated = result.Truncated || truncated

	visibleIDs := visible().Select("crm_presale_requests.id")
	var assignees []FilterOption
	if err = r.tx(ctx).Table("crm_presale_assignments AS option_assignment").
		Joins("JOIN (?) AS visible_request ON visible_request.id=option_assignment.request_id", visibleIDs).
		Where("option_assignment.tenant_id=? AND option_assignment.deleted_at IS NULL", scope.TenantID).
		Select("option_assignment.assignee_id AS value, MAX(option_assignment.assignee_name_snapshot) AS label").
		Group("option_assignment.assignee_id").Order("label,value").Limit(rowLimit).Scan(&assignees).Error; err != nil {
		return RequestFilterOptions{}, err
	}
	if len(assignees) > limit {
		result.Truncated = true
		assignees = assignees[:limit]
	}
	result.Assignees = assignees

	visibleIDs = visible().Select("crm_presale_requests.id")
	var pushStatuses []FilterOption
	if err = r.tx(ctx).Table("crm_presale_worklogs AS option_worklog").
		Joins("JOIN (?) AS visible_request ON visible_request.id=option_worklog.request_id", visibleIDs).
		Where("option_worklog.tenant_id=? AND option_worklog.deleted_at IS NULL AND option_worklog.voided_at IS NULL", scope.TenantID).
		Select("option_worklog.push_status AS value, option_worklog.push_status AS label").
		Group("option_worklog.push_status").Order("value").Limit(rowLimit).Scan(&pushStatuses).Error; err != nil {
		return RequestFilterOptions{}, err
	}
	if len(pushStatuses) > limit {
		result.Truncated = true
		pushStatuses = pushStatuses[:limit]
	}
	result.PushStatuses = pushStatuses
	return result, nil
}

// 上层完成商机授权后，本查询只按可信租户和商机读取摘要；它不套用更窄的售前详情范围，
// 详情能力由服务层逐条计算，且摘要本身不含联系人等敏感字段。
func (r *GORMRepository) ListOpportunityRequests(ctx context.Context, tenant string, opportunityID uint64, page, pageSize int, now time.Time) (RequestListPage, error) {
	return r.ListRequests(ctx, RequestQueryScope{TenantID: tenant, All: true}, RequestListQuery{
		OpportunityID: opportunityID, Page: page, PageSize: pageSize, SortBy: "created_at", SortOrder: "desc",
	}, now)
}

func (r *GORMRepository) HistoricalAssignmentRequestIDs(ctx context.Context, tenant, personID string, requestIDs []uint64) (map[uint64]bool, error) {
	result := make(map[uint64]bool)
	if personID == "" || len(requestIDs) == 0 {
		return result, nil
	}
	var rows []struct{ RequestID uint64 }
	err := r.tx(ctx).Model(&Assignment{}).Distinct("request_id").
		Where("tenant_id=? AND assignee_id=? AND request_id IN ? AND deleted_at IS NULL", tenant, personID, requestIDs).
		Scan(&rows).Error
	for _, row := range rows {
		result[row.RequestID] = true
	}
	return result, err
}

func (r *GORMRepository) RequestAggregate(ctx context.Context, tenant string, requestID uint64) (RequestAggregate, error) {
	var result RequestAggregate
	err := r.tx(ctx).Raw(`SELECT
		COALESCE(CAST(SUM(work_hours) AS CHAR), '0.00') AS total_work_hours,
		COALESCE(SUM(CASE WHEN push_status IN ('RETRY_WAIT','DEAD_LETTER') THEN 1 ELSE 0 END), 0) AS push_exception_count
		FROM crm_presale_worklogs
		WHERE tenant_id=? AND request_id=? AND deleted_at IS NULL AND voided_at IS NULL`, tenant, requestID).Scan(&result).Error
	return result, err
}

func (r *GORMRepository) AlertAggregate(ctx context.Context, tenant string, requestID uint64) (AlertAggregate, error) {
	type row struct {
		Level   string
		DueAt   *time.Time
		BasisAt *time.Time
	}
	var value row
	err := r.tx(ctx).Raw(`SELECT
		CASE WHEN SUM(a.alert_type IN ('APPROVAL_NODE_1_OVERDUE','APPROVAL_NODE_2_OVERDUE','ASSIGNMENT_OVERDUE','EXECUTION_OVERDUE')) > 0 THEN 'OVERDUE'
		     WHEN SUM(a.alert_type='EXECUTION_DUE_SOON') > 0 THEN 'DUE_SOON' ELSE 'NONE' END AS level,
		MIN(a.due_at) AS due_at, MIN(a.basis_at) AS basis_at
		FROM crm_presale_alerts a JOIN crm_presale_requests r ON r.tenant_id=a.tenant_id AND r.id=a.request_id
		WHERE a.tenant_id=? AND a.request_id=? AND a.status='UNREAD' AND a.deleted_at IS NULL
		AND ((r.status='PENDING_APPROVAL' AND r.current_approval_node=1 AND a.alert_type='APPROVAL_NODE_1_OVERDUE')
		  OR (r.status='PENDING_APPROVAL' AND r.current_approval_node=2 AND a.alert_type='APPROVAL_NODE_2_OVERDUE')
		  OR (r.status='APPROVED_PENDING_ASSIGNMENT' AND a.alert_type='ASSIGNMENT_OVERDUE')
		  OR (r.status='EXECUTING' AND a.alert_type IN ('EXECUTION_DUE_SOON','EXECUTION_OVERDUE')))`, tenant, requestID).Scan(&value).Error
	if value.Level == "" {
		value.Level = "NONE"
	}
	return AlertAggregate{Level: value.Level, DueAt: value.DueAt, BasisAt: value.BasisAt}, err
}

func (r *GORMRepository) ListCurrentAssignments(ctx context.Context, tenant string, requestIDs []uint64) (map[uint64][]Assignment, error) {
	result := make(map[uint64][]Assignment)
	if len(requestIDs) == 0 {
		return result, nil
	}
	var values []Assignment
	err := r.tx(ctx).Where("tenant_id=? AND request_id IN ? AND is_current=1 AND deleted_at IS NULL", tenant, requestIDs).
		Order("request_id,assigned_at,id").Find(&values).Error
	for _, value := range values {
		result[value.RequestID] = append(result[value.RequestID], value)
	}
	return result, err
}

func (r *GORMRepository) LatestProgressByRequest(ctx context.Context, tenant string, requestIDs []uint64) (map[uint64]string, error) {
	result := make(map[uint64]string)
	if len(requestIDs) == 0 {
		return result, nil
	}
	type row struct {
		RequestID uint64
		Content   string
	}
	var rows []row
	err := r.tx(ctx).Raw(`SELECT p.request_id,p.content FROM crm_presale_progress_logs p
		JOIN (SELECT request_id,MAX(id) AS max_id FROM crm_presale_progress_logs
		WHERE tenant_id=? AND request_id IN ? GROUP BY request_id) latest ON latest.max_id=p.id`, tenant, requestIDs).Scan(&rows).Error
	for _, value := range rows {
		result[value.RequestID] = value.Content
	}
	return result, err
}
func (r *GORMRepository) UpdateRequestVersioned(ctx context.Context, v *PresaleRequest, version uint64, fields map[string]any) error {
	fields["version"] = gorm.Expr("version + 1")
	fields["updated_at"] = time.Now().UTC()
	res := r.tx(ctx).Model(&PresaleRequest{}).Where("tenant_id = ? AND id = ? AND version = ?", v.TenantID, v.ID, version).Updates(fields)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return ErrVersionConflict
	}
	v.Version = version + 1
	return nil
}
func (r *GORMRepository) CreateApprovalInstance(ctx context.Context, v *ApprovalInstance) error {
	return r.tx(ctx).Create(v).Error
}
func (r *GORMRepository) FindApprovalInstanceForUpdate(ctx context.Context, tenant string, requestID uint64) (*ApprovalInstance, error) {
	var v ApprovalInstance
	e := r.tx(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND request_id=?", tenant, requestID).Take(&v).Error
	return &v, mapNotFound(e)
}
func (r *GORMRepository) FindApprovalInstance(ctx context.Context, tenant string, requestID uint64) (*ApprovalInstance, error) {
	var v ApprovalInstance
	e := r.tx(ctx).Where("tenant_id=? AND request_id=?", tenant, requestID).Take(&v).Error
	return &v, mapNotFound(e)
}
func (r *GORMRepository) UpdateApprovalInstance(ctx context.Context, v *ApprovalInstance, f map[string]any) error {
	return r.tx(ctx).Model(v).Updates(f).Error
}
func (r *GORMRepository) CreateApprovalLog(ctx context.Context, v *ApprovalLog) error {
	return r.tx(ctx).Create(v).Error
}
func (r *GORMRepository) ListApprovalLogs(ctx context.Context, tenant string, id uint64) ([]ApprovalLog, error) {
	var v []ApprovalLog
	e := r.tx(ctx).Where("tenant_id=? AND request_id=?", tenant, id).Order("approved_at,id").Find(&v).Error
	return v, e
}
func (r *GORMRepository) FindEngineTaskLog(ctx context.Context, tenant, key string) (*ApprovalLog, error) {
	var v ApprovalLog
	e := r.tx(ctx).Where("tenant_id=? AND engine_task_id=?", tenant, key).Take(&v).Error
	return &v, mapNotFound(e)
}
func (r *GORMRepository) FindEngineers(ctx context.Context, tenant string, ids []string) ([]Engineer, error) {
	var v []Engineer
	e := r.tx(ctx).Where("tenant_id=? AND person_id IN ?", tenant, ids).Find(&v).Error
	return v, e
}
func (r *GORMRepository) FindEngineersForUpdate(ctx context.Context, tenant string, ids []string) ([]Engineer, error) {
	var v []Engineer
	e := r.tx(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND person_id IN ?", tenant, ids).Find(&v).Error
	return v, e
}
func (r *GORMRepository) ListCurrentAssignmentsForUpdate(ctx context.Context, tenant string, id uint64) ([]Assignment, error) {
	var v []Assignment
	e := r.tx(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND request_id=? AND is_current=1", tenant, id).Find(&v).Error
	return v, e
}
func (r *GORMRepository) ListAssignments(ctx context.Context, tenant string, id uint64) ([]Assignment, error) {
	var v []Assignment
	e := r.tx(ctx).Where("tenant_id=? AND request_id=?", tenant, id).Order("assigned_at,id").Find(&v).Error
	return v, e
}
func (r *GORMRepository) CreateAssignment(ctx context.Context, v *Assignment) error {
	return r.tx(ctx).Create(v).Error
}
func (r *GORMRepository) EndAssignment(ctx context.Context, tenant string, id uint64, version uint64, by string, at time.Time) error {
	res := r.tx(ctx).Model(&Assignment{}).Where("tenant_id=? AND id=? AND version=? AND is_current=1", tenant, id, version).Updates(map[string]any{"is_current": false, "ended_at": at, "updated_by": by, "version": gorm.Expr("version+1")})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return ErrVersionConflict
	}
	return nil
}
func (r *GORMRepository) CreateAssignmentEvent(ctx context.Context, value *AssignmentEvent) error {
	return r.tx(ctx).Create(value).Error
}
func (r *GORMRepository) CreateProgress(ctx context.Context, v *ProgressLog) error {
	return r.tx(ctx).Create(v).Error
}
func (r *GORMRepository) CreateProgressNotificationEvent(ctx context.Context, value *ProgressNotificationEvent) error {
	return r.tx(ctx).Create(value).Error
}
func (r *GORMRepository) FindProgressByKey(ctx context.Context, tenant, key string) (*ProgressLog, error) {
	var value ProgressLog
	err := r.tx(ctx).Where("tenant_id=? AND idempotency_key=?", tenant, key).Take(&value).Error
	return &value, mapNotFound(err)
}
func (r *GORMRepository) FindProgressByKeyForUpdate(ctx context.Context, tenant, key string) (*ProgressLog, error) {
	var value ProgressLog
	err := r.tx(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("tenant_id=? AND idempotency_key=?", tenant, key).Take(&value).Error
	return &value, mapNotFound(err)
}
func (r *GORMRepository) CreateStatusLog(ctx context.Context, v *StatusLog) error {
	return r.tx(ctx).Create(v).Error
}
func (r *GORMRepository) FindMutationReplay(ctx context.Context, tenant string, requestID uint64, actorID, key string) (*MutationReplay, error) {
	var value MutationReplay
	err := r.tx(ctx).Where("tenant_id=? AND request_id=? AND actor_id=? AND idempotency_key=?", tenant, requestID, actorID, key).Take(&value).Error
	return &value, mapNotFound(err)
}
func (r *GORMRepository) CreateMutationReplay(ctx context.Context, value *MutationReplay) error {
	return r.tx(ctx).Create(value).Error
}
func (r *GORMRepository) CreateWorklog(ctx context.Context, v *Worklog) error {
	return r.tx(ctx).Create(v).Error
}
func (r *GORMRepository) FindWorklog(ctx context.Context, tenant string, id uint64) (*Worklog, error) {
	var v Worklog
	e := r.tx(ctx).Where("tenant_id=? AND id=?", tenant, id).Take(&v).Error
	return &v, mapNotFound(e)
}
func (r *GORMRepository) FindWorklogForUpdate(ctx context.Context, tenant string, id uint64) (*Worklog, error) {
	var v Worklog
	e := r.tx(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND id=?", tenant, id).Take(&v).Error
	return &v, mapNotFound(e)
}
func (r *GORMRepository) FindWorklogByKey(ctx context.Context, tenant, key string) (*Worklog, error) {
	var v Worklog
	e := r.tx(ctx).Where("tenant_id=? AND idempotency_key=?", tenant, key).Take(&v).Error
	return &v, mapNotFound(e)
}
func (r *GORMRepository) ListWorklogs(ctx context.Context, tenant string, requestID uint64) ([]Worklog, error) {
	var values []Worklog
	err := r.tx(ctx).Where("tenant_id=? AND request_id=?", tenant, requestID).Order("work_start,id").Find(&values).Error
	return values, err
}

// 时间线在数据库内合并申请、状态、审批、分派、进展和工时等不可变事实。
// 外层使用“发生时间、类型优先级、源记录 ID”的降序游标，并发插入不会像 OFFSET
// 那样推移已经返回的记录。
func (r *GORMRepository) ListTimeline(ctx context.Context, tenant string, requestID uint64, cursor *TimelineCursor, limit int) ([]TimelineRecord, error) {
	type timelineRow struct {
		SourceID     uint64
		TypePriority uint8
		EventType    string
		OccurredAt   time.Time
		ActorID      string
		ActorName    string
		SubjectID    string
		SubjectName  string
		FromStatus   RequestStatus
		ToStatus     RequestStatus
		Result       string
		Content      string
		LinkURL      string
		ProgressPct  *uint8
		WorkHours    string
		WorkContent  string
	}
	const timelineSQL = `SELECT source_id,type_priority,event_type,occurred_at,
		actor_id,actor_name,subject_id,subject_name,from_status,to_status,result,
		content,link_url,progress_pct,work_hours,work_content
	FROM (
		SELECT r.id AS source_id,1 AS type_priority,'REQUEST_CREATED' AS event_type,r.created_at AS occurred_at,
			r.applicant_id AS actor_id,r.applicant_name_snapshot AS actor_name,'' AS subject_id,'' AS subject_name,
			'' AS from_status,'APPROVAL_STARTING' AS to_status,'' AS result,'' AS content,'' AS link_url,NULL AS progress_pct,
			'' AS work_hours,'' AS work_content
		FROM crm_presale_requests r
		WHERE r.tenant_id=? AND r.id=? AND r.deleted_at IS NULL
		UNION ALL
		SELECT s.id,10,'STATUS_CHANGED',s.occurred_at,s.operator_id,'','','',s.from_status,s.to_status,'','','',NULL,'',''
		FROM crm_presale_status_logs s WHERE s.tenant_id=? AND s.request_id=?
		UNION ALL
		SELECT a.id,20,'APPROVAL_DECIDED',a.approved_at,a.approver_id,a.approver_name_snapshot,'','',
			'','',a.result,'','',NULL,'',''
		FROM crm_presale_approval_logs a WHERE a.tenant_id=? AND a.request_id=?
		UNION ALL
		SELECT a.id,30,'ASSIGNEE_ADDED',a.assigned_at,a.assigned_by,'',a.assignee_id,a.assignee_name_snapshot,
			'','','','','',NULL,'',''
		FROM crm_presale_assignments a WHERE a.tenant_id=? AND a.request_id=? AND a.deleted_at IS NULL
		UNION ALL
		SELECT a.id,31,'ASSIGNEE_REMOVED',a.ended_at,a.updated_by,'',a.assignee_id,a.assignee_name_snapshot,
			'','','','','',NULL,'',''
		FROM crm_presale_assignments a WHERE a.tenant_id=? AND a.request_id=? AND a.deleted_at IS NULL AND a.ended_at IS NOT NULL
		UNION ALL
		SELECT p.id,40,'PROGRESS_ADDED',p.created_at,p.author_id,'','','','','','',p.content,p.link_url,p.progress_pct,'',''
		FROM crm_presale_progress_logs p WHERE p.tenant_id=? AND p.request_id=?
			AND LENGTH(p.content)<=2000 AND (p.link_url='' OR p.link_url LIKE 'https://%')
		UNION ALL
		SELECT w.id,50,'WORKLOG_ADDED',w.created_at,w.created_by,w.person_name_snapshot,w.person_id,w.person_name_snapshot,
			'','','','','',NULL,CAST(w.work_hours AS CHAR),w.work_content
		FROM crm_presale_worklogs w
		WHERE w.tenant_id=? AND w.request_id=? AND w.deleted_at IS NULL AND w.voided_at IS NULL
	) timeline
	WHERE (?=0 OR occurred_at<? OR (occurred_at=? AND type_priority<?) OR
		(occurred_at=? AND type_priority=? AND source_id<?))
	ORDER BY occurred_at DESC,type_priority DESC,source_id DESC
	LIMIT ?`
	args := make([]any, 0, 22)
	for range 7 {
		args = append(args, tenant, requestID)
	}
	var cursorPresent uint8
	var occurredAt time.Time
	var priority uint8
	var sourceID uint64
	if cursor != nil {
		cursorPresent, occurredAt, priority, sourceID = 1, cursor.OccurredAt.UTC(), cursor.TypePriority, cursor.SourceID
	}
	args = append(args, cursorPresent, occurredAt, occurredAt, priority, occurredAt, priority, sourceID, limit)
	var rows []timelineRow
	if err := r.tx(ctx).Raw(timelineSQL, args...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]TimelineRecord, 0, len(rows))
	for _, row := range rows {
		result = append(result, TimelineRecord{
			SourceID: row.SourceID, TypePriority: row.TypePriority, EventType: row.EventType,
			OccurredAt: row.OccurredAt, ActorID: row.ActorID, ActorName: row.ActorName,
			SubjectID: row.SubjectID, SubjectName: row.SubjectName,
			FromStatus: row.FromStatus, ToStatus: row.ToStatus, Result: row.Result,
			Content: row.Content, LinkURL: row.LinkURL, ProgressPct: row.ProgressPct,
			WorkHours: row.WorkHours, WorkContent: row.WorkContent,
		})
	}
	return result, nil
}
func (r *GORMRepository) HasOverlappingWorklog(ctx context.Context, tenant string, rid uint64, pid string, start, end time.Time) (bool, error) {
	var n int64
	e := r.tx(ctx).Model(&Worklog{}).Where("tenant_id=? AND request_id=? AND person_id=? AND voided_at IS NULL AND work_start < ? AND work_end > ?", tenant, rid, pid, end, start).Count(&n).Error
	return n > 0, e
}
func (r *GORMRepository) AssigneeIDsWithValidWorklogs(ctx context.Context, tenant string, rid uint64, ids []string) (map[string]bool, error) {
	var rows []struct{ PersonID string }
	e := r.tx(ctx).Model(&Worklog{}).Distinct("person_id").Where("tenant_id=? AND request_id=? AND person_id IN ? AND voided_at IS NULL", tenant, rid, ids).Scan(&rows).Error
	m := map[string]bool{}
	for _, v := range rows {
		m[v.PersonID] = true
	}
	return m, e
}
func (r *GORMRepository) CreateOutbox(ctx context.Context, v *OutboxEvent) error {
	return r.tx(ctx).Create(v).Error
}
func (r *GORMRepository) RequeueOutboxByAggregate(ctx context.Context, tenant, aggregateType, aggregateID string) error {
	result := r.tx(ctx).Model(&OutboxEvent{}).
		Where("tenant_id=? AND aggregate_type=? AND aggregate_id=?", tenant, aggregateType, aggregateID).
		Updates(map[string]any{"status": "PENDING", "retry_count": 0, "next_retry_at": nil, "locked_by": "", "locked_until": nil, "last_error_summary": ""})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrNotFound
	}
	return nil
}
func (r *GORMRepository) UpdateWorklogDelivery(ctx context.Context, tenant string, id uint64, f map[string]any) error {
	return r.tx(ctx).Model(&Worklog{}).Where("tenant_id=? AND id=?", tenant, id).Updates(f).Error
}
func (r *GORMRepository) CreateIntegrationAttempt(ctx context.Context, v *IntegrationAttempt) error {
	return r.tx(ctx).Create(v).Error
}

func mapNotFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}
