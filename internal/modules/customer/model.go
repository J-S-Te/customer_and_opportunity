package customer

import (
	"time"

	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
)

const (
	StatusActive = "ACTIVE"
	StatusVoid   = "VOID"
	StatusMerged = "MERGED"
)

type Customer struct {
	database.Model
	CustomerNo              string     `gorm:"size:32;not null;uniqueIndex:uk_customer_no,priority:2"`
	Name                    string     `gorm:"size:200;not null"`
	NormalizedName          string     `gorm:"size:200;not null;index"`
	UnifiedCreditCodeCipher []byte     `gorm:"type:varbinary(512)"`
	UnifiedCreditCodeHMAC   *string    `gorm:"size:64;uniqueIndex:uk_customer_credit,priority:2"`
	CustomerType            string     `gorm:"size:64;not null"`
	Industry                string     `gorm:"size:64;not null"`
	Region                  string     `gorm:"size:64;not null"`
	OwnerUserID             string     `gorm:"size:64;not null;index"`
	OwnerOrgID              string     `gorm:"size:64;not null;index"`
	Status                  string     `gorm:"size:32;not null;index"`
	EndDate                 *time.Time `gorm:"type:date"`
	MergedIntoID            *uint64
	Contacts                []Contact `gorm:"foreignKey:CustomerID"`
}

func (Customer) TableName() string { return "crm_customers" }

type Contact struct {
	database.Model
	CustomerID     uint64 `gorm:"not null;index"`
	Name           string `gorm:"size:100;not null"`
	PhoneCipher    []byte `gorm:"type:varbinary(512);not null"`
	PhoneMasked    string `gorm:"size:32;not null"`
	EmailCipher    []byte `gorm:"type:varbinary(512)"`
	EmailMasked    string `gorm:"size:200"`
	IsRegistration bool   `gorm:"not null;index"`
	SortOrder      int    `gorm:"not null"`
}

func (Contact) TableName() string { return "crm_customer_contacts" }

// Stakeholder is a customer-scoped key participant. Phone and email are
// encrypted independently; only their masked companions may leave the module.
type Stakeholder struct {
	database.Model
	CustomerID          uint64 `gorm:"not null;index"`
	Name                string `gorm:"size:100;not null"`
	RoleTitle           string `gorm:"size:100;not null"`
	Influence           string `gorm:"size:16;not null"`
	RelationshipSummary string `gorm:"size:500;not null"`
	PhoneCipher         []byte `gorm:"type:varbinary(512)"`
	PhoneMasked         string `gorm:"size:32;not null"`
	EmailCipher         []byte `gorm:"type:varbinary(512)"`
	EmailMasked         string `gorm:"size:200;not null"`
	SortOrder           int    `gorm:"not null"`
}

func (Stakeholder) TableName() string { return "crm_customer_stakeholders" }

// InformationSystem describes a customer's protected information system. Its
// protection level is an MLPS system level and is unrelated to customer credit.
type InformationSystem struct {
	database.Model
	CustomerID          uint64     `gorm:"not null;index"`
	Name                string     `gorm:"size:200;not null"`
	ProtectionLevel     string     `gorm:"size:16;not null"`
	ApplicationScenario string     `gorm:"size:500;not null"`
	FilingNo            string     `gorm:"size:100;not null"`
	GradingDate         *time.Time `gorm:"type:date"`
	FilingStatus        string     `gorm:"size:16;not null"`
	SortOrder           int        `gorm:"not null"`
}

func (InformationSystem) TableName() string { return "crm_customer_systems" }

type Followup struct {
	database.Model
	CustomerID   uint64     `gorm:"not null;index"`
	Type         string     `gorm:"size:32;not null"`
	Content      string     `gorm:"type:text;not null"`
	FollowedAt   time.Time  `gorm:"precision:3;not null;index"`
	FollowedBy   string     `gorm:"size:64;not null"`
	NextFollowAt *time.Time `gorm:"precision:3"`
}

func (Followup) TableName() string { return "crm_customer_followups" }

// ChangeLog is an append-only, field-oriented customer audit companion. Sensitive
// before/after values must already be masked or hashed before persistence.
type ChangeLog struct {
	ID         uint64    `gorm:"primaryKey"`
	TenantID   string    `gorm:"size:64;not null;index"`
	CustomerID uint64    `gorm:"not null;index"`
	FieldName  string    `gorm:"size:128;not null"`
	BeforeJSON []byte    `gorm:"type:json"`
	AfterJSON  []byte    `gorm:"type:json"`
	Reason     string    `gorm:"size:500;not null"`
	OperatorID string    `gorm:"size:64;not null"`
	RequestID  string    `gorm:"size:64;not null;index"`
	OccurredAt time.Time `gorm:"precision:3;not null"`
}

func (ChangeLog) TableName() string { return "crm_customer_change_logs" }

// MergeLog is the immutable business record for a committed customer merge.
// MigratedCountsJSON contains aggregate counts only and must never contain
// contact details or other sensitive customer data.
type MergeLog struct {
	ID                 uint64    `gorm:"primaryKey;autoIncrement"`
	TenantID           string    `gorm:"size:64;not null;index"`
	SourceCustomerID   uint64    `gorm:"not null;index"`
	TargetCustomerID   uint64    `gorm:"not null;index"`
	SourceVersion      uint64    `gorm:"not null"`
	TargetVersion      uint64    `gorm:"not null"`
	MigratedCountsJSON []byte    `gorm:"type:json;not null"`
	Reason             string    `gorm:"size:500;not null"`
	OperatorID         string    `gorm:"size:64;not null"`
	RequestID          string    `gorm:"size:64;not null;index"`
	OccurredAt         time.Time `gorm:"precision:3;not null"`
}

func (MergeLog) TableName() string { return "crm_customer_merge_logs" }

// MergeIdempotency binds a key to one actor and one canonical request. The
// response is persisted in the same transaction as the merge for safe replay.
type MergeIdempotency struct {
	ID           uint64    `gorm:"primaryKey;autoIncrement"`
	TenantID     string    `gorm:"size:64;not null;uniqueIndex:uq_customer_merge_idempotency,priority:1"`
	ActorID      string    `gorm:"size:64;not null;uniqueIndex:uq_customer_merge_idempotency,priority:2"`
	Key          string    `gorm:"column:idempotency_key;size:128;not null;uniqueIndex:uq_customer_merge_idempotency,priority:3"`
	RequestHash  string    `gorm:"size:64;not null"`
	ResponseJSON []byte    `gorm:"type:json"`
	CreatedAt    time.Time `gorm:"precision:3;not null"`
}

func (MergeIdempotency) TableName() string { return "crm_customer_merge_idempotency" }

// CreateIdempotency is the durable, append-only replay record for interactive
// customer creation. RequestHash is a digest of a canonical request whose
// sensitive values were first protected with the deployment HMAC key.
// ResponseJSON contains only the already-masked public Response DTO.
type CreateIdempotency struct {
	ID           uint64    `gorm:"primaryKey;autoIncrement"`
	TenantID     string    `gorm:"size:64;not null;uniqueIndex:uq_customer_create_idempotency,priority:1"`
	ActorID      string    `gorm:"size:64;not null;uniqueIndex:uq_customer_create_idempotency,priority:2"`
	Key          string    `gorm:"column:idempotency_key;size:128;not null;uniqueIndex:uq_customer_create_idempotency,priority:3"`
	RequestHash  string    `gorm:"size:64;not null"`
	CustomerID   uint64    `gorm:"not null;index"`
	Status       string    `gorm:"size:16;not null"`
	ResponseJSON []byte    `gorm:"type:json;not null"`
	ResponseHash string    `gorm:"size:64;not null"`
	CreatedAt    time.Time `gorm:"precision:3;not null"`
}

func (CreateIdempotency) TableName() string { return "crm_customer_create_idempotency" }

// MergeOutboxEvent is the customer module's view of the shared CRM outbox.
type MergeOutboxEvent struct {
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
	LastErrorSummary string     `gorm:"size:1000;not null"`
	CreatedAt        time.Time  `gorm:"precision:3;not null"`
	SentAt           *time.Time `gorm:"precision:3"`
}

func (MergeOutboxEvent) TableName() string { return "crm_outbox_events" }

type ImportJob struct {
	ID                   uint64     `gorm:"primaryKey;autoIncrement"`
	TenantID             string     `gorm:"size:64;not null"`
	JobNo                string     `gorm:"size:64;not null"`
	ActorID              string     `gorm:"size:64;not null"`
	Status               string     `gorm:"size:32;not null"`
	Reason               string     `gorm:"size:500;not null"`
	TotalRows            uint32     `gorm:"not null"`
	ImportableRows       uint32     `gorm:"not null"`
	WarningRows          uint32     `gorm:"not null"`
	ErrorRows            uint32     `gorm:"not null"`
	SucceededRows        uint32     `gorm:"not null"`
	FailedRows           uint32     `gorm:"not null"`
	ExpiresAt            time.Time  `gorm:"precision:3;not null"`
	CompletedAt          *time.Time `gorm:"precision:3"`
	CommitRequestVersion uint64     `gorm:"not null"`
	CommitIdempotencyKey string     `gorm:"size:128;not null"`
	LockedBy             string     `gorm:"size:128;not null"`
	LockedUntil          *time.Time `gorm:"precision:3"`
	CreatedAt            time.Time  `gorm:"precision:3;not null"`
	UpdatedAt            time.Time  `gorm:"precision:3;not null"`
	Version              uint64     `gorm:"not null"`
}

func (ImportJob) TableName() string { return "crm_customer_import_jobs" }

type ImportRow struct {
	ID            uint64 `gorm:"primaryKey;autoIncrement"`
	TenantID      string `gorm:"size:64;not null"`
	JobID         uint64 `gorm:"not null"`
	RowNo         uint32 `gorm:"not null"`
	Status        string `gorm:"size:32;not null"`
	CommandCipher []byte `gorm:"type:varbinary(4096)"`
	ErrorColumn   string `gorm:"size:100;not null"`
	ErrorCode     string `gorm:"size:64;not null"`
	ErrorMessage  string `gorm:"size:500;not null"`
	CustomerID    *uint64
	CustomerNo    string    `gorm:"size:32;not null"`
	CreatedAt     time.Time `gorm:"precision:3;not null"`
	UpdatedAt     time.Time `gorm:"precision:3;not null"`
}

func (ImportRow) TableName() string { return "crm_customer_import_rows" }

type ImportIdempotency struct {
	ID           uint64    `gorm:"primaryKey;autoIncrement"`
	TenantID     string    `gorm:"size:64;not null"`
	ActorID      string    `gorm:"size:64;not null"`
	Key          string    `gorm:"column:idempotency_key;size:128;not null"`
	RequestHash  string    `gorm:"size:64;not null"`
	Status       string    `gorm:"size:16;not null"`
	ResponseJSON []byte    `gorm:"type:json"`
	CreatedAt    time.Time `gorm:"precision:3;not null"`
}

func (ImportIdempotency) TableName() string { return "crm_customer_import_idempotency" }
