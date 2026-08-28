package credit

import "time"

const (
	LevelA = "A"
	LevelB = "B"
	LevelC = "C"
	LevelD = "D"
)

type PaymentEvent struct {
	EventID    string     `json:"event_id" binding:"required,max=128"`
	PaymentID  string     `json:"payment_id" binding:"required,max=128"`
	CustomerID uint64     `json:"customer_id" binding:"required"`
	DueDate    time.Time  `json:"due_date" binding:"required"`
	PaidDate   *time.Time `json:"paid_date"`
	DueAmount  string     `json:"due_amount" binding:"required"`
	PaidAmount string     `json:"paid_amount" binding:"required"`
}

type LevelResponse struct {
	CustomerID uint64     `json:"customer_id"`
	Level      string     `json:"credit_level"`
	UpdatedAt  *time.Time `json:"updated_at,omitempty"`
	Source     string     `json:"source"`
}

type PaymentRecord struct {
	ID          uint64    `json:"id"`
	EventID     string    `json:"event_id"`
	PaymentID   string    `json:"payment_id"`
	Evaluation  string    `json:"evaluation"`
	GraceDays   int       `json:"grace_days"`
	EvaluatedAt time.Time `json:"evaluated_at"`
}

type creditPaymentRecord struct {
	ID                                       uint64 `gorm:"primaryKey"`
	TenantID, EventID, PaymentID, Evaluation string
	CustomerID                               uint64
	DueDate, PaidDate                        *time.Time
	DueAmount, PaidAmount                    string
	GraceDays                                int
	EvaluatedAt, CreatedAt                   time.Time
}

func (creditPaymentRecord) TableName() string { return "crm_customer_credit_payment_records" }

type CreditLog struct {
	ID                                          uint64 `gorm:"primaryKey"`
	TenantID                                    string
	CustomerID                                  uint64
	FromLevel, ToLevel, Source, Reason, EventID string
	OccurredAt                                  time.Time
}

func (CreditLog) TableName() string { return "crm_customer_credit_logs" }

type Application struct {
	ID                                                                                uint64 `gorm:"primaryKey"`
	TenantID, ApplicantID, FromLevel, TargetLevel, Reason, Status, Opinion, DecidedBy string
	CustomerID                                                                        uint64
	DecidedAt                                                                         *time.Time
	CreatedAt, UpdatedAt                                                              time.Time
	Version                                                                           uint64
	PendingCustomerID                                                                 *uint64
}

func (Application) TableName() string { return "crm_customer_credit_applications" }

type ApplyRequest struct {
	TargetLevel string `json:"target_level"`
	Reason      string `json:"reason"`
}
type DecisionRequest struct {
	Opinion string `json:"opinion"`
	Version uint64 `json:"version"`
}
