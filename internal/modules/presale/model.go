package presale

import (
	"time"

	"gorm.io/gorm"
)

// BaseModel 统一承载可变售前记录的租户边界、审计字段、软删除标记和乐观锁版本。
// 业务写操作必须同时限定 tenant_id 与 version，避免跨租户访问和并发覆盖。
type BaseModel struct {
	ID        uint64         `gorm:"primaryKey;autoIncrement"`
	TenantID  string         `gorm:"size:64;not null;index"`
	CreatedBy string         `gorm:"size:64;not null"`
	UpdatedBy string         `gorm:"size:64;not null"`
	CreatedAt time.Time      `gorm:"precision:3;not null"`
	UpdatedAt time.Time      `gorm:"precision:3;not null"`
	DeletedAt gorm.DeletedAt `gorm:"precision:3;index"`
	Version   uint64         `gorm:"not null;default:1"`
}

type RequestStatus string

// 售前申请以审批引擎确认启动作为进入审批态的边界；审批通过后必须完成分派才能执行。
// COMPLETED、REJECTED、CANCELLED 是终态，不再参与逾期判断或普通状态推进。
const (
	StatusApprovalStarting          RequestStatus = "APPROVAL_STARTING"
	StatusPendingApproval           RequestStatus = "PENDING_APPROVAL"
	StatusApprovedPendingAssignment RequestStatus = "APPROVED_PENDING_ASSIGNMENT"
	StatusExecuting                 RequestStatus = "EXECUTING"
	StatusCompleted                 RequestStatus = "COMPLETED"
	StatusRejected                  RequestStatus = "REJECTED"
	StatusCancelled                 RequestStatus = "CANCELLED"
)

type Venue string

const (
	VenueOnsite Venue = "ONSITE"
	VenueRemote Venue = "REMOTE"
)

type Urgency string

const (
	UrgencyNormal Urgency = "NORMAL"
	UrgencyUrgent Urgency = "URGENT"
)

type PresaleRequest struct {
	BaseModel
	RequestNo             string        `gorm:"size:32;not null"`
	OpportunityID         uint64        `gorm:"not null;index"`
	OpportunityNoSnapshot string        `gorm:"size:64;not null"`
	ApplicantID           string        `gorm:"size:64;not null;index"`
	ApplicantNameSnapshot string        `gorm:"size:128;not null"`
	Venue                 Venue         `gorm:"size:16;not null"`
	ServiceAddress        string        `gorm:"size:500"`
	ContactName           string        `gorm:"size:128;not null"`
	ContactPhoneCipher    []byte        `gorm:"type:varbinary(1024);not null"`
	ContactPhoneMasked    string        `gorm:"size:64;not null"`
	Description           string        `gorm:"type:text;not null"`
	ExpectedStart         time.Time     `gorm:"precision:3;not null"`
	ExpectedEnd           time.Time     `gorm:"precision:3;not null;index"`
	Urgency               Urgency       `gorm:"size:16;not null"`
	Status                RequestStatus `gorm:"size:32;not null;index"`
	CurrentApprovalNode   uint8         `gorm:"not null;default:0"`
	ExecutionDepartmentID string        `gorm:"size:64;not null"`
	ExecutionDepartment   string        `gorm:"size:128;not null"`
	RejectReason          string        `gorm:"size:2000"`
	CompletedAt           *time.Time    `gorm:"precision:3"`
	CancelledAt           *time.Time    `gorm:"precision:3"`
	CreateIdempotencyKey  string        `gorm:"size:128;not null"`
	CreateRequestHash     string        `gorm:"size:64;not null"`
}

func (PresaleRequest) TableName() string { return "crm_presale_requests" }

type ApprovalInstance struct {
	BaseModel
	RequestID        uint64     `gorm:"not null;uniqueIndex"`
	EngineInstanceID string     `gorm:"size:128;index"`
	Status           string     `gorm:"size:32;not null"`
	CurrentNode      uint8      `gorm:"not null;default:0"`
	LastEventSeq     uint64     `gorm:"not null;default:0"`
	PendingTaskID    string     `gorm:"size:128;not null"`
	PendingApprover  string     `gorm:"size:64;not null"`
	PendingAction    string     `gorm:"size:16;not null"`
	StartedAt        *time.Time `gorm:"precision:3"`
	FinishedAt       *time.Time `gorm:"precision:3"`
	RuleID           string     `gorm:"size:64;not null"`
	RuleVersion      uint64     `gorm:"not null;default:0"`
	NodesJSON        []byte     `gorm:"type:json"`
}

func (ApprovalInstance) TableName() string { return "crm_presale_approval_instances" }

type ApprovalLog struct {
	ID                   uint64    `gorm:"primaryKey;autoIncrement"`
	TenantID             string    `gorm:"size:64;not null;index"`
	RequestID            uint64    `gorm:"not null;index"`
	Node                 uint8     `gorm:"not null"`
	ApproverID           string    `gorm:"size:64;not null"`
	ApproverNameSnapshot string    `gorm:"size:128;not null"`
	Result               string    `gorm:"size:16;not null"`
	Comment              string    `gorm:"size:2000"`
	ApprovedAt           time.Time `gorm:"precision:3;not null"`
	EngineTaskID         string    `gorm:"size:128;not null"`
	EngineInstanceID     string    `gorm:"size:128;not null"`
	EventSequence        uint64    `gorm:"not null"`
	RequestIDTrace       string    `gorm:"size:64;not null"`
}

func (ApprovalLog) TableName() string { return "crm_presale_approval_logs" }

type Engineer struct {
	BaseModel
	PersonID        string    `gorm:"size:64;not null"`
	PersonName      string    `gorm:"size:128;not null"`
	Department      string    `gorm:"size:128"`
	Role            string    `gorm:"size:32;not null"`
	SkillTagsJSON   []byte    `gorm:"type:json"`
	ContactCipher   []byte    `gorm:"type:varbinary(1024)"`
	ValidFlag       bool      `gorm:"not null;index"`
	SourceUpdatedAt time.Time `gorm:"precision:3;not null"`
	SyncedAt        time.Time `gorm:"precision:3;not null"`
}

func (Engineer) TableName() string { return "crm_presale_engineers" }

type EngineerSyncState struct {
	BaseModel
	LastAttemptAt      *time.Time `gorm:"precision:3"`
	LastSuccessfulAt   *time.Time `gorm:"precision:3"`
	LastSourceRevision *time.Time `gorm:"precision:3"`
	NextSyncAt         time.Time  `gorm:"precision:3;not null;index"`
	LastJobNo          string     `gorm:"size:64"`
	LastPersonCount    uint32     `gorm:"not null;default:0"`
}

func (EngineerSyncState) TableName() string { return "crm_presale_engineer_sync_states" }

type EngineerSyncJob struct {
	BaseModel
	JobNo          string     `gorm:"size:64;not null"`
	TriggerType    string     `gorm:"size:16;not null"`
	RequestedBy    string     `gorm:"size:64;not null"`
	IdempotencyKey string     `gorm:"size:128;not null"`
	RequestHash    string     `gorm:"size:64;not null"`
	Status         string     `gorm:"size:16;not null;index"`
	RetryCount     uint8      `gorm:"not null;default:0"`
	NextRetryAt    *time.Time `gorm:"precision:3;index"`
	LockedBy       string     `gorm:"size:128"`
	LockedUntil    *time.Time `gorm:"precision:3;index"`
	LastError      string     `gorm:"size:1000"`
	SourceRevision *time.Time `gorm:"precision:3"`
	PersonCount    uint32     `gorm:"not null;default:0"`
	StartedAt      *time.Time `gorm:"precision:3"`
	FinishedAt     *time.Time `gorm:"precision:3"`
}

func (EngineerSyncJob) TableName() string { return "crm_presale_engineer_sync_jobs" }

// EngineerSyncRequest 单独保存“操作者 + 幂等键”与同步任务的绑定。
// 即使多次人工请求被合并到同一个活动任务，每个 HTTP 请求仍能获得稳定、可核验的重放结果。
type EngineerSyncRequest struct {
	BaseModel
	RequestedBy    string `gorm:"size:64;not null"`
	IdempotencyKey string `gorm:"size:128;not null"`
	RequestHash    string `gorm:"size:64;not null"`
	JobID          uint64 `gorm:"not null;index"`
	JobNo          string `gorm:"size:64;not null"`
}

func (EngineerSyncRequest) TableName() string { return "crm_presale_engineer_sync_requests" }

type Assignment struct {
	BaseModel
	RequestID                  uint64     `gorm:"not null;index"`
	AssigneeID                 string     `gorm:"size:64;not null;index"`
	AssigneeNameSnapshot       string     `gorm:"size:128;not null"`
	AssigneeDepartmentSnapshot string     `gorm:"size:128;not null"`
	AssigneeRole               string     `gorm:"size:32;not null"`
	AssignedBy                 string     `gorm:"size:64;not null"`
	AssignedAt                 time.Time  `gorm:"precision:3;not null"`
	EndedAt                    *time.Time `gorm:"precision:3"`
	IsCurrent                  bool       `gorm:"not null;index"`
	BatchNo                    uint64     `gorm:"not null"`
	ChangeReason               string     `gorm:"size:1000;not null"`
}

func (Assignment) TableName() string { return "crm_presale_assignments" }

const (
	AssignmentEventAdded   = "ADDED"
	AssignmentEventRemoved = "REMOVED"
)

// AssignmentEvent 是人员加入或退出当前执行集合时只追加、不改写的业务证据。
// 通知消费者依据该记录生成投影，而不信任 Outbox 中可被协议演进影响的展示文本。
type AssignmentEvent struct {
	ID                 uint64    `gorm:"primaryKey;autoIncrement"`
	EventID            string    `gorm:"size:64;not null;uniqueIndex"`
	TenantID           string    `gorm:"size:64;not null;index"`
	RequestID          uint64    `gorm:"not null;index"`
	AssignmentID       uint64    `gorm:"not null;index"`
	EventType          string    `gorm:"size:16;not null"`
	RecipientPersonID  string    `gorm:"size:64;not null;index"`
	PersonNameSnapshot string    `gorm:"size:128;not null"`
	RoleSnapshot       string    `gorm:"size:32;not null"`
	ChangeReason       string    `gorm:"size:1000;not null"`
	ActorID            string    `gorm:"size:64;not null"`
	RequestIDTrace     string    `gorm:"size:64;not null"`
	OccurredAt         time.Time `gorm:"precision:3;not null"`
}

func (AssignmentEvent) TableName() string { return "crm_presale_assignment_events" }

type ProgressLog struct {
	ID             uint64 `gorm:"primaryKey;autoIncrement"`
	TenantID       string `gorm:"size:64;not null;index"`
	RequestID      uint64 `gorm:"not null;index"`
	AuthorID       string `gorm:"size:64;not null"`
	Content        string `gorm:"type:text;not null"`
	LinkURL        string `gorm:"size:1000"`
	ProgressPct    *uint8
	IdempotencyKey string    `gorm:"size:128"`
	RequestHash    string    `gorm:"size:64"`
	CreatedAt      time.Time `gorm:"precision:3;not null"`
}

func (ProgressLog) TableName() string { return "crm_presale_progress_logs" }

const (
	ProgressRecipientUser      = "USER"
	ProgressRecipientPerson    = "PERSON"
	ProgressRecipientApplicant = "APPLICANT"
	ProgressRecipientAssignee  = "CURRENT_ASSIGNEE"
)

// ProgressNotificationEvent 固化进展发生时的个人收件人证据。
// 新申请的申请人和执行人都使用基础平台 user_id；命名空间字段保留用于兼容历史事件。
type ProgressNotificationEvent struct {
	ID                 uint64    `gorm:"primaryKey;autoIncrement"`
	EventID            string    `gorm:"size:64;not null;uniqueIndex"`
	TenantID           string    `gorm:"size:64;not null;index"`
	RequestID          uint64    `gorm:"not null;index"`
	ProgressID         uint64    `gorm:"not null;index"`
	AssignmentID       uint64    `gorm:"not null"`
	RecipientID        string    `gorm:"size:64;not null;index"`
	RecipientNamespace string    `gorm:"size:16;not null"`
	RecipientKind      string    `gorm:"size:32;not null"`
	AuthorUserID       string    `gorm:"size:64;not null"`
	AuthorPersonID     string    `gorm:"size:64;not null"`
	RequestIDTrace     string    `gorm:"size:64;not null"`
	OccurredAt         time.Time `gorm:"precision:3;not null"`
}

func (ProgressNotificationEvent) TableName() string {
	return "crm_presale_progress_notification_events"
}

type StatusLog struct {
	ID             uint64        `gorm:"primaryKey;autoIncrement"`
	TenantID       string        `gorm:"size:64;not null;index"`
	RequestID      uint64        `gorm:"not null;index"`
	FromStatus     RequestStatus `gorm:"size:32;not null"`
	ToStatus       RequestStatus `gorm:"size:32;not null"`
	Trigger        string        `gorm:"size:64;not null"`
	Reason         string        `gorm:"size:2000"`
	OperatorID     string        `gorm:"size:64;not null"`
	OccurredAt     time.Time     `gorm:"precision:3;not null"`
	RequestIDTrace string        `gorm:"size:64;not null"`
}

func (StatusLog) TableName() string { return "crm_presale_status_logs" }

// MutationReplay 协调审批、分派和撤销命令的幂等重放。
// 表内只保存绑定操作者、资源和操作语义的规范化摘要；业务事实仍以领域表和 Outbox 为准，
// 避免幂等记录成为第二份可漂移的业务数据。
type MutationReplay struct {
	ID              uint64    `gorm:"primaryKey;autoIncrement"`
	TenantID        string    `gorm:"size:64;not null;index"`
	RequestID       uint64    `gorm:"not null;index"`
	Operation       string    `gorm:"size:32;not null"`
	Action          string    `gorm:"size:32;not null"`
	ActorID         string    `gorm:"size:64;not null"`
	IdempotencyKey  string    `gorm:"size:128;not null"`
	RequestHash     string    `gorm:"size:64;not null"`
	ResponseVersion uint64    `gorm:"not null"`
	RequestIDTrace  string    `gorm:"size:64;not null"`
	CreatedAt       time.Time `gorm:"precision:3;not null"`
}

func (MutationReplay) TableName() string { return "crm_presale_mutation_replays" }

type PushStatus string

// PushStatus 保留旧字段和枚举以兼容存量数据。
// 新工时直接保存在 CRM 内并记为 SUCCESS，不再进入外部投递队列。
const (
	PushPending    PushStatus = "PENDING"
	PushSending    PushStatus = "SENDING"
	PushSuccess    PushStatus = "SUCCESS"
	PushRetryWait  PushStatus = "RETRY_WAIT"
	PushDeadLetter PushStatus = "DEAD_LETTER"
)

type Worklog struct {
	BaseModel
	WorklogNo          string     `gorm:"size:32;not null"`
	RequestID          uint64     `gorm:"not null;index"`
	PersonID           string     `gorm:"size:64;not null;index"`
	DepartmentSnapshot string     `gorm:"size:128"`
	PersonNameSnapshot string     `gorm:"size:128;not null"`
	WorkStart          time.Time  `gorm:"precision:3;not null"`
	WorkEnd            time.Time  `gorm:"precision:3;not null"`
	RawUnit            string     `gorm:"size:16;not null"`
	RawValue           string     `gorm:"type:decimal(10,2);not null"`
	ConversionFactor   string     `gorm:"type:decimal(10,2);not null"`
	WorkHours          string     `gorm:"type:decimal(10,2);not null"`
	Unit               string     `gorm:"size:16;not null;default:'HOUR'"`
	WorkSiteAddress    string     `gorm:"size:500;not null"`
	WorkContent        string     `gorm:"size:32;not null"`
	Remark             string     `gorm:"size:1000"`
	PushStatus         PushStatus `gorm:"size:16;not null;index"`
	PushAttempts       uint8      `gorm:"not null;default:0"`
	NextRetryAt        *time.Time `gorm:"precision:3;index"`
	LastErrorSummary   string     `gorm:"size:1000"`
	IdempotencyKey     string     `gorm:"size:128;not null"`
	RequestHash        string     `gorm:"size:64;not null"`
	CompletedTask      bool       `gorm:"not null;default:false"`
	VoidedAt           *time.Time `gorm:"precision:3"`
}

func (Worklog) TableName() string { return "crm_presale_worklogs" }

type OutboxEvent struct {
	ID               uint64     `gorm:"primaryKey;autoIncrement"`
	EventID          string     `gorm:"size:64;not null;uniqueIndex"`
	TenantID         string     `gorm:"size:64;not null;index"`
	EventType        string     `gorm:"size:64;not null;index"`
	AggregateType    string     `gorm:"size:64;not null"`
	AggregateID      string     `gorm:"size:64;not null;index"`
	Payload          []byte     `gorm:"type:json;not null"`
	Status           string     `gorm:"size:16;not null;index"`
	RetryCount       uint8      `gorm:"not null;default:0"`
	NextRetryAt      *time.Time `gorm:"precision:3;index"`
	LockedBy         string     `gorm:"size:128"`
	LockedUntil      *time.Time `gorm:"precision:3;index"`
	LastErrorSummary string     `gorm:"size:1000"`
	CreatedAt        time.Time  `gorm:"precision:3;not null"`
	SentAt           *time.Time `gorm:"precision:3"`
}

func (OutboxEvent) TableName() string { return "crm_outbox_events" }

type IntegrationAttempt struct {
	ID           uint64    `gorm:"primaryKey;autoIncrement"`
	TenantID     string    `gorm:"size:64;not null;index"`
	WorklogID    uint64    `gorm:"not null;index"`
	AttemptNo    uint8     `gorm:"not null"`
	Result       string    `gorm:"size:16;not null"`
	ErrorSummary string    `gorm:"size:1000"`
	ResponseCode string    `gorm:"size:32"`
	AttemptedAt  time.Time `gorm:"precision:3;not null"`
}

func (IntegrationAttempt) TableName() string { return "crm_integration_attempts" }

type NumberSequence struct {
	ID           uint64 `gorm:"primaryKey;autoIncrement"`
	TenantID     string `gorm:"size:64;not null;uniqueIndex:uq_presale_sequence"`
	SequenceType string `gorm:"size:16;not null;uniqueIndex:uq_presale_sequence"`
	SequenceDate string `gorm:"size:8;not null;uniqueIndex:uq_presale_sequence"`
	LastValue    uint32 `gorm:"not null"`
}

func (NumberSequence) TableName() string { return "crm_presale_number_sequences" }

type AlertType string

const (
	AlertApprovalNode1Overdue AlertType = "APPROVAL_NODE_1_OVERDUE"
	AlertApprovalNode2Overdue AlertType = "APPROVAL_NODE_2_OVERDUE"
	AlertAssignmentOverdue    AlertType = "ASSIGNMENT_OVERDUE"
	AlertExecutionDueSoon     AlertType = "EXECUTION_DUE_SOON"
	AlertExecutionOverdue     AlertType = "EXECUTION_OVERDUE"
)

type AlertRule struct {
	BaseModel
	Type           AlertType `gorm:"size:40;not null"`
	ThresholdHours uint32    `gorm:"not null"`
	Enabled        bool      `gorm:"not null"`
	ConfigVersion  uint64    `gorm:"not null;default:1"`
}

func (AlertRule) TableName() string { return "crm_presale_alert_rules" }

type Alert struct {
	BaseModel
	RequestID     uint64     `gorm:"not null;index"`
	AlertType     AlertType  `gorm:"size:40;not null"`
	RuleVersion   uint64     `gorm:"not null"`
	BasisAt       time.Time  `gorm:"precision:3;not null"`
	DueAt         time.Time  `gorm:"precision:3;not null"`
	Status        string     `gorm:"size:16;not null"`
	RecipientKind string     `gorm:"size:16;not null"`
	RecipientID   string     `gorm:"size:128;not null;index"`
	SentAt        *time.Time `gorm:"precision:3"`
	ReadAt        *time.Time `gorm:"precision:3"`
}

func (Alert) TableName() string { return "crm_presale_alerts" }
