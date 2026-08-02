package projectexport

import (
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/modules/portal/project"
)

const (
	StatusPending    = "PENDING"
	StatusGenerating = "GENERATING"
	StatusReady      = "READY"
	StatusFailed     = "FAILED"
)

// Job is a durable, account-scoped request to render the exact project
// snapshot captured at request time. Payload never contains unmasked contact
// details or authorization credentials.
type Job struct {
	ID              uint64     `gorm:"primaryKey;autoIncrement"`
	PublicID        string     `gorm:"size:64;not null;uniqueIndex:uq_portal_project_export_public"`
	TenantID        string     `gorm:"size:64;not null;uniqueIndex:uq_portal_project_export_key,priority:1"`
	CustomerID      uint64     `gorm:"not null;uniqueIndex:uq_portal_project_export_key,priority:2"`
	AccountID       string     `gorm:"size:128;not null;uniqueIndex:uq_portal_project_export_key,priority:3"`
	ProjectID       string     `gorm:"size:64;not null;index"`
	IdempotencyKey  string     `gorm:"size:128;not null;uniqueIndex:uq_portal_project_export_key,priority:4"`
	RequestHash     string     `gorm:"size:64;not null"`
	SnapshotJSON    []byte     `gorm:"type:json;not null"`
	SourceUpdatedAt time.Time  `gorm:"precision:3;not null"`
	Status          string     `gorm:"size:16;not null;index"`
	FileName        string     `gorm:"size:255;not null;default:''"`
	FileHash        string     `gorm:"size:64;not null;default:''"`
	FileSize        int64      `gorm:"not null;default:0"`
	FileBytes       []byte     `gorm:"type:mediumblob"`
	FailureCode     string     `gorm:"size:64;not null;default:''"`
	LockedBy        string     `gorm:"size:128;not null;default:''"`
	LockedUntil     *time.Time `gorm:"precision:3"`
	CreatedAt       time.Time  `gorm:"precision:3;not null"`
	UpdatedAt       time.Time  `gorm:"precision:3;not null"`
	CompletedAt     *time.Time `gorm:"precision:3"`
	Version         uint64     `gorm:"not null;default:1"`
}

func (Job) TableName() string { return "portal_project_exports" }

type Grant struct {
	ID         uint64     `gorm:"primaryKey;autoIncrement"`
	PublicID   string     `gorm:"size:64;not null;uniqueIndex:uq_portal_project_export_grant_public"`
	TenantID   string     `gorm:"size:64;not null;index"`
	CustomerID uint64     `gorm:"not null;index"`
	AccountID  string     `gorm:"size:128;not null;index"`
	ExportID   uint64     `gorm:"not null;index"`
	TokenHash  string     `gorm:"size:64;not null;uniqueIndex:uq_portal_project_export_grant_token"`
	Status     string     `gorm:"size:16;not null"`
	ExpiresAt  time.Time  `gorm:"precision:3;not null;index"`
	CreatedAt  time.Time  `gorm:"precision:3;not null"`
	UsedAt     *time.Time `gorm:"precision:3"`
}

func (Grant) TableName() string { return "portal_project_export_grants" }

type Event struct {
	ID           uint64    `gorm:"primaryKey;autoIncrement"`
	TenantID     string    `gorm:"size:64;not null;index"`
	CustomerID   uint64    `gorm:"not null"`
	AccountID    string    `gorm:"size:128;not null"`
	ExportID     uint64    `gorm:"not null;index"`
	EventType    string    `gorm:"size:64;not null"`
	Result       string    `gorm:"size:16;not null"`
	ReasonCode   string    `gorm:"size:64;not null;default:''"`
	RequestTrace string    `gorm:"size:128;not null;default:''"`
	OccurredAt   time.Time `gorm:"precision:3;not null"`
}

func (Event) TableName() string { return "portal_project_export_events" }

// Capture carries the authorization scope alongside the immutable business
// snapshot. Snapshot's TenantID is intentionally hidden from normal JSON, so
// the worker must not infer scope from its public representation.
type Capture struct {
	TenantID   string         `json:"tenant_id"`
	CustomerID uint64         `json:"customer_id"`
	Detail     project.Detail `json:"detail"`
}
