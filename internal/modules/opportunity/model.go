package opportunity

import (
	"time"

	"github.com/shopspring/decimal"
	"github.com/unified-identity-auth-platform/customer-and-opportunity/internal/shared/database"
)

const (
	StageInitial               = "初步接触"
	StageRequirement           = "需求沟通"
	StageSolution              = "方案制定"
	StageQuotation             = "报价"
	StageBid                   = "投标"
	StageSigned                = "已签约"
	StageFailed                = "失败"
	StatusFollowing            = "FOLLOWING"
	StatusClosed               = "CLOSED"
	StatusVoid                 = "VOID"
	PendingNone                = "NONE"
	PendingContract            = "CONTRACT"
	PendingLostReason          = "LOST_REASON"
	SourceManual               = "MANUAL"
	SourceQBCallback           = "QB_CALLBACK"
	MemberRoleSalesSupport     = "SALES_SUPPORT"
	MemberRoleTechnicalSupport = "TECHNICAL_SUPPORT"
	MemberRoleBusinessSupport  = "BUSINESS_SUPPORT"
	MemberRoleOther            = "OTHER"
)

type Opportunity struct {
	database.Model
	OpportunityNo           string          `gorm:"size:32;not null;uniqueIndex:uk_opportunity_no,priority:2"`
	Name                    string          `gorm:"size:200;not null"`
	CustomerID              uint64          `gorm:"not null;index"`
	Type                    string          `gorm:"size:64;not null"`
	Source                  string          `gorm:"size:64;not null"`
	ExpectedAmount          decimal.Decimal `gorm:"type:decimal(18,2);not null"`
	ExpectedSignDate        time.Time       `gorm:"type:date;not null"`
	RequirementSummary      string          `gorm:"type:text;not null"`
	SystemCount             uint32          `gorm:"not null;default:0"`
	PainPoints              string          `gorm:"type:text"`
	CompetitorInfo          string          `gorm:"type:text"`
	OwnerUserID             string          `gorm:"size:64;not null;index"`
	OwnerOrgID              string          `gorm:"size:64;not null;index"`
	CurrentStage            string          `gorm:"size:32;not null;index"`
	Status                  string          `gorm:"column:opp_status;size:32;not null;index"`
	ContractRef             *string         `gorm:"size:64"`
	LostReason              *string         `gorm:"size:64"`
	TerminalPendingType     string          `gorm:"size:32;not null"`
	StageChangedAt          time.Time       `gorm:"precision:3;not null"`
	ExternalStatusChangedAt *time.Time      `gorm:"precision:3"`
	EndDate                 *time.Time      `gorm:"type:date"`
	StatusBeforeVoid        *string         `gorm:"size:32"`
}

func (Opportunity) TableName() string { return "crm_opportunities" }

type StageLog struct {
	ID            uint64    `gorm:"primaryKey"`
	TenantID      string    `gorm:"size:64;not null;index"`
	OpportunityID uint64    `gorm:"not null;index"`
	FromStage     string    `gorm:"size:32;not null"`
	ToStage       string    `gorm:"size:32;not null"`
	Source        string    `gorm:"size:32;not null"`
	SourceID      string    `gorm:"size:64;not null;uniqueIndex:uk_stage_source,priority:2"`
	Reason        string    `gorm:"size:500"`
	ContractRef   *string   `gorm:"size:64"`
	LostReason    *string   `gorm:"size:64"`
	PendingType   string    `gorm:"size:32;not null"`
	OperatorID    string    `gorm:"size:64;not null"`
	ChangedAt     time.Time `gorm:"precision:3;not null"`
	RequestID     string    `gorm:"size:64;not null;index"`
}

func (StageLog) TableName() string { return "crm_opportunity_stage_logs" }

type Followup struct {
	database.Model
	OpportunityID uint64     `gorm:"not null;index"`
	Type          string     `gorm:"size:32;not null"`
	Content       string     `gorm:"type:text;not null"`
	FollowedAt    time.Time  `gorm:"precision:3;not null;index"`
	FollowedBy    string     `gorm:"size:64;not null"`
	NextFollowAt  *time.Time `gorm:"precision:3"`
}

func (Followup) TableName() string { return "crm_opportunity_followups" }

// ExternalLink is the immutable local projection of one trusted quotation or
// bid status callback. CRM owns only this read-only snapshot; the originating
// system remains authoritative for the external business document.
type ExternalLink struct {
	ID            uint64           `gorm:"primaryKey;autoIncrement"`
	TenantID      string           `gorm:"size:64;not null;uniqueIndex:uq_opportunity_external_status,priority:1"`
	OpportunityID uint64           `gorm:"not null;index;uniqueIndex:uq_opportunity_external_status,priority:2"`
	Type          string           `gorm:"size:16;not null"`
	SourceID      string           `gorm:"size:64;not null;uniqueIndex:uq_opportunity_external_status,priority:3"`
	Status        string           `gorm:"size:32;not null;uniqueIndex:uq_opportunity_external_status,priority:4"`
	Amount        *decimal.Decimal `gorm:"type:decimal(18,2)"`
	ChangedAt     time.Time        `gorm:"precision:3;not null;index"`
	SnapshotJSON  []byte           `gorm:"type:json;not null"`
	CreatedAt     time.Time        `gorm:"precision:3;not null"`
}

func (ExternalLink) TableName() string { return "crm_opportunity_external_links" }

// Member is the canonical membership row for one platform subject. Replacing a
// team deactivates removed rows instead of deleting them, so historical
// subjects remain available to audits even after they leave the current team.
type Member struct {
	database.Model
	OpportunityID uint64     `gorm:"not null;index;uniqueIndex:uq_opportunity_member,priority:2"`
	UserID        string     `gorm:"size:64;not null;uniqueIndex:uq_opportunity_member,priority:3"`
	Role          string     `gorm:"size:32;not null"`
	IsActive      bool       `gorm:"not null;index"`
	EndedAt       *time.Time `gorm:"precision:3"`
}

func (Member) TableName() string { return "crm_opportunity_members" }

const (
	MemberTermSourceRecorded       = "RECORDED"
	MemberTermSourceLegacySnapshot = "LEGACY_SNAPSHOT"
)

// MemberTerm is one immutable participation interval for an opportunity team
// member. The canonical Member row answers who is on the team now; terms retain
// repeated joins, removals and role changes without inferring a display name or
// employment status from an opaque platform subject.
type MemberTerm struct {
	ID               uint64     `gorm:"primaryKey;autoIncrement"`
	TenantID         string     `gorm:"size:64;not null;index"`
	OpportunityID    uint64     `gorm:"not null;index"`
	MemberID         uint64     `gorm:"not null;index"`
	UserID           string     `gorm:"size:64;not null;index"`
	Role             string     `gorm:"size:32;not null"`
	StartedAt        *time.Time `gorm:"precision:3;index"`
	SnapshotAt       *time.Time `gorm:"precision:3"`
	ActiveAtSnapshot *bool      `gorm:"column:active_at_snapshot"`
	EndedAt          *time.Time `gorm:"precision:3"`
	StartedBy        *string    `gorm:"size:64"`
	EndedBy          *string    `gorm:"size:64"`
	SourceKind       string     `gorm:"size:32;not null"`
}

func (MemberTerm) TableName() string { return "crm_opportunity_member_terms" }

type ChangeIdempotency struct {
	ID            uint64    `gorm:"primaryKey;autoIncrement"`
	TenantID      string    `gorm:"size:64;not null;uniqueIndex:uq_opportunity_change_idem,priority:1"`
	OpportunityID uint64    `gorm:"not null;uniqueIndex:uq_opportunity_change_idem,priority:2"`
	Operation     string    `gorm:"size:32;not null;uniqueIndex:uq_opportunity_change_idem,priority:3"`
	ActorID       string    `gorm:"size:64;not null;uniqueIndex:uq_opportunity_change_idem,priority:4"`
	Key           string    `gorm:"column:idempotency_key;size:128;not null;uniqueIndex:uq_opportunity_change_idem,priority:5"`
	RequestHash   string    `gorm:"size:64;not null"`
	ResponseJSON  []byte    `gorm:"type:json;not null"`
	CreatedAt     time.Time `gorm:"precision:3;not null"`
}

func (ChangeIdempotency) TableName() string { return "crm_opportunity_change_idempotency" }

// CreateIdempotency is the durable, actor-bound replay coordinate for one
// opportunity creation command. The request is retained only as a digest. The
// response snapshot contains the same public DTO returned at creation time so
// later master-data changes cannot alter an exact retry's result.
type CreateIdempotency struct {
	ID             uint64    `gorm:"primaryKey;autoIncrement"`
	TenantID       string    `gorm:"size:64;not null;uniqueIndex:uq_opportunity_create_idem,priority:1"`
	ActorID        string    `gorm:"size:64;not null;uniqueIndex:uq_opportunity_create_idem,priority:2"`
	Key            string    `gorm:"column:idempotency_key;size:128;not null;uniqueIndex:uq_opportunity_create_idem,priority:3"`
	CustomerID     uint64    `gorm:"not null"`
	OpportunityID  uint64    `gorm:"not null"`
	RequestHash    string    `gorm:"size:64;not null"`
	ResponseHash   string    `gorm:"size:64;not null"`
	ResponseJSON   []byte    `gorm:"type:json;not null"`
	RequestIDTrace string    `gorm:"size:64;not null"`
	CreatedAt      time.Time `gorm:"precision:3;not null"`
}

func (CreateIdempotency) TableName() string { return "crm_opportunity_create_idempotency" }

// OutboxEvent is a module-local view of the shared CRM transactional outbox.
// Owner-change notifications are inserted in the same transaction as the
// optimistic opportunity update and consumed by the CRM in-product
// notification projection worker.
type OutboxEvent struct {
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
	LastErrorSummary string     `gorm:"size:1000"`
	CreatedAt        time.Time  `gorm:"precision:3;not null"`
	SentAt           *time.Time `gorm:"precision:3"`
}

func (OutboxEvent) TableName() string { return "crm_outbox_events" }
