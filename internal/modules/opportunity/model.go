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
	Type                    string          `gorm:"type:text;not null"`
	Source                  string          `gorm:"type:text;not null"`
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
	ContractID              *string         `gorm:"size:26"`
	ContractIntakeID        *string         `gorm:"size:64"`
	ContractLinkStatus      string          `gorm:"size:32;not null"`
	ContractLinkedAt        *time.Time      `gorm:"precision:3"`
	ContractSyncVersion     uint64          `gorm:"not null"`
	ContractLinkEventID     *string         `gorm:"size:128"`
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

// ExternalLink 是可信报价/投标状态回调在 CRM 中的不可变投影。CRM 只拥有该只读快照，
// 外部业务单据及其最终状态仍由来源系统负责。
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

// Member 是平台主体在商机团队中的当前规范记录。替换团队时仅停用移除成员而不物理删除，
// 保证成员退出当前团队后仍能被历史审计引用。
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

// MemberTerm 保存团队成员的一段不可变参与区间；Member 回答“现在是谁”，区间记录则保留
// 多次加入、移除和角色变化。平台主体是不透明标识，不能据此推断姓名或任职状态。
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

// CreateIdempotency 是绑定操作者的商机创建重放坐标。请求仅保留摘要，响应快照保存创建时的公开 DTO，
// 后续主数据变化不能改变完全相同重试的返回结果。
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

// OutboxEvent 是共享事务发件箱在商机模块中的本地模型。负责人变更通知与乐观锁更新原子写入，
// 再由 CRM 站内通知投影任务异步消费。
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
