package report

import (
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
	"gorm.io/gorm"
)

// ActorModel mirrors the shared persistence fields for the two report records
// whose human actor is an external Portal OIDC account. Portal account/subject
// identifiers are bounded at 128 bytes, unlike the 64-byte internal-operator
// default used by database.Model. Keeping the GORM metadata aligned with
// migration 000041 prevents schema tooling and reviewers from inferring the
// obsolete width; this service still uses explicit migrations, not AutoMigrate.
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

type Status string

const (
	StatusSubmitted          Status = "SUBMITTED"
	StatusApproving          Status = "APPROVING"
	StatusApprovedProcessing Status = "APPROVED_PROCESSING"
	StatusIngestPending      Status = "INGEST_PENDING"
	StatusIssued             Status = "ISSUED"
	StatusRejected           Status = "REJECTED"
	StatusProcessingFailed   Status = "PROCESSING_FAILED"
)

type Request struct {
	ActorModel
	RequestNo           string     `gorm:"size:32;not null;uniqueIndex:uq_portal_report_no,priority:2"`
	ProjectID           string     `gorm:"size:64;not null;index"`
	CustomerID          uint64     `gorm:"not null;index"`
	AccountID           string     `gorm:"size:128;not null;index"`
	ReportType          string     `gorm:"size:64;not null"`
	Reason              string     `gorm:"size:2000;not null"`
	ReceiveEmailCipher  []byte     `gorm:"type:varbinary(1024)"`
	Status              Status     `gorm:"size:32;not null;index"`
	DownstreamRequestID string     `gorm:"size:128;index"`
	ApprovalResult      string     `gorm:"size:2000"`
	SubmittedAt         time.Time  `gorm:"precision:3;not null"`
	ApprovedAt          *time.Time `gorm:"precision:3"`
	IssuedAt            *time.Time `gorm:"precision:3"`
	IdempotencyKey      string     `gorm:"size:128;not null;uniqueIndex:uq_portal_report_idempotency,priority:2"`
	RequestHash         string     `gorm:"size:64;not null"`
	LastCallbackVersion uint64     `gorm:"not null;default:0"`
	LastCallbackKey     string     `gorm:"size:128;not null;default:''"`
	LastCallbackHash    string     `gorm:"size:64;not null;default:''"`
}

func (Request) TableName() string { return "portal_report_requests" }

// StatusEvent is the append-only audit projection used by the Portal report
// detail timeline. Actor and trace fields are retained for server-side audit,
// but are deliberately not exposed by the browser response.
type StatusEvent struct {
	ID            uint64    `gorm:"primaryKey;autoIncrement"`
	TenantID      string    `gorm:"size:64;not null"`
	CustomerID    uint64    `gorm:"not null"`
	RequestID     uint64    `gorm:"not null"`
	EventType     string    `gorm:"size:64;not null"`
	Sequence      uint64    `gorm:"not null"`
	FromStatus    Status    `gorm:"size:32;not null;default:''"`
	ToStatus      Status    `gorm:"size:32;not null"`
	ActorType     string    `gorm:"size:32;not null"`
	ActorID       string    `gorm:"size:128;not null;default:''"`
	SourceKeyHash string    `gorm:"size:64;not null"`
	PayloadHash   string    `gorm:"size:64;not null;default:''"`
	RequestTrace  string    `gorm:"size:128;not null;default:''"`
	OccurredAt    time.Time `gorm:"precision:3;not null"`
}

func (StatusEvent) TableName() string { return "portal_report_status_events" }

const (
	NotificationKindIssued = "REPORT_ISSUED"
	NotificationUnread     = "UNREAD"
	NotificationRead       = "READ"
)

// Notification is an account-scoped Portal station message. It is created
// only after trusted report ingestion succeeds; it does not imply email or
// external-message delivery.
type Notification struct {
	ID         uint64     `gorm:"primaryKey;autoIncrement;uniqueIndex:uq_portal_report_notification_scope,priority:3"`
	TenantID   string     `gorm:"size:64;not null;uniqueIndex:uq_portal_report_notification_scope,priority:1"`
	CustomerID uint64     `gorm:"not null;uniqueIndex:uq_portal_report_notification_scope,priority:2"`
	RequestID  uint64     `gorm:"not null;uniqueIndex:uq_portal_report_notification_scope,priority:4"`
	AccountID  string     `gorm:"size:128;not null;uniqueIndex:uq_portal_report_notification_scope,priority:5"`
	Kind       string     `gorm:"size:32;not null"`
	Status     string     `gorm:"size:16;not null"`
	CreatedAt  time.Time  `gorm:"precision:3;not null"`
	ReadAt     *time.Time `gorm:"precision:3"`
}

func (Notification) TableName() string { return "portal_report_notifications" }

// NotificationReadEvent is append-only evidence for the first successful
// UNREAD-to-READ transition. Exact retries do not append another event.
type NotificationReadEvent struct {
	ID             uint64    `gorm:"primaryKey;autoIncrement"`
	TenantID       string    `gorm:"size:64;not null"`
	CustomerID     uint64    `gorm:"not null"`
	NotificationID uint64    `gorm:"not null;uniqueIndex:uq_portal_report_notification_first_read"`
	RequestID      uint64    `gorm:"not null"`
	AccountID      string    `gorm:"size:128;not null"`
	RequestTrace   string    `gorm:"size:128;not null;default:''"`
	OccurredAt     time.Time `gorm:"precision:3;not null"`
}

func (NotificationReadEvent) TableName() string {
	return "portal_report_notification_read_events"
}

type File struct {
	database.Model
	RequestID           uint64     `gorm:"not null;uniqueIndex"`
	ObjectKeyCipher     []byte     `gorm:"type:varbinary(1024);not null"`
	ObjectVersion       string     `gorm:"size:256;not null"`
	FileName            string     `gorm:"size:255;not null"`
	MIME                string     `gorm:"size:128;not null"`
	Size                int64      `gorm:"not null"`
	FileHash            string     `gorm:"size:128;not null"`
	EncryptionKeyRef    string     `gorm:"size:255;not null"`
	EncryptionAlgorithm string     `gorm:"size:32;not null"`
	ScanStatus          string     `gorm:"size:16;not null"`
	ScanReference       string     `gorm:"size:128;not null"`
	ScannedAt           *time.Time `gorm:"precision:3"`
	WatermarkStatus     string     `gorm:"size:32;not null"`
}

func (File) TableName() string { return "portal_report_files" }

const (
	IngestPending    = "PENDING"
	IngestProcessing = "PROCESSING"
	IngestRetryWait  = "RETRY_WAIT"
	IngestCompleted  = "COMPLETED"
	IngestDeadLetter = "DEAD_LETTER"
)

// IngestJob is the durable boundary between a trusted project-service
// callback and object retrieval, malware scanning and envelope encryption.
// DescriptorCipher never stores the upstream object reference in plaintext.
type IngestJob struct {
	ID               uint64     `gorm:"primaryKey;autoIncrement"`
	EventID          string     `gorm:"size:64;not null;uniqueIndex"`
	TenantID         string     `gorm:"size:64;not null;index"`
	CustomerID       uint64     `gorm:"not null"`
	RequestID        uint64     `gorm:"not null;uniqueIndex"`
	DescriptorCipher []byte     `gorm:"type:varbinary(2048);not null"`
	DescriptorHash   string     `gorm:"size:64;not null"`
	Status           string     `gorm:"size:16;not null;index"`
	RetryCount       uint8      `gorm:"not null;default:0"`
	NextRetryAt      *time.Time `gorm:"precision:3"`
	LockedBy         string     `gorm:"size:128;not null;default:''"`
	LockedUntil      *time.Time `gorm:"precision:3"`
	LastErrorSummary string     `gorm:"size:1000;not null;default:''"`
	CreatedAt        time.Time  `gorm:"precision:3;not null"`
	CompletedAt      *time.Time `gorm:"precision:3"`
}

func (IngestJob) TableName() string { return "portal_report_ingest_jobs" }

type GrantStatus string

const (
	GrantActive  GrantStatus = "ACTIVE"
	GrantExpired GrantStatus = "EXPIRED"
	GrantFrozen  GrantStatus = "FROZEN"
	GrantRevoked GrantStatus = "REVOKED"
)

// Grant stores only a digest of the high-entropy download credential. The
// plaintext credential exists only in the successful create response.
type Grant struct {
	ActorModel
	PublicID       string      `gorm:"size:64;not null;uniqueIndex:uq_portal_report_grant_public,priority:2"`
	CustomerID     uint64      `gorm:"not null;index"`
	RequestID      uint64      `gorm:"not null;index"`
	AccountID      string      `gorm:"size:128;not null;index"`
	TokenHash      string      `gorm:"size:64;not null;uniqueIndex:uq_portal_report_grant_token"`
	IssueKeyHash   string      `gorm:"size:64;not null"`
	Status         GrantStatus `gorm:"size:16;not null;index"`
	ActiveSlot     *string     `gorm:"size:8"`
	ExpiresAt      time.Time   `gorm:"precision:3;not null;index"`
	DownloadCount  uint64      `gorm:"not null;default:0"`
	RiskState      string      `gorm:"size:32;not null;default:''"`
	LastDownloadAt *time.Time  `gorm:"precision:3"`
}

func (Grant) TableName() string { return "portal_report_grants" }

// DownloadEvent is append-only. It deliberately stores only keyed digests of
// network/device metadata and never stores a grant token or object reference.
type DownloadEvent struct {
	ID              uint64    `gorm:"primaryKey;autoIncrement"`
	TenantID        string    `gorm:"size:64;not null"`
	CustomerID      uint64    `gorm:"not null"`
	RequestID       uint64    `gorm:"not null"`
	GrantID         *uint64   `gorm:"index"`
	AccountID       string    `gorm:"size:128;not null;default:''"`
	EventType       string    `gorm:"size:64;not null"`
	Result          string    `gorm:"size:32;not null"`
	ReasonCode      string    `gorm:"size:64;not null;default:''"`
	IPHash          string    `gorm:"size:64;not null;default:''"`
	DeviceHash      string    `gorm:"size:64;not null;default:''"`
	TrackingDigest  string    `gorm:"size:64;not null;default:'';index"`
	RequestTrace    string    `gorm:"size:128;not null;default:''"`
	IdempotencyHash string    `gorm:"size:64;not null;default:''"`
	DedupeKey       *string   `gorm:"size:64;uniqueIndex:uq_portal_report_download_dedupe"`
	OccurredAt      time.Time `gorm:"precision:3;not null"`
}

func (DownloadEvent) TableName() string { return "portal_report_download_events" }

const (
	RiskAlertOpen     = "OPEN"
	RiskAlertResolved = "RESOLVED"

	RiskActionUnfreeze         = "UNFREEZE"
	RiskActionRevokeAndReissue = "REVOKE_AND_REISSUE"
)

// RiskAlert is both the immutable detection evidence and the account-scoped
// station alert for a grant frozen by a trusted risk policy. active_slot
// prevents concurrent duplicate OPEN alerts for one grant while still
// allowing a later, independently reviewed incident on the same grant.
type RiskAlert struct {
	ID               uint64     `gorm:"primaryKey;autoIncrement"`
	PublicID         string     `gorm:"size:64;not null;uniqueIndex:uq_portal_report_risk_alert_public,priority:2"`
	TenantID         string     `gorm:"size:64;not null;uniqueIndex:uq_portal_report_risk_alert_public,priority:1"`
	CustomerID       uint64     `gorm:"not null;index"`
	RequestID        uint64     `gorm:"not null;index"`
	GrantID          uint64     `gorm:"not null;index"`
	AccountID        string     `gorm:"size:128;not null;index"`
	RiskCode         string     `gorm:"size:64;not null"`
	Status           string     `gorm:"size:16;not null;index"`
	ActiveSlot       *string    `gorm:"size:8"`
	DetectedAt       time.Time  `gorm:"precision:3;not null"`
	AcknowledgedAt   *time.Time `gorm:"precision:3"`
	ResolvedAt       *time.Time `gorm:"precision:3"`
	ResolvedBy       string     `gorm:"size:128;not null;default:''"`
	ResolutionAction string     `gorm:"size:32;not null;default:''"`
	ResolutionReason string     `gorm:"size:500;not null;default:''"`
	RequestTrace     string     `gorm:"size:128;not null;default:''"`
	Version          uint64     `gorm:"not null;default:1"`
}

func (RiskAlert) TableName() string { return "portal_report_risk_alerts" }

// RiskReviewEvent is append-only operator evidence. The idempotency and
// payload digests let an exact retry succeed without reactivating or revoking
// a grant twice, while conflicting reuse is rejected.
type RiskReviewEvent struct {
	ID              uint64    `gorm:"primaryKey;autoIncrement"`
	TenantID        string    `gorm:"size:64;not null;uniqueIndex:uq_portal_report_risk_review_key,priority:1"`
	AlertID         uint64    `gorm:"not null;index"`
	ActorID         string    `gorm:"size:128;not null;uniqueIndex:uq_portal_report_risk_review_key,priority:2"`
	Action          string    `gorm:"size:32;not null"`
	IdempotencyHash string    `gorm:"size:64;not null;uniqueIndex:uq_portal_report_risk_review_key,priority:3"`
	PayloadHash     string    `gorm:"size:64;not null"`
	RequestTrace    string    `gorm:"size:128;not null;default:''"`
	OccurredAt      time.Time `gorm:"precision:3;not null"`
}

func (RiskReviewEvent) TableName() string { return "portal_report_risk_review_events" }

type Outbox struct {
	ID               uint64     `gorm:"primaryKey;autoIncrement"`
	EventID          string     `gorm:"size:64;not null;uniqueIndex"`
	TenantID         string     `gorm:"size:64;not null;index"`
	EventType        string     `gorm:"size:64;not null;index"`
	AggregateID      uint64     `gorm:"not null;index"`
	Payload          []byte     `gorm:"type:json;not null"`
	Status           string     `gorm:"size:16;not null;index"`
	RetryCount       uint8      `gorm:"not null;default:0"`
	NextRetryAt      *time.Time `gorm:"precision:3"`
	LockedBy         string     `gorm:"size:128;not null;default:''"`
	LockedUntil      *time.Time `gorm:"precision:3"`
	LastErrorSummary string     `gorm:"size:1000;not null;default:''"`
	CreatedAt        time.Time  `gorm:"precision:3;not null"`
	SentAt           *time.Time `gorm:"precision:3"`
}

func (Outbox) TableName() string { return "portal_report_outbox" }
