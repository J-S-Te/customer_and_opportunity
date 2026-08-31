package credit

import "time"

const (
	LevelA = "A"
	LevelB = "B"
	LevelC = "C"
	LevelD = "D"
)

type PaymentEvent struct {
	EventID      string     `json:"event_id" binding:"required,max=128"`
	PaymentID    string     `json:"payment_id" binding:"required,max=128"`
	CustomerID   uint64     `json:"customer_id" binding:"required"`
	DueDate      time.Time  `json:"due_date" binding:"required"`
	PaidDate     *time.Time `json:"paid_date"`
	DueAmount    string     `json:"due_amount" binding:"required"`
	PaidAmount   string     `json:"paid_amount" binding:"required"`
	ContractNo   string     `json:"contract_no" binding:"max=64"`
	PeriodNo     string     `json:"period_no" binding:"max=64"`
	SourceSystem string     `json:"source_system" binding:"max=64"`
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
	ID           uint64     `json:"id"`
	EventID      string     `json:"event_id"`
	PaymentID    string     `json:"payment_id"`
	Evaluation   string     `json:"evaluation"`
	GraceDays    int        `json:"grace_days"`
	DueDate      *time.Time `json:"due_date,omitempty"`
	PaidDate     *time.Time `json:"paid_date,omitempty"`
	DueAmount    string     `json:"due_amount"`
	PaidAmount   string     `json:"paid_amount"`
	ContractNo   string     `json:"contract_no,omitempty"`
	PeriodNo     string     `json:"period_no,omitempty"`
	SourceSystem string     `json:"source_system,omitempty"`
	IgnoreReason string     `json:"ignore_reason,omitempty"`
	EvaluatedAt  time.Time  `json:"evaluated_at"`
}

type creditPaymentRecord struct {
	ID                                                                      uint64 `gorm:"primaryKey"`
	TenantID, EventID, PaymentID, Evaluation                                string
	CustomerID                                                              uint64
	DueDate, PaidDate                                                       *time.Time
	DueAmount, PaidAmount, ContractNo, PeriodNo, SourceSystem, IgnoreReason string
	GraceDays                                                               int
	EvaluatedAt, CreatedAt                                                  time.Time
}

func (creditPaymentRecord) TableName() string { return "crm_customer_credit_payment_records" }

type CreditLog struct {
	ID                                                                 uint64 `gorm:"primaryKey"`
	TenantID                                                           string
	CustomerID                                                         uint64
	FromLevel, ToLevel, Source, Reason, EventID, PaymentID, OperatorID string
	ApplicationID                                                      uint64
	OccurredAt                                                         time.Time
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
	Reason         string `json:"reason"`
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
	// UpdatedAt is the snapshot timestamp returned by GET. It is required when
	// replacing an existing rule so concurrent administrator edits fail closed.
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
	Opinion string `json:"opinion"`
	Version uint64 `json:"version"`
}
