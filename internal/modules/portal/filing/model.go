package filing

import (
	"time"

	"gorm.io/gorm"
)

const (
	FormVersion            = "2025.1"
	StatusDraft            = "DRAFT"
	StatusWaitingContract  = "WAITING_CONTRACT"
	StatusSubmitting       = "SUBMITTING"
	StatusSubmissionFailed = "SUBMISSION_FAILED"
	StatusSubmitted        = "SUBMITTED" // reached only with an immutable trusted provider receipt
	MaterialPendingUpload  = "PENDING_UPLOAD"
	MaterialFinalizing     = "FINALIZING"
	MaterialScanning       = "SCANNING"
	MaterialClean          = "CLEAN"
	MaterialRejected       = "REJECTED"
	MaterialScanFailed     = "SCAN_FAILED"
)

// Filing is the mutable head of one customer-scoped filing. Submitted content
// is never read from this mutable head; each submission has an immutable
// SubmissionSnapshot instead.
type Filing struct {
	ID                   uint64         `gorm:"primaryKey"`
	TenantID             string         `gorm:"size:64;not null;index"`
	CreatedBy            string         `gorm:"size:128;not null"`
	UpdatedBy            string         `gorm:"size:128;not null"`
	CreatedAt            time.Time      `gorm:"precision:3;not null"`
	UpdatedAt            time.Time      `gorm:"precision:3;not null"`
	DeletedAt            gorm.DeletedAt `gorm:"precision:3;index"`
	Version              uint64         `gorm:"not null;default:1"`
	PublicID             string         `gorm:"size:64;not null;uniqueIndex"`
	FilingNo             string         `gorm:"size:48;not null;uniqueIndex:uq_portal_filing_no,priority:2"`
	CustomerID           uint64         `gorm:"not null;index:idx_portal_filing_customer,priority:1;uniqueIndex:uq_portal_filing_create,priority:2"`
	AccountID            string         `gorm:"size:128;not null;uniqueIndex:uq_portal_filing_create,priority:3"`
	ProjectID            string         `gorm:"size:64;not null;default:''"`
	FormVersion          string         `gorm:"size:16;not null"`
	Status               string         `gorm:"size:24;not null;index:idx_portal_filing_customer,priority:2"`
	CurrentStep          uint8          `gorm:"not null;default:1"`
	CompletionPct        uint8          `gorm:"not null;default:0"`
	SubmittedAt          *time.Time     `gorm:"precision:3"`
	LockedAt             *time.Time     `gorm:"precision:3"`
	UnlockedAt           *time.Time     `gorm:"precision:3"`
	UnlockReasonCipher   []byte         `gorm:"type:mediumblob"`
	CreateIdempotencyKey string         `gorm:"size:128;not null;uniqueIndex:uq_portal_filing_create,priority:4"`
	CreateRequestHash    string         `gorm:"size:64;not null"`
}

func (Filing) TableName() string { return "portal_filings" }

// Section contains only the current validated-shape draft. Every replacement
// emits an append-only Action record with hash and encrypted result metadata.
type Section struct {
	ID               uint64    `gorm:"primaryKey;autoIncrement"`
	TenantID         string    `gorm:"size:64;not null;uniqueIndex:uq_portal_filing_section,priority:1"`
	FilingID         uint64    `gorm:"not null;uniqueIndex:uq_portal_filing_section,priority:2;index"`
	SectionCode      string    `gorm:"size:48;not null;uniqueIndex:uq_portal_filing_section,priority:3"`
	SchemaVersion    string    `gorm:"size:16;not null"`
	DataCipher       []byte    `gorm:"type:mediumblob;not null"`
	ValidationStatus string    `gorm:"size:16;not null"`
	UpdatedBy        string    `gorm:"size:128;not null"`
	CreatedAt        time.Time `gorm:"precision:3;not null"`
	UpdatedAt        time.Time `gorm:"precision:3;not null"`
	Version          uint64    `gorm:"not null;default:1"`
}

func (Section) TableName() string { return "portal_filing_sections" }

// MatrixSelection has exactly one row per matrix code. A revoked selection is
// retained with Selected=false so its optimistic version never resets.
type MatrixSelection struct {
	ID         uint64    `gorm:"primaryKey;autoIncrement"`
	TenantID   string    `gorm:"size:64;not null;uniqueIndex:uq_portal_filing_matrix,priority:1"`
	FilingID   uint64    `gorm:"not null;uniqueIndex:uq_portal_filing_matrix,priority:2;index"`
	MatrixCode string    `gorm:"size:48;not null;uniqueIndex:uq_portal_filing_matrix,priority:3"`
	RowCode    string    `gorm:"size:48;not null;default:''"`
	ColumnCode string    `gorm:"size:48;not null;default:''"`
	Selected   bool      `gorm:"not null;default:false"`
	UpdatedBy  string    `gorm:"size:128;not null"`
	CreatedAt  time.Time `gorm:"precision:3;not null"`
	UpdatedAt  time.Time `gorm:"precision:3;not null"`
	Version    uint64    `gorm:"not null;default:1"`
}

func (MatrixSelection) TableName() string { return "portal_filing_matrix" }

// SubmissionSnapshot is immutable application data. No repository update or
// delete method exists for it; an unlock creates a later snapshot on resubmit.
type SubmissionSnapshot struct {
	ID              uint64    `gorm:"primaryKey;autoIncrement"`
	TenantID        string    `gorm:"size:64;not null;uniqueIndex:uq_portal_filing_submission,priority:1"`
	FilingID        uint64    `gorm:"not null;uniqueIndex:uq_portal_filing_submission,priority:2;index"`
	Sequence        uint64    `gorm:"not null;uniqueIndex:uq_portal_filing_submission,priority:3"`
	FormVersion     string    `gorm:"size:16;not null"`
	CanonicalCipher []byte    `gorm:"type:longblob;not null"`
	SnapshotSHA256  string    `gorm:"size:64;not null"`
	SubmittedBy     string    `gorm:"size:128;not null"`
	SubmittedAt     time.Time `gorm:"precision:3;not null"`
}

func (SubmissionSnapshot) TableName() string { return "portal_filing_submission_snapshots" }

// SubmissionOutbox contains only a stable reference to the immutable Portal
// snapshot. It is not a police-system wire payload. Until a signed external
// contract is configured, WAITING_CONTRACT is an intentional fail-closed state
// and no worker may claim the row for delivery.
type SubmissionOutbox struct {
	ID               uint64     `gorm:"primaryKey;autoIncrement"`
	EventID          string     `gorm:"size:64;not null;uniqueIndex:uq_portal_filing_submission_event,priority:2"`
	TenantID         string     `gorm:"size:64;not null;uniqueIndex:uq_portal_filing_submission_event,priority:1"`
	FilingID         uint64     `gorm:"not null;index"`
	SubmissionID     uint64     `gorm:"not null;uniqueIndex:uq_portal_filing_submission_outbox,priority:2"`
	ContractVersion  string     `gorm:"size:48;not null"`
	Payload          []byte     `gorm:"type:json;not null"`
	PayloadSHA256    string     `gorm:"size:64;not null"`
	Status           string     `gorm:"size:32;not null;index"`
	RetryCount       uint32     `gorm:"not null;default:0"`
	NextRetryAt      *time.Time `gorm:"precision:3"`
	LockedBy         string     `gorm:"size:128;not null;default:''"`
	LockedUntil      *time.Time `gorm:"precision:3"`
	LastErrorSummary string     `gorm:"size:1000;not null;default:''"`
	CreatedAt        time.Time  `gorm:"precision:3;not null"`
	SentAt           *time.Time `gorm:"precision:3"`
}

func (SubmissionOutbox) TableName() string { return "portal_filing_submission_outbox" }

// SubmissionReceipt is immutable proof returned by a configured public-
// security submission provider. A locally locked snapshot never creates this
// row; only the delivery worker may persist it after provider verification.
type SubmissionReceipt struct {
	ID                     uint64    `gorm:"primaryKey;autoIncrement"`
	TenantID               string    `gorm:"size:64;not null;uniqueIndex:uq_portal_filing_receipt_submission,priority:1"`
	FilingID               uint64    `gorm:"not null;index"`
	SubmissionID           uint64    `gorm:"not null;uniqueIndex:uq_portal_filing_receipt_submission,priority:2"`
	EventID                string    `gorm:"size:64;not null"`
	ProviderReceiptID      string    `gorm:"size:128;not null;uniqueIndex:uq_portal_filing_receipt_external,priority:2"`
	ProviderAuthority      string    `gorm:"size:128;not null;uniqueIndex:uq_portal_filing_receipt_external,priority:1"`
	ProviderEvidenceCipher []byte    `gorm:"type:mediumblob;not null" json:"-"`
	ProviderEvidenceSHA256 string    `gorm:"size:64;not null"`
	ReceivedAt             time.Time `gorm:"precision:3;not null"`
	CreatedAt              time.Time `gorm:"precision:3;not null"`
}

func (SubmissionReceipt) TableName() string { return "portal_filing_submission_receipts" }

// Material stores an encrypted immutable object reference and scanner evidence;
// file bytes never enter MySQL. One row per material code prevents an older
// clean version from being confused with a newly uploaded unscanned version.
type Material struct {
	ID                 uint64     `gorm:"primaryKey;autoIncrement"`
	TenantID           string     `gorm:"size:64;not null;uniqueIndex:uq_portal_filing_material_public,priority:1;uniqueIndex:uq_portal_filing_material_code,priority:1;uniqueIndex:uq_portal_filing_material_create,priority:1"`
	PublicID           string     `gorm:"size:64;not null;uniqueIndex:uq_portal_filing_material_public,priority:2"`
	FilingID           uint64     `gorm:"not null;uniqueIndex:uq_portal_filing_material_code,priority:2"`
	MaterialCode       string     `gorm:"size:48;not null;uniqueIndex:uq_portal_filing_material_code,priority:3"`
	ObjectKeyCipher    []byte     `gorm:"type:varbinary(1024);not null" json:"-"`
	ObjectVersion      string     `gorm:"size:256;not null" json:"-"`
	FileName           string     `gorm:"size:255;not null"`
	MIMEType           string     `gorm:"size:160;not null"`
	SizeBytes          uint64     `gorm:"not null"`
	SHA256             string     `gorm:"size:64;not null"`
	ScanStatus         string     `gorm:"size:32;not null"`
	ScanReference      string     `gorm:"size:128;not null" json:"-"`
	FinalizeLeaseUntil *time.Time `gorm:"precision:3" json:"-"`
	CreateActorID      string     `gorm:"size:128;not null;uniqueIndex:uq_portal_filing_material_create,priority:2" json:"-"`
	CreateKeyHash      string     `gorm:"size:64;not null;uniqueIndex:uq_portal_filing_material_create,priority:3" json:"-"`
	CreateRequestHash  string     `gorm:"size:64;not null" json:"-"`
	UploadedAt         *time.Time `gorm:"precision:3"`
	ScannedAt          *time.Time `gorm:"precision:3"`
	CreatedBy          string     `gorm:"size:128;not null" json:"-"`
	UpdatedBy          string     `gorm:"size:128;not null" json:"-"`
	CreatedAt          time.Time  `gorm:"precision:3;not null"`
	UpdatedAt          time.Time  `gorm:"precision:3;not null"`
	Version            uint64     `gorm:"not null;default:1"`
}

func (Material) TableName() string { return "portal_filing_materials" }

// Action is an append-only audit and idempotency ledger. ResponseCipher holds
// the exact minimized successful response encrypted under the Portal data key.
type Action struct {
	ID             uint64    `gorm:"primaryKey;autoIncrement"`
	TenantID       string    `gorm:"size:64;not null;uniqueIndex:uq_portal_filing_action,priority:1"`
	FilingID       uint64    `gorm:"not null;uniqueIndex:uq_portal_filing_action,priority:2;index"`
	Action         string    `gorm:"size:48;not null;uniqueIndex:uq_portal_filing_action,priority:4"`
	ActorType      string    `gorm:"size:16;not null"`
	ActorID        string    `gorm:"size:128;not null;uniqueIndex:uq_portal_filing_action,priority:3"`
	IdempotencyKey string    `gorm:"size:128;not null;uniqueIndex:uq_portal_filing_action,priority:5"`
	RequestHash    string    `gorm:"size:64;not null"`
	RequestCipher  []byte    `gorm:"type:mediumblob;not null"`
	RequestID      string    `gorm:"size:128;not null;default:''"`
	ResponseCipher []byte    `gorm:"type:mediumblob;not null"`
	OccurredAt     time.Time `gorm:"precision:3;not null"`
}

func (Action) TableName() string { return "portal_filing_actions" }
