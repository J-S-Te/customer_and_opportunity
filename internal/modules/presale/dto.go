package presale

import (
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/pagination"
)

type Actor struct {
	TenantID        string
	UserID          string
	UserName        string
	PersonID        string
	ScopeMode       string
	OrganizationIDs []string
	Roles           map[string]bool
	Permissions     map[string]bool
	RequestID       string
}

func (a Actor) Can(permission string) bool { return a.Permissions[permission] }
func (a Actor) HasRole(role string) bool   { return a.Roles[role] }

// RequestListQuery 只表达业务筛选条件，不允许客户端传入授权范围。
// 可见范围由服务端根据 Actor 计算，因此筛选只能收窄结果，不能扩大权限边界。
type RequestListQuery struct {
	RequestNo     string
	OpportunityID uint64
	ApplicantID   string
	AssigneeID    string
	Status        RequestStatus
	Venue         Venue
	Urgency       Urgency
	CreatedFrom   *time.Time
	CreatedTo     *time.Time
	ExpectedFrom  *time.Time
	ExpectedTo    *time.Time
	Overdue       *bool
	PushStatus    PushStatus
	Page          int
	PageSize      int
	SortBy        string
	SortOrder     string
}

// RequestQueryScope 是服务层完成授权判断后的可信查询条件，仓储层只接受该结果，
// 从接口结构上阻止前端伪造“查看全部”或冒充申请人、执行人的范围参数。
type RequestQueryScope struct {
	TenantID    string
	All         bool
	ApplicantID string
	AssigneeID  string
}

type CreateRequestInput struct {
	OpportunityID  uint64    `json:"opportunity_id" binding:"required"`
	Venue          Venue     `json:"venue" binding:"required"`
	ServiceAddress string    `json:"service_address"`
	ContactName    string    `json:"contact_name" binding:"required"`
	ContactPhone   string    `json:"contact_phone" binding:"required"`
	Description    string    `json:"description" binding:"required"`
	ExpectedStart  time.Time `json:"expected_start" binding:"required"`
	ExpectedEnd    time.Time `json:"expected_end" binding:"required"`
	Urgency        Urgency   `json:"urgency" binding:"required"`
}

// ReopenRequestInput carries the editable business fields when a rejected or
// cancelled request is submitted again. The opportunity and request identity
// remain immutable so the original approval history stays attached to the
// same request.
type ReopenRequestInput struct {
	Venue          Venue     `json:"venue" binding:"required"`
	ServiceAddress string    `json:"service_address"`
	ContactName    string    `json:"contact_name" binding:"required"`
	ContactPhone   string    `json:"contact_phone" binding:"required"`
	Description    string    `json:"description" binding:"required"`
	ExpectedStart  time.Time `json:"expected_start" binding:"required"`
	ExpectedEnd    time.Time `json:"expected_end" binding:"required"`
	Urgency        Urgency   `json:"urgency" binding:"required"`
}

type ApprovalStartedInput struct {
	RequestID        uint64
	EngineInstanceID string
	EventSequence    uint64
}

type ApprovalCallbackInput struct {
	RequestID        uint64    `json:"request_id" binding:"required"`
	EngineInstanceID string    `json:"engine_instance_id" binding:"required"`
	EngineTaskID     string    `json:"engine_task_id" binding:"required"`
	EventSequence    uint64    `json:"event_sequence" binding:"required"`
	Node             uint8     `json:"node" binding:"required"`
	Result           string    `json:"result" binding:"required"`
	Comment          string    `json:"comment"`
	ApproverID       string    `json:"approver_id" binding:"required"`
	ApproverName     string    `json:"approver_name" binding:"required"`
	OccurredAt       time.Time `json:"occurred_at" binding:"required"`
}

type ApprovalActionInput struct {
	Action  string `json:"action" binding:"required"`
	Comment string `json:"comment"`
	Version uint64 `json:"version" binding:"required"`
}

type AssignmentTarget struct {
	PersonID     string `json:"person_id" binding:"required"`
	PersonName   string `json:"person_name"`
	Department   string `json:"department"`
	DepartmentID string `json:"department_id"`
	Role         string `json:"role" binding:"required"`
}

type ReplaceAssignmentsInput struct {
	Assignees    []AssignmentTarget `json:"assignees" binding:"required,min=1"`
	ChangeReason string             `json:"change_reason" binding:"required"`
	Version      uint64             `json:"version" binding:"required"`
}

type SelectDepartmentInput struct {
	DepartmentID string `json:"department_id" binding:"required"`
	Department   string `json:"department"`
	Version      uint64 `json:"version" binding:"required"`
}

type AddProgressInput struct {
	Content     string `json:"content" binding:"required"`
	LinkURL     string `json:"link_url"`
	ProgressPct *uint8 `json:"progress_pct"`
	Version     uint64 `json:"version" binding:"required"`
}

type CancelInput struct {
	Reason  string `json:"reason" binding:"required"`
	Version uint64 `json:"version" binding:"required"`
}

type AddWorklogInput struct {
	WorkStart       time.Time `json:"work_start" binding:"required"`
	WorkEnd         time.Time `json:"work_end" binding:"required"`
	RawUnit         string    `json:"raw_unit" binding:"required"`
	RawValue        string    `json:"raw_value" binding:"required"`
	WorkSiteAddress string    `json:"work_site_address" binding:"required"`
	WorkContent     string    `json:"work_content" binding:"required"`
	Remark          string    `json:"remark"`
	Version         uint64    `json:"version" binding:"required"`
}

type OpportunitySnapshot struct {
	ID            uint64
	OpportunityNo string
	Venue         Venue
}

// RequestView 是面向接口的显式投影，避免直接序列化 GORM 模型而泄露联系人密文、
// 幂等键或请求摘要等内部字段。
type RequestView struct {
	ID                    uint64        `json:"id"`
	RequestNo             string        `json:"request_no"`
	OpportunityID         uint64        `json:"opportunity_id"`
	OpportunityNo         string        `json:"opportunity_no"`
	ApplicantID           string        `json:"applicant_id"`
	ApplicantName         string        `json:"applicant_name"`
	Venue                 Venue         `json:"venue"`
	ServiceAddress        string        `json:"service_address,omitempty"`
	ContactName           string        `json:"contact_name"`
	ContactPhoneMasked    string        `json:"contact_phone"`
	Description           string        `json:"description"`
	ExpectedStart         time.Time     `json:"expected_start"`
	ExpectedEnd           time.Time     `json:"expected_end"`
	Urgency               Urgency       `json:"urgency"`
	Status                RequestStatus `json:"status"`
	CurrentApprovalNode   uint8         `json:"current_approval_node"`
	ExecutionDepartmentID string        `json:"execution_department_id,omitempty"`
	ExecutionDepartment   string        `json:"execution_department,omitempty"`
	Version               uint64        `json:"version"`
	CreatedAt             time.Time     `json:"created_at"`
	UpdatedAt             time.Time     `json:"updated_at"`
}

func requestView(value *PresaleRequest) RequestView {
	return RequestView{
		ID: value.ID, RequestNo: value.RequestNo, OpportunityID: value.OpportunityID,
		OpportunityNo: value.OpportunityNoSnapshot, ApplicantID: value.ApplicantID,
		ApplicantName: value.ApplicantNameSnapshot, Venue: value.Venue,
		ServiceAddress: value.ServiceAddress, ContactName: value.ContactName,
		ContactPhoneMasked: value.ContactPhoneMasked, Description: value.Description,
		ExpectedStart: value.ExpectedStart, ExpectedEnd: value.ExpectedEnd,
		Urgency: value.Urgency, Status: value.Status,
		CurrentApprovalNode: value.CurrentApprovalNode, Version: value.Version,
		ExecutionDepartmentID: value.ExecutionDepartmentID, ExecutionDepartment: value.ExecutionDepartment,
		CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
}

type AssigneeSummaryView struct {
	PersonID   string `json:"person_id"`
	PersonName string `json:"person_name"`
	Role       string `json:"role"`
}

// RequestListItem 只包含列表决策所需字段，不携带联系人密文、幂等键和请求摘要。
type RequestListItem struct {
	ID                  uint64                `json:"id"`
	RequestNo           string                `json:"request_no"`
	OpportunityID       uint64                `json:"opportunity_id"`
	OpportunityNo       string                `json:"opportunity_no"`
	OpportunityName     string                `json:"opportunity_name"`
	ApplicantID         string                `json:"applicant_id"`
	ApplicantName       string                `json:"applicant_name"`
	CurrentAssignees    []AssigneeSummaryView `json:"current_assignees"`
	Status              RequestStatus         `json:"status"`
	CurrentApprovalNode uint8                 `json:"current_approval_node"`
	Venue               Venue                 `json:"venue"`
	Urgency             Urgency               `json:"urgency"`
	ExpectedEnd         time.Time             `json:"expected_end"`
	CreatedAt           time.Time             `json:"created_at"`
	Overdue             bool                  `json:"overdue"`
	TotalWorkHours      string                `json:"total_work_hours"`
	PushExceptionCount  int64                 `json:"push_exception_count"`
	AlertLevel          string                `json:"alert_level"`
	AlertDueAt          *time.Time            `json:"alert_due_at,omitempty"`
	AlertBasisAt        *time.Time            `json:"alert_basis_at,omitempty"`
	AvailableActions    []string              `json:"available_actions"`
}

type RequestDetailView struct {
	Request             RequestView           `json:"request"`
	CurrentAssignees    []AssigneeSummaryView `json:"current_assignees"`
	TotalWorkHours      string                `json:"total_work_hours"`
	PushExceptionCount  int64                 `json:"push_exception_count"`
	Overdue             bool                  `json:"overdue"`
	AlertLevel          string                `json:"alert_level"`
	AlertDueAt          *time.Time            `json:"alert_due_at,omitempty"`
	AlertBasisAt        *time.Time            `json:"alert_basis_at,omitempty"`
	AvailableActions    []string              `json:"available_actions"`
	CanViewContactPhone bool                  `json:"can_view_contact_phone"`
}

// ContactPhoneView 仅由独立的敏感信息接口返回；普通详情不会触发解密，
// 从而使电话号码访问始终经过单独授权和审计。
type ContactPhoneView struct {
	RequestID    uint64 `json:"request_id"`
	ContactPhone string `json:"contact_phone"`
}

type AlertAggregate struct {
	Level   string
	DueAt   *time.Time
	BasisAt *time.Time
}

type UpdateAlertRuleInput struct {
	ThresholdHours uint32 `json:"threshold_hours"`
	Enabled        bool   `json:"enabled"`
	Version        uint64 `json:"version" binding:"required"`
}

type AlertRuleView struct {
	Type           AlertType `json:"type"`
	ThresholdHours uint32    `json:"threshold_hours"`
	Enabled        bool      `json:"enabled"`
	ConfigVersion  uint64    `json:"config_version"`
	UpdatedBy      string    `json:"updated_by"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type AlertView struct {
	ID            uint64     `json:"id"`
	RequestID     uint64     `json:"request_id"`
	RequestNo     string     `json:"request_no"`
	AlertType     AlertType  `json:"alert_type"`
	RuleVersion   uint64     `json:"rule_version"`
	BasisAt       time.Time  `json:"basis_at"`
	DueAt         time.Time  `json:"due_at"`
	Status        string     `json:"status"`
	RecipientKind string     `json:"recipient_kind"`
	SentAt        *time.Time `json:"sent_at,omitempty"`
	ReadAt        *time.Time `json:"read_at,omitempty"`
}

type AlertListPage = pagination.Page[AlertView]

// OpportunityPresaleItem 是商机侧的受限摘要，刻意排除联系人信息；
// 是否可进入售前详情由 CanViewDetail 单独表达，不能由摘要内容反推出权限。
type OpportunityPresaleItem struct {
	ID               uint64                `json:"id"`
	RequestNo        string                `json:"request_no"`
	OpportunityName  string                `json:"opportunity_name"`
	CreatedAt        time.Time             `json:"created_at"`
	Status           RequestStatus         `json:"status"`
	Urgency          Urgency               `json:"urgency"`
	Venue            Venue                 `json:"venue"`
	CurrentAssignees []AssigneeSummaryView `json:"current_assignees"`
	LatestProgress   string                `json:"latest_progress,omitempty"`
	TotalWorkHours   string                `json:"total_work_hours"`
	ExpectedEnd      time.Time             `json:"expected_end"`
	Overdue          bool                  `json:"overdue"`
	CanViewDetail    bool                  `json:"can_view_detail"`
}

type RequestListPage = pagination.Page[RequestListItem]
type OpportunityPresalePage = pagination.Page[OpportunityPresaleItem]

// RequestBoardColumn 是有上限的状态泳道。Total 与列表接口使用相同授权范围和筛选条件，
// Items 最多返回 ColumnLimit 条，避免看板为了展示数量而加载全部任务。
type RequestBoardColumn struct {
	Status RequestStatus     `json:"status"`
	Items  []RequestListItem `json:"items"`
	Total  int64             `json:"total"`
}

type RequestBoardView struct {
	Columns     []RequestBoardColumn `json:"columns"`
	ColumnLimit int                  `json:"column_limit"`
}

type FilterOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type OpportunityFilterOption struct {
	Value uint64 `json:"value"`
	Label string `json:"label"`
}

// RequestFilterOptions 只从调用者已经有权查看且符合筛选条件的申请中提取选项，
// 不查询全量人员目录，避免筛选器侧信道泄露不可见人员或业务数据。
type RequestFilterOptions struct {
	Opportunities []OpportunityFilterOption `json:"opportunities"`
	Applicants    []FilterOption            `json:"applicants"`
	Assignees     []FilterOption            `json:"assignees"`
	Statuses      []FilterOption            `json:"statuses"`
	Venues        []FilterOption            `json:"venues"`
	Urgencies     []FilterOption            `json:"urgencies"`
	PushStatuses  []FilterOption            `json:"push_statuses"`
	Truncated     bool                      `json:"truncated"`
}

type RequestAggregate struct {
	TotalWorkHours     string
	PushExceptionCount int64
}

type DeliveryView struct {
	WorklogID        uint64     `json:"worklog_id"`
	Status           PushStatus `json:"status"`
	Attempts         uint8      `json:"attempts"`
	NextRetryAt      *time.Time `json:"next_retry_at,omitempty"`
	LastErrorSummary string     `json:"last_error_summary,omitempty"`
}

type WorklogView struct {
	ID               uint64     `json:"id"`
	WorklogNo        string     `json:"worklog_no"`
	RequestID        uint64     `json:"request_id"`
	PersonID         string     `json:"person_id"`
	PersonName       string     `json:"person_name"`
	WorkStart        time.Time  `json:"work_start"`
	WorkEnd          time.Time  `json:"work_end"`
	RawUnit          string     `json:"raw_unit"`
	RawValue         string     `json:"raw_value"`
	ConversionFactor string     `json:"conversion_factor"`
	WorkHours        string     `json:"work_hours"`
	Unit             string     `json:"unit"`
	WorkSiteAddress  string     `json:"work_site_address"`
	WorkContent      string     `json:"work_content"`
	Remark           string     `json:"remark,omitempty"`
	PushStatus       PushStatus `json:"push_status"`
	CompletedTask    bool       `json:"completed_task"`
	Version          uint64     `json:"version"`
}

func worklogView(value *Worklog) WorklogView {
	return WorklogView{
		ID: value.ID, WorklogNo: value.WorklogNo, RequestID: value.RequestID,
		PersonID: value.PersonID, PersonName: value.PersonNameSnapshot,
		WorkStart: value.WorkStart, WorkEnd: value.WorkEnd, RawUnit: value.RawUnit,
		RawValue: value.RawValue, ConversionFactor: value.ConversionFactor,
		WorkHours: value.WorkHours, Unit: value.Unit, WorkSiteAddress: value.WorkSiteAddress,
		WorkContent: value.WorkContent, Remark: value.Remark, PushStatus: value.PushStatus,
		CompletedTask: value.CompletedTask, Version: value.Version,
	}
}
