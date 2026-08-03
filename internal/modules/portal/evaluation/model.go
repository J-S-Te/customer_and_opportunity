package evaluation

import (
	"time"

	"gorm.io/gorm"
)

const (
	StatusSubmitted = "SUBMITTED"
	lowScoreRule    = "LOW_SCORE_V1"
)

// ServiceEvaluation 是客户对项目的一次不可变评价源记录；浏览器只看到 PublicID，不暴露数据库主键。
type ServiceEvaluation struct {
	ID                   uint64         `gorm:"primaryKey" json:"-"`
	TenantID             string         `gorm:"size:64;not null;uniqueIndex:uq_portal_evaluation_no,priority:1;uniqueIndex:uq_portal_evaluation_project,priority:1;uniqueIndex:uq_portal_evaluation_idempotency,priority:1" json:"-"`
	PublicID             string         `gorm:"size:64;not null;uniqueIndex" json:"-"`
	EvaluationNo         string         `gorm:"size:48;not null;uniqueIndex:uq_portal_evaluation_no,priority:2" json:"-"`
	CustomerID           uint64         `gorm:"not null;uniqueIndex:uq_portal_evaluation_project,priority:2;uniqueIndex:uq_portal_evaluation_idempotency,priority:2" json:"-"`
	AccountID            string         `gorm:"size:128;not null;index;uniqueIndex:uq_portal_evaluation_idempotency,priority:3" json:"-"`
	ProjectID            string         `gorm:"size:64;not null;uniqueIndex:uq_portal_evaluation_project,priority:3" json:"-"`
	ProfessionalScore    uint8          `gorm:"not null" json:"-"`
	ResponseScore        uint8          `gorm:"not null" json:"-"`
	ReportScore          uint8          `gorm:"not null" json:"-"`
	AttitudeScore        uint8          `gorm:"not null" json:"-"`
	TotalScore           uint8          `gorm:"not null" json:"-"`
	AverageScore         string         `gorm:"type:decimal(3,2);not null" json:"-"`
	Comment              string         `gorm:"type:text;not null" json:"-"`
	Status               string         `gorm:"size:24;not null;index" json:"-"`
	SubmittedAt          time.Time      `gorm:"precision:3;not null;index" json:"-"`
	CreateIdempotencyKey string         `gorm:"size:128;not null;uniqueIndex:uq_portal_evaluation_idempotency,priority:4" json:"-"`
	CreateRequestHash    string         `gorm:"size:64;not null" json:"-"`
	CreatedBy            string         `gorm:"size:64;not null" json:"-"`
	UpdatedBy            string         `gorm:"size:64;not null" json:"-"`
	CreatedAt            time.Time      `gorm:"precision:3" json:"-"`
	UpdatedAt            time.Time      `gorm:"precision:3" json:"-"`
	DeletedAt            gorm.DeletedAt `gorm:"precision:3;index" json:"-"`
	Version              uint64         `gorm:"not null;default:1" json:"-"`
}

func (ServiceEvaluation) TableName() string { return "portal_service_evaluations" }

// AuditLog 只追加；ActorID 用于审计，但不会出现在 Portal 客户响应中。
type AuditLog struct {
	ID           uint64    `gorm:"primaryKey;autoIncrement"`
	TenantID     string    `gorm:"size:64;not null;index"`
	EvaluationID uint64    `gorm:"not null;index"`
	Action       string    `gorm:"size:32;not null"`
	ActorID      string    `gorm:"size:128;not null"`
	RequestID    string    `gorm:"size:64;not null;default:''"`
	OccurredAt   time.Time `gorm:"precision:3;not null"`
}

func (AuditLog) TableName() string { return "portal_evaluation_audit_logs" }

type Alert struct {
	ID           uint64    `gorm:"primaryKey;autoIncrement"`
	TenantID     string    `gorm:"size:64;not null;uniqueIndex:uq_portal_evaluation_alert,priority:1"`
	EvaluationID uint64    `gorm:"not null;uniqueIndex:uq_portal_evaluation_alert,priority:2"`
	RuleCode     string    `gorm:"size:48;not null;uniqueIndex:uq_portal_evaluation_alert,priority:3"`
	Status       string    `gorm:"size:16;not null"`
	TriggeredAt  time.Time `gorm:"precision:3;not null"`
}

func (Alert) TableName() string { return "portal_evaluation_alerts" }

// Notification 是 Portal 自有的内部待办投影，不表示外部 IM、邮件或管理平台已经接收通知。
type Notification struct {
	ID           uint64     `gorm:"primaryKey;autoIncrement"`
	TenantID     string     `gorm:"size:64;not null;index;uniqueIndex:uq_portal_evaluation_notice,priority:1"`
	EvaluationID uint64     `gorm:"not null;uniqueIndex:uq_portal_evaluation_notice,priority:2"`
	Kind         string     `gorm:"size:48;not null;uniqueIndex:uq_portal_evaluation_notice,priority:3"`
	Status       string     `gorm:"size:16;not null;index"`
	CreatedAt    time.Time  `gorm:"precision:3;not null"`
	ReadAt       *time.Time `gorm:"precision:3"`
	ReadBy       string     `gorm:"size:128;not null;default:''"`
}

func (Notification) TableName() string { return "portal_evaluation_notifications" }

type Outbox struct {
	ID          uint64     `gorm:"primaryKey;autoIncrement"`
	EventID     string     `gorm:"size:64;not null;uniqueIndex"`
	TenantID    string     `gorm:"size:64;not null;index"`
	EventType   string     `gorm:"size:64;not null;index"`
	AggregateID uint64     `gorm:"not null;index"`
	Payload     []byte     `gorm:"type:json;not null"`
	Status      string     `gorm:"size:16;not null;index"`
	CreatedAt   time.Time  `gorm:"precision:3;not null"`
	SentAt      *time.Time `gorm:"precision:3"`
}

func (Outbox) TableName() string { return "portal_evaluation_outbox" }
