// Package requestaudit persists one immutable delivery identity for every HTTP
// request and forwards completed records to the base platform audit service.
package requestaudit

import "time"

const (
	StatusStarted    = "STARTED"
	StatusPending    = "PENDING"
	StatusProcessing = "PROCESSING"
	StatusRetry      = "RETRY"
	StatusDelivered  = "DELIVERED"
)

// Record deliberately contains only an allow-listed operation summary. Raw
// paths, query strings, headers, cookies, request bodies and response bodies
// must never be persisted in the request audit outbox.
type Record struct {
	ID              uint64     `gorm:"primaryKey"`
	EventID         string     `gorm:"size:64;not null;uniqueIndex"`
	TenantID        string     `gorm:"size:64;not null;index"`
	ApplicationCode string     `gorm:"size:64;not null"`
	EnvironmentCode string     `gorm:"size:64;not null"`
	ActorType       string     `gorm:"size:16;not null"`
	ActorID         string     `gorm:"size:128;not null"`
	ActorName       string     `gorm:"size:200;not null"`
	Action          string     `gorm:"size:128;not null"`
	ResourceType    string     `gorm:"size:64;not null"`
	ResourceID      string     `gorm:"size:128;not null"`
	RequestID       string     `gorm:"size:128;not null;index"`
	Method          string     `gorm:"size:16;not null"`
	Route           string     `gorm:"size:500;not null"`
	HTTPStatus      int        `gorm:"not null"`
	Result          string     `gorm:"size:16;not null"`
	ReasonCode      string     `gorm:"size:128;not null"`
	RiskLevel       string     `gorm:"size:16;not null"`
	DeliveryStatus  string     `gorm:"size:16;not null;index:idx_request_audit_delivery,priority:1"`
	Attempts        uint32     `gorm:"not null"`
	NextAttemptAt   *time.Time `gorm:"precision:3;index:idx_request_audit_delivery,priority:2"`
	LockedBy        string     `gorm:"size:128;not null"`
	LockedUntil     *time.Time `gorm:"precision:3"`
	LastErrorCode   string     `gorm:"size:128;not null"`
	OccurredAt      time.Time  `gorm:"precision:3;not null"`
	CompletedAt     *time.Time `gorm:"precision:3"`
	DeliveredAt     *time.Time `gorm:"precision:3"`
	CreatedAt       time.Time  `gorm:"precision:3;not null"`
	UpdatedAt       time.Time  `gorm:"precision:3;not null"`
}

func (Record) TableName() string { return "application_request_audit_outbox" }

type Start struct {
	EventID, TenantID, ApplicationCode, EnvironmentCode, RequestID, Method string
	OccurredAt                                                             time.Time
}

type Completion struct {
	ActorType, ActorID, ActorName, Action, Route string
	HTTPStatus                                   int
	Result, ReasonCode, RiskLevel                string
	CompletedAt                                  time.Time
}

// BusinessEvent is an allow-listed business mutation summary. Detailed before
// and after snapshots remain in the subsystem's protected audit table.
type BusinessEvent struct {
	EventID, TenantID, ApplicationCode, EnvironmentCode string
	ActorType, ActorID, ActorName                       string
	Action, ResourceType, ResourceID, RequestID         string
	Result, ReasonCode, RiskLevel                       string
	OccurredAt                                          time.Time
}
