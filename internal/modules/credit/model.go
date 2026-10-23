package credit

import "time"

const (
	LevelA = "A"
	LevelB = "B"
	LevelC = "C"
	LevelD = "D"
)

const (
	PaymentEventCompleted = "customer.credit.payment.completed.v1"
	PaymentEventReversed  = "customer.credit.payment.reversed.v1"
)

type PaymentEvent struct {
	EventID             string     `json:"event_id" binding:"required,max=128"`
	EventType           string     `json:"event_type"`
	PaymentID           string     `json:"payment_id" binding:"required,max=128"`
	ReversalOfPaymentID string     `json:"reversal_of_payment_id"`
	CustomerID          uint64     `json:"customer_id" binding:"required"`
	DueDate             time.Time  `json:"due_date" binding:"required"`
	PaidDate            *time.Time `json:"paid_date"`
	DueAmount           string     `json:"due_amount" binding:"required"`
	PaidAmount          string     `json:"paid_amount" binding:"required"`
	ContractNo          string     `json:"contract_no" binding:"max=64"`
	PeriodNo            string     `json:"period_no" binding:"max=64"`
	SourceSystem        string     `json:"source_system" binding:"max=64"`
}

type LevelResponse struct {
	CustomerID             uint64     `json:"customer_id"`
	Level                  string     `json:"credit_level"`
	UpdatedAt              *time.Time `json:"updated_at,omitempty"`
	Source                 string     `json:"source"`
	ConsecutiveOntimeCount uint32     `json:"consecutive_ontime_count"`
	ConsecutiveLateCount   uint32     `json:"consecutive_late_count"`
	LastLatePeriodNo       string     `json:"last_late_period_no,omitempty"`
	LastLatePaidAt         *time.Time `json:"last_late_paid_at,omitempty"`
	RecentLateCount        int64      `json:"recent_late_count"`
}

type PaymentRecord struct {
	ID                  uint64     `json:"id"`
	EventID             string     `json:"event_id"`
	PaymentID           string     `json:"payment_id"`
	EventType           string     `json:"event_type,omitempty"`
	ReversalOfPaymentID string     `json:"reversal_of_payment_id,omitempty"`
	Evaluation          string     `json:"evaluation"`
	GraceDays           int        `json:"grace_days"`
	DueDate             *time.Time `json:"due_date,omitempty"`
	PaidDate            *time.Time `json:"paid_date,omitempty"`
	DueAmount           string     `json:"due_amount"`
	PaidAmount          string     `json:"paid_amount"`
	ContractNo          string     `json:"contract_no,omitempty"`
	PeriodNo            string     `json:"period_no,omitempty"`
	SourceSystem        string     `json:"source_system,omitempty"`
	IgnoreReason        string     `json:"ignore_reason,omitempty"`
	EvaluatedAt         time.Time  `json:"evaluated_at"`
	ReversedAt          *time.Time `json:"reversed_at,omitempty"`
	ReversalEventID     string     `json:"reversal_event_id,omitempty"`
}

type creditPaymentRecord struct {
	ID                  uint64     `gorm:"primaryKey"`
	TenantID            string     `gorm:"column:tenant_id"`
	EventID             string     `gorm:"column:event_id"`
	PaymentID           string     `gorm:"column:payment_id"`
	EventType           string     `gorm:"column:event_type"`
	ReversalOfPaymentID string     `gorm:"column:reversal_of_payment_id"`
	CustomerID          uint64     `gorm:"column:customer_id"`
	DueDate             *time.Time `gorm:"column:due_date"`
	PaidDate            *time.Time `gorm:"column:paid_date"`
	DueAmount           string     `gorm:"column:due_amount"`
	PaidAmount          string     `gorm:"column:paid_amount"`
	ContractNo          string     `gorm:"column:contract_no"`
	PeriodNo            string     `gorm:"column:period_no"`
	SourceSystem        string     `gorm:"column:source_system"`
	Evaluation          string     `gorm:"column:evaluation"`
	IgnoreReason        string     `gorm:"column:ignore_reason"`
	GraceDays           int        `gorm:"column:grace_days"`
	PrevLevel           *string    `gorm:"column:prev_level"`
	PrevOntimeCount     *uint32    `gorm:"column:prev_ontime_count"`
	PrevLateCount       *uint32    `gorm:"column:prev_late_count"`
	PrevLastEvalAt      *time.Time `gorm:"column:prev_last_eval_at"`
	ResultLevel         *string    `gorm:"column:result_level"`
	ResultOntimeCount   *uint32    `gorm:"column:result_ontime_count"`
	ResultLateCount     *uint32    `gorm:"column:result_late_count"`
	ReversedAt          *time.Time `gorm:"column:reversed_at"`
	ReversalEventID     string     `gorm:"column:reversal_event_id"`
	EvaluatedAt         time.Time  `gorm:"column:evaluated_at"`
	CreatedAt           time.Time  `gorm:"column:created_at"`
}

func (creditPaymentRecord) TableName() string { return "crm_customer_credit_payment_records" }

type CreditLog struct {
	ID            uint64    `gorm:"primaryKey" json:"id"`
	TenantID      string    `json:"-"`
	CustomerID    uint64    `json:"customer_id"`
	FromLevel     string    `json:"from_level"`
	ToLevel       string    `json:"to_level"`
	Source        string    `json:"source"`
	Reason        string    `json:"reason,omitempty"`
	EventID       string    `json:"event_id,omitempty"`
	PaymentID     string    `json:"payment_id,omitempty"`
	OperatorID    string    `json:"operator_id,omitempty"`
	ApplicationID uint64    `json:"application_id,omitempty"`
	OccurredAt    time.Time `json:"occurred_at"`
}

func (CreditLog) TableName() string { return "crm_customer_credit_logs" }

type Application struct {
	ID                 uint64     `gorm:"primaryKey" json:"id"`
	TenantID           string     `json:"-"`
	ApplicantID        string     `json:"applicant_id"`
	IdempotencyKey     string     `json:"-"`
	FromLevel          string     `json:"from_level"`
	TargetLevel        string     `json:"target_level"`
	Reason             string     `json:"reason"`
	Status             string     `json:"status"`
	Opinion            string     `json:"opinion,omitempty"`
	DecidedBy          string     `json:"decided_by,omitempty"`
	CustomerID         uint64     `json:"customer_id"`
	ApprovalInstanceID uint64     `json:"approval_instance_id"`
	DecidedAt          *time.Time `json:"decided_at,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	Version            uint64     `json:"version"`
	PendingCustomerID  *uint64    `json:"-"`
}

func (Application) TableName() string { return "crm_customer_credit_applications" }

// PendingApplication 是销售总监审批待办的稳定 API 投影。
// 申请表只保存客户 ID，待办查询额外投影客户名称和编号，避免前端只能显示
// “客户 #ID”，也避免把 GORM 内部字段名直接暴露给浏览器。
type PendingApplication struct {
	ID                 uint64    `json:"id"`
	CustomerID         uint64    `json:"customer_id"`
	CustomerNo         string    `json:"customer_no,omitempty"`
	CustomerName       string    `json:"customer_name,omitempty"`
	ApplicantID        string    `json:"applicant_id"`
	FromLevel          string    `json:"from_level"`
	TargetLevel        string    `json:"target_level"`
	Reason             string    `json:"reason"`
	Status             string    `json:"status"`
	ApprovalInstanceID uint64    `json:"approval_instance_id"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
	Version            uint64    `json:"version"`
}

type ApplyRequest struct {
	TargetLevel    string `json:"target_level"`
	Reason         string `json:"reason" binding:"omitempty,max=500"`
	IdempotencyKey string `json:"-"`
}
type RuleSettings struct {
	TenantID        string    `gorm:"primaryKey" json:"-"`
	GraceDays       int       `json:"grace_days"`
	OnTimeThreshold int       `json:"on_time_threshold"`
	LateThreshold   int       `json:"late_threshold"`
	LevelStep       int       `json:"level_step"`
	Enabled         bool      `json:"enabled"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func (RuleSettings) TableName() string { return "crm_credit_rule_settings" }

type UpdateRuleSettingsRequest struct {
	GraceDays       int    `json:"grace_days"`
	OnTimeThreshold int    `json:"on_time_threshold"`
	LateThreshold   int    `json:"late_threshold"`
	LevelStep       int    `json:"level_step"`
	Enabled         bool   `json:"enabled"`
	Reason          string `json:"reason"`
	// UpdatedAt 是 GET 返回的快照时间戳。替换
	// 现有规则时必须提供，以在管理员并发编辑时安全拒绝冲突。
	UpdatedAt *time.Time `json:"updated_at"`
}
type Statistics struct {
	AttentionCustomers int64 `json:"attention_customers"`
	LevelC             int64 `json:"level_c"`
	LevelD             int64 `json:"level_d"`
}

type RuleSettingsVersion struct {
	ID              uint64    `json:"id"`
	TenantID        string    `json:"-"`
	GraceDays       int       `json:"grace_days"`
	OnTimeThreshold int       `json:"on_time_threshold"`
	LateThreshold   int       `json:"late_threshold"`
	LevelStep       int       `json:"level_step"`
	Enabled         bool      `json:"enabled"`
	ChangedBy       string    `json:"changed_by"`
	Reason          string    `json:"reason"`
	ChangedAt       time.Time `json:"changed_at"`
}

func (RuleSettingsVersion) TableName() string { return "crm_credit_rule_setting_versions" }

type ApprovalInstance struct {
	ID                                              uint64 `gorm:"primaryKey"`
	TenantID, BizType, Status, CreatedBy, DecidedBy string
	BusinessID, CurrentTaskID, Version              uint64
	DecidedAt                                       *time.Time
	CreatedAt, UpdatedAt                            time.Time
}

func (ApprovalInstance) TableName() string { return "crm_approval_instances" }

type ApprovalTask struct {
	ID                                                  uint64 `gorm:"primaryKey"`
	TenantID, TaskCode, AssigneeRole, Status, DecidedBy string
	InstanceID                                          uint64
	DecidedAt                                           *time.Time
	CreatedAt, UpdatedAt                                time.Time
}

func (ApprovalTask) TableName() string { return "crm_approval_tasks" }

type DecisionRequest struct {
	Opinion string `json:"opinion" binding:"omitempty,max=500"`
	Version uint64 `json:"version"`
}
