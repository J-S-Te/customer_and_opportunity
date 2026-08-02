package feedback

import (
	"time"

	"gorm.io/gorm"
)

type Status string

const (
	StatusSubmitted        Status = "SUBMITTED"
	StatusAccepted         Status = "ACCEPTED"
	StatusProcessing       Status = "PROCESSING"
	StatusNeedCustomerInfo Status = "NEED_CUSTOMER_INFO"
	StatusResolved         Status = "RESOLVED"
	StatusClosed           Status = "CLOSED"
	StatusRejected         Status = "REJECTED"
)

// ActorModel mirrors the shared persistence fields but keeps Portal OIDC
// account actors at the signed 128-byte boundary. The shared CRM-oriented model
// uses 64 bytes for internal operator identifiers.
type ActorModel struct {
	ID        uint64         `gorm:"primaryKey" json:"id"`
	TenantID  string         `gorm:"size:64;not null;index" json:"-"`
	CreatedBy string         `gorm:"size:128;not null" json:"created_by"`
	UpdatedBy string         `gorm:"size:128;not null" json:"updated_by"`
	CreatedAt time.Time      `gorm:"precision:3" json:"created_at"`
	UpdatedAt time.Time      `gorm:"precision:3" json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"precision:3;index" json:"-"`
	Version   uint64         `gorm:"not null;default:1" json:"version"`
}

type Feedback struct {
	ActorModel
	PublicID              string     `gorm:"size:64;not null;uniqueIndex"`
	FeedbackNo            string     `gorm:"size:48;not null;uniqueIndex:uq_portal_feedback_no,priority:2"`
	CustomerID            uint64     `gorm:"not null;index:idx_portal_feedback_customer,priority:1"`
	AccountID             string     `gorm:"size:128;not null;index:idx_portal_feedback_customer,priority:2"`
	ProjectID             string     `gorm:"size:64;not null;default:''"`
	Type                  string     `gorm:"size:24;not null"`
	Title                 string     `gorm:"size:200;not null"`
	Description           string     `gorm:"type:text;not null"`
	ExpectedContactCipher []byte     `gorm:"type:varbinary(1024);not null" json:"-"`
	ExpectedContactMasked string     `gorm:"size:200;not null;default:''"`
	Status                Status     `gorm:"size:32;not null;index:idx_portal_feedback_sla,priority:1"`
	RejectReason          string     `gorm:"size:1000;not null;default:''"`
	SubmittedAt           time.Time  `gorm:"precision:3;not null"`
	FirstResponseDueAt    time.Time  `gorm:"precision:3;not null;index:idx_portal_feedback_sla,priority:2"`
	FirstRespondedAt      *time.Time `gorm:"precision:3"`
	ResolvedAt            *time.Time `gorm:"precision:3"`
	ClosedAt              *time.Time `gorm:"precision:3"`
	CreateIdempotencyKey  string     `gorm:"size:128;not null;uniqueIndex:uq_portal_feedback_create,priority:4" json:"-"`
	CreateRequestHash     string     `gorm:"size:64;not null" json:"-"`
}

func (Feedback) TableName() string { return "portal_feedbacks" }

type Message struct {
	ID             uint64    `gorm:"primaryKey;autoIncrement"`
	TenantID       string    `gorm:"size:64;not null;uniqueIndex:uq_portal_feedback_message_idempotency,priority:1"`
	FeedbackID     uint64    `gorm:"not null;index;uniqueIndex:uq_portal_feedback_message_idempotency,priority:2"`
	SenderType     string    `gorm:"size:16;not null;uniqueIndex:uq_portal_feedback_message_idempotency,priority:3"`
	SenderID       string    `gorm:"size:128;not null;uniqueIndex:uq_portal_feedback_message_idempotency,priority:4"`
	Content        string    `gorm:"type:text;not null"`
	Visibility     string    `gorm:"size:16;not null"`
	IdempotencyKey string    `gorm:"size:128;not null;uniqueIndex:uq_portal_feedback_message_idempotency,priority:5" json:"-"`
	RequestHash    string    `gorm:"size:64;not null" json:"-"`
	CreatedAt      time.Time `gorm:"precision:3;not null;index"`
}

func (Message) TableName() string { return "portal_feedback_messages" }

type StatusLog struct {
	ID             uint64    `gorm:"primaryKey;autoIncrement"`
	TenantID       string    `gorm:"size:64;not null;index;uniqueIndex:uq_portal_feedback_status_action,priority:1"`
	FeedbackID     uint64    `gorm:"not null;index"`
	FromStatus     Status    `gorm:"size:32;not null"`
	ToStatus       Status    `gorm:"size:32;not null"`
	Reason         string    `gorm:"size:1000;not null;default:''"`
	ActorType      string    `gorm:"size:16;not null"`
	ActorID        string    `gorm:"size:128;not null"`
	RequestID      string    `gorm:"size:64;not null;default:''"`
	IdempotencyKey *string   `gorm:"size:128;uniqueIndex:uq_portal_feedback_status_action,priority:2" json:"-"`
	RequestHash    string    `gorm:"size:64;not null;default:''" json:"-"`
	OccurredAt     time.Time `gorm:"precision:3;not null"`
}

func (StatusLog) TableName() string { return "portal_feedback_status_logs" }

type Escalation struct {
	ID         uint64    `gorm:"primaryKey;autoIncrement"`
	TenantID   string    `gorm:"size:64;not null;uniqueIndex:uq_portal_feedback_escalation,priority:1"`
	FeedbackID uint64    `gorm:"not null;uniqueIndex:uq_portal_feedback_escalation,priority:2"`
	Level      uint8     `gorm:"not null;uniqueIndex:uq_portal_feedback_escalation,priority:3"`
	Reason     string    `gorm:"size:64;not null"`
	SentAt     time.Time `gorm:"precision:3;not null"`
}

func (Escalation) TableName() string { return "portal_feedback_escalations" }

// Notification is the Portal-owned internal pending-work projection. It does
// not pretend that an external IM, email or customer-service platform accepted
// the alert.
type Notification struct {
	ID         uint64     `gorm:"primaryKey;autoIncrement"`
	TenantID   string     `gorm:"size:64;not null;index"`
	FeedbackID uint64     `gorm:"not null;uniqueIndex:uq_portal_feedback_notice,priority:1"`
	Kind       string     `gorm:"size:48;not null;uniqueIndex:uq_portal_feedback_notice,priority:2"`
	Status     string     `gorm:"size:16;not null;index"`
	CreatedAt  time.Time  `gorm:"precision:3;not null"`
	ReadAt     *time.Time `gorm:"precision:3"`
}

func (Notification) TableName() string { return "portal_feedback_notifications" }

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

func (Outbox) TableName() string { return "portal_feedback_outbox" }
